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
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/mirror"
)

// fakeWorkerLinks is fakeWorkerMirror's counterpart for the Worker's four
// /internal/archived-links endpoints: faithful to their contract (checkpoint =
// MAX(updated_at) of what's stored, upsert, delete by id), not a reimplementation
// of D1. The real endpoints are covered by terraform/worker/tests/archived-links.test.js;
// this only exercises Syncer's own orchestration.
type fakeWorkerLinks struct {
	mu       sync.Mutex
	links    map[int64]map[string]any
	requests int
	// missing makes every route a 404, as a Worker deployed before the links
	// feature behaves.
	missing bool
	// failing makes every route a 500.
	failing bool
}

func newFakeWorkerLinks() *fakeWorkerLinks {
	return &fakeWorkerLinks{links: map[int64]map[string]any{}}
}

func (f *fakeWorkerLinks) routes(mux *http.ServeMux) {
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.requests++
			missing, failing := f.missing, f.failing
			f.mu.Unlock()
			switch {
			case missing:
				http.NotFound(w, r)
			case failing:
				http.Error(w, "boom", http.StatusInternalServerError)
			default:
				h(w, r)
			}
		}
	}

	mux.HandleFunc("GET /internal/archived-links/last-sync", guard(func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var latest *string
		for _, l := range f.links {
			u := l["updated_at"].(string)
			if latest == nil || u > *latest {
				latest = &u
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"last_sync": latest})
	}))

	mux.HandleFunc("POST /internal/archived-links/mirror", guard(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Links []map[string]any `json:"links"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, l := range body.Links {
			f.links[int64(l["link_id"].(float64))] = l
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"upserted": len(body.Links)})
	}))

	mux.HandleFunc("GET /internal/archived-links/link-ids", guard(func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ids := make([]int64, 0, len(f.links))
		for id := range f.links {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		_ = json.NewEncoder(w).Encode(map[string]any{"link_ids": ids})
	}))

	mux.HandleFunc("POST /internal/archived-links/delete", guard(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			LinkIDs []int64 `json:"link_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, id := range body.LinkIDs {
			delete(f.links, id)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": len(body.LinkIDs)})
	}))
}

func (f *fakeWorkerLinks) linkIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0, len(f.links))
	for id := range f.links {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (f *fakeWorkerLinks) get(id int64) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.links[id]
}

// bothFakes serves the pages and links endpoints from one server.
func bothFakes(pages *fakeWorkerMirror, links *fakeWorkerLinks) *httptest.Server {
	mux := http.NewServeMux()
	pages.routes(mux)
	links.routes(mux)
	return httptest.NewServer(mux)
}

func upsertTestLink(t *testing.T, ctx context.Context, q *db.Queries, userID int64, rawURL, title string) db.Link {
	t.Helper()
	link, err := q.UpsertLink(ctx, db.UpsertLinkParams{
		UserID: userID, RawUrl: rawURL, NormalizedUrl: rawURL,
		Title: pgtype.Text{String: title, Valid: title != ""}, Source: "extension",
	})
	require.NoError(t, err)
	return link
}

type linkSyncEnv struct {
	ctx    context.Context
	q      *db.Queries
	user   db.User
	pages  *fakeWorkerMirror
	links  *fakeWorkerLinks
	server *httptest.Server
	logs   *bytes.Buffer
	syncer *mirror.Syncer
}

func newLinkSyncEnv(t *testing.T, linksEnabled bool) *linkSyncEnv {
	t.Helper()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	e := &linkSyncEnv{
		ctx: context.Background(), q: db.New(pool), user: dbtest.CreateUser(t, pool, "member"),
		pages: newFakeWorkerMirror(), links: newFakeWorkerLinks(), logs: &bytes.Buffer{},
	}
	e.server = bothFakes(e.pages, e.links)
	t.Cleanup(e.server.Close)
	e.syncer = mirror.NewSyncer(mirror.SyncerParams{
		Queries: e.q, Client: mirror.NewClient(e.server.URL, "test-secret"),
		LinksEnabled: linksEnabled, Logger: slog.New(slog.NewTextHandler(e.logs, nil)),
	})
	return e
}

func TestSyncer_SyncOnce_Links(t *testing.T) {
	t.Run("first sync pushes every link, with its raw URL and title", func(t *testing.T) {
		e := newLinkSyncEnv(t, true)
		a := upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/a#frag", "A")
		b := upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/b", "")

		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		assert.Equal(t, []int64{a.ID, b.ID}, e.links.linkIDs())
		pushed := e.links.get(a.ID)
		assert.Equal(t, "https://example.com/a#frag", pushed["raw_url"])
		assert.Equal(t, "A", pushed["title"])
		assert.EqualValues(t, e.user.ID, pushed["user_id"])
		assert.NotContains(t, e.links.get(b.ID), "title", "a link with no title sends none")
	})

	t.Run("a second sync with nothing changed pushes nothing new", func(t *testing.T) {
		e := newLinkSyncEnv(t, true)
		upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/a", "A")
		require.NoError(t, e.syncer.SyncOnce(e.ctx))
		before := e.links.get(e.links.linkIDs()[0])

		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		assert.Len(t, e.links.linkIDs(), 1)
		assert.Equal(t, before, e.links.get(e.links.linkIDs()[0]))
	})

	t.Run("only what changed since the checkpoint is pushed later: new links and edited titles", func(t *testing.T) {
		e := newLinkSyncEnv(t, true)
		a := upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/a", "Before")
		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		c := upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/c", "C")
		_, err := e.q.SetLinkTitle(e.ctx, db.SetLinkTitleParams{
			Title: pgtype.Text{String: "After", Valid: true}, ID: a.ID, UserID: e.user.ID,
		})
		require.NoError(t, err)

		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		assert.Equal(t, []int64{a.ID, c.ID}, e.links.linkIDs())
		assert.Equal(t, "After", e.links.get(a.ID)["title"])
	})

	t.Run("a link excluded from the sync is never pushed", func(t *testing.T) {
		e := newLinkSyncEnv(t, true)
		kept := upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/kept", "Kept")
		skipped := upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/skipped", "Skipped")
		_, err := e.q.SetLinkExcludedFromMirror(e.ctx, db.SetLinkExcludedFromMirrorParams{
			ExcludedFromMirror: true, ID: skipped.ID, UserID: e.user.ID,
		})
		require.NoError(t, err)

		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		assert.Equal(t, []int64{kept.ID}, e.links.linkIDs())
	})

	t.Run("excluding an already-synced link removes it, and including it again brings it back", func(t *testing.T) {
		e := newLinkSyncEnv(t, true)
		link := upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/toggle", "Toggle")
		require.NoError(t, e.syncer.SyncOnce(e.ctx))
		require.Equal(t, []int64{link.ID}, e.links.linkIDs())

		_, err := e.q.SetLinkExcludedFromMirror(e.ctx, db.SetLinkExcludedFromMirrorParams{
			ExcludedFromMirror: true, ID: link.ID, UserID: e.user.ID,
		})
		require.NoError(t, err)
		require.NoError(t, e.syncer.SyncOnce(e.ctx))
		assert.Empty(t, e.links.linkIDs(), "removed by deletion reconciliation")

		_, err = e.q.SetLinkExcludedFromMirror(e.ctx, db.SetLinkExcludedFromMirrorParams{
			ExcludedFromMirror: false, ID: link.ID, UserID: e.user.ID,
		})
		require.NoError(t, err)
		require.NoError(t, e.syncer.SyncOnce(e.ctx))
		assert.Equal(t, []int64{link.ID}, e.links.linkIDs(), "the change moved updated_at past the checkpoint")
	})

	t.Run("deleting a link removes it from the mirror", func(t *testing.T) {
		e := newLinkSyncEnv(t, true)
		gone := upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/gone", "Gone")
		kept := upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/stays", "Stays")
		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		_, err := e.q.DeleteLink(e.ctx, db.DeleteLinkParams{ID: gone.ID, UserID: e.user.ID})
		require.NoError(t, err)
		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		assert.Equal(t, []int64{kept.ID}, e.links.linkIDs())
	})

	t.Run("pages and links are mirrored separately in the same cycle", func(t *testing.T) {
		e := newLinkSyncEnv(t, true)
		upsertTestPage(t, e.ctx, e.q, e.user.ID, "https://example.com/page", "Page", time.Now())
		upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/link", "Link")

		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		assert.Len(t, e.pages.pageIDs(), 1)
		assert.Len(t, e.links.linkIDs(), 1)
	})

	t.Run("with links off the links endpoints are never called and pages still sync", func(t *testing.T) {
		e := newLinkSyncEnv(t, false)
		upsertTestPage(t, e.ctx, e.q, e.user.ID, "https://example.com/page", "Page", time.Now())
		upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/link", "Link")

		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		assert.Len(t, e.pages.pageIDs(), 1)
		assert.Empty(t, e.links.linkIDs())
		assert.Zero(t, e.links.requests)
	})

	t.Run("a Worker without the routes is skipped with a warning, not failed, and pages still sync", func(t *testing.T) {
		e := newLinkSyncEnv(t, true)
		e.links.missing = true
		upsertTestPage(t, e.ctx, e.q, e.user.ID, "https://example.com/page", "Page", time.Now())
		upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/link", "Link")

		require.NoError(t, e.syncer.SyncOnce(e.ctx))

		assert.Len(t, e.pages.pageIDs(), 1)
		assert.Contains(t, e.logs.String(), "no archived-links routes")
	})

	t.Run("a links failure is reported but doesn't stop pages from syncing", func(t *testing.T) {
		e := newLinkSyncEnv(t, true)
		e.links.failing = true
		upsertTestPage(t, e.ctx, e.q, e.user.ID, "https://example.com/page", "Page", time.Now())
		upsertTestLink(t, e.ctx, e.q, e.user.ID, "https://example.com/link", "Link")

		err := e.syncer.SyncOnce(e.ctx)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "link sync")
		assert.Len(t, e.pages.pageIDs(), 1)
	})

	t.Run("a pages failure is reported but doesn't stop links from syncing", func(t *testing.T) {
		pool := dbtest.Setup(t)
		dbtest.Reset(t, pool)
		q := db.New(pool)
		user := dbtest.CreateUser(t, pool, "member")
		links := newFakeWorkerLinks()
		mux := http.NewServeMux()
		links.routes(mux)
		mux.HandleFunc("/internal/archived-pages/", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})
		server := httptest.NewServer(mux)
		defer server.Close()
		upsertTestLink(t, context.Background(), q, user.ID, "https://example.com/link", "Link")

		syncer := mirror.NewSyncer(mirror.SyncerParams{
			Queries: q, Client: mirror.NewClient(server.URL, "test-secret"), LinksEnabled: true,
		})
		err := syncer.SyncOnce(context.Background())

		require.Error(t, err)
		assert.Len(t, links.linkIDs(), 1)
	})
}
