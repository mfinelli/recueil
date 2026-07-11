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
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSessionToken(t *testing.T) {
	raw, hash, err := GenerateSessionToken()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(raw, "rcl_sess_"))
	assert.Equal(t, HashToken(raw), hash, "returned hash must match HashToken(raw)")
	assert.Len(t, hash, 64, "SHA-256 hex-encoded should be 64 characters")

	raw2, hash2, err := GenerateSessionToken()
	require.NoError(t, err)
	assert.NotEqual(t, raw, raw2, "two tokens should never collide")
	assert.NotEqual(t, hash, hash2)
}

func TestHashToken(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		assert.Equal(t, HashToken("same-input"), HashToken("same-input"))
	})

	t.Run("different input produces different output", func(t *testing.T) {
		assert.NotEqual(t, HashToken("a"), HashToken("b"))
	})

	t.Run("matches a real SHA-256 hex digest, not just self-consistent", func(t *testing.T) {
		// Independently computed via crypto/sha256 directly, rather than
		// trusting HashToken to check itself.
		sum := sha256.Sum256([]byte("recueil"))
		want := hex.EncodeToString(sum[:])
		assert.Equal(t, want, HashToken("recueil"))
	})

	t.Run("known test vector: SHA-256 of the empty string", func(t *testing.T) {
		assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", HashToken(""))
	})
}

func TestSetSessionCookie(t *testing.T) {
	tests := []struct {
		name   string
		secure bool
	}{
		{name: "secure=true", secure: true},
		{name: "secure=false", secure: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			SetSessionCookie(w, "raw-token-value", tt.secure)

			cookies := w.Result().Cookies()
			require.Len(t, cookies, 1)
			c := cookies[0]

			assert.Equal(t, cookieName, c.Name)
			assert.Equal(t, "raw-token-value", c.Value)
			assert.Equal(t, "/", c.Path)
			assert.True(t, c.HttpOnly)
			assert.Equal(t, tt.secure, c.Secure)
			assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
			assert.WithinDuration(t, time.Now().Add(sessionTTL), c.Expires, 5*time.Second)
		})
	}
}

func TestClearSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	ClearSessionCookie(w, true)

	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	c := cookies[0]

	assert.Equal(t, cookieName, c.Name)
	assert.Empty(t, c.Value)
	assert.Equal(t, -1, c.MaxAge, "MaxAge -1 is what actually tells the browser to delete the cookie")
}

func TestSessionExpiry(t *testing.T) {
	assert.WithinDuration(t, time.Now().Add(30*24*time.Hour), SessionExpiry(), 5*time.Second)
}

func TestUserFromContext(t *testing.T) {
	t.Run("returns the user when present", func(t *testing.T) {
		want := db.User{ID: 42, Username: "mario", Role: "admin"}
		ctx := context.WithValue(context.Background(), userContextKey, want)

		got, ok := UserFromContext(ctx)
		assert.True(t, ok)
		assert.Equal(t, want, got)
	})

	t.Run("returns false when absent", func(t *testing.T) {
		_, ok := UserFromContext(context.Background())
		assert.False(t, ok)
	})

	t.Run("returns false when the key holds an unexpected type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), userContextKey, "not-a-CurrentUser")
		_, ok := UserFromContext(ctx)
		assert.False(t, ok, "type assertion should fail safely, not panic")
	})
}

// TestRequireSession_NoDatabaseNeeded covers only the branch that returns
// before ever touching *db.Queries (passing nil confirms that path really
// doesn't dereference it).
func TestRequireSession(t *testing.T) {
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
				name:         "no session cookie at all",
				setupRequest: func(r *http.Request) {},
			},
			{
				name: "session cookie present but empty",
				setupRequest: func(r *http.Request) {
					r.AddCookie(&http.Cookie{Name: cookieName, Value: ""})
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				handlerCalled = false
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				tt.setupRequest(r)
				w := httptest.NewRecorder()

				RequireSession(nil)(next).ServeHTTP(w, r)

				assert.Equal(t, http.StatusUnauthorized, w.Code)
				assert.False(t, handlerCalled, "the wrapped handler must never run without a valid session")
			})
		}
	})

	t.Run("With Database", func(t *testing.T) {
		pool := dbtest.Setup(t)
		q := db.New(pool)

		tests := []struct {
			name       string
			makeCookie func(t *testing.T) *http.Cookie
			wantStatus int
			wantCalled bool
		}{
			{
				name: "valid, unexpired session succeeds",
				makeCookie: func(t *testing.T) *http.Cookie {
					user := dbtest.CreateUser(t, pool, "member")
					raw, hash, err := GenerateSessionToken()
					require.NoError(t, err)
					dbtest.CreateSession(t, pool, db.CreateSessionParams{
						SessionHash: hash,
						UserID:      user.ID,
						ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
					})
					return &http.Cookie{Name: cookieName, Value: raw}
				},
				wantStatus: http.StatusOK,
				wantCalled: true,
			},
			{
				name: "expired session is rejected",
				makeCookie: func(t *testing.T) *http.Cookie {
					user := dbtest.CreateUser(t, pool, "member")
					raw, hash, err := GenerateSessionToken()
					require.NoError(t, err)
					dbtest.CreateSession(t, pool, db.CreateSessionParams{
						SessionHash: hash,
						UserID:      user.ID,
						ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true}, // already expired
					})
					return &http.Cookie{Name: cookieName, Value: raw}
				},
				wantStatus: http.StatusUnauthorized,
				wantCalled: false,
			},
			{
				name: "well-formed but unknown token is rejected",
				makeCookie: func(t *testing.T) *http.Cookie {
					raw, _, err := GenerateSessionToken()
					require.NoError(t, err)
					return &http.Cookie{Name: cookieName, Value: raw} // never inserted
				},
				wantStatus: http.StatusUnauthorized,
				wantCalled: false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				handlerCalled := false
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handlerCalled = true
					w.WriteHeader(http.StatusOK)
				})

				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.AddCookie(tt.makeCookie(t))
				w := httptest.NewRecorder()

				RequireSession(q)(next).ServeHTTP(w, r)

				assert.Equal(t, tt.wantStatus, w.Code)
				assert.Equal(t, tt.wantCalled, handlerCalled)
			})
		}
	})
}

func TestRequireAdmin(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)

	sessionCookieFor := func(t *testing.T, role string) *http.Cookie {
		user := dbtest.CreateUser(t, pool, role)
		raw, hash, err := GenerateSessionToken()
		require.NoError(t, err)
		dbtest.CreateSession(t, pool, db.CreateSessionParams{
			SessionHash: hash,
			UserID:      user.ID,
			ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		})
		return &http.Cookie{Name: cookieName, Value: raw}
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("admin role is allowed through", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(sessionCookieFor(t, "admin"))
		w := httptest.NewRecorder()

		RequireAdmin(q)(next).ServeHTTP(w, r)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("member role is forbidden", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(sessionCookieFor(t, "member"))
		w := httptest.NewRecorder()

		RequireAdmin(q)(next).ServeHTTP(w, r)
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}
