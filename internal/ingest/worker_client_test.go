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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/ingest"
)

func TestWorkerClient_ListPendingCaptures(t *testing.T) {
	t.Run("parses the response and sends the expected request", func(t *testing.T) {
		var gotMethod, gotPath, gotQuery, gotServiceKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotQuery = r.URL.RawQuery
			gotServiceKey = r.Header.Get("X-Service-Key")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pending_captures": []map[string]any{
					{
						"id":            "capture-1",
						"user_id":       42,
						"queue_item_id": "queue-1",
						"url":           "https://example.com/page",
						"r2_key_html":   "pending/42/capture-1/page.html",
						"captured_at":   "2026-07-12T12:00:00.000Z",
						"created_at":    "2026-07-12T12:00:05.000Z",
					},
				},
			})
		}))
		defer server.Close()

		client := ingest.NewWorkerClient(server.URL, "test-secret")
		captures, err := client.ListPendingCaptures(context.Background(), 50)
		require.NoError(t, err)

		assert.Equal(t, http.MethodGet, gotMethod)
		assert.Equal(t, "/internal/pending-captures", gotPath)
		assert.Equal(t, "limit=50", gotQuery)
		assert.Equal(t, "test-secret", gotServiceKey)

		require.Len(t, captures, 1)
		assert.Equal(t, "capture-1", captures[0].ID)
		assert.Equal(t, int64(42), captures[0].UserID)
		require.NotNil(t, captures[0].QueueItemID)
		assert.Equal(t, "queue-1", *captures[0].QueueItemID)
		assert.Equal(t, "https://example.com/page", captures[0].URL)
		assert.Equal(t, "pending/42/capture-1/page.html", captures[0].R2KeyHTML)
	})

	t.Run("a null queue_item_id decodes to a nil pointer (direct capture)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pending_captures": []map[string]any{
					{
						"id": "capture-2", "user_id": 1, "queue_item_id": nil,
						"url": "https://example.com", "r2_key_html": "x",
						"captured_at": "2026-07-12T12:00:00Z", "created_at": "2026-07-12T12:00:00Z",
					},
				},
			})
		}))
		defer server.Close()

		client := ingest.NewWorkerClient(server.URL, "test-secret")
		captures, err := client.ListPendingCaptures(context.Background(), 50)
		require.NoError(t, err)
		require.Len(t, captures, 1)
		assert.Nil(t, captures[0].QueueItemID)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		client := ingest.NewWorkerClient(server.URL, "wrong-secret")
		_, err := client.ListPendingCaptures(context.Background(), 50)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})
}

func TestWorkerClient_MarkFetched(t *testing.T) {
	t.Run("sends the expected request", func(t *testing.T) {
		var gotMethod, gotPath, gotServiceKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotServiceKey = r.Header.Get("X-Service-Key")
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client := ingest.NewWorkerClient(server.URL, "test-secret")
		err := client.MarkFetched(context.Background(), "capture-1")
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/internal/pending-captures/capture-1/fetched", gotPath)
		assert.Equal(t, "test-secret", gotServiceKey)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := ingest.NewWorkerClient(server.URL, "test-secret")
		err := client.MarkFetched(context.Background(), "does-not-exist")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "404")
	})
}
