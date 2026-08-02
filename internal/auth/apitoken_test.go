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

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAPIToken(t *testing.T) {
	raw, hash, err := GenerateAPIToken()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(raw, "rcl_api_"))
	assert.Equal(t, HashToken(raw), hash, "returned hash must match HashToken(raw)")
	assert.Len(t, hash, 64, "SHA-256 hex-encoded should be 64 characters")

	raw2, hash2, err := GenerateAPIToken()
	require.NoError(t, err)
	assert.NotEqual(t, raw, raw2, "two tokens should never collide")
	assert.NotEqual(t, hash, hash2)
}

func TestRequireAPIToken(t *testing.T) {
	t.Run("No Database", func(t *testing.T) {
		handlerCalled := false
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusOK)
		})

		tests := []struct {
			name         string
			setupRequest func(r *http.Request)
		}{
			{
				name:         "no Authorization header at all",
				setupRequest: func(r *http.Request) {},
			},
			{
				name: "Authorization header present but not a Bearer scheme",
				setupRequest: func(r *http.Request) {
					r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
				},
			},
			{
				name: "Bearer scheme with an empty token",
				setupRequest: func(r *http.Request) {
					r.Header.Set("Authorization", "Bearer ")
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				handlerCalled = false
				r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
				tt.setupRequest(r)
				w := httptest.NewRecorder()

				RequireAPIToken(nil)(next).ServeHTTP(w, r)

				assert.Equal(t, http.StatusUnauthorized, w.Code)
				assert.False(t, handlerCalled, "the wrapped handler must never run without a valid token")
			})
		}
	})

	t.Run("With Database", func(t *testing.T) {
		pool := dbtest.Setup(t)
		q := db.New(pool)

		t.Run("valid token succeeds and attaches the owning user", func(t *testing.T) {
			user := dbtest.CreateUser(t, pool, "member")
			raw, hash, err := GenerateAPIToken()
			require.NoError(t, err)
			_, err = q.CreateApiToken(context.Background(), db.CreateApiTokenParams{
				UserID: user.ID, TokenHash: hash, Name: "test client",
			})
			require.NoError(t, err)

			var gotUser db.User
			var gotOK bool
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotUser, gotOK = UserFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			r.Header.Set("Authorization", "Bearer "+raw)
			w := httptest.NewRecorder()

			RequireAPIToken(q)(next).ServeHTTP(w, r)

			assert.Equal(t, http.StatusOK, w.Code)
			require.True(t, gotOK, "handler must see a user in its context")
			assert.Equal(t, user.ID, gotUser.ID)
		})

		t.Run("unknown token is rejected", func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("handler must not run for an unrecognized token")
			})

			r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			r.Header.Set("Authorization", "Bearer rcl_api_doesnotexist")
			w := httptest.NewRecorder()

			RequireAPIToken(q)(next).ServeHTTP(w, r)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})

		t.Run("revoked token is rejected -- takes effect immediately, no propagation delay", func(t *testing.T) {
			user := dbtest.CreateUser(t, pool, "member")
			raw, hash, err := GenerateAPIToken()
			require.NoError(t, err)
			row, err := q.CreateApiToken(context.Background(), db.CreateApiTokenParams{
				UserID: user.ID, TokenHash: hash, Name: "test client",
			})
			require.NoError(t, err)

			rowsAffected, err := q.DeleteApiTokenForUser(context.Background(), db.DeleteApiTokenForUserParams{
				ID: row.ID, UserID: user.ID,
			})
			require.NoError(t, err)
			require.Equal(t, int64(1), rowsAffected)

			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("handler must not run for a revoked token")
			})

			r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			r.Header.Set("Authorization", "Bearer "+raw)
			w := httptest.NewRecorder()

			RequireAPIToken(q)(next).ServeHTTP(w, r)

			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})
	})
}
