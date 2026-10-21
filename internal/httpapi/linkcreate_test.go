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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/linkicons"
)

type createdLinkBody struct {
	linkBody
}

type linkExistsBody struct {
	Error  string `json:"error"`
	LinkID int64  `json:"link_id"`
}

func b64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

func jsonBody(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

func TestCreateLink(t *testing.T) {
	pool := dbtest.Setup(t)
	server, store := newLinksTestServer(t, pool, true)
	q := db.New(pool)

	create := func(t *testing.T, cookie *http.Cookie, body map[string]any) *http.Response {
		return requestWithCookieBody(t, server, http.MethodPost, "/api/links", cookie, jsonBody(t, body))
	}

	t.Run("adds a link from just a URL, as a manual one", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		resp := create(t, sessionCookieFor(t, pool, &user), map[string]any{
			"url": "  https://Example.com/Page?utm_source=newsletter&id=7  ",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		got := decodeBody[createdLinkBody](t, resp)

		assert.Equal(t, "https://Example.com/Page?utm_source=newsletter&id=7", got.RawURL, "raw_url is what was typed, trimmed")
		assert.Equal(t, "https://example.com/Page?id=7", got.NormalizedURL)
		assert.Equal(t, "manual", got.Source)
		assert.Nil(t, got.Title, "nothing is fetched, so there is no title")
		assert.Nil(t, got.FaviconPath)
		assert.Nil(t, got.Notes)

		stored, err := q.GetLinkByIDForUser(context.Background(), db.GetLinkByIDForUserParams{ID: got.ID, UserID: user.ID})
		require.NoError(t, err)
		assert.Equal(t, "manual", stored.Source)
	})

	t.Run("stores a title, trimmed, and treats a blank one as none", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)

		resp := create(t, cookie, map[string]any{"url": "https://example.com/titled", "title": "  Jellyfin  "})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		titled := decodeBody[createdLinkBody](t, resp)
		require.NotNil(t, titled.Title)
		assert.Equal(t, "Jellyfin", *titled.Title)

		resp = create(t, cookie, map[string]any{"url": "https://example.com/untitled", "title": "   "})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		assert.Nil(t, decodeBody[createdLinkBody](t, resp).Title)
	})

	t.Run("cuts a title past the defensive cap rather than refusing", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		resp := create(t, sessionCookieFor(t, pool, &user), map[string]any{
			"url": "https://example.com/long-title", "title": strings.Repeat("é", 5000),
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		got := decodeBody[createdLinkBody](t, resp)
		require.NotNil(t, got.Title)
		assert.Equal(t, 4096, len([]rune(*got.Title)))
	})

	t.Run("stores a favicon in the link store and serves it", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		icon := []byte("\x89PNG pretend icon")

		resp := create(t, cookie, map[string]any{
			"url": "https://example.com/with-icon", "favicon_ext": "png", "favicon_data": b64(icon),
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		got := decodeBody[createdLinkBody](t, resp)
		require.NotNil(t, got.FaviconPath)

		stored, err := q.GetLinkByIDForUser(context.Background(), db.GetLinkByIDForUserParams{ID: got.ID, UserID: user.ID})
		require.NoError(t, err)
		assert.Equal(t, int32(len(icon)), stored.FaviconSizeBytes.Int32)
		assert.NotEmpty(t, stored.FaviconHash.String)

		resp = requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d/favicon", got.ID), cookie)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, icon, body)

		rc, err := store.Archive().Open(*got.FaviconPath)
		require.NoError(t, err)
		_ = rc.Close()
	})

	t.Run("compresses an svg favicon and takes the extension in any case", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		svg := []byte(strings.Repeat("<svg>fake</svg>", 40))

		resp := create(t, cookie, map[string]any{
			"url": "https://example.com/svg-icon", "favicon_ext": "SVG", "favicon_data": b64(svg),
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		got := decodeBody[createdLinkBody](t, resp)
		require.NotNil(t, got.FaviconPath)
		assert.True(t, strings.HasSuffix(*got.FaviconPath, ".svg.zst"))

		resp = requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/links/%d/favicon", got.ID), cookie)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, svg, body, "served decompressed")
	})

	t.Run("a favicon the store can't hold is dropped and the link is still saved", func(t *testing.T) {
		// newTestServer has links on but no link store.
		bare, _ := newTestServer(t, pool, unreachable)
		user := dbtest.CreateUser(t, pool, "member")
		resp := requestWithCookieBody(t, bare, http.MethodPost, "/api/links", sessionCookieFor(t, pool, &user),
			jsonBody(t, map[string]any{"url": "https://example.com/no-store", "favicon_ext": "png", "favicon_data": b64([]byte("x"))}))
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		assert.Nil(t, decodeBody[createdLinkBody](t, resp).FaviconPath)
	})

	t.Run("rejects bad input with 400, writing nothing", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		icon := b64([]byte("icon"))
		for name, body := range map[string]map[string]any{
			"no url":                      {},
			"a blank url":                 {"url": "   "},
			"an unparseable url":          {"url": "http://[::1"},
			"an unsupported favicon type": {"url": "https://example.com/a", "favicon_ext": "gif", "favicon_data": icon},
			"a favicon with no data":      {"url": "https://example.com/b", "favicon_ext": "png"},
			"favicon data with no type":   {"url": "https://example.com/c", "favicon_data": icon},
			"favicon data that isn't base64": {
				"url": "https://example.com/d", "favicon_ext": "png", "favicon_data": "***not base64***",
			},
		} {
			resp := create(t, cookie, body)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode, name)
		}
		resp := requestWithCookieBody(t, server, http.MethodPost, "/api/links", cookie, "not json")
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		list := decodeBody[linkListBody](t, requestWithCookie(t, server, http.MethodGet, "/api/links", cookie))
		assert.Empty(t, list.Links, "none of those wrote a link")
	})

	t.Run("refuses a favicon over the cap, and a request over the body limit, with 413", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)

		atCap := create(t, cookie, map[string]any{
			"url": "https://example.com/at-cap", "favicon_ext": "png", "favicon_data": b64(make([]byte, 512<<10)),
		})
		assert.Equal(t, http.StatusCreated, atCap.StatusCode)

		over := create(t, cookie, map[string]any{
			"url": "https://example.com/over-cap", "favicon_ext": "png", "favicon_data": b64(make([]byte, 512<<10+1)),
		})
		assert.Equal(t, http.StatusRequestEntityTooLarge, over.StatusCode)

		huge := create(t, cookie, map[string]any{
			"url": "https://example.com/huge", "favicon_ext": "png", "favicon_data": strings.Repeat("A", 2<<20),
		})
		assert.Equal(t, http.StatusRequestEntityTooLarge, huge.StatusCode)
	})

	t.Run("a URL that's already a link is a 409 naming it, and nothing changes", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie := sessionCookieFor(t, pool, &user)
		first := decodeBody[createdLinkBody](t, create(t, cookie, map[string]any{
			"url": "https://example.com/dup", "title": "Original",
			"favicon_ext": "png", "favicon_data": b64([]byte("original icon")),
		}))
		_, err := q.SetLinkNotes(context.Background(), db.SetLinkNotesParams{
			Notes: pgtype.Text{String: "keep me", Valid: true}, ID: first.ID, UserID: user.ID,
		})
		require.NoError(t, err)
		filesBefore := countFiles(t, store)

		// Same normalized URL, different raw (a tracking parameter).
		resp := create(t, cookie, map[string]any{
			"url": "https://example.com/dup?utm_source=x", "title": "Replacement",
			"favicon_ext": "png", "favicon_data": b64([]byte("replacement icon")),
		})
		require.Equal(t, http.StatusConflict, resp.StatusCode)
		got := decodeBody[linkExistsBody](t, resp)
		assert.Equal(t, first.ID, got.LinkID)
		assert.Contains(t, got.Error, "already saved as a link")

		after, err := q.GetLinkByIDForUser(context.Background(), db.GetLinkByIDForUserParams{ID: first.ID, UserID: user.ID})
		require.NoError(t, err)
		assert.Equal(t, "Original", after.Title.String)
		assert.Equal(t, "keep me", after.Notes.String)
		assert.Equal(t, "https://example.com/dup", after.RawUrl)
		assert.Equal(t, first.FaviconPath, textPtr(after.FaviconPath))
		assert.Equal(t, filesBefore, countFiles(t, store), "no favicon was written for the refused add")
	})

	t.Run("the same URL for another user, or as a page, is no conflict", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		saveLink(t, pool, owner.ID, "https://example.com/shared", "Theirs")
		page := dbtest.CreatePage(t, pool, other.ID, "https://example.com/shared")
		cookie := sessionCookieFor(t, pool, &other)

		resp := create(t, cookie, map[string]any{"url": "https://example.com/shared"})
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		list := decodeBody[linkListBody](t, requestWithCookie(t, server, http.MethodGet, "/api/links", cookie))
		require.Len(t, list.Links, 1)
		require.NotNil(t, list.Links[0].MatchingPageID)
		assert.Equal(t, page.ID, *list.Links[0].MatchingPageID)
		assert.Equal(t, "manual", list.Links[0].Source)
	})

	t.Run("requires a session", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/links", strings.NewReader(`{"url":"https://example.com"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// countFiles is how many files the link store holds, for asserting a refused
// add wrote nothing.
func countFiles(t *testing.T, store *linkicons.Store) int {
	t.Helper()
	n := 0
	require.NoError(t, store.Archive().Walk(func(string, int64, time.Time) error {
		n++
		return nil
	}))
	return n
}

func TestCreateLinkIfAbsent_NeverOverwrites(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	ctx := context.Background()
	user := dbtest.CreateUser(t, pool, "member")

	first, err := q.CreateLinkIfAbsent(ctx, db.CreateLinkIfAbsentParams{
		UserID: user.ID, RawUrl: "https://example.com/race", NormalizedUrl: "https://example.com/race",
		Title: pgtype.Text{String: "First", Valid: true}, Source: "manual",
	})
	require.NoError(t, err)

	_, err = q.CreateLinkIfAbsent(ctx, db.CreateLinkIfAbsentParams{
		UserID: user.ID, RawUrl: "https://example.com/race", NormalizedUrl: "https://example.com/race",
		Title: pgtype.Text{String: "Second", Valid: true}, Source: "manual",
	})
	assert.ErrorIs(t, err, pgx.ErrNoRows)

	after, err := q.GetLinkByIDForUser(ctx, db.GetLinkByIDForUserParams{ID: first.ID, UserID: user.ID})
	require.NoError(t, err)
	assert.Equal(t, "First", after.Title.String)
}

func TestLinkSource(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	ctx := context.Background()
	user := dbtest.CreateUser(t, pool, "member")
	url := "https://example.com/source"

	manual, err := q.CreateLinkIfAbsent(ctx, db.CreateLinkIfAbsentParams{
		UserID: user.ID, RawUrl: url, NormalizedUrl: url, Source: "manual",
	})
	require.NoError(t, err)
	assert.Equal(t, "manual", manual.Source)

	t.Run("a save from the extension makes it an extension link: its title now comes from a tab", func(t *testing.T) {
		refreshed, err := q.UpsertLink(ctx, db.UpsertLinkParams{
			UserID: user.ID, RawUrl: url, NormalizedUrl: url, Source: "extension",
			Title: pgtype.Text{String: "From the tab", Valid: true},
		})
		require.NoError(t, err)
		assert.Equal(t, manual.ID, refreshed.ID)
		assert.Equal(t, "extension", refreshed.Source)
	})

	t.Run("the database refuses any other value", func(t *testing.T) {
		_, err := q.UpsertLink(ctx, db.UpsertLinkParams{
			UserID: user.ID, RawUrl: "https://example.com/bad-source", NormalizedUrl: "https://example.com/bad-source",
			Source: "manual_upload",
		})
		assert.Error(t, err)
	})
}
