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

package queueitems_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/queueitems"
)

func TestClient_ListFailed(t *testing.T) {
	t.Run("sends the expected request and parses D1-native timestamps", func(t *testing.T) {
		var gotMethod, gotPath, gotServiceKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.RequestURI()
			gotServiceKey = r.Header.Get("X-Service-Key")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[
				{"id":"item-1","url":"https://example.com/a","status":"failed","manual_retry":0,"created_at":"2026-06-01 12:00:00"},
				{"id":"item-2","url":"https://example.com/b","status":"failed","manual_retry":1,"created_at":"2026-07-19 08:30:15"}
			]}`))
		}))
		defer server.Close()

		client := queueitems.NewClient(server.URL, "test-secret")
		got, err := client.ListFailed(context.Background(), 42)
		require.NoError(t, err)

		assert.Equal(t, http.MethodGet, gotMethod)
		assert.Equal(t, "/internal/queue-items?user_id=42&status=failed", gotPath)
		assert.Equal(t, "test-secret", gotServiceKey)

		require.Len(t, got, 2)
		assert.Equal(t, "item-1", got[0].ID)
		assert.Equal(t, "https://example.com/a", got[0].URL)
		assert.Equal(t, "failed", got[0].Status)
		assert.False(t, got[0].ManualRetry)
		assert.True(t, got[0].CreatedAt.Equal(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)))

		assert.Equal(t, "item-2", got[1].ID)
		assert.True(t, got[1].ManualRetry)
	})

	t.Run("an empty item list decodes to an empty (not nil) slice", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"items":[]}`))
		}))
		defer server.Close()

		client := queueitems.NewClient(server.URL, "test-secret")
		got, err := client.ListFailed(context.Background(), 1)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		client := queueitems.NewClient(server.URL, "wrong-secret")
		_, err := client.ListFailed(context.Background(), 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("returns an error for a timestamp that isn't D1-native format", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// RFC 3339 (with a 'T' and 'Z'), not what D1's own
			// CURRENT_TIMESTAMP actually produces -- must be rejected,
			// not silently accepted.
			_, _ = w.Write([]byte(`{"items":[{"id":"x","url":"https://example.com","status":"failed","manual_retry":0,"created_at":"2026-06-01T12:00:00Z"}]}`))
		}))
		defer server.Close()

		client := queueitems.NewClient(server.URL, "test-secret")
		_, err := client.ListFailed(context.Background(), 1)
		require.Error(t, err)
	})
}

func TestClient_Retry(t *testing.T) {
	t.Run("sends the expected request", func(t *testing.T) {
		var gotMethod, gotPath, gotServiceKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.RequestURI()
			gotServiceKey = r.Header.Get("X-Service-Key")
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client := queueitems.NewClient(server.URL, "test-secret")
		err := client.Retry(context.Background(), 42, "item-7")
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/internal/queue-items/item-7/retry?user_id=42", gotPath)
		assert.Equal(t, "test-secret", gotServiceKey)
	})

	t.Run("a 404 maps to ErrNotFound", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := queueitems.NewClient(server.URL, "test-secret")
		err := client.Retry(context.Background(), 42, "item-7")
		require.Error(t, err)
		assert.True(t, errors.Is(err, queueitems.ErrNotFound))
	})

	t.Run("other non-2xx responses are a plain error, not ErrNotFound", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client := queueitems.NewClient(server.URL, "test-secret")
		err := client.Retry(context.Background(), 42, "item-7")
		require.Error(t, err)
		assert.False(t, errors.Is(err, queueitems.ErrNotFound))
	})
}
