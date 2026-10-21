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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
)

type relatedBody struct {
	ID            int64   `json:"id"`
	Title         *string `json:"title"`
	NormalizedURL string  `json:"normalized_url"`
	FaviconPath   *string `json:"favicon_path"`
}

type linkWithRelated struct {
	Links []relatedBody `json:"links"`
}

func TestLinkLinks(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)
	q := db.New(pool)
	ctx := context.Background()

	relate := func(t *testing.T, cookie *http.Cookie, id, other int64) *http.Response {
		return requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/links/%d/links", id), cookie,
			fmt.Sprintf(`{"related_link_id":%d}`, other))
	}
	unrelate := func(t *testing.T, cookie *http.Cookie, id, other int64) *http.Response {
		return requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/links/%d/links/%d", id, other), cookie)
	}
	relatedOf := func(t *testing.T, cookie *http.Cookie, id int64) []int64 {
		t.Helper()
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", id), cookie)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var ids []int64
		for _, l := range decodeBody[linkWithRelated](t, resp).Links {
			ids = append(ids, l.ID)
		}
		return ids
	}

	t.Run("relates two links, shown on both ends, and answers with the other link", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		a := saveLink(t, pool, user.ID, "https://example.com/a", "Alpha")
		b := saveLink(t, pool, user.ID, "https://example.com/b", "Beta")

		resp := relate(t, cookie, a.ID, b.ID)

		require.Equal(t, http.StatusCreated, resp.StatusCode)
		got := decodeBody[relatedBody](t, resp)
		assert.Equal(t, b.ID, got.ID)
		assert.Equal(t, "Beta", *got.Title)
		assert.Equal(t, "https://example.com/b", got.NormalizedURL)
		assert.Equal(t, []int64{b.ID}, relatedOf(t, cookie, a.ID))
		assert.Equal(t, []int64{a.ID}, relatedOf(t, cookie, b.ID), "visible from the other end too")
	})

	t.Run("a pair has one representation, whichever end it's made from", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		a := saveLink(t, pool, user.ID, "https://example.com/p", "P")
		b := saveLink(t, pool, user.ID, "https://example.com/q", "Q")

		require.Equal(t, http.StatusCreated, relate(t, cookie, b.ID, a.ID).StatusCode)
		require.Equal(t, http.StatusCreated, relate(t, cookie, a.ID, b.ID).StatusCode, "relating again is a no-op")

		var rows int
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT COUNT(*) FROM link_links WHERE link_id_a = LEAST($1::bigint,$2::bigint) AND link_id_b = GREATEST($1::bigint,$2::bigint)", a.ID, b.ID).Scan(&rows))
		assert.Equal(t, 1, rows)
		assert.Equal(t, []int64{b.ID}, relatedOf(t, cookie, a.ID))
	})

	t.Run("lists related links by title, untitled last", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		hub := saveLink(t, pool, user.ID, "https://example.com/hub", "Hub")
		zed := saveLink(t, pool, user.ID, "https://example.com/zed", "Zed")
		untitled := saveLink(t, pool, user.ID, "https://example.com/untitled", "")
		abe := saveLink(t, pool, user.ID, "https://example.com/abe", "Abe")
		for _, other := range []int64{zed.ID, untitled.ID, abe.ID} {
			require.Equal(t, http.StatusCreated, relate(t, cookie, hub.ID, other).StatusCode)
		}

		assert.Equal(t, []int64{abe.ID, zed.ID, untitled.ID}, relatedOf(t, cookie, hub.ID))
	})

	t.Run("a link with none has an empty list, not null", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/lonely", "Lonely")
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", link.ID), sessionCookieFor(t, pool, &user))
		body := new(strings.Builder)
		_, err := fmt.Fprint(body, decodeBody[map[string]any](t, resp)["links"])
		require.NoError(t, err)
		assert.Equal(t, "[]", body.String())
	})

	t.Run("refuses to relate a link to itself", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/self", "Self")
		resp := relate(t, sessionCookieFor(t, pool, &user), link.ID, link.ID)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("requires related_link_id", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLink(t, pool, user.ID, "https://example.com/missing", "Missing")
		cookie := sessionCookieFor(t, pool, &user)
		resp := requestWithCookieBody(t, server, http.MethodPost, fmt.Sprintf("/api/links/%d/links", link.ID), cookie, `{}`)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("404s when either link isn't the caller's, relating nothing", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		mine := saveLink(t, pool, other.ID, "https://example.com/mine", "Mine")
		theirs := saveLink(t, pool, owner.ID, "https://example.com/theirs", "Theirs")
		cookie := sessionCookieFor(t, pool, &other)

		assert.Equal(t, http.StatusNotFound, relate(t, cookie, mine.ID, theirs.ID).StatusCode, "the other end isn't theirs")
		assert.Equal(t, http.StatusNotFound, relate(t, cookie, theirs.ID, mine.ID).StatusCode, "this end isn't theirs")
		assert.Equal(t, http.StatusNotFound, relate(t, cookie, mine.ID, 999999999).StatusCode)

		var rows int
		require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM link_links WHERE link_id_a = ANY($1) OR link_id_b = ANY($1)", []int64{mine.ID, theirs.ID}).Scan(&rows))
		assert.Zero(t, rows)
	})

	t.Run("removes a relation from either end, leaving the others", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		a := saveLink(t, pool, user.ID, "https://example.com/r1", "R1")
		b := saveLink(t, pool, user.ID, "https://example.com/r2", "R2")
		c := saveLink(t, pool, user.ID, "https://example.com/r3", "R3")
		require.Equal(t, http.StatusCreated, relate(t, cookie, a.ID, b.ID).StatusCode)
		require.Equal(t, http.StatusCreated, relate(t, cookie, a.ID, c.ID).StatusCode)

		assert.Equal(t, http.StatusNoContent, unrelate(t, cookie, b.ID, a.ID).StatusCode, "removed from the other end")

		assert.Equal(t, []int64{c.ID}, relatedOf(t, cookie, a.ID))
		assert.Empty(t, relatedOf(t, cookie, b.ID))
	})

	t.Run("removing a pair that isn't related is a quiet no-op", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		a := saveLink(t, pool, user.ID, "https://example.com/n1", "N1")
		b := saveLink(t, pool, user.ID, "https://example.com/n2", "N2")
		assert.Equal(t, http.StatusNoContent, unrelate(t, cookie, a.ID, b.ID).StatusCode)
	})

	t.Run("removing from another user's link is a 404 and leaves the relation", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		a := saveLink(t, pool, owner.ID, "https://example.com/o1", "O1")
		b := saveLink(t, pool, owner.ID, "https://example.com/o2", "O2")
		require.Equal(t, http.StatusCreated, relate(t, sessionCookieFor(t, pool, &owner), a.ID, b.ID).StatusCode)

		resp := unrelate(t, sessionCookieFor(t, pool, &other), a.ID, b.ID)

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Equal(t, []int64{b.ID}, relatedOf(t, sessionCookieFor(t, pool, &owner), a.ID))
	})

	t.Run("deleting a link removes its relations from the other end", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		a := saveLink(t, pool, user.ID, "https://example.com/d1", "D1")
		b := saveLink(t, pool, user.ID, "https://example.com/d2", "D2")
		require.Equal(t, http.StatusCreated, relate(t, cookie, a.ID, b.ID).StatusCode)

		_, err := q.DeleteLink(ctx, db.DeleteLinkParams{ID: a.ID, UserID: user.ID})
		require.NoError(t, err)

		assert.Empty(t, relatedOf(t, cookie, b.ID))
	})

	t.Run("a bad id is a 400, and a session is required", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		assert.Equal(t, http.StatusBadRequest, requestWithCookieBody(t, server, http.MethodPost, "/api/links/abc/links", cookie, `{"related_link_id":1}`).StatusCode)
		assert.Equal(t, http.StatusBadRequest, requestWithCookie(t, server, http.MethodDelete, "/api/links/1/links/abc", cookie).StatusCode)

		resp, err := http.Post(server.URL+"/api/links/1/links", "application/json", strings.NewReader(`{"related_link_id":2}`))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestSearchLinksForLinking(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)

	search := func(t *testing.T, cookie *http.Cookie, query string) []int64 {
		t.Helper()
		resp := requestWithCookie(t, server, http.MethodGet, "/api/links/link-candidates"+query, cookie)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var ids []int64
		for _, l := range decodeBody[linkWithRelated](t, resp).Links {
			ids = append(ids, l.ID)
		}
		return ids
	}

	user := dbtest.CreateUser(t, pool, "member")
	other := dbtest.CreateUser(t, pool, "member")
	cookie := sessionCookieFor(t, pool, &user)
	byTitle := saveLink(t, pool, user.ID, "https://example.com/one", "Rust Ownership")
	byURL := saveLink(t, pool, user.ID, "https://github.com/rust-lang/rust", "A repo")
	self := saveLink(t, pool, user.ID, "https://example.com/three", "Rust notes")
	saveLink(t, pool, user.ID, "https://example.com/four", "Cooking")
	saveLink(t, pool, other.ID, "https://example.com/theirs", "Rust theirs")

	t.Run("matches the title or the URL, case-insensitively, never another user's links", func(t *testing.T) {
		got := search(t, cookie, "?q=RUST")
		assert.ElementsMatch(t, []int64{byTitle.ID, byURL.ID, self.ID}, got)
	})

	t.Run("excludes the link being related from", func(t *testing.T) {
		got := search(t, cookie, fmt.Sprintf("?q=rust&exclude=%d", self.ID))
		assert.ElementsMatch(t, []int64{byTitle.ID, byURL.ID}, got)
	})

	t.Run("an empty or missing query is an empty list, not an error", func(t *testing.T) {
		assert.Empty(t, search(t, cookie, "?q="))
		assert.Empty(t, search(t, cookie, ""))
	})

	t.Run("an unmatched query is an empty list", func(t *testing.T) {
		assert.Empty(t, search(t, cookie, "?q=zzzzz"))
	})

	t.Run("is capped, as the page picker is", func(t *testing.T) {
		for i := range 12 {
			saveLink(t, pool, user.ID, fmt.Sprintf("https://example.com/many-%d", i), "Many matches")
		}
		assert.Len(t, search(t, cookie, "?q=many"), 8)
	})

	t.Run("requires a session", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/links/link-candidates?q=x")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestLinkLinks_NotRoutedWithLinksOff(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, false)
	user := dbtest.CreateUser(t, pool, "member")
	cookie := sessionCookieFor(t, pool, &user)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/links/link-candidates?q=x"},
		{http.MethodPost, "/api/links/1/links"},
		{http.MethodDelete, "/api/links/1/links/2"},
	} {
		resp := requestWithCookieBody(t, server, tc.method, tc.path, cookie, `{}`)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "%s %s", tc.method, tc.path)
	}
}
