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
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
)

// saveLinkAs saves a link whose raw and normalized URLs differ, the way ingestion
// stores one that carried tracking parameters.
func saveLinkAs(t *testing.T, q *db.Queries, userID int64, raw, normalized, title string) db.Link {
	t.Helper()
	link, err := q.UpsertLink(context.Background(), db.UpsertLinkParams{
		UserID: userID, RawUrl: raw, NormalizedUrl: normalized, Source: "extension",
		Title: pgtype.Text{String: title, Valid: title != ""},
	})
	require.NoError(t, err)
	return link
}

type cleanedBody struct {
	CleanedURL *string `json:"cleaned_url"`
}

func TestLinkCleanedURL(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)
	q := db.New(pool)
	user := dbtest.CreateUser(t, pool, "member")
	cookie := sessionCookieFor(t, pool, &user)

	cleaned := func(t *testing.T, raw, normalized string) *string {
		t.Helper()
		link := saveLinkAs(t, q, user.ID, raw, normalized, "T")
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", link.ID), cookie)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return decodeBody[cleanedBody](t, resp).CleanedURL
	}

	t.Run("offers the normalized URL when tracking was stripped", func(t *testing.T) {
		got := cleaned(t, "https://example.com/a?utm_source=news&id=7", "https://example.com/a?id=7")
		require.NotNil(t, got)
		assert.Equal(t, "https://example.com/a?id=7", *got)
	})

	t.Run("keeps the fragment, since it's what makes a hash-routed app work", func(t *testing.T) {
		got := cleaned(t, "https://app.example.com/?utm_x=1#/board/3", "https://app.example.com/")
		require.NotNil(t, got)
		assert.Equal(t, "https://app.example.com/#/board/3", *got)
	})

	t.Run("keeps a fragment exactly as it was written", func(t *testing.T) {
		got := cleaned(t, "https://app.example.com/?utm_x=1#/a%20b/c?d=e", "https://app.example.com/")
		require.NotNil(t, got)
		assert.Equal(t, "https://app.example.com/#/a%20b/c?d=e", *got)
	})

	t.Run("an empty fragment is dropped", func(t *testing.T) {
		got := cleaned(t, "https://example.com/e?utm=1#", "https://example.com/e")
		require.NotNil(t, got)
		assert.Equal(t, "https://example.com/e", *got)
	})

	t.Run("is null for a URL that's already clean", func(t *testing.T) {
		assert.Nil(t, cleaned(t, "https://example.com/clean", "https://example.com/clean"))
	})

	t.Run("is null when only a fragment separates raw from normalized, since cleaning would change nothing", func(t *testing.T) {
		assert.Nil(t, cleaned(t, "https://example.com/frag#section", "https://example.com/frag"))
	})
}

func TestCleanLinkURL(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newLinksTestServer(t, pool, true)
	q := db.New(pool)
	ctx := context.Background()

	clean := func(t *testing.T, cookie *http.Cookie, id int64) *http.Response {
		return requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/links/%d/clean-url", id), cookie)
	}

	t.Run("replaces the raw URL with the cleaned one, leaving the link's identity and its own data alone", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		link := saveLinkAs(t, q, user.ID, "https://example.com/a?utm_source=news&id=7#/x", "https://example.com/a?id=7", "Title")
		_, err := q.SetLinkNotes(ctx, db.SetLinkNotesParams{Notes: pgtype.Text{String: "my note", Valid: true}, ID: link.ID, UserID: user.ID})
		require.NoError(t, err)

		resp := clean(t, cookie, link.ID)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeBody[linkBody](t, resp)
		assert.Equal(t, "https://example.com/a?id=7#/x", got.RawURL, "the fragment survives")
		assert.Equal(t, "https://example.com/a?id=7", got.NormalizedURL, "identity untouched")
		assert.Equal(t, "Title", *got.Title)
		assert.Equal(t, "my note", *got.Notes)

		stored, err := q.GetLinkByIDForUser(ctx, db.GetLinkByIDForUserParams{ID: link.ID, UserID: user.ID})
		require.NoError(t, err)
		assert.Equal(t, "https://example.com/a?id=7#/x", stored.RawUrl)
	})

	t.Run("the detail then offers nothing more to clean", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		link := saveLinkAs(t, q, user.ID, "https://example.com/b?utm_x=1", "https://example.com/b", "B")
		require.Equal(t, http.StatusOK, clean(t, cookie, link.ID).StatusCode)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d", link.ID), cookie)
		assert.Nil(t, decodeBody[cleanedBody](t, resp).CleanedURL)
	})

	t.Run("moves updated_at, so the bookmark sync pushes the new URL", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		link := saveLinkAs(t, q, user.ID, "https://example.com/c?utm_x=1", "https://example.com/c", "C")

		require.Equal(t, http.StatusOK, clean(t, cookie, link.ID).StatusCode)

		changed, err := q.GetLinksUpdatedSince(ctx, pgtype.Timestamptz{Time: link.UpdatedAt.Time, Valid: true})
		require.NoError(t, err)
		var urls []string
		for i := range changed {
			if changed[i].ID == link.ID {
				urls = append(urls, changed[i].RawUrl)
			}
		}
		assert.Equal(t, []string{"https://example.com/c"}, urls)
	})

	t.Run("is idempotent: an already-clean link comes back unchanged and untouched", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		link := saveLinkAs(t, q, user.ID, "https://example.com/d", "https://example.com/d", "D")

		resp := clean(t, cookie, link.ID)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "https://example.com/d", decodeBody[linkBody](t, resp).RawURL)
		after, err := q.GetLinkByIDForUser(ctx, db.GetLinkByIDForUserParams{ID: link.ID, UserID: user.ID})
		require.NoError(t, err)
		assert.True(t, after.UpdatedAt.Time.Equal(link.UpdatedAt.Time), "no write, so updated_at didn't move")
	})

	t.Run("404s another user's link, changing nothing", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		link := saveLinkAs(t, q, owner.ID, "https://example.com/e?utm_x=1", "https://example.com/e", "E")

		resp := clean(t, sessionCookieFor(t, pool, &other), link.ID)

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		after, err := q.GetLinkByIDForUser(ctx, db.GetLinkByIDForUserParams{ID: link.ID, UserID: owner.ID})
		require.NoError(t, err)
		assert.Equal(t, "https://example.com/e?utm_x=1", after.RawUrl)
	})

	t.Run("400s a bad id and requires a session", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		resp := requestWithCookie(t, server, http.MethodPost, "/api/links/abc/clean-url", sessionCookieFor(t, pool, &user))
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		resp, err := http.Post(server.URL+"/api/links/1/clean-url", "application/json", nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("is not routed with links off", func(t *testing.T) {
		off, _ := newLinksTestServer(t, pool, false)
		user := dbtest.CreateUser(t, pool, "member")
		link := saveLinkAs(t, q, user.ID, "https://example.com/f?utm_x=1", "https://example.com/f", "F")
		resp := requestWithCookie(t, off, http.MethodPost, fmt.Sprintf("/api/links/%d/clean-url", link.ID), sessionCookieFor(t, pool, &user))
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}
