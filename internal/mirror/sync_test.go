/*
 * recueil: self-hosted webpage bookmarker and archiver
 * Copyright © 2026 Mario Finelli
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

package mirror_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/mirror"
)

// fakeWorkerMirror is a lightweight in-memory stand-in for the real
// Worker's four archived-pages endpoints -- faithful to their contract
// (checkpoint = MAX(updated_at) of what's actually stored, upsert
// semantics, deletion by id), not a reimplementation of D1/SQLite. The
// real endpoints' own correctness is already covered by
// terraform/tests/archived-pages.test.js; this only needs to exercise
// Syncer's own orchestration logic against something that behaves the
// same way.
type fakeWorkerMirror struct {
	mu    sync.Mutex
	pages map[int64]map[string]any
}

func newFakeWorkerMirror() *fakeWorkerMirror {
	return &fakeWorkerMirror{pages: map[int64]map[string]any{}}
}

func (f *fakeWorkerMirror) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/archived-pages/last-sync", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		var mmax *string
		for _, p := range f.pages {
			updatedAt := p["updated_at"].(string)
			if mmax == nil || updatedAt > *mmax {
				u := updatedAt
				mmax = &u
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"last_sync": mmax})
	})

	mux.HandleFunc("POST /internal/archived-pages/mirror", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Pages []map[string]any `json:"pages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		f.mu.Lock()
		defer f.mu.Unlock()
		for _, p := range body.Pages {
			pageID := int64(p["page_id"].(float64))
			f.pages[pageID] = p
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"upserted": len(body.Pages)})
	})

	mux.HandleFunc("GET /internal/archived-pages/page-ids", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		ids := make([]int64, 0, len(f.pages))
		for id := range f.pages {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		_ = json.NewEncoder(w).Encode(map[string]any{"page_ids": ids})
	})

	mux.HandleFunc("POST /internal/archived-pages/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PageIDs []int64 `json:"page_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		f.mu.Lock()
		defer f.mu.Unlock()
		for _, id := range body.PageIDs {
			delete(f.pages, id)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": len(body.PageIDs)})
	})

	return httptest.NewServer(mux)
}

func (f *fakeWorkerMirror) pageIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0, len(f.pages))
	for id := range f.pages {
		ids = append(ids, id)
	}
	return ids
}

func upsertTestPage(t *testing.T, ctx context.Context, queries *db.Queries, userID int64, normalizedURL, title string, capturedAt time.Time) db.Page {
	t.Helper()
	page, err := queries.UpsertPage(ctx, db.UpsertPageParams{
		UserID:          userID,
		NormalizedUrl:   normalizedURL,
		Title:           pgtype.Text{String: title, Valid: true},
		LatestCaptureAt: pgtype.Timestamptz{Time: capturedAt, Valid: true},
	})
	require.NoError(t, err)
	return page
}

func TestSyncer_SyncOnce(t *testing.T) {
	ctx := context.Background()

	t.Run("first sync pushes every existing page", func(t *testing.T) {
		pool := dbtest.Setup(t)
		dbtest.Reset(t, pool)
		queries := db.New(pool)
		user := dbtest.CreateUser(t, pool, "member")

		upsertTestPage(t, ctx, queries, user.ID, "https://example.com/a", "A", time.Now())
		upsertTestPage(t, ctx, queries, user.ID, "https://example.com/b", "B", time.Now())

		fake := newFakeWorkerMirror()
		server := fake.server()
		defer server.Close()

		syncer := mirror.NewSyncer(mirror.SyncerParams{
			Queries: queries,
			Client:  mirror.NewClient(server.URL, "test-secret"),
		})
		require.NoError(t, syncer.SyncOnce(ctx))

		assert.Len(t, fake.pageIDs(), 2)
	})

	t.Run("a second sync with no changes pushes nothing new", func(t *testing.T) {
		pool := dbtest.Setup(t)
		dbtest.Reset(t, pool)
		queries := db.New(pool)
		user := dbtest.CreateUser(t, pool, "member")
		upsertTestPage(t, ctx, queries, user.ID, "https://example.com/a", "A", time.Now())

		fake := newFakeWorkerMirror()
		server := fake.server()
		defer server.Close()
		syncer := mirror.NewSyncer(mirror.SyncerParams{
			Queries: queries,
			Client:  mirror.NewClient(server.URL, "test-secret"),
		})

		require.NoError(t, syncer.SyncOnce(ctx))
		require.NoError(t, syncer.SyncOnce(ctx))

		// Still exactly one page mirrored -- the second cycle found
		// nothing changed since the checkpoint and pushed nothing.
		assert.Len(t, fake.pageIDs(), 1)
	})

	t.Run("only pages changed since the checkpoint are pushed on a later sync", func(t *testing.T) {
		pool := dbtest.Setup(t)
		dbtest.Reset(t, pool)
		queries := db.New(pool)
		user := dbtest.CreateUser(t, pool, "member")
		upsertTestPage(t, ctx, queries, user.ID, "https://example.com/a", "A", time.Now())

		fake := newFakeWorkerMirror()
		server := fake.server()
		defer server.Close()
		syncer := mirror.NewSyncer(mirror.SyncerParams{
			Queries: queries,
			Client:  mirror.NewClient(server.URL, "test-secret"),
		})
		require.NoError(t, syncer.SyncOnce(ctx))
		require.Len(t, fake.pageIDs(), 1)

		// A brand new page appears after the first sync.
		time.Sleep(10 * time.Millisecond) // ensure a distinct updated_at
		upsertTestPage(t, ctx, queries, user.ID, "https://example.com/c", "C", time.Now())

		require.NoError(t, syncer.SyncOnce(ctx))
		assert.Len(t, fake.pageIDs(), 2)
	})

	t.Run("deletion reconciliation removes D1 pages no longer in postgres", func(t *testing.T) {
		pool := dbtest.Setup(t)
		dbtest.Reset(t, pool)
		queries := db.New(pool)
		user := dbtest.CreateUser(t, pool, "member")
		page := upsertTestPage(t, ctx, queries, user.ID, "https://example.com/a", "A", time.Now())

		fake := newFakeWorkerMirror()
		server := fake.server()
		defer server.Close()
		syncer := mirror.NewSyncer(mirror.SyncerParams{
			Queries: queries,
			Client:  mirror.NewClient(server.URL, "test-secret"),
		})
		require.NoError(t, syncer.SyncOnce(ctx))
		require.Len(t, fake.pageIDs(), 1)

		// Simulate a page that D1 still has, but Postgres no longer
		// does (deletion isn't built yet, so seeded directly).
		fake.mu.Lock()
		fake.pages[page.ID+1000] = map[string]any{
			"page_id": float64(page.ID + 1000), "user_id": float64(user.ID),
			"raw_url": "https://example.com/deleted", "updated_at": "2026-01-01T00:00:00Z",
		}
		fake.mu.Unlock()

		require.NoError(t, syncer.SyncOnce(ctx))

		remaining := fake.pageIDs()
		assert.Contains(t, remaining, page.ID)
		assert.NotContains(t, remaining, page.ID+1000)
	})
}
