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

package devices_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/devices"
)

func TestClient_ListTokens(t *testing.T) {
	t.Run("sends the expected request and parses D1-native timestamps", func(t *testing.T) {
		var gotMethod, gotPath, gotServiceKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.RequestURI()
			gotServiceKey = r.Header.Get("X-Service-Key")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tokens":[
				{"id":1,"device_name":"laptop","device_type":"extension","created_at":"2026-06-01 12:00:00","last_used_at":"2026-07-19 08:30:15"},
				{"id":2,"device_name":"cli","device_type":"cli","created_at":"2026-07-01 00:00:00","last_used_at":null}
			]}`))
		}))
		defer server.Close()

		client := devices.NewClient(server.URL, "test-secret")
		got, err := client.ListTokens(context.Background(), 42)
		require.NoError(t, err)

		assert.Equal(t, http.MethodGet, gotMethod)
		assert.Equal(t, "/internal/tokens?user_id=42", gotPath)
		assert.Equal(t, "test-secret", gotServiceKey)

		require.Len(t, got, 2)
		assert.Equal(t, int64(1), got[0].ID)
		assert.Equal(t, "laptop", got[0].DeviceName)
		assert.Equal(t, "extension", got[0].DeviceType)
		assert.True(t, got[0].CreatedAt.Equal(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)))
		require.NotNil(t, got[0].LastUsedAt)
		assert.True(t, got[0].LastUsedAt.Equal(time.Date(2026, 7, 19, 8, 30, 15, 0, time.UTC)))

		assert.Equal(t, int64(2), got[1].ID)
		assert.Nil(t, got[1].LastUsedAt, "a never-used device has a nil last_used_at, not a zero time")
	})

	t.Run("an empty token list decodes to an empty (not nil) slice", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"tokens":[]}`))
		}))
		defer server.Close()

		client := devices.NewClient(server.URL, "test-secret")
		got, err := client.ListTokens(context.Background(), 1)
		require.NoError(t, err)
		assert.NotNil(t, got)
		assert.Empty(t, got)
	})

	t.Run("returns an error on a non-2xx response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		client := devices.NewClient(server.URL, "wrong-secret")
		_, err := client.ListTokens(context.Background(), 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("returns an error for a timestamp that isn't D1-native format", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// RFC 3339 (with a 'T' and 'Z'), not what D1's own
			// CURRENT_TIMESTAMP actually produces -- must be rejected,
			// not silently accepted, so a real format mismatch is
			// caught rather than producing a subtly wrong time.
			_, _ = w.Write([]byte(`{"tokens":[{"id":1,"device_name":"x","device_type":"cli","created_at":"2026-06-01T12:00:00Z","last_used_at":null}]}`))
		}))
		defer server.Close()

		client := devices.NewClient(server.URL, "test-secret")
		_, err := client.ListTokens(context.Background(), 1)
		require.Error(t, err)
	})
}

func TestClient_RevokeToken(t *testing.T) {
	t.Run("sends the expected request", func(t *testing.T) {
		var gotMethod, gotPath, gotServiceKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.RequestURI()
			gotServiceKey = r.Header.Get("X-Service-Key")
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client := devices.NewClient(server.URL, "test-secret")
		err := client.RevokeToken(context.Background(), 42, 7)
		require.NoError(t, err)

		assert.Equal(t, http.MethodDelete, gotMethod)
		assert.Equal(t, "/internal/tokens/7?user_id=42", gotPath)
		assert.Equal(t, "test-secret", gotServiceKey)
	})

	t.Run("a 404 maps to ErrNotFound", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		client := devices.NewClient(server.URL, "test-secret")
		err := client.RevokeToken(context.Background(), 42, 7)
		require.Error(t, err)
		assert.True(t, errors.Is(err, devices.ErrNotFound))
	})

	t.Run("other non-2xx responses are a plain error, not ErrNotFound", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client := devices.NewClient(server.URL, "test-secret")
		err := client.RevokeToken(context.Background(), 42, 7)
		require.Error(t, err)
		assert.False(t, errors.Is(err, devices.ErrNotFound))
	})
}
