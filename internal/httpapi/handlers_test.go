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

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/httpapi"
	"github.com/mfinelli/recueil/internal/mirror"
)

// The cookie name is a private constant in internal/auth (cookieName =
// "recueil_session"). It's hardcoded here rather than referenced, since
// this is an external test package exercising the public HTTP surface
// only. If that constant ever changes, this needs updating alongside
// it.
const sessionCookieName = "recueil_session"

// newTestServer wires a full, real Server behind chi's router: a real
// Postgres connection (pool), a mirror.Client pointed at mirrorURL (point
// it at an unreachable address like "http://127.0.0.1:1" for tests that
// don't care about mirror push behavior;  PushUser failures are logged,
// not blocking, so this is safe), and a fresh bootstrap token.
func newTestServer(t *testing.T, pool *pgxpool.Pool, mirrorURL string) (server *httptest.Server, rawBootstrapToken string) {
	t.Helper()
	q := db.New(pool)
	m := mirror.NewClient(mirrorURL, "test-secret")
	bootstrap, rawToken, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)

	s := httpapi.NewServer(q, m, bootstrap, false)
	srv := httptest.NewServer(httpapi.NewRouter(s, q))
	t.Cleanup(srv.Close)

	return srv, rawToken
}

// newMirrorServer is a mock Worker: records every request path it receives
// and returns 204.
func newMirrorServer(t *testing.T) (server *httptest.Server, receivedPaths *[]string) {
	t.Helper()
	var received []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = append(received, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, &received
}

func decodeUserResponse(t *testing.T, body *http.Response) struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
} {
	t.Helper()
	var got struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	require.NoError(t, json.NewDecoder(body.Body).Decode(&got))
	return got
}

func hasSessionCookie(resp *http.Response) bool {
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return true
		}
	}
	return false
}

func deleteUserByUsername(t *testing.T, pool *pgxpool.Pool, username string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE username = $1", username)
	})
}

const unreachable = "http://127.0.0.1:1" // reserved/unroutable; connections fail fast

func TestSetup(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool) // Setup's "already completed" check needs a genuinely empty table to start

	t.Run("missing fields returns 400", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Post(server.URL+"/api/setup", "application/json", strings.NewReader(`{}`))
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Post(server.URL+"/api/setup", "application/json", strings.NewReader(`not json`))
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("wrong bootstrap token returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		body := `{"bootstrap_token":"wrong","username":"setup-wrongtoken","password":"hunter2"}`
		resp, err := http.Post(server.URL+"/api/setup", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("valid token creates the admin, sets a session cookie, pushes the mirror", func(t *testing.T) {
		mirrorServer, received := newMirrorServer(t)
		server, rawToken := newTestServer(t, pool, mirrorServer.URL)
		deleteUserByUsername(t, pool, "setup-success")

		body := fmt.Sprintf(`{"bootstrap_token":"%s","username":"setup-success","password":"hunter2"}`, rawToken)
		resp, err := http.Post(server.URL+"/api/setup", "application/json", strings.NewReader(body))
		require.NoError(t, err)

		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		assert.True(t, hasSessionCookie(resp))

		got := decodeUserResponse(t, resp)
		assert.Equal(t, "setup-success", got.Username)
		assert.Equal(t, "admin", got.Role)

		assert.Equal(t, []string{"/internal/users/mirror"}, *received)
	})

	t.Run("reusing the same token after success returns 409, not 401", func(t *testing.T) {
		// Not a token-reuse-specific check: Setup's "already completed"
		// check (count > 0) runs before bootstrap-token validation, so once
		// the first call above creates an admin, *any* further call
		// (valid-but-consumed token or otherwise) hits that check first.
		mirrorServer, _ := newMirrorServer(t)
		server, rawToken := newTestServer(t, pool, mirrorServer.URL)
		deleteUserByUsername(t, pool, "setup-reuse")

		body := fmt.Sprintf(`{"bootstrap_token":"%s","username":"setup-reuse","password":"hunter2"}`, rawToken)
		resp1, err := http.Post(server.URL+"/api/setup", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp1.StatusCode)

		resp2, err := http.Post(server.URL+"/api/setup", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, http.StatusConflict, resp2.StatusCode)
	})

	t.Run("account creation still succeeds even if the mirror push fails", func(t *testing.T) {
		brokenMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(brokenMirror.Close)
		server, rawToken := newTestServer(t, pool, brokenMirror.URL)
		deleteUserByUsername(t, pool, "setup-mirrorfail")

		body := fmt.Sprintf(`{"bootstrap_token":"%s","username":"setup-mirrorfail","password":"hunter2"}`, rawToken)
		resp, err := http.Post(server.URL+"/api/setup", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, http.StatusCreated, resp.StatusCode, "mirror push failure must not block account creation")
	})

	// Runs last deliberately: it's the one case that depends on the users
	// table already having a row, so it must not run before the empty-table
	// assumptions the earlier cases rely on.
	t.Run("setup already completed (a user already exists) returns 409", func(t *testing.T) {
		dbtest.CreateUser(t, pool, "member")
		server, rawToken := newTestServer(t, pool, unreachable)

		body := fmt.Sprintf(`{"bootstrap_token":"%s","username":"setup-toolate","password":"hunter2"}`, rawToken)
		resp, err := http.Post(server.URL+"/api/setup", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})
}

func TestRegister(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("missing fields returns 400", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{}`))
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`not json`))
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("successful registration creates a member and sets a session cookie", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		deleteUserByUsername(t, pool, "register-success")

		body := `{"username":"register-success","password":"hunter2"}`
		resp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(body))
		require.NoError(t, err)

		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		assert.True(t, hasSessionCookie(resp))

		got := decodeUserResponse(t, resp)
		assert.Equal(t, "member", got.Role, "open registration (§5) must never grant admin")
	})

	t.Run("duplicate username returns 409", func(t *testing.T) {
		existing := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)

		body := fmt.Sprintf(`{"username":"%s","password":"hunter2"}`, existing.Username)
		resp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})
}

// createUserWithPassword bypasses dbtest.CreateUser's placeholder
// password_hash (it's not a real bcrypt hash, and dbtest deliberately
// doesn't import internal/auth; see dbtest.go's package doc) since Login
// needs a real hash for a known plaintext password to authenticate against.
func createUserWithPassword(t *testing.T, pool *pgxpool.Pool, username, password string) db.User {
	t.Helper()
	hash, err := auth.HashPassword(password)
	require.NoError(t, err)
	user, err := db.New(pool).CreateUser(context.Background(), db.CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		Role:         "member",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID)
	})
	return user
}

func TestLogin(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("invalid JSON returns 400", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`not json`))
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("unknown username returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		body := `{"username":"nobody-like-this-exists","password":"whatever"}`
		resp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("wrong password returns 401", func(t *testing.T) {
		user := createUserWithPassword(t, pool, "login-wrongpw", "correct-password")
		server, _ := newTestServer(t, pool, unreachable)

		body := fmt.Sprintf(`{"username":"%s","password":"incorrect-password"}`, user.Username)
		resp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("correct credentials succeed and set a session cookie", func(t *testing.T) {
		user := createUserWithPassword(t, pool, "login-success", "correct-password")
		server, _ := newTestServer(t, pool, unreachable)

		body := fmt.Sprintf(`{"username":"%s","password":"correct-password"}`, user.Username)
		resp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(body))
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, hasSessionCookie(resp))

		got := decodeUserResponse(t, resp)
		assert.Equal(t, user.Username, got.Username)
	})
}

func TestLogout(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newTestServer(t, pool, unreachable)

	t.Run("clears the cookie and actually deletes the session", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		raw, hash, err := auth.GenerateSessionToken()
		require.NoError(t, err)
		dbtest.CreateSession(t, pool, db.CreateSessionParams{
			SessionHash: hash, UserID: user.ID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		})

		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/auth/logout", nil)
		require.NoError(t, err)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		var cleared bool
		for _, c := range resp.Cookies() {
			if c.Name == sessionCookieName && c.MaxAge == -1 {
				cleared = true
			}
		}
		assert.True(t, cleared, "logout must clear the session cookie (MaxAge -1)")

		// The session must actually be gone from the DB, not just the
		// cookie cleared client-side: reusing the same (pre-logout) raw
		// token against /api/auth/me must now be rejected.
		req2, err := http.NewRequest(http.MethodGet, server.URL+"/api/auth/me", nil)
		require.NoError(t, err)
		req2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
		resp2, err := http.DefaultClient.Do(req2)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
	})

	t.Run("succeeds even without a session cookie", func(t *testing.T) {
		resp, err := http.Post(server.URL+"/api/auth/logout", "application/json", nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})
}

func TestMe(t *testing.T) {
	pool := dbtest.Setup(t)
	server, _ := newTestServer(t, pool, unreachable)

	t.Run("returns the current user for a valid session", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "admin")
		raw, hash, err := auth.GenerateSessionToken()
		require.NoError(t, err)
		dbtest.CreateSession(t, pool, db.CreateSessionParams{
			SessionHash: hash, UserID: user.ID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		})

		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/auth/me", nil)
		require.NoError(t, err)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		got := decodeUserResponse(t, resp)
		assert.Equal(t, user.ID, got.ID)
		assert.Equal(t, user.Username, got.Username)
		assert.Equal(t, "admin", got.Role)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/auth/me")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("an unmapped route 404s via chi's own default", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/does-not-exist")
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}
