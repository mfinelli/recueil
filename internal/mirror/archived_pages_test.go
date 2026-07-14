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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/mirror"
)

func TestClient_GetArchivedPagesLastSync(t *testing.T) {
	t.Run("parses a real timestamp", func(t *testing.T) {
		var gotMethod, gotPath, gotServiceKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotServiceKey = r.Header.Get("X-Service-Key")
			_ = json.NewEncoder(w).Encode(map[string]any{"last_sync": "2026-07-12T15:00:00Z"})
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		got, err := client.GetArchivedPagesLastSync(context.Background())
		require.NoError(t, err)

		assert.Equal(t, http.MethodGet, gotMethod)
		assert.Equal(t, "/internal/archived-pages/last-sync", gotPath)
		assert.Equal(t, "test-secret", gotServiceKey)
		require.NotNil(t, got)
		assert.True(t, got.Equal(time.Date(2026, 7, 12, 15, 0, 0, 0, time.UTC)))
	})

	t.Run("a null last_sync means nothing has ever been pushed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"last_sync": nil})
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		got, err := client.GetArchivedPagesLastSync(context.Background())
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "wrong-secret")
		_, err := client.GetArchivedPagesLastSync(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})
}

func TestClient_MirrorArchivedPages(t *testing.T) {
	t.Run("sends the expected request shape", func(t *testing.T) {
		var gotMethod, gotPath, gotServiceKey, gotContentType string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotServiceKey = r.Header.Get("X-Service-Key")
			gotContentType = r.Header.Get("Content-Type")
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		title := "Example"
		client := mirror.NewClient(server.URL, "test-secret")
		err := client.MirrorArchivedPages(context.Background(), []mirror.ArchivedPage{
			{
				PageID:          1,
				UserID:          2,
				RawURL:          "https://example.com/page",
				Title:           &title,
				LatestCaptureAt: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
				UpdatedAt:       time.Date(2026, 7, 12, 13, 0, 0, 0, time.UTC),
			},
		})
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/internal/archived-pages/mirror", gotPath)
		assert.Equal(t, "test-secret", gotServiceKey)
		assert.Equal(t, "application/json", gotContentType)

		pages, ok := gotBody["pages"].([]any)
		require.True(t, ok)
		require.Len(t, pages, 1)
		page := pages[0].(map[string]any)
		assert.Equal(t, float64(1), page["page_id"])
		assert.Equal(t, float64(2), page["user_id"])
		assert.Equal(t, "https://example.com/page", page["raw_url"])
		assert.Equal(t, "Example", page["title"])
		assert.Equal(t, "2026-07-12T12:00:00Z", page["latest_capture_at"])
		assert.Equal(t, "2026-07-12T13:00:00Z", page["updated_at"])
	})

	t.Run("a nil title is omitted from the wire payload", func(t *testing.T) {
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		err := client.MirrorArchivedPages(context.Background(), []mirror.ArchivedPage{
			{
				PageID: 1, UserID: 2, RawURL: "https://example.com/page",
				LatestCaptureAt: time.Now(), UpdatedAt: time.Now(),
			},
		})
		require.NoError(t, err)

		pages := gotBody["pages"].([]any)
		page := pages[0].(map[string]any)
		_, hasTitle := page["title"]
		assert.False(t, hasTitle)
	})

	t.Run("an empty slice is a no-op that never makes a request", func(t *testing.T) {
		called := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		err := client.MirrorArchivedPages(context.Background(), nil)
		require.NoError(t, err)
		assert.False(t, called)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		err := client.MirrorArchivedPages(context.Background(), []mirror.ArchivedPage{
			{PageID: 1, UserID: 1, RawURL: "https://example.com", LatestCaptureAt: time.Now(), UpdatedAt: time.Now()},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})
}

func TestClient_ListArchivedPageIDs(t *testing.T) {
	t.Run("parses the id list", func(t *testing.T) {
		var gotMethod, gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(map[string]any{"page_ids": []int64{1, 2, 3}})
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		ids, err := client.ListArchivedPageIDs(context.Background())
		require.NoError(t, err)

		assert.Equal(t, http.MethodGet, gotMethod)
		assert.Equal(t, "/internal/archived-pages/page-ids", gotPath)
		assert.Equal(t, []int64{1, 2, 3}, ids)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		_, err := client.ListArchivedPageIDs(context.Background())
		require.Error(t, err)
	})
}

func TestClient_DeleteArchivedPages(t *testing.T) {
	t.Run("sends the expected request", func(t *testing.T) {
		var gotMethod, gotPath string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		err := client.DeleteArchivedPages(context.Background(), []int64{1, 2})
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/internal/archived-pages/delete", gotPath)
		ids, ok := gotBody["page_ids"].([]any)
		require.True(t, ok)
		assert.Equal(t, []any{float64(1), float64(2)}, ids)
	})

	t.Run("an empty slice is a no-op that never makes a request", func(t *testing.T) {
		called := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		err := client.DeleteArchivedPages(context.Background(), nil)
		require.NoError(t, err)
		assert.False(t, called)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client := mirror.NewClient(server.URL, "test-secret")
		err := client.DeleteArchivedPages(context.Background(), []int64{1})
		require.Error(t, err)
	})
}
