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

package pendingcaptures_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/pendingcaptures"
)

func TestClient_ClaimPendingCaptures(t *testing.T) {
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

		client := pendingcaptures.NewClient(server.URL, "test-secret")
		captures, err := client.ClaimPendingCaptures(context.Background(), 50)
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, gotMethod)
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

		client := pendingcaptures.NewClient(server.URL, "test-secret")
		captures, err := client.ClaimPendingCaptures(context.Background(), 50)
		require.NoError(t, err)
		require.Len(t, captures, 1)
		assert.Nil(t, captures[0].QueueItemID)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		client := pendingcaptures.NewClient(server.URL, "wrong-secret")
		_, err := client.ClaimPendingCaptures(context.Background(), 50)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})
}

func TestClient_MarkFetched(t *testing.T) {
	t.Run("sends the expected request", func(t *testing.T) {
		var gotMethod, gotPath, gotServiceKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotServiceKey = r.Header.Get("X-Service-Key")
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client := pendingcaptures.NewClient(server.URL, "test-secret")
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

		client := pendingcaptures.NewClient(server.URL, "test-secret")
		err := client.MarkFetched(context.Background(), "does-not-exist")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "404")
	})
}

func TestClient_ListForUser(t *testing.T) {
	t.Run("parses both timestamp formats on the same row", func(t *testing.T) {
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
						"id":  "capture-1",
						"url": "https://example.com/page",
						// captured_at is RFC 3339, written by the
						// capturing device; claimed_at is SQLite's own
						// CURRENT_TIMESTAMP format, written by D1. Two
						// columns on one row, two formats -- the whole
						// reason this method doesn't just pass the strings
						// through.
						"captured_at":        "2026-07-12T12:00:00.000Z",
						"claimed_at":         "2026-07-12 12:05:09",
						"fetched_by_backend": 0,
					},
				},
			})
		}))
		defer server.Close()

		client := pendingcaptures.NewClient(server.URL, "test-secret")
		captures, err := client.ListForUser(context.Background(), 42)
		require.NoError(t, err)

		assert.Equal(t, http.MethodGet, gotMethod)
		assert.Equal(t, "/internal/pending-captures", gotPath)
		assert.Equal(t, "user_id=42", gotQuery)
		assert.Equal(t, "test-secret", gotServiceKey)

		require.Len(t, captures, 1)
		assert.Equal(t, "capture-1", captures[0].ID)
		assert.Equal(t, "https://example.com/page", captures[0].URL)
		assert.False(t, captures[0].FetchedByBackend)

		assert.Equal(t,
			time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
			captures[0].CapturedAt.UTC())

		// The one that matters: a bare "YYYY-MM-DD HH:MM:SS" carries no
		// zone marker at all, so parsing it as anything but UTC would shift
		// every relative time on the Queue screen by the running process's
		// own offset.
		require.NotNil(t, captures[0].ClaimedAt)
		assert.Equal(t,
			time.Date(2026, 7, 12, 12, 5, 9, 0, time.UTC),
			captures[0].ClaimedAt.UTC())
	})

	// An unclaimed capture is the normal case, not a malformed row.
	t.Run("treats a null claimed_at as absent rather than an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pending_captures": []map[string]any{
					{
						"id":                 "capture-unclaimed",
						"url":                "https://example.com/waiting",
						"captured_at":        "2026-07-12T12:00:00Z",
						"claimed_at":         nil,
						"fetched_by_backend": 0,
					},
				},
			})
		}))
		defer server.Close()

		client := pendingcaptures.NewClient(server.URL, "test-secret")
		captures, err := client.ListForUser(context.Background(), 42)
		require.NoError(t, err)

		require.Len(t, captures, 1)
		assert.Nil(t, captures[0].ClaimedAt)
	})

	// SQLite has no boolean type -- the column is an INTEGER holding 0/1.
	t.Run("maps the integer fetched flag to a real bool", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pending_captures": []map[string]any{
					{
						"id":                 "capture-done",
						"url":                "https://example.com/done",
						"captured_at":        "2026-07-12T12:00:00Z",
						"claimed_at":         "2026-07-12 12:05:09",
						"fetched_by_backend": 1,
					},
				},
			})
		}))
		defer server.Close()

		client := pendingcaptures.NewClient(server.URL, "test-secret")
		captures, err := client.ListForUser(context.Background(), 42)
		require.NoError(t, err)

		require.Len(t, captures, 1)
		assert.True(t, captures[0].FetchedByBackend)
	})

	t.Run("surfaces an unparseable timestamp rather than silently zeroing it", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pending_captures": []map[string]any{
					{
						"id":                 "capture-bad",
						"url":                "https://example.com/bad",
						"captured_at":        "not-a-timestamp",
						"claimed_at":         nil,
						"fetched_by_backend": 0,
					},
				},
			})
		}))
		defer server.Close()

		client := pendingcaptures.NewClient(server.URL, "test-secret")
		_, err := client.ListForUser(context.Background(), 42)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "capture-bad")
	})

	t.Run("returns an error for a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		client := pendingcaptures.NewClient(server.URL, "test-secret")
		_, err := client.ListForUser(context.Background(), 42)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})
}
