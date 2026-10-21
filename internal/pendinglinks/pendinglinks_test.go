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

package pendinglinks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/pendinglinks"
)

func TestClient_ClaimPendingLinks(t *testing.T) {
	t.Run("sends the expected request and parses every field", func(t *testing.T) {
		var gotMethod, gotPath, gotQuery, gotServiceKey, gotUserAgent string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotQuery = r.URL.RawQuery
			gotServiceKey = r.Header.Get("X-Service-Key")
			gotUserAgent = r.Header.Get("User-Agent")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pending_links": []map[string]any{{
					"id":             "link-1",
					"user_id":        42,
					"queue_item_id":  "queue-1",
					"url":            "https://example.com/page",
					"title":          "A page",
					"favicon_ext":    "png",
					"favicon_data":   "AAAA",
					"r2_key_favicon": nil,
					"saved_at":       "2026-07-12T12:00:00.000Z",
					"claimed_at":     "2026-07-12 12:30:00",
					"created_at":     "2026-07-12 12:00:05",
				}},
			})
		}))
		defer server.Close()

		links, err := pendinglinks.NewClient(server.URL, "test-secret").ClaimPendingLinks(context.Background(), 25)
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/internal/pending-links", gotPath)
		assert.Equal(t, "limit=25", gotQuery)
		assert.Equal(t, "test-secret", gotServiceKey)
		assert.Equal(t, "recueil/1.0", gotUserAgent)

		require.Len(t, links, 1)
		l := links[0]
		assert.Equal(t, "link-1", l.ID)
		assert.Equal(t, int64(42), l.UserID)
		require.NotNil(t, l.QueueItemID)
		assert.Equal(t, "queue-1", *l.QueueItemID)
		assert.Equal(t, "https://example.com/page", l.URL)
		require.NotNil(t, l.Title)
		assert.Equal(t, "A page", *l.Title)
		require.NotNil(t, l.FaviconExt)
		assert.Equal(t, "png", *l.FaviconExt)
		require.NotNil(t, l.FaviconData)
		assert.Equal(t, "AAAA", *l.FaviconData)
		assert.Nil(t, l.R2KeyFavicon)
		assert.Equal(t, "2026-07-12T12:00:00.000Z", l.SavedAt)
	})

	t.Run("nulls decode to nil pointers: a bare direct save", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pending_links": []map[string]any{{
					"id": "link-2", "user_id": 1, "queue_item_id": nil,
					"url": "https://example.com/", "title": nil, "favicon_ext": nil,
					"favicon_data": nil, "r2_key_favicon": nil,
					"saved_at": "2026-07-12T12:00:00.000Z",
				}},
			})
		}))
		defer server.Close()

		links, err := pendinglinks.NewClient(server.URL, "s").ClaimPendingLinks(context.Background(), 1)
		require.NoError(t, err)
		require.Len(t, links, 1)
		assert.Nil(t, links[0].QueueItemID)
		assert.Nil(t, links[0].Title)
		assert.Nil(t, links[0].FaviconExt)
		assert.Nil(t, links[0].FaviconData)
		assert.Nil(t, links[0].R2KeyFavicon)
	})

	t.Run("an R2 favicon decodes with its key and no inline data", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pending_links": []map[string]any{{
					"id": "link-3", "user_id": 1, "url": "https://example.com/",
					"favicon_ext":    "ico",
					"r2_key_favicon": "pending-links/1/link-3/favicon.ico",
					"saved_at":       "2026-07-12T12:00:00.000Z",
				}},
			})
		}))
		defer server.Close()

		links, err := pendinglinks.NewClient(server.URL, "s").ClaimPendingLinks(context.Background(), 1)
		require.NoError(t, err)
		require.NotNil(t, links[0].R2KeyFavicon)
		assert.Equal(t, "pending-links/1/link-3/favicon.ico", *links[0].R2KeyFavicon)
		assert.Nil(t, links[0].FaviconData)
	})

	t.Run("an empty batch is no error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"pending_links": []any{}})
		}))
		defer server.Close()

		links, err := pendinglinks.NewClient(server.URL, "s").ClaimPendingLinks(context.Background(), 25)
		require.NoError(t, err)
		assert.Empty(t, links)
	})

	t.Run("a 404 means the Worker predates links, and says so distinctly", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer server.Close()

		_, err := pendinglinks.NewClient(server.URL, "s").ClaimPendingLinks(context.Background(), 25)
		assert.ErrorIs(t, err, pendinglinks.ErrWorkerWithoutLinks)
	})

	t.Run("any other non-2xx is a plain error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		_, err := pendinglinks.NewClient(server.URL, "wrong").ClaimPendingLinks(context.Background(), 25)
		require.Error(t, err)
		assert.NotErrorIs(t, err, pendinglinks.ErrWorkerWithoutLinks)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("malformed JSON is an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{not json"))
		}))
		defer server.Close()

		_, err := pendinglinks.NewClient(server.URL, "s").ClaimPendingLinks(context.Background(), 25)
		assert.Error(t, err)
	})

	t.Run("an unreachable Worker is an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := server.URL
		server.Close()

		_, err := pendinglinks.NewClient(url, "s").ClaimPendingLinks(context.Background(), 25)
		assert.Error(t, err)
	})
}

func TestClient_MarkFetched(t *testing.T) {
	t.Run("posts to the link's own fetched route", func(t *testing.T) {
		var gotMethod, gotPath, gotServiceKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotServiceKey = r.Header.Get("X-Service-Key")
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		require.NoError(t, pendinglinks.NewClient(server.URL, "test-secret").MarkFetched(context.Background(), "link-9"))
		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/internal/pending-links/link-9/fetched", gotPath)
		assert.Equal(t, "test-secret", gotServiceKey)
	})

	t.Run("a non-2xx is an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		assert.Error(t, pendinglinks.NewClient(server.URL, "s").MarkFetched(context.Background(), "x"))
	})
}

func TestClient_CleanupPendingLinks(t *testing.T) {
	t.Run("returns the deleted count", func(t *testing.T) {
		var gotMethod, gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(map[string]any{"deleted": 7})
		}))
		defer server.Close()

		n, err := pendinglinks.NewClient(server.URL, "s").CleanupPendingLinks(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 7, n)
		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/internal/pending-links/cleanup", gotPath)
	})

	t.Run("a 404 is ErrWorkerWithoutLinks", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer server.Close()

		_, err := pendinglinks.NewClient(server.URL, "s").CleanupPendingLinks(context.Background())
		assert.ErrorIs(t, err, pendinglinks.ErrWorkerWithoutLinks)
	})

	t.Run("a non-2xx is an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()

		_, err := pendinglinks.NewClient(server.URL, "s").CleanupPendingLinks(context.Background())
		assert.Error(t, err)
	})
}
