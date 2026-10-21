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
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/mirror"
	"github.com/mfinelli/recueil/internal/pendinglinks"
)

type recordedRequest struct {
	method, path, serviceKey, userAgent, contentType string
	body                                             map[string]any
}

// linksWorker is a one-route fake that records what it was sent and answers with
// a fixed status and body.
func linksWorker(t *testing.T, status int, respond string) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	var got []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordedRequest{
			method: r.Method, path: r.URL.Path, serviceKey: r.Header.Get("X-Service-Key"),
			userAgent: r.Header.Get("User-Agent"), contentType: r.Header.Get("Content-Type"),
		}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			require.NoError(t, json.Unmarshal(raw, &rec.body))
		}
		got = append(got, rec)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respond))
	}))
	t.Cleanup(server.Close)
	return server, &got
}

func TestClient_GetArchivedLinksLastSync(t *testing.T) {
	ctx := context.Background()

	t.Run("reads the checkpoint, sending the service key and user agent", func(t *testing.T) {
		server, got := linksWorker(t, 200, `{"last_sync":"2026-09-01T12:30:45.123456789Z"}`)
		last, err := mirror.NewClient(server.URL, "secret").GetArchivedLinksLastSync(ctx)
		require.NoError(t, err)
		require.NotNil(t, last)
		assert.True(t, last.Equal(time.Date(2026, 9, 1, 12, 30, 45, 123456789, time.UTC)))

		require.Len(t, *got, 1)
		assert.Equal(t, http.MethodGet, (*got)[0].method)
		assert.Equal(t, "/internal/archived-links/last-sync", (*got)[0].path)
		assert.Equal(t, "secret", (*got)[0].serviceKey)
		assert.Equal(t, "recueil/1.0", (*got)[0].userAgent)
	})

	t.Run("nil when nothing has been pushed", func(t *testing.T) {
		server, _ := linksWorker(t, 200, `{"last_sync":null}`)
		last, err := mirror.NewClient(server.URL, "s").GetArchivedLinksLastSync(ctx)
		require.NoError(t, err)
		assert.Nil(t, last)
	})

	t.Run("errors on a timestamp it can't parse", func(t *testing.T) {
		server, _ := linksWorker(t, 200, `{"last_sync":"yesterday"}`)
		_, err := mirror.NewClient(server.URL, "s").GetArchivedLinksLastSync(ctx)
		assert.ErrorContains(t, err, "parsing")
	})
}

func TestClient_MirrorArchivedLinks(t *testing.T) {
	ctx := context.Background()

	t.Run("pushes the batch with the title only when there is one, and updated_at in UTC", func(t *testing.T) {
		server, got := linksWorker(t, 200, `{"upserted":2}`)
		title := "A title"
		local := time.FixedZone("UTC+2", 2*3600)
		err := mirror.NewClient(server.URL, "secret").MirrorArchivedLinks(ctx, []mirror.ArchivedLink{
			{LinkID: 1, UserID: 9, RawURL: "https://example.com/a#frag", Title: &title, UpdatedAt: time.Date(2026, 9, 1, 14, 0, 0, 0, local)},
			{LinkID: 2, UserID: 9, RawURL: "https://example.com/b", UpdatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)},
		})
		require.NoError(t, err)

		require.Len(t, *got, 1)
		req := (*got)[0]
		assert.Equal(t, http.MethodPost, req.method)
		assert.Equal(t, "/internal/archived-links/mirror", req.path)
		assert.Equal(t, "application/json", req.contentType)
		links := req.body["links"].([]any)
		require.Len(t, links, 2)
		first := links[0].(map[string]any)
		assert.EqualValues(t, 1, first["link_id"])
		assert.EqualValues(t, 9, first["user_id"])
		assert.Equal(t, "https://example.com/a#frag", first["raw_url"])
		assert.Equal(t, "A title", first["title"])
		assert.Equal(t, "2026-09-01T12:00:00Z", first["updated_at"])
		assert.NotContains(t, links[1].(map[string]any), "title")
	})

	t.Run("an empty batch makes no request", func(t *testing.T) {
		server, got := linksWorker(t, 200, `{}`)
		require.NoError(t, mirror.NewClient(server.URL, "s").MirrorArchivedLinks(ctx, nil))
		assert.Empty(t, *got)
	})
}

func TestClient_ListAndDeleteArchivedLinks(t *testing.T) {
	ctx := context.Background()

	t.Run("lists the ids in the mirror", func(t *testing.T) {
		server, got := linksWorker(t, 200, `{"link_ids":[3,1,2]}`)
		ids, err := mirror.NewClient(server.URL, "s").ListArchivedLinkIDs(ctx)
		require.NoError(t, err)
		assert.Equal(t, []int64{3, 1, 2}, ids)
		assert.Equal(t, "/internal/archived-links/link-ids", (*got)[0].path)
	})

	t.Run("deletes by id, and an empty list makes no request", func(t *testing.T) {
		server, got := linksWorker(t, 200, `{"deleted":2}`)
		c := mirror.NewClient(server.URL, "s")

		require.NoError(t, c.DeleteArchivedLinks(ctx, []int64{4, 5}))
		require.Len(t, *got, 1)
		assert.Equal(t, http.MethodPost, (*got)[0].method)
		assert.Equal(t, "/internal/archived-links/delete", (*got)[0].path)
		assert.Equal(t, []any{float64(4), float64(5)}, (*got)[0].body["link_ids"])

		require.NoError(t, c.DeleteArchivedLinks(ctx, nil))
		assert.Len(t, *got, 1)
	})
}

func TestClient_ArchivedLinks_Errors(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(c *mirror.Client) error{
		"last-sync": func(c *mirror.Client) error { _, err := c.GetArchivedLinksLastSync(ctx); return err },
		"mirror": func(c *mirror.Client) error {
			return c.MirrorArchivedLinks(ctx, []mirror.ArchivedLink{{LinkID: 1, UserID: 1, RawURL: "https://x"}})
		},
		"link-ids": func(c *mirror.Client) error { _, err := c.ListArchivedLinkIDs(ctx); return err },
		"delete":   func(c *mirror.Client) error { return c.DeleteArchivedLinks(ctx, []int64{1}) },
	}

	for name, call := range calls {
		t.Run(name+": a 404 means the Worker predates links", func(t *testing.T) {
			server, _ := linksWorker(t, http.StatusNotFound, `Not Found`)
			err := call(mirror.NewClient(server.URL, "s"))
			assert.ErrorIs(t, err, pendinglinks.ErrWorkerWithoutLinks)
		})

		t.Run(name+": any other failure status is an error", func(t *testing.T) {
			server, _ := linksWorker(t, http.StatusInternalServerError, `boom`)
			err := call(mirror.NewClient(server.URL, "s"))
			require.Error(t, err)
			assert.NotErrorIs(t, err, pendinglinks.ErrWorkerWithoutLinks)
			assert.ErrorContains(t, err, "500")
		})

		t.Run(name+": an unreachable Worker is an error", func(t *testing.T) {
			server, _ := linksWorker(t, 200, `{}`)
			url := server.URL
			server.Close()
			assert.Error(t, call(mirror.NewClient(url, "s")))
		})
	}
}
