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

package deviceapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/deviceapi"
)

func TestPair(t *testing.T) {
	t.Run("sends the expected request and parses a successful response", func(t *testing.T) {
		var gotMethod, gotPath, gotContentType string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotContentType = r.Header.Get("Content-Type")
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "rcl_live_abc123", "device_id": 42, "device_name": "my-laptop",
				"device_type": "cli",
			})
		}))
		defer server.Close()

		result, err := deviceapi.Pair(context.Background(), server.URL, "some-pairing-token", "my-laptop", "cli")
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/pair", gotPath)
		assert.Equal(t, "application/json", gotContentType)
		assert.Equal(t, "some-pairing-token", gotBody["pairing_token"])
		assert.Equal(t, "my-laptop", gotBody["device_name"])
		assert.Equal(t, "cli", gotBody["device_type"])

		assert.Equal(t, "rcl_live_abc123", result.Token)
		assert.Equal(t, int64(42), result.DeviceID)
		assert.Equal(t, "my-laptop", result.DeviceName)
	})

	t.Run("trims a trailing slash from the worker URL", func(t *testing.T) {
		var gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "t", "device_id": 1, "device_name": "d"})
		}))
		defer server.Close()

		_, err := deviceapi.Pair(context.Background(), server.URL+"/", "token", "d", "cli")
		require.NoError(t, err)
		assert.Equal(t, "/pair", gotPath)
	})

	t.Run("an invalid pairing token surfaces as an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		_, err := deviceapi.Pair(context.Background(), server.URL, "wrong-token", "d", "cli")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("network failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		unreachableURL := server.URL
		server.Close()

		_, err := deviceapi.Pair(context.Background(), unreachableURL, "token", "d", "cli")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "pairing request failed")
	})
}

func TestClient_Enqueue(t *testing.T) {
	t.Run("sends the expected request", func(t *testing.T) {
		var gotMethod, gotPath, gotAuth string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client := deviceapi.NewClient(server.URL, "rcl_live_abc123")
		err := client.Enqueue(context.Background(), "some-uuid", "https://example.com/page")
		require.NoError(t, err)

		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/queue", gotPath)
		assert.Equal(t, "Bearer rcl_live_abc123", gotAuth)
		assert.Equal(t, "some-uuid", gotBody["id"])
		assert.Equal(t, "https://example.com/page", gotBody["url"])
	})

	t.Run("an unauthorized token surfaces as an error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		client := deviceapi.NewClient(server.URL, "bad-token")
		err := client.Enqueue(context.Background(), "id", "https://example.com")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("network failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		unreachableURL := server.URL
		server.Close()

		client := deviceapi.NewClient(unreachableURL, "token")
		err := client.Enqueue(context.Background(), "id", "https://example.com")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "enqueue request failed")
	})
}
