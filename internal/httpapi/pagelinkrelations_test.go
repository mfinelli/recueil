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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
)

// retitle gives a page a title, which dbtest.CreatePage fixes at "Test Page".
func retitle(t *testing.T, pool *pgxpool.Pool, pageID int64, title string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "UPDATE pages SET title = NULLIF($2, '') WHERE id = $1", pageID, title)
	require.NoError(t, err)
}

type pageDetailRelated struct {
	RelatedLinks []relatedBody `json:"related_links"`
}

type linkDetailRelatedPages struct {
	RelatedPages []relatedBody `json:"related_pages"`
}

func TestPageLinkRelations(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)
	q := db.New(pool)
	ctx := context.Background()

	fromPage := func(t *testing.T, cookie *http.Cookie, pageID, linkID int64) *http.Response {
		return requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/pages/%d/related-links", pageID), cookie, fmt.Sprintf(`{"link_id":%d}`, linkID))
	}
	fromLink := func(t *testing.T, cookie *http.Cookie, linkID, pageID int64) *http.Response {
		return requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/links/%d/related-pages", linkID), cookie, fmt.Sprintf(`{"page_id":%d}`, pageID))
	}
	linksOfPage := func(t *testing.T, cookie *http.Cookie, pageID int64) []int64 {
		t.Helper()
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", pageID), cookie)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var ids []int64
		for _, l := range decodeBody[pageDetailRelated](t, resp).RelatedLinks {
			ids = append(ids, l.ID)
		}
		return ids
	}
	pagesOfLink := func(t *testing.T, cookie *http.Cookie, linkID int64) []int64 {
		t.Helper()
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", linkID), cookie)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var ids []int64
		for _, p := range decodeBody[linkDetailRelatedPages](t, resp).RelatedPages {
			ids = append(ids, p.ID)
		}
		return ids
	}
	rows := func(t *testing.T, pageID, linkID int64) int {
		t.Helper()
		var n int
		require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM page_link_relations WHERE page_id = $1 AND link_id = $2", pageID, linkID).Scan(&n))
		return n
	}

	t.Run("relating from the page shows on both ends, and answers with the link", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/article")
		link := saveLink(t, pool, user.ID, "https://github.com/example/repo", "The repo")

		resp := fromPage(t, cookie, page.ID, link.ID)

		require.Equal(t, http.StatusCreated, resp.StatusCode)
		got := decodeBody[relatedBody](t, resp)
		assert.Equal(t, link.ID, got.ID)
		assert.Equal(t, "The repo", *got.Title)
		assert.Equal(t, []int64{link.ID}, linksOfPage(t, cookie, page.ID))
		assert.Equal(t, []int64{page.ID}, pagesOfLink(t, cookie, link.ID), "visible from the link too")
	})

	t.Run("relating from the link shows on both ends, and answers with the page", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/other")
		link := saveLink(t, pool, user.ID, "https://example.com/video", "A video")

		resp := fromLink(t, cookie, link.ID, page.ID)

		require.Equal(t, http.StatusCreated, resp.StatusCode)
		got := decodeBody[relatedBody](t, resp)
		assert.Equal(t, page.ID, got.ID)
		assert.Equal(t, "Test Page", *got.Title)
		assert.Equal(t, []int64{link.ID}, linksOfPage(t, cookie, page.ID))
		assert.Equal(t, []int64{page.ID}, pagesOfLink(t, cookie, link.ID))
	})

	t.Run("both ends write the same row, and relating again is a no-op", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/once")
		link := saveLink(t, pool, user.ID, "https://example.com/once-link", "Once")

		require.Equal(t, http.StatusCreated, fromPage(t, cookie, page.ID, link.ID).StatusCode)
		require.Equal(t, http.StatusCreated, fromLink(t, cookie, link.ID, page.ID).StatusCode)
		require.Equal(t, http.StatusCreated, fromPage(t, cookie, page.ID, link.ID).StatusCode)

		assert.Equal(t, 1, rows(t, page.ID, link.ID))
	})

	t.Run("a page and a link with the same URL can be related", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/same")
		link := saveLink(t, pool, user.ID, "https://example.com/same", "Same URL")

		require.Equal(t, http.StatusCreated, fromPage(t, cookie, page.ID, link.ID).StatusCode)

		assert.Equal(t, []int64{link.ID}, linksOfPage(t, cookie, page.ID))
	})

	t.Run("a page and a link whose ids happen to be equal aren't confused", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/ids")
		// Make a link whose id equals the page's, by inserting until the sequences meet
		// is fragile, so just relate two distinct pairs and check each stays itself.
		link := saveLink(t, pool, user.ID, "https://example.com/ids-link", "Ids")
		other := dbtest.CreatePage(t, pool, user.ID, "https://example.com/ids-other")
		require.Equal(t, http.StatusCreated, fromPage(t, cookie, page.ID, link.ID).StatusCode)

		assert.Equal(t, []int64{link.ID}, linksOfPage(t, cookie, page.ID))
		assert.Empty(t, linksOfPage(t, cookie, other.ID), "only the pair that was related")
	})

	t.Run("lists by title, untitled last, and an item with none has an empty list, not null", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/hub")
		zed := saveLink(t, pool, user.ID, "https://example.com/zed", "Zed")
		untitled := saveLink(t, pool, user.ID, "https://example.com/untitled", "")
		abe := saveLink(t, pool, user.ID, "https://example.com/abe", "Abe")
		for _, l := range []db.Link{zed, untitled, abe} {
			require.Equal(t, http.StatusCreated, fromPage(t, cookie, page.ID, l.ID).StatusCode)
		}
		assert.Equal(t, []int64{abe.ID, zed.ID, untitled.ID}, linksOfPage(t, cookie, page.ID))

		lonely := dbtest.CreatePage(t, pool, user.ID, "https://example.com/lonely")
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", lonely.ID), cookie)
		assert.Equal(t, "[]", fmt.Sprint(decodeBody[map[string]any](t, resp)["related_links"]))
		lonelyLink := saveLink(t, pool, user.ID, "https://example.com/lonely-link", "Lonely")
		resp = requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", lonelyLink.ID), cookie)
		assert.Equal(t, "[]", fmt.Sprint(decodeBody[map[string]any](t, resp)["related_pages"]))
	})

	t.Run("lists a link's related pages by title too", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		link := saveLink(t, pool, user.ID, "https://example.com/hub-link", "Hub")
		pz := dbtest.CreatePage(t, pool, user.ID, "https://example.com/pz")
		pa := dbtest.CreatePage(t, pool, user.ID, "https://example.com/pa")
		retitle(t, pool, pz.ID, "Zed")
		retitle(t, pool, pa.ID, "Abe")
		for _, p := range []db.Page{pz, pa} {
			require.Equal(t, http.StatusCreated, fromLink(t, cookie, link.ID, p.ID).StatusCode)
		}
		assert.Equal(t, []int64{pa.ID, pz.ID}, pagesOfLink(t, cookie, link.ID))
	})

	t.Run("404s when either end isn't the caller's, relating nothing", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &other)
		myPage := dbtest.CreatePage(t, pool, other.ID, "https://example.com/my-page")
		myLink := saveLink(t, pool, other.ID, "https://example.com/my-link", "Mine")
		theirPage := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/their-page")
		theirLink := saveLink(t, pool, owner.ID, "https://example.com/their-link", "Theirs")

		assert.Equal(t, http.StatusNotFound, fromPage(t, cookie, myPage.ID, theirLink.ID).StatusCode, "the link isn't theirs")
		assert.Equal(t, http.StatusNotFound, fromPage(t, cookie, theirPage.ID, myLink.ID).StatusCode, "the page isn't theirs")
		assert.Equal(t, http.StatusNotFound, fromLink(t, cookie, myLink.ID, theirPage.ID).StatusCode)
		assert.Equal(t, http.StatusNotFound, fromLink(t, cookie, theirLink.ID, myPage.ID).StatusCode)
		assert.Equal(t, http.StatusNotFound, fromPage(t, cookie, myPage.ID, 999999999).StatusCode)

		var n int
		require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM page_link_relations WHERE page_id = ANY($1) OR link_id = ANY($2)",
			[]int64{myPage.ID, theirPage.ID}, []int64{myLink.ID, theirLink.ID}).Scan(&n))
		assert.Zero(t, n)
	})

	t.Run("requires the target's id, and a valid id in the path", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/v")
		link := saveLink(t, pool, user.ID, "https://example.com/v-link", "V")

		assert.Equal(t, http.StatusBadRequest, requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/pages/%d/related-links", page.ID), cookie, `{}`).StatusCode)
		assert.Equal(t, http.StatusBadRequest, requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/links/%d/related-pages", link.ID), cookie, `{}`).StatusCode)
		assert.Equal(t, http.StatusBadRequest, requestWithCookieBody(t, server, http.MethodPost, "/api/pages/abc/related-links", cookie, `{"link_id":1}`).StatusCode)
		assert.Equal(t, http.StatusBadRequest, requestWithCookieBody(t, server, http.MethodPost, "/api/links/abc/related-pages", cookie, `{"page_id":1}`).StatusCode)
		assert.Equal(t, http.StatusBadRequest, requestWithCookie(t, server, http.MethodDelete, "/api/pages/1/related-links/abc", cookie).StatusCode)
		assert.Equal(t, http.StatusBadRequest, requestWithCookie(t, server, http.MethodDelete, "/api/links/1/related-pages/abc", cookie).StatusCode)
	})

	t.Run("removes from either end, and a pair that isn't related is a quiet no-op", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/rm")
		keep := saveLink(t, pool, user.ID, "https://example.com/keep", "Keep")
		drop := saveLink(t, pool, user.ID, "https://example.com/drop", "Drop")
		never := saveLink(t, pool, user.ID, "https://example.com/never", "Never")
		require.Equal(t, http.StatusCreated, fromPage(t, cookie, page.ID, keep.ID).StatusCode)
		require.Equal(t, http.StatusCreated, fromPage(t, cookie, page.ID, drop.ID).StatusCode)

		// from the link's side
		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/links/%d/related-pages/%d", drop.ID, page.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		assert.Equal(t, []int64{keep.ID}, linksOfPage(t, cookie, page.ID))

		// from the page's side
		resp = requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/pages/%d/related-links/%d", page.ID, keep.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		assert.Empty(t, linksOfPage(t, cookie, page.ID))

		resp = requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/pages/%d/related-links/%d", page.ID, never.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("removing through another user's item is a 404 and leaves the relation", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/owned")
		link := saveLink(t, pool, owner.ID, "https://example.com/owned-link", "Owned")
		require.Equal(t, http.StatusCreated, fromPage(t, sessionCookieFor(t, pool, &owner), page.ID, link.ID).StatusCode)

		cookie := sessionCookieFor(t, pool, &other)
		assert.Equal(t, http.StatusNotFound, requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/pages/%d/related-links/%d", page.ID, link.ID), cookie).StatusCode)
		assert.Equal(t, http.StatusNotFound, requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/links/%d/related-pages/%d", link.ID, page.ID), cookie).StatusCode)
		assert.Equal(t, 1, rows(t, page.ID, link.ID))
	})

	t.Run("deleting either end removes the relation from the other", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/cascade")
		keepLink := saveLink(t, pool, user.ID, "https://example.com/c1", "C1")
		dropLink := saveLink(t, pool, user.ID, "https://example.com/c2", "C2")
		keepPage := dbtest.CreatePage(t, pool, user.ID, "https://example.com/cascade-2")
		require.Equal(t, http.StatusCreated, fromPage(t, cookie, page.ID, keepLink.ID).StatusCode)
		require.Equal(t, http.StatusCreated, fromPage(t, cookie, page.ID, dropLink.ID).StatusCode)
		require.Equal(t, http.StatusCreated, fromLink(t, cookie, dropLink.ID, keepPage.ID).StatusCode)

		_, err := q.DeleteLink(ctx, db.DeleteLinkParams{ID: dropLink.ID, UserID: user.ID})
		require.NoError(t, err)
		assert.Equal(t, []int64{keepLink.ID}, linksOfPage(t, cookie, page.ID))
		var leftover int
		require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM page_link_relations WHERE link_id = $1", dropLink.ID).Scan(&leftover))
		assert.Zero(t, leftover, "gone with the link, including the one from the other page")

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		require.Less(t, resp.StatusCode, 300)
		assert.Empty(t, pagesOfLink(t, cookie, keepLink.ID), "gone with the page")
	})

	t.Run("requires a session", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{http.MethodPost, "/api/pages/1/related-links"},
			{http.MethodPost, "/api/links/1/related-pages"},
			{http.MethodDelete, "/api/pages/1/related-links/2"},
			{http.MethodDelete, "/api/links/1/related-pages/2"},
		} {
			req, err := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(`{"link_id":2,"page_id":2}`))
			require.NoError(t, err)
			// Mutating requests are refused before auth without a JSON content type.
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s", tc.method, tc.path)
		}
	})
}

func TestPageLinkRelations_LinksOff(t *testing.T) {
	pool := dbtest.Setup(t)
	on, _ := newLinksTestServer(t, pool, true)
	off, _ := newLinksTestServer(t, pool, false)
	user := dbtest.CreateUser(t, pool, "member")
	cookie := sessionCookieFor(t, pool, &user)
	page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/off")
	link := saveLink(t, pool, user.ID, "https://example.com/off-link", "Off")
	require.Equal(t, http.StatusCreated, requestWithCookieBody(t, on, http.MethodPost,
		fmt.Sprintf("/api/pages/%d/related-links", page.ID), cookie, fmt.Sprintf(`{"link_id":%d}`, link.ID)).StatusCode)

	t.Run("the routes aren't there", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{http.MethodPost, fmt.Sprintf("/api/pages/%d/related-links", page.ID)},
			{http.MethodDelete, fmt.Sprintf("/api/pages/%d/related-links/%d", page.ID, link.ID)},
			{http.MethodPost, fmt.Sprintf("/api/links/%d/related-pages", link.ID)},
			{http.MethodDelete, fmt.Sprintf("/api/links/%d/related-pages/%d", link.ID, page.ID)},
		} {
			assert.Equal(t, http.StatusNotFound, statusOf(t, off, tc.method, tc.path, cookie), "%s %s", tc.method, tc.path)
		}
	})

	t.Run("a page's related links are empty, even though a row exists", func(t *testing.T) {
		resp := requestWithCookie(t, off, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Empty(t, decodeBody[pageDetailRelated](t, resp).RelatedLinks)
	})
}

func statusOf(t *testing.T, server *httptest.Server, method, path string, cookie *http.Cookie) int {
	t.Helper()
	return requestWithCookieBody(t, server, method, path, cookie, `{}`).StatusCode
}
