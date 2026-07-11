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

package mirror

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPushUser(t *testing.T) {
	hash := "hashed-pairing-token"

	tests := []struct {
		name         string
		serverStatus int
		wantErr      bool
		errContains  string
	}{
		{name: "204 No Content is success", serverStatus: http.StatusNoContent},
		{name: "200 OK is also success", serverStatus: http.StatusOK},
		{name: "401 unauthorized is an error", serverStatus: http.StatusUnauthorized, wantErr: true, errContains: "401"},
		{name: "500 server error is an error", serverStatus: http.StatusInternalServerError, wantErr: true, errContains: "500"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath, gotServiceKey, gotContentType string
			var gotBody userMirrorPayload

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotServiceKey = r.Header.Get("X-Service-Key")
				gotContentType = r.Header.Get("Content-Type")
				require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
				w.WriteHeader(tt.serverStatus)
			}))
			defer server.Close()

			client := NewClient(server.URL, "test-secret")
			err := client.PushUser(context.Background(), 42, &hash)

			assert.Equal(t, http.MethodPost, gotMethod)
			assert.Equal(t, "/internal/users/mirror", gotPath)
			assert.Equal(t, "test-secret", gotServiceKey, "service secret must be sent as X-Service-Key")
			assert.Equal(t, "application/json", gotContentType)
			assert.Equal(t, userMirrorPayload{ID: 42, PairingTokenHash: &hash}, gotBody)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
		})
	}

	t.Run("nil pairingTokenHash marshals to JSON null (revoke)", func(t *testing.T) {
		var gotRaw map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotRaw))
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		client := NewClient(server.URL, "test-secret")
		err := client.PushUser(context.Background(), 42, nil)
		require.NoError(t, err)

		gotHash, present := gotRaw["pairing_token_hash"]
		assert.True(t, present, "pairing_token_hash key must still be present in the JSON body")
		assert.Nil(t, gotHash, "pairing_token_hash must marshal to JSON null, not be omitted")
	})

	t.Run("network failure (server unreachable)", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		unreachableURL := server.URL
		server.Close() // closed before any request is made, so the connection is refused

		client := NewClient(unreachableURL, "test-secret")
		err := client.PushUser(context.Background(), 1, &hash)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mirror push request failed")
	})
}
