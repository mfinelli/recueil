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

package ingest_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/ingest"
	"github.com/mfinelli/recueil/internal/linkicons"
	"github.com/mfinelli/recueil/internal/pendinglinks"
)

// fakeLinkWorker is fakeWorker's counterpart for the links client: an
// in-memory stand-in for the two methods LinkIngester calls.
type fakeLinkWorker struct {
	pending              []pendinglinks.PendingLink
	fetched              []string
	claimErr             error
	failMarkFetchedTimes int
}

func (f *fakeLinkWorker) ClaimPendingLinks(_ context.Context, limit int) ([]pendinglinks.PendingLink, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if limit < len(f.pending) {
		return f.pending[:limit], nil
	}
	return f.pending, nil
}

func (f *fakeLinkWorker) MarkFetched(_ context.Context, linkID string) error {
	if f.failMarkFetchedTimes > 0 {
		f.failMarkFetchedTimes--
		return errors.New("fake worker: simulated MarkFetched failure")
	}
	f.fetched = append(f.fetched, linkID)
	return nil
}

// failingDeleteR2 is a fakeR2 whose Delete always errors, for the "R2 favicon
// cleanup is best-effort" case.
type failingDeleteR2 struct{ *fakeR2 }

func (failingDeleteR2) Delete(context.Context, string) error {
	return errors.New("fake r2: simulated delete failure")
}

type linkEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	queries *db.Queries
	worker  *fakeLinkWorker
	r2      *fakeR2
	root    string
	store   *linkicons.Store
	user    db.User
}

func newLinkEnv(t *testing.T) *linkEnv {
	t.Helper()
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool)
	root := t.TempDir()
	return &linkEnv{
		t:       t,
		pool:    pool,
		queries: db.New(pool),
		worker:  &fakeLinkWorker{},
		r2:      newFakeR2(),
		root:    root,
		store:   linkicons.New(root),
		user:    dbtest.CreateUser(t, pool, "member"),
	}
}

func (e *linkEnv) ingester() *ingest.LinkIngester {
	return ingest.NewLinkIngester(ingest.LinkParams{
		Queries:  e.queries,
		Worker:   e.worker,
		R2:       e.r2,
		Store:    e.store,
		Pipeline: newTestPipeline(e.t),
	})
}

func (e *linkEnv) run() error {
	e.t.Helper()
	return e.ingester().RunOnce(context.Background())
}

func (e *linkEnv) link(normalizedURL string) db.Link {
	e.t.Helper()
	l, err := e.queries.GetLinkByNormalizedURL(context.Background(), db.GetLinkByNormalizedURLParams{
		UserID: e.user.ID, NormalizedUrl: normalizedURL,
	})
	require.NoError(e.t, err)
	return l
}

func (e *linkEnv) fileCount() int {
	e.t.Helper()
	n := 0
	require.NoError(e.t, e.store.Archive().Walk(func(string, int64, time.Time) error {
		n++
		return nil
	}))
	return n
}

func strp(s string) *string { return &s }

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func inlineLink(userID int64, id, url, savedAt string, ext string, data []byte) pendinglinks.PendingLink {
	pl := pendinglinks.PendingLink{ID: id, UserID: userID, URL: url, SavedAt: savedAt}
	if ext != "" {
		pl.FaviconExt = strp(ext)
		pl.FaviconData = strp(base64.StdEncoding.EncodeToString(data))
	}
	return pl
}

func TestLinkIngester_RunOnce_InlineFavicon(t *testing.T) {
	e := newLinkEnv(t)
	icon := []byte("\x89PNG pretend favicon bytes")
	pl := inlineLink(e.user.ID, "link-1", "https://example.com/page?utm_source=newsletter&id=42",
		"2026-07-12T12:00:00.000Z", "png", icon)
	pl.Title = strp("  An Example  ")
	e.worker.pending = []pendinglinks.PendingLink{pl}

	require.NoError(t, e.run())

	l := e.link("https://example.com/page?id=42")
	assert.Equal(t, e.user.ID, l.UserID)
	assert.Equal(t, "https://example.com/page?utm_source=newsletter&id=42", l.RawUrl, "raw_url is what was open in the tab")
	assert.Equal(t, "  An Example  ", l.Title.String, "the title is stored as the device sent it")
	assert.Equal(t, sha256Hex(icon), l.FaviconHash.String)
	assert.Equal(t, int32(len(icon)), l.FaviconSizeBytes.Int32)
	assert.Equal(t, "favicon.png", filepath.Base(l.FaviconPath.String))
	onDisk, err := os.ReadFile(filepath.Join(e.root, l.FaviconPath.String))
	require.NoError(t, err)
	assert.Equal(t, icon, onDisk)

	// The row's own timestamps are ingest time, as for pages and captures --
	// never the device's saved_at (2026), which would break the mirror's
	// updated_at checkpoint.
	assert.WithinDuration(t, time.Now(), l.CreatedAt.Time, time.Minute)
	assert.WithinDuration(t, time.Now(), l.UpdatedAt.Time, time.Minute)

	assert.Equal(t, []string{"link-1"}, e.worker.fetched)
}

func TestLinkIngester_RunOnce_R2FaviconIsPulledStoredAndDeleted(t *testing.T) {
	e := newLinkEnv(t)
	icon := []byte("a large favicon that went through R2")
	const key = "pending-links/1/link-r2/favicon.ico"
	e.r2.objects[key] = icon
	e.worker.pending = []pendinglinks.PendingLink{{
		ID: "link-r2", UserID: e.user.ID, URL: "https://example.com/r2",
		FaviconExt: strp("ico"), R2KeyFavicon: strp(key), SavedAt: "2026-07-12T12:00:00.000Z",
	}}

	require.NoError(t, e.run())

	l := e.link("https://example.com/r2")
	assert.Equal(t, "favicon.ico", filepath.Base(l.FaviconPath.String))
	assert.Equal(t, sha256Hex(icon), l.FaviconHash.String)
	assert.NotContains(t, e.r2.objects, key, "the R2 object is deleted once the link is durable")
	assert.Equal(t, []string{"link-r2"}, e.worker.fetched)
}

func TestLinkIngester_RunOnce_SVGIsCompressedWithTheHashOfTheOriginal(t *testing.T) {
	e := newLinkEnv(t)
	svg := []byte(strings.Repeat(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>`, 40))
	e.worker.pending = []pendinglinks.PendingLink{
		inlineLink(e.user.ID, "link-svg", "https://example.com/svg", "2026-07-12T12:00:00.000Z", "svg", svg),
	}

	require.NoError(t, e.run())

	l := e.link("https://example.com/svg")
	assert.Equal(t, "favicon.svg.zst", filepath.Base(l.FaviconPath.String))
	assert.Equal(t, sha256Hex(svg), l.FaviconHash.String)
	info, err := os.Stat(filepath.Join(e.root, l.FaviconPath.String))
	require.NoError(t, err)
	assert.Equal(t, int32(info.Size()), l.FaviconSizeBytes.Int32)
}

func TestLinkIngester_RunOnce_NoFavicon(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-bare", UserID: e.user.ID, URL: "https://example.com/bare", SavedAt: "2026-07-12T12:00:00.000Z"},
	}

	require.NoError(t, e.run())

	l := e.link("https://example.com/bare")
	assert.False(t, l.Title.Valid)
	assert.False(t, l.FaviconPath.Valid)
	assert.False(t, l.FaviconSizeBytes.Valid)
	assert.False(t, l.FaviconHash.Valid)
	assert.Equal(t, 0, e.fileCount())
	assert.Equal(t, []string{"link-bare"}, e.worker.fetched)
}

func TestLinkIngester_RunOnce_AnEmptyTitleIsStoredAsNull(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-empty-title", UserID: e.user.ID, URL: "https://example.com/t", Title: strp(""), SavedAt: "2026-07-12T12:00:00.000Z"},
	}
	require.NoError(t, e.run())
	assert.False(t, e.link("https://example.com/t").Title.Valid)
}

// The faithful rule: a re-save whose device saw no favicon means the site
// stopped serving one, so the row's favicon is cleared -- and the old file,
// no longer referenced, is left for gc.
func TestLinkIngester_RunOnce_ASaveWithNoFaviconClearsTheOldOne(t *testing.T) {
	e := newLinkEnv(t)
	url := "https://example.com/stopped-serving"
	e.worker.pending = []pendinglinks.PendingLink{
		inlineLink(e.user.ID, "link-a", url, "2026-07-12T12:00:00.000Z", "png", []byte("the old icon")),
	}
	require.NoError(t, e.run())
	old := e.link(url)
	require.True(t, old.FaviconPath.Valid)

	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-b", UserID: e.user.ID, URL: url, SavedAt: "2026-07-13T12:00:00.000Z"},
	}
	require.NoError(t, e.run())

	now := e.link(url)
	assert.Equal(t, old.ID, now.ID, "still the one link")
	assert.False(t, now.FaviconPath.Valid)
	assert.False(t, now.FaviconSizeBytes.Valid)
	assert.False(t, now.FaviconHash.Valid)
	assert.FileExists(t, filepath.Join(e.root, old.FaviconPath.String), "the orphaned file is gc's to reclaim")
}

func TestLinkIngester_RunOnce_ANewFaviconReplacesTheOldOne(t *testing.T) {
	e := newLinkEnv(t)
	url := "https://example.com/new-icon"
	e.worker.pending = []pendinglinks.PendingLink{
		inlineLink(e.user.ID, "link-a", url, "2026-07-12T12:00:00.000Z", "png", []byte("old icon")),
	}
	require.NoError(t, e.run())
	old := e.link(url)

	e.worker.pending = []pendinglinks.PendingLink{
		inlineLink(e.user.ID, "link-b", url, "2026-07-13T12:00:00.000Z", "png", []byte("new icon")),
	}
	require.NoError(t, e.run())

	now := e.link(url)
	assert.Equal(t, old.ID, now.ID)
	assert.NotEqual(t, old.FaviconPath.String, now.FaviconPath.String)
	assert.Equal(t, sha256Hex([]byte("new icon")), now.FaviconHash.String)
	assert.Equal(t, 2, e.fileCount(), "the old file stays until gc")
}

func TestLinkIngester_RunOnce_ARefreshKeepsNotesAndCreatedAtAndAdvancesUpdatedAt(t *testing.T) {
	e := newLinkEnv(t)
	url := "https://example.com/refresh"
	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-a", UserID: e.user.ID, URL: url, Title: strp("Old title"), SavedAt: "2026-07-12T12:00:00.000Z"},
	}
	require.NoError(t, e.run())
	first := e.link(url)
	_, err := e.queries.SetLinkNotes(context.Background(), db.SetLinkNotesParams{
		Notes: pgtype.Text{String: "why I saved it", Valid: true}, ID: first.ID, UserID: e.user.ID,
	})
	require.NoError(t, err)

	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-b", UserID: e.user.ID, URL: url + "#frag", Title: strp("New title"), SavedAt: "2026-07-13T09:30:00.000Z"},
	}
	require.NoError(t, e.run())

	l := e.link(url)
	assert.Equal(t, first.ID, l.ID)
	assert.Equal(t, "New title", l.Title.String)
	assert.Equal(t, "why I saved it", l.Notes.String)
	assert.True(t, l.CreatedAt.Time.Equal(first.CreatedAt.Time), "created_at stays the first ingest")
	assert.True(t, l.UpdatedAt.Time.After(first.UpdatedAt.Time), "updated_at moves forward, which is what the mirror's checkpoint needs")
}

// A declared favicon that can't be stored must not erase the one the link
// already has, whatever the reason.
func TestLinkIngester_RunOnce_ADeclaredButUnstorableFaviconKeepsTheExistingOne(t *testing.T) {
	cases := []struct {
		name    string
		pending func(userID int64, url string) pendinglinks.PendingLink
		setup   func(e *linkEnv)
	}{
		{
			name: "the R2 object is gone",
			pending: func(userID int64, url string) pendinglinks.PendingLink {
				return pendinglinks.PendingLink{
					ID: "link-b", UserID: userID, URL: url, SavedAt: "2026-07-13T12:00:00.000Z",
					FaviconExt: strp("png"), R2KeyFavicon: strp("pending-links/1/link-b/favicon.png"),
				}
			},
		},
		{
			name: "the inline data isn't valid base64",
			pending: func(userID int64, url string) pendinglinks.PendingLink {
				return pendinglinks.PendingLink{
					ID: "link-b", UserID: userID, URL: url, SavedAt: "2026-07-13T12:00:00.000Z",
					FaviconExt: strp("png"), FaviconData: strp("***"),
				}
			},
		},
		{
			name: "the format isn't one the store accepts",
			pending: func(userID int64, url string) pendinglinks.PendingLink {
				return inlineLink(userID, "link-b", url, "2026-07-13T12:00:00.000Z", "gif", []byte("x"))
			},
		},
		{
			name: "the favicon declares itself but carries nothing",
			pending: func(userID int64, url string) pendinglinks.PendingLink {
				return pendinglinks.PendingLink{
					ID: "link-b", UserID: userID, URL: url, SavedAt: "2026-07-13T12:00:00.000Z", FaviconExt: strp("png"),
				}
			},
		},
		{
			name: "the disk write fails",
			pending: func(userID int64, url string) pendinglinks.PendingLink {
				return inlineLink(userID, "link-b", url, "2026-07-13T12:00:00.000Z", "png", []byte("new icon"))
			},
			setup: func(e *linkEnv) {
				// A store whose root sits under a regular file can't make a directory.
				blocker := filepath.Join(t.TempDir(), "blocker")
				require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
				e.store = linkicons.New(filepath.Join(blocker, "links"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLinkEnv(t)
			url := "https://example.com/keep-favicon"
			e.worker.pending = []pendinglinks.PendingLink{
				inlineLink(e.user.ID, "link-a", url, "2026-07-12T12:00:00.000Z", "png", []byte("the icon to keep")),
			}
			require.NoError(t, e.run())
			before := e.link(url)
			require.True(t, before.FaviconPath.Valid)

			if tc.setup != nil {
				tc.setup(e)
			}
			pl := tc.pending(e.user.ID, url)
			pl.Title = strp("Refreshed title")
			e.worker.pending = []pendinglinks.PendingLink{pl}
			e.worker.fetched = nil
			require.NoError(t, e.run())

			after := e.link(url)
			assert.Equal(t, before.FaviconPath, after.FaviconPath)
			assert.Equal(t, before.FaviconSizeBytes, after.FaviconSizeBytes)
			assert.Equal(t, before.FaviconHash, after.FaviconHash)
			assert.Equal(t, "Refreshed title", after.Title.String, "everything else is still refreshed")
			assert.Equal(t, []string{"link-b"}, e.worker.fetched, "and the row is still marked fetched")
		})
	}
}

func TestLinkIngester_RunOnce_AnUnstorableFaviconOnANewLinkLeavesItWithNone(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.pending = []pendinglinks.PendingLink{{
		ID: "link-1", UserID: e.user.ID, URL: "https://example.com/new", SavedAt: "2026-07-12T12:00:00.000Z",
		FaviconExt: strp("png"), R2KeyFavicon: strp("pending-links/1/link-1/favicon.png"),
	}}
	require.NoError(t, e.run())
	assert.False(t, e.link("https://example.com/new").FaviconPath.Valid)
	assert.Equal(t, []string{"link-1"}, e.worker.fetched)
}

// The retry hazard: a crash after the R2 favicon was deleted but before the
// row was marked fetched. The retry can't find the object, and must not clear
// the favicon the first attempt stored.
func TestLinkIngester_RunOnce_ARetryAfterTheR2ObjectWasDeletedKeepsTheFavicon(t *testing.T) {
	e := newLinkEnv(t)
	icon := []byte("a large favicon that went through R2")
	const key = "pending-links/1/link-r2/favicon.png"
	e.r2.objects[key] = icon
	e.worker.pending = []pendinglinks.PendingLink{{
		ID: "link-r2", UserID: e.user.ID, URL: "https://example.com/crashy", Title: strp("Crashy"),
		FaviconExt: strp("png"), R2KeyFavicon: strp(key), SavedAt: "2026-07-12T12:00:00.000Z",
	}}
	e.worker.failMarkFetchedTimes = 1

	// First attempt: everything lands and the R2 object is deleted, then marking
	// it fetched fails -- as if the agent had died right there.
	require.NoError(t, e.run())
	first := e.link("https://example.com/crashy")
	require.True(t, first.FaviconPath.Valid)
	require.NotContains(t, e.r2.objects, key)
	require.Empty(t, e.worker.fetched)

	// The Worker hands the same row out again.
	require.NoError(t, e.run())

	again := e.link("https://example.com/crashy")
	assert.Equal(t, first.ID, again.ID, "one link, not two")
	assert.Equal(t, first.FaviconPath, again.FaviconPath, "the favicon the first attempt stored is still there")
	assert.Equal(t, first.FaviconHash, again.FaviconHash)
	assert.FileExists(t, filepath.Join(e.root, again.FaviconPath.String))
	assert.Equal(t, []string{"link-r2"}, e.worker.fetched)
}

// With an inline favicon the retry has the bytes again, so it's a plain
// refresh: one link, a fresh favicon directory, the first attempt's left for gc.
func TestLinkIngester_RunOnce_ARetryOfAnInlineSaveRefreshesTheSameLink(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.pending = []pendinglinks.PendingLink{
		inlineLink(e.user.ID, "link-1", "https://example.com/retry", "2026-07-12T12:00:00.000Z", "png", []byte("icon")),
	}
	e.worker.failMarkFetchedTimes = 1

	require.NoError(t, e.run())
	first := e.link("https://example.com/retry")
	require.NoError(t, e.run())
	again := e.link("https://example.com/retry")

	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, sha256Hex([]byte("icon")), again.FaviconHash.String)
	assert.Equal(t, []string{"link-1"}, e.worker.fetched)

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM links").Scan(&n))
	assert.Equal(t, 1, n)
}

// A link is upserted, so when the same URL was saved twice before a poll, the
// Worker's oldest-saved-first order makes the later save the one that sticks.
func TestLinkIngester_RunOnce_TheLaterOfTwoSavesInOneBatchWins(t *testing.T) {
	e := newLinkEnv(t)
	url := "https://example.com/twice"
	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-first", UserID: e.user.ID, URL: url, Title: strp("First"), SavedAt: "2026-07-12T12:00:00.000Z"},
		{ID: "link-second", UserID: e.user.ID, URL: url, Title: strp("Second"), SavedAt: "2026-07-12T12:05:00.000Z"},
	}

	require.NoError(t, e.run())

	l := e.link(url)
	assert.Equal(t, "Second", l.Title.String)
	assert.Equal(t, []string{"link-first", "link-second"}, e.worker.fetched)
}

func TestLinkIngester_RunOnce_URLsThatNormalizeAlikeAreOneLink(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-1", UserID: e.user.ID, URL: "https://Example.com/page?utm_source=a", SavedAt: "2026-07-12T12:00:00.000Z"},
		{ID: "link-2", UserID: e.user.ID, URL: "https://example.com:443/page?utm_campaign=b", SavedAt: "2026-07-12T12:01:00.000Z"},
	}
	require.NoError(t, e.run())

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM links").Scan(&n))
	assert.Equal(t, 1, n)
	e.link("https://example.com/page")
}

func TestLinkIngester_RunOnce_TheSameURLForTwoUsersIsTwoLinks(t *testing.T) {
	e := newLinkEnv(t)
	other := dbtest.CreateUser(t, e.pool, "member")
	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-1", UserID: e.user.ID, URL: "https://example.com/shared", SavedAt: "2026-07-12T12:00:00.000Z"},
		{ID: "link-2", UserID: other.ID, URL: "https://example.com/shared", SavedAt: "2026-07-12T12:00:00.000Z"},
	}
	require.NoError(t, e.run())

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM links").Scan(&n))
	assert.Equal(t, 2, n)
}

func TestLinkIngester_RunOnce_OneBadLinkDoesNotStopTheBatch(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-bad-url", UserID: e.user.ID, URL: "http://[::1", SavedAt: "2026-07-12T12:00:00.000Z"},
		{ID: "link-good", UserID: e.user.ID, URL: "https://example.com/c", SavedAt: "2026-07-12T12:00:00.000Z"},
	}

	require.NoError(t, e.run(), "a failing item never fails the batch")

	e.link("https://example.com/c")

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM links").Scan(&n))
	assert.Equal(t, 1, n, "the link that failed wrote nothing")
	assert.Equal(t, []string{"link-good"}, e.worker.fetched, "a link that failed is left unfetched, to be claimed again")
}

func TestLinkIngester_RunOnce_ARetryAfterAFailedMarkFetchedIsIdempotent(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.pending = []pendinglinks.PendingLink{
		{ID: "link-1", UserID: e.user.ID, URL: "https://example.com/idem", Title: strp("T"), SavedAt: "2026-07-12T12:00:00.000Z"},
	}
	e.worker.failMarkFetchedTimes = 1

	require.NoError(t, e.run())
	assert.Empty(t, e.worker.fetched)
	require.NoError(t, e.run())
	assert.Equal(t, []string{"link-1"}, e.worker.fetched)

	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM links").Scan(&n))
	assert.Equal(t, 1, n)
}

func TestLinkIngester_RunOnce_AFailedR2DeleteIsHarmless(t *testing.T) {
	e := newLinkEnv(t)
	const key = "pending-links/1/link-1/favicon.png"
	e.r2.objects[key] = []byte("icon")
	e.worker.pending = []pendinglinks.PendingLink{{
		ID: "link-1", UserID: e.user.ID, URL: "https://example.com/del", SavedAt: "2026-07-12T12:00:00.000Z",
		FaviconExt: strp("png"), R2KeyFavicon: strp(key),
	}}

	ing := ingest.NewLinkIngester(ingest.LinkParams{
		Queries: e.queries, Worker: e.worker, R2: failingDeleteR2{e.r2}, Store: e.store, Pipeline: newTestPipeline(t),
	})
	require.NoError(t, ing.RunOnce(context.Background()))

	assert.True(t, e.link("https://example.com/del").FaviconPath.Valid)
	assert.Equal(t, []string{"link-1"}, e.worker.fetched)
}

func TestLinkIngester_RunOnce_AWorkerWithoutLinksIsNotAnError(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.claimErr = fmt.Errorf("claiming: %w", pendinglinks.ErrWorkerWithoutLinks)
	assert.NoError(t, e.run())
	assert.Empty(t, e.worker.fetched)
}

func TestLinkIngester_RunOnce_AClaimFailureIsReturned(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.claimErr = errors.New("worker is down")
	err := e.run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker is down")
}

func TestLinkIngester_RunOnce_AnEmptyBatchDoesNothing(t *testing.T) {
	e := newLinkEnv(t)
	require.NoError(t, e.run())
	assert.Empty(t, e.worker.fetched)
}

// An R2 favicon whose body read fails midway is a favicon failure, not a
// link failure.
type brokenBodyR2 struct{ *fakeR2 }

func (brokenBodyR2) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(&failingReader{}), nil
}

type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestLinkIngester_RunOnce_AFaviconBodyThatFailsMidReadStillSavesTheLink(t *testing.T) {
	e := newLinkEnv(t)
	e.worker.pending = []pendinglinks.PendingLink{{
		ID: "link-1", UserID: e.user.ID, URL: "https://example.com/reset", SavedAt: "2026-07-12T12:00:00.000Z",
		FaviconExt: strp("png"), R2KeyFavicon: strp("pending-links/1/link-1/favicon.png"),
	}}

	ing := ingest.NewLinkIngester(ingest.LinkParams{
		Queries: e.queries, Worker: e.worker, R2: brokenBodyR2{e.r2}, Store: e.store, Pipeline: newTestPipeline(t),
	})
	require.NoError(t, ing.RunOnce(context.Background()))

	assert.False(t, e.link("https://example.com/reset").FaviconPath.Valid)
	assert.Equal(t, []string{"link-1"}, e.worker.fetched)
}
