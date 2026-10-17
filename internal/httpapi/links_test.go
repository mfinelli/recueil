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

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/httplog/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/devices"
	"github.com/mfinelli/recueil/internal/httpapi"
	"github.com/mfinelli/recueil/internal/linkicons"
	"github.com/mfinelli/recueil/internal/mirror"
	"github.com/mfinelli/recueil/internal/pendingcaptures"
	"github.com/mfinelli/recueil/internal/queueitems"
)

// newLinksTestServer is newTestServer's twin for the links API: it builds the
// server with the feature switched as asked and, when it's on, a real link
// favicon store the test can write into. (newTestServer itself always has links
// on but no link store, which is all the other ~40 tests need.)
func newLinksTestServer(t *testing.T, pool *pgxpool.Pool, linksEnabled bool) (*httptest.Server, *linkicons.Store) {
	t.Helper()
	q := db.New(pool)
	bootstrap, _, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)

	var linkStore *linkicons.Store
	if linksEnabled {
		linkStore = linkicons.New(t.TempDir())
	}
	s := httpapi.NewServer(q, pool, archive.New(t.TempDir()), mirror.NewClient(unreachable, "test-secret"),
		devices.NewClient(unreachable, "test-secret"), queueitems.NewClient(unreachable, "test-secret"),
		pendingcaptures.NewClient(unreachable, "test-secret"), bootstrap, false, testPairingKey(t), true,
		"test-readability-version", "test-ai-model", "https://worker.test.example", testPipeline(t),
		testManualUploadMaxBytes, linksEnabled, linkStore)
	logger := httplog.NewLogger("recueil-test")
	logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{}, nil)
	require.NoError(t, err)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, linkStore
}

func decodeBody[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var v T
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	return v
}

type linkBody struct {
	ID             int64   `json:"id"`
	RawURL         string  `json:"raw_url"`
	NormalizedURL  string  `json:"normalized_url"`
	Title          *string `json:"title"`
	FaviconPath    *string `json:"favicon_path"`
	Notes          *string `json:"notes"`
	MatchingPageID *int64  `json:"matching_page_id"`
}

type linkListBody struct {
	Links []linkBody `json:"links"`
	Total int64      `json:"total"`
}

type linkDetailBody struct {
	linkBody
	MatchingPageTitle *string `json:"matching_page_title"`
	Tags              []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"tags"`
	Collections []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		ParentID *int64 `json:"parent_id"`
	} `json:"collections"`
}

func saveLink(t *testing.T, pool *pgxpool.Pool, userID int64, url, title string) db.Link {
	t.Helper()
	link, err := db.New(pool).UpsertLink(context.Background(), db.UpsertLinkParams{
		UserID: userID, RawUrl: url, NormalizedUrl: url,
		Title: pgtype.Text{String: title, Valid: title != ""},
	})
	require.NoError(t, err)
	return link
}

func TestListLinks(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)

	t.Run("an empty list is [] and 0, not null", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		resp := requestWithCookie(t, server, http.MethodGet, "/api/links", sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"links":[],"total":0}`, string(body))
	})

	t.Run("newest first, only the caller's, with the full total", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		saveLink(t, pool, other.ID, "https://example.com/not-mine", "Not mine")
		a := saveLink(t, pool, user.ID, "https://example.com/a", "A")
		b := saveLink(t, pool, user.ID, "https://example.com/b", "B")
		c := saveLink(t, pool, user.ID, "https://example.com/c", "C")
		cookie := sessionCookieFor(t, pool, &user)

		got := decodeBody[linkListBody](t, requestWithCookie(t, server, http.MethodGet, "/api/links", cookie))
		assert.Equal(t, int64(3), got.Total)
		require.Len(t, got.Links, 3)
		assert.Equal(t, []int64{c.ID, b.ID, a.ID}, []int64{got.Links[0].ID, got.Links[1].ID, got.Links[2].ID})
		assert.Equal(t, "https://example.com/c", got.Links[0].RawURL)

		page := decodeBody[linkListBody](t, requestWithCookie(t, server, http.MethodGet, "/api/links?limit=1&offset=1", cookie))
		require.Len(t, page.Links, 1)
		assert.Equal(t, b.ID, page.Links[0].ID)
		assert.Equal(t, int64(3), page.Total, "total is the whole set, not the page")
	})

	t.Run("carries the same-URL hint when an archived page has the same URL", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/both")
		saveLink(t, pool, user.ID, "https://example.com/both", "Both")
		saveLink(t, pool, user.ID, "https://example.com/only-a-link", "Only")

		got := decodeBody[linkListBody](t, requestWithCookie(t, server, http.MethodGet, "/api/links", sessionCookieFor(t, pool, &user)))
		require.Len(t, got.Links, 2)
		byURL := map[string]linkBody{}
		for _, l := range got.Links {
			byURL[l.NormalizedURL] = l
		}
		require.NotNil(t, byURL["https://example.com/both"].MatchingPageID)
		assert.Equal(t, page.ID, *byURL["https://example.com/both"].MatchingPageID)
		assert.Nil(t, byURL["https://example.com/only-a-link"].MatchingPageID)
	})

	t.Run("another user's page with the same URL is no hint", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		dbtest.CreatePage(t, pool, other.ID, "https://example.com/theirs")
		saveLink(t, pool, user.ID, "https://example.com/theirs", "Mine")

		got := decodeBody[linkListBody](t, requestWithCookie(t, server, http.MethodGet, "/api/links", sessionCookieFor(t, pool, &user)))
		require.Len(t, got.Links, 1)
		assert.Nil(t, got.Links[0].MatchingPageID)
	})

	t.Run("requires a session", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/links")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestListLinks_Search(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)
	user := dbtest.CreateUser(t, pool, "member")
	cookie := sessionCookieFor(t, pool, &user)
	q := db.New(pool)

	byTitle := saveLink(t, pool, user.ID, "https://example.com/one", "Rust Ownership Explained")
	byURL := saveLink(t, pool, user.ID, "https://github.com/rust-lang/rust", "Some repo")
	byNotes := saveLink(t, pool, user.ID, "https://example.com/three", "Three")
	_, err := q.SetLinkNotes(context.Background(), db.SetLinkNotesParams{
		Notes: pgtype.Text{String: "good talk about rust async", Valid: true}, ID: byNotes.ID, UserID: user.ID,
	})
	require.NoError(t, err)
	noMatch := saveLink(t, pool, user.ID, "https://example.com/four", "Cooking pasta")
	percent := saveLink(t, pool, user.ID, "https://example.com/five", "100% organic")
	underscore := saveLink(t, pool, user.ID, "https://example.com/six", "snake_case names")

	search := func(query string) []int64 {
		resp := requestWithCookie(t, server, http.MethodGet, "/api/links?q="+query, cookie)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var ids []int64
		for _, l := range decodeBody[linkListBody](t, resp).Links {
			ids = append(ids, l.ID)
		}
		return ids
	}

	t.Run("matches the title, the URL and the notes, case-insensitively", func(t *testing.T) {
		assert.ElementsMatch(t, []int64{byTitle.ID, byURL.ID, byNotes.ID}, search("RUST"))
	})

	t.Run("a search with no hits is an empty list", func(t *testing.T) {
		resp := requestWithCookie(t, server, http.MethodGet, "/api/links?q=zzzzzz", cookie)
		got := decodeBody[linkListBody](t, resp)
		assert.Empty(t, got.Links)
		assert.Equal(t, int64(0), got.Total)
	})

	t.Run("LIKE's own wildcards match literally", func(t *testing.T) {
		assert.Equal(t, []int64{percent.ID}, search("100%25"), "a lone % is not a wildcard")
		assert.Equal(t, []int64{underscore.ID}, search("snake_case"), "_ is not a single-character wildcard")
		assert.Empty(t, search("sna_e"), "_ must not match an arbitrary character")
		assert.NotContains(t, search("%25"), noMatch.ID, "searching for % must not match everything")
	})

	t.Run("a blank query is the plain listing", func(t *testing.T) {
		assert.Len(t, search("%20%20"), 6)
	})

	t.Run("paginates a search and reports the full total", func(t *testing.T) {
		resp := requestWithCookie(t, server, http.MethodGet, "/api/links?q=rust&limit=2", cookie)
		got := decodeBody[linkListBody](t, resp)
		assert.Len(t, got.Links, 2)
		assert.Equal(t, int64(3), got.Total)
	})

	t.Run("never matches another user's links", func(t *testing.T) {
		other := dbtest.CreateUser(t, pool, "member")
		saveLink(t, pool, other.ID, "https://example.com/rust-theirs", "Rust theirs")
		assert.ElementsMatch(t, []int64{byTitle.ID, byURL.ID, byNotes.ID}, search("rust"))
	})
}

func TestGetLink(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)
	q := db.New(pool)

	t.Run("returns the link with its tags, collections and same-URL hint", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/detail")
		link := saveLink(t, pool, user.ID, "https://example.com/detail", "Detail")
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "video", Slug: "video"})
		require.NoError(t, err)
		parent, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{UserID: user.ID, Name: "Watch", Slug: "watch"})
		require.NoError(t, err)
		require.NoError(t, q.AddLinkTag(context.Background(), db.AddLinkTagParams{LinkID: link.ID, TagID: tag.ID}))
		require.NoError(t, q.AddLinkToCollection(context.Background(), db.AddLinkToCollectionParams{LinkID: link.ID, CollectionID: parent.ID}))

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", link.ID), sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[linkDetailBody](t, resp)

		assert.Equal(t, link.ID, got.ID)
		assert.Equal(t, "Detail", *got.Title)
		require.NotNil(t, got.MatchingPageID)
		assert.Equal(t, page.ID, *got.MatchingPageID)
		require.NotNil(t, got.MatchingPageTitle)
		assert.Equal(t, "Test Page", *got.MatchingPageTitle, "the callout names the page")
		require.Len(t, got.Tags, 1)
		assert.Equal(t, "video", got.Tags[0].Name)
		require.Len(t, got.Collections, 1)
		assert.Equal(t, "Watch", got.Collections[0].Name)
		assert.Nil(t, got.Collections[0].ParentID)
	})

	t.Run("a matching page with no title has a null title, not an empty one", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page, err := q.UpsertPage(context.Background(), db.UpsertPageParams{
			UserID: user.ID, NormalizedUrl: "https://example.com/untitled-page",
			LatestCaptureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		})
		require.NoError(t, err)
		link := saveLink(t, pool, user.ID, "https://example.com/untitled-page", "Titled link")

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", link.ID), sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[linkDetailBody](t, resp)
		require.NotNil(t, got.MatchingPageID)
		assert.Equal(t, page.ID, *got.MatchingPageID)
		assert.Nil(t, got.MatchingPageTitle)
	})

	t.Run("a link with nothing attached has empty arrays, not null", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/bare", "")
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", link.ID), sessionCookieFor(t, pool, &user))
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"tags":[]`)
		assert.Contains(t, string(body), `"collections":[]`)
		assert.Contains(t, string(body), `"title":null`)
		assert.Contains(t, string(body), `"matching_page_id":null`)
		assert.Contains(t, string(body), `"matching_page_title":null`)
	})

	t.Run("404s a link that isn't the caller's, or doesn't exist", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, owner.ID, "https://example.com/private", "Private")
		cookie := sessionCookieFor(t, pool, &other)
		for _, id := range []string{fmt.Sprint(link.ID), "999999999"} {
			resp := requestWithCookie(t, server, http.MethodGet, "/api/links/"+id, cookie)
			assert.Equal(t, http.StatusNotFound, resp.StatusCode, id)
		}
	})

	t.Run("400s an id that isn't a number", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		resp := requestWithCookie(t, server, http.MethodGet, "/api/links/abc", sessionCookieFor(t, pool, &user))
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestPatchLink(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)

	patch := func(t *testing.T, cookie *http.Cookie, id int64, body string) *http.Response {
		return requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/links/%d", id), cookie, body)
	}

	t.Run("sets notes, trimmed, and clears them with an empty value", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/notes", "Notes")
		cookie := sessionCookieFor(t, pool, &user)

		resp := patch(t, cookie, link.ID, `{"notes":"  why I saved this \n"}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[linkBody](t, resp)
		require.NotNil(t, got.Notes)
		assert.Equal(t, "why I saved this", *got.Notes)

		resp = patch(t, cookie, link.ID, `{"notes":"   "}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Nil(t, decodeBody[linkBody](t, resp).Notes, "blank clears the note to NULL")
	})

	t.Run("sets a title, trimmed, until the link's next save replaces it", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/retitle", "From the tab")
		cookie := sessionCookieFor(t, pool, &user)

		resp := patch(t, cookie, link.ID, `{"title":"  My own title \n"}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[linkBody](t, resp)
		require.NotNil(t, got.Title)
		assert.Equal(t, "My own title", *got.Title)

		// No override is tracked: the next save simply replaces it, as a
		// recapture does a page's.
		again := saveLink(t, pool, user.ID, "https://example.com/retitle", "Fresh from the tab")
		assert.Equal(t, link.ID, again.ID)
		assert.Equal(t, "Fresh from the tab", again.Title.String)
	})

	t.Run("an edit moves updated_at, which the bookmark mirror's checkpoint reads", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/checkpoint", "Before")
		cookie := sessionCookieFor(t, pool, &user)

		require.Equal(t, http.StatusOK, patch(t, cookie, link.ID, `{"title":"After"}`).StatusCode)
		after, err := db.New(pool).GetLinkByIDForUser(context.Background(), db.GetLinkByIDForUserParams{ID: link.ID, UserID: user.ID})
		require.NoError(t, err)
		assert.True(t, after.UpdatedAt.Time.After(link.UpdatedAt.Time))
	})

	t.Run("rejects an empty title, as for pages, leaving the old one", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/blank", "Keep me")
		cookie := sessionCookieFor(t, pool, &user)

		assert.Equal(t, http.StatusBadRequest, patch(t, cookie, link.ID, `{"title":""}`).StatusCode)
		assert.Equal(t, http.StatusBadRequest, patch(t, cookie, link.ID, `{"title":"   "}`).StatusCode)
		after, err := db.New(pool).GetLinkByIDForUser(context.Background(), db.GetLinkByIDForUserParams{ID: link.ID, UserID: user.ID})
		require.NoError(t, err)
		assert.Equal(t, "Keep me", after.Title.String)
	})

	t.Run("applies a title and notes sent together", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/both-fields", "Old")
		cookie := sessionCookieFor(t, pool, &user)

		resp := patch(t, cookie, link.ID, `{"title":"New","notes":"a note"}`)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[linkBody](t, resp)
		assert.Equal(t, "New", *got.Title)
		assert.Equal(t, "a note", *got.Notes)
	})

	t.Run("requires title or notes, and valid JSON", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/nothing", "Original")
		cookie := sessionCookieFor(t, pool, &user)

		assert.Equal(t, http.StatusBadRequest, patch(t, cookie, link.ID, `{}`).StatusCode)
		assert.Equal(t, http.StatusBadRequest, patch(t, cookie, link.ID, `{"favicon_path":"x"}`).StatusCode,
			"nothing but title and notes is editable")
		assert.Equal(t, http.StatusBadRequest, patch(t, cookie, link.ID, `not json`).StatusCode)
	})

	t.Run("404s another user's link and leaves it alone", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, owner.ID, "https://example.com/theirs", "Theirs")

		cookie := sessionCookieFor(t, pool, &other)
		assert.Equal(t, http.StatusNotFound, patch(t, cookie, link.ID, `{"notes":"hijack"}`).StatusCode)
		assert.Equal(t, http.StatusNotFound, patch(t, cookie, link.ID, `{"title":"Hijacked"}`).StatusCode)

		after, err := db.New(pool).GetLinkByIDForUser(context.Background(), db.GetLinkByIDForUserParams{ID: link.ID, UserID: owner.ID})
		require.NoError(t, err)
		assert.False(t, after.Notes.Valid)
		assert.Equal(t, "Theirs", after.Title.String)
	})
}

func TestDeleteLink(t *testing.T) {
	pool := dbtest.Setup(t)
	server, store := newLinksTestServer(t, pool, true)
	q := db.New(pool)

	t.Run("deletes the link and its memberships, but not the tag, and leaves the favicon for gc", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		fav, err := store.Write([]byte("icon"), "png")
		require.NoError(t, err)
		link, err := q.UpsertLink(context.Background(), db.UpsertLinkParams{
			UserID: user.ID, RawUrl: "https://example.com/doomed", NormalizedUrl: "https://example.com/doomed",
			FaviconPath: pgtype.Text{String: fav.Path, Valid: true},
		})
		require.NoError(t, err)
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "t", Slug: "t"})
		require.NoError(t, err)
		require.NoError(t, q.AddLinkTag(context.Background(), db.AddLinkTagParams{LinkID: link.ID, TagID: tag.ID}))

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/links/%d", link.ID), sessionCookieFor(t, pool, &user))
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		_, err = q.GetLinkByIDForUser(context.Background(), db.GetLinkByIDForUserParams{ID: link.ID, UserID: user.ID})
		assert.Error(t, err)
		tagLinks, err := q.ListTagLinks(context.Background(), tag.ID)
		require.NoError(t, err)
		assert.Empty(t, tagLinks)
		_, err = q.GetTagByID(context.Background(), db.GetTagByIDParams{ID: tag.ID, UserID: user.ID})
		assert.NoError(t, err, "the tag itself survives")

		rc, err := store.Archive().Open(fav.Path)
		require.NoError(t, err, "the favicon file is gc's to reclaim, not this request's")
		_ = rc.Close()
	})

	t.Run("404s another user's link and leaves it in place", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, owner.ID, "https://example.com/safe", "Safe")

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/links/%d", link.ID), sessionCookieFor(t, pool, &other))
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		_, err := q.GetLinkByIDForUser(context.Background(), db.GetLinkByIDForUserParams{ID: link.ID, UserID: owner.ID})
		assert.NoError(t, err)
	})

	t.Run("404s a link that's already gone", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		resp := requestWithCookie(t, server, http.MethodDelete, "/api/links/999999999", sessionCookieFor(t, pool, &user))
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestGetLinkFavicon(t *testing.T) {
	pool := dbtest.Setup(t)
	server, store := newLinksTestServer(t, pool, true)
	q := db.New(pool)

	withFavicon := func(t *testing.T, userID int64, url, ext string, data []byte) db.Link {
		fav, err := store.Write(data, ext)
		require.NoError(t, err)
		link, err := q.UpsertLink(context.Background(), db.UpsertLinkParams{
			UserID: userID, RawUrl: url, NormalizedUrl: url,
			FaviconPath: pgtype.Text{String: fav.Path, Valid: true},
		})
		require.NoError(t, err)
		return link
	}

	t.Run("serves a png as it is", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		icon := []byte("\x89PNG pretend")
		link := withFavicon(t, user.ID, "https://example.com/png", "png", icon)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d/favicon", link.ID), sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, icon, got)
	})

	t.Run("serves an svg decompressed, with the right content type", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		svg := []byte(strings.Repeat("<svg>fake favicon</svg>", 20))
		link := withFavicon(t, user.ID, "https://example.com/svg", "svg", svg)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d/favicon", link.ID), sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "image/svg+xml", resp.Header.Get("Content-Type"))
		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, svg, got, "must be decompressed, not the raw zstd bytes")
	})

	t.Run("a link with no favicon is a 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/none", "None")
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d/favicon", link.ID), sessionCookieFor(t, pool, &user))
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("another user's link is a 404, not their favicon", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := withFavicon(t, owner.ID, "https://example.com/theirs", "png", []byte("theirs"))
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d/favicon", link.ID), sessionCookieFor(t, pool, &other))
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestLinkTags(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)
	q := db.New(pool)

	add := func(t *testing.T, cookie *http.Cookie, linkID int64, name string) *http.Response {
		return requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/links/%d/tags", linkID), cookie, fmt.Sprintf(`{"name":%q}`, name))
	}

	t.Run("adds a tag by name, reusing the tag a page already has", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/tagged-page")
		link := saveLink(t, pool, user.ID, "https://example.com/tagged-link", "Link")
		cookie := sessionCookieFor(t, pool, &user)

		pageResp := requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/pages/%d/tags", page.ID), cookie, `{"name":"Reading List"}`)
		require.Equal(t, http.StatusCreated, pageResp.StatusCode)
		pageTag := decodeBody[struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
		}](t, pageResp)

		linkResp := add(t, cookie, link.ID, "Reading List")
		require.Equal(t, http.StatusCreated, linkResp.StatusCode)
		linkTag := decodeBody[struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}](t, linkResp)

		assert.Equal(t, pageTag.ID, linkTag.ID, "one tag spans pages and links")
		assert.Equal(t, "Reading List", linkTag.Name)
		assert.Equal(t, pageTag.Slug, linkTag.Slug)

		tags, err := q.ListLinkTags(context.Background(), link.ID)
		require.NoError(t, err)
		require.Len(t, tags, 1)
	})

	t.Run("adding the same tag twice is fine", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/twice", "Twice")
		cookie := sessionCookieFor(t, pool, &user)
		require.Equal(t, http.StatusCreated, add(t, cookie, link.ID, "dup").StatusCode)
		require.Equal(t, http.StatusCreated, add(t, cookie, link.ID, "dup").StatusCode)
		tags, err := q.ListLinkTags(context.Background(), link.ID)
		require.NoError(t, err)
		assert.Len(t, tags, 1)
	})

	t.Run("rejects a missing name, and a name with no usable slug", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/badtag", "Bad")
		cookie := sessionCookieFor(t, pool, &user)
		assert.Equal(t, http.StatusBadRequest, add(t, cookie, link.ID, "").StatusCode)
		assert.Equal(t, http.StatusBadRequest, add(t, cookie, link.ID, "!!!").StatusCode)
	})

	t.Run("404s another user's link, adding nothing", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, owner.ID, "https://example.com/not-yours", "Not yours")
		resp := add(t, sessionCookieFor(t, pool, &other), link.ID, "sneaky")
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		tags, err := q.ListLinkTags(context.Background(), link.ID)
		require.NoError(t, err)
		assert.Empty(t, tags)
	})

	t.Run("removes a tag and leaves the others", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/untag", "Untag")
		cookie := sessionCookieFor(t, pool, &user)
		first := decodeBody[tagBody](t, add(t, cookie, link.ID, "first"))
		decodeBody[tagBody](t, add(t, cookie, link.ID, "second"))

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/links/%d/tags/%d", link.ID, first.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		tags, err := q.ListLinkTags(context.Background(), link.ID)
		require.NoError(t, err)
		require.Len(t, tags, 1)
		assert.Equal(t, "second", tags[0].Name)
	})

	t.Run("removing from another user's link is a 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, owner.ID, "https://example.com/theirs-tag", "Theirs")
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: owner.ID, Name: "keep", Slug: "keep"})
		require.NoError(t, err)
		require.NoError(t, q.AddLinkTag(context.Background(), db.AddLinkTagParams{LinkID: link.ID, TagID: tag.ID}))

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/links/%d/tags/%d", link.ID, tag.ID), sessionCookieFor(t, pool, &other))
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		tags, err := q.ListLinkTags(context.Background(), link.ID)
		require.NoError(t, err)
		assert.Len(t, tags, 1)
	})
}

type tagBody struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func TestLinkCollections(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)
	q := db.New(pool)

	createCollection := func(t *testing.T, userID int64, name, slug string) db.Collection {
		c, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{UserID: userID, Name: name, Slug: slug})
		require.NoError(t, err)
		return c
	}
	add := func(t *testing.T, cookie *http.Cookie, linkID, collectionID int64) *http.Response {
		return requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/links/%d/collections", linkID), cookie,
			fmt.Sprintf(`{"collection_id":%d}`, collectionID))
	}

	t.Run("adds a link to a collection, idempotently", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/filed", "Filed")
		col := createCollection(t, user.ID, "Watch later", "watch-later")
		cookie := sessionCookieFor(t, pool, &user)

		assert.Equal(t, http.StatusNoContent, add(t, cookie, link.ID, col.ID).StatusCode)
		assert.Equal(t, http.StatusNoContent, add(t, cookie, link.ID, col.ID).StatusCode)
		cols, err := q.ListLinkCollections(context.Background(), link.ID)
		require.NoError(t, err)
		assert.Len(t, cols, 1)
	})

	t.Run("requires a collection_id, and 404s one that doesn't exist or isn't the caller's", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/cols", "Cols")
		theirs := createCollection(t, other.ID, "Theirs", "theirs")
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/links/%d/collections", link.ID), cookie, `{}`)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Equal(t, http.StatusNotFound, add(t, cookie, link.ID, 999999999).StatusCode)
		assert.Equal(t, http.StatusNotFound, add(t, cookie, link.ID, theirs.ID).StatusCode)

		cols, err := q.ListLinkCollections(context.Background(), link.ID)
		require.NoError(t, err)
		assert.Empty(t, cols)
	})

	t.Run("404s another user's link", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, owner.ID, "https://example.com/not-mine", "Not mine")
		mine := createCollection(t, other.ID, "Mine", "mine")
		assert.Equal(t, http.StatusNotFound, add(t, sessionCookieFor(t, pool, &other), link.ID, mine.ID).StatusCode)
	})

	t.Run("removes a link from one collection and leaves the others", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/remove", "Remove")
		a := createCollection(t, user.ID, "A", "a")
		b := createCollection(t, user.ID, "B", "b")
		cookie := sessionCookieFor(t, pool, &user)
		require.Equal(t, http.StatusNoContent, add(t, cookie, link.ID, a.ID).StatusCode)
		require.Equal(t, http.StatusNoContent, add(t, cookie, link.ID, b.ID).StatusCode)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/links/%d/collections/%d", link.ID, a.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		cols, err := q.ListLinkCollections(context.Background(), link.ID)
		require.NoError(t, err)
		require.Len(t, cols, 1)
		assert.Equal(t, "B", cols[0].Name)
	})

	t.Run("removing from another user's link is a 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, owner.ID, "https://example.com/theirs-col", "Theirs")
		col := createCollection(t, owner.ID, "Keep", "keep")
		require.NoError(t, q.AddLinkToCollection(context.Background(), db.AddLinkToCollectionParams{LinkID: link.ID, CollectionID: col.ID}))

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/links/%d/collections/%d", link.ID, col.ID), sessionCookieFor(t, pool, &other))
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		cols, err := q.ListLinkCollections(context.Background(), link.ID)
		require.NoError(t, err)
		assert.Len(t, cols, 1)
	})
}

func TestPageDetail_MatchingLinkID(t *testing.T) {
	pool := dbtest.Setup(t)

	type pageDetail struct {
		MatchingLinkID    *int64  `json:"matching_link_id"`
		MatchingLinkTitle *string `json:"matching_link_title"`
	}

	t.Run("points at the link with the same normalized URL", func(t *testing.T) {
		server, _ := newLinksTestServer(t, pool, true)
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/both-ways")
		link := saveLink(t, pool, user.ID, "https://example.com/both-ways", "Both")

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[pageDetail](t, resp)
		require.NotNil(t, got.MatchingLinkID)
		assert.Equal(t, link.ID, *got.MatchingLinkID)
		require.NotNil(t, got.MatchingLinkTitle)
		assert.Equal(t, "Both", *got.MatchingLinkTitle, "the callout names the link")
	})

	t.Run("a matching link with no title has a null title", func(t *testing.T) {
		server, _ := newLinksTestServer(t, pool, true)
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/untitled-link")
		link := saveLink(t, pool, user.ID, "https://example.com/untitled-link", "")

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[pageDetail](t, resp)
		require.NotNil(t, got.MatchingLinkID)
		assert.Equal(t, link.ID, *got.MatchingLinkID)
		assert.Nil(t, got.MatchingLinkTitle)
	})

	t.Run("is null without a matching link, or when the link is another user's", func(t *testing.T) {
		server, _ := newLinksTestServer(t, pool, true)
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/lonely")
		saveLink(t, pool, other.ID, "https://example.com/lonely", "Theirs")

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[pageDetail](t, resp)
		assert.Nil(t, got.MatchingLinkID)
		assert.Nil(t, got.MatchingLinkTitle)
	})

	t.Run("is always null with links off, even if a link row exists", func(t *testing.T) {
		server, _ := newLinksTestServer(t, pool, false)
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/off")
		saveLink(t, pool, user.ID, "https://example.com/off", "Off")

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), sessionCookieFor(t, pool, &user))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[pageDetail](t, resp)
		assert.Nil(t, got.MatchingLinkID)
		assert.Nil(t, got.MatchingLinkTitle)
	})
}

func TestLinksRoutes_AreNotRoutedWithLinksOff(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, false)
	user := dbtest.CreateUser(t, pool, "member")
	link := saveLink(t, pool, user.ID, "https://example.com/off", "Off")
	cookie := sessionCookieFor(t, pool, &user)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/links"},
		{http.MethodGet, fmt.Sprintf("/api/links/%d", link.ID)},
		{http.MethodPatch, fmt.Sprintf("/api/links/%d", link.ID)},
		{http.MethodDelete, fmt.Sprintf("/api/links/%d", link.ID)},
		{http.MethodGet, fmt.Sprintf("/api/links/%d/favicon", link.ID)},
		{http.MethodPost, fmt.Sprintf("/api/links/%d/tags", link.ID)},
		{http.MethodDelete, fmt.Sprintf("/api/links/%d/tags/1", link.ID)},
		{http.MethodPost, fmt.Sprintf("/api/links/%d/collections", link.ID)},
		{http.MethodDelete, fmt.Sprintf("/api/links/%d/collections/1", link.ID)},
	} {
		resp := requestWithCookieBody(t, server, tc.method, tc.path, cookie, `{}`)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "%s %s", tc.method, tc.path)
	}

	// And nothing was touched.
	_, err := db.New(pool).GetLinkByIDForUser(context.Background(), db.GetLinkByIDForUserParams{ID: link.ID, UserID: user.ID})
	assert.NoError(t, err)
}
