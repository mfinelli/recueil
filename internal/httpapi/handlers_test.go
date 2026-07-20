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
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/httplog/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/devices"
	"github.com/mfinelli/recueil/internal/httpapi"
	"github.com/mfinelli/recueil/internal/mirror"
)

// The cookie name is a private constant in internal/auth (cookieName =
// "recueil_session"). It's hardcoded here rather than referenced, since
// this is an external test package exercising the public HTTP surface
// only. If that constant ever changes, this needs updating alongside
// it.
const sessionCookieName = "recueil_session"

// testPairingKey returns a fresh, valid random AES-256 pairing key for
// tests that don't care about a specific value.
func testPairingKey(t *testing.T) auth.PairingKey {
	t.Helper()
	var key auth.PairingKey
	_, err := rand.Read(key[:])
	require.NoError(t, err)
	return key
}

// newTestServer wires a full, real Server behind chi's router: a real
// Postgres connection (pool), a mirror.Client and a devices.Client both
// pointed at mirrorURL (point it at an unreachable address like
// "http://127.0.0.1:1" for tests that don't care about outbound Worker
// calls; mirror's PushUser failures are logged, not blocking, so this is
// safe -- devices.Client calls, on the other hand, are directly awaited
// by ListDevices/RevokeDevice, so a test exercising those needs a real
// mock server, not the unreachable address), and a fresh bootstrap
// token. One shared URL for both clients mirrors production, where
// they're both pointed at the same real Worker deployment
// (cfg.WorkerURL).
func newTestServer(t *testing.T, pool *pgxpool.Pool, mirrorURL string) (server *httptest.Server, rawBootstrapToken string) {
	t.Helper()
	q := db.New(pool)
	m := mirror.NewClient(mirrorURL, "test-secret")
	d := devices.NewClient(mirrorURL, "test-secret")
	bootstrap, rawToken, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)

	s := httpapi.NewServer(q, m, d, bootstrap, false, testPairingKey(t))
	logger := httplog.NewLogger("recueil-test")
	logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{})
	require.NoError(t, err)
	srv := httptest.NewServer(r)
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

// newMirrorServerCapturing is like newMirrorServer, but also decodes and
// records each pushed JSON body, so pairing-token tests can assert that
// what the dashboard decrypts and shows actually hashes to what was pushed
// to the D1 mirror.
func newMirrorServerCapturing(t *testing.T) (server *httptest.Server, bodies *[]map[string]any) {
	t.Helper()
	var received []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		received = append(received, body)
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

		body := fmt.Sprintf(`{"bootstrap_token":%q,"username":"setup-success","password":"hunter2"}`, rawToken)
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

		body := fmt.Sprintf(`{"bootstrap_token":%q,"username":"setup-reuse","password":"hunter2"}`, rawToken)
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

		body := fmt.Sprintf(`{"bootstrap_token":%q,"username":"setup-mirrorfail","password":"hunter2"}`, rawToken)
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

		body := fmt.Sprintf(`{"bootstrap_token":%q,"username":"setup-toolate","password":"hunter2"}`, rawToken)
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

		body := fmt.Sprintf(`{"username":%q,"password":"hunter2"}`, existing.Username)
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

		body := fmt.Sprintf(`{"username":%q,"password":"incorrect-password"}`, user.Username)
		resp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("correct credentials succeed and set a session cookie", func(t *testing.T) {
		user := createUserWithPassword(t, pool, "login-success", "correct-password")
		server, _ := newTestServer(t, pool, unreachable)

		body := fmt.Sprintf(`{"username":%q,"password":"correct-password"}`, user.Username)
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

		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/auth/logout", http.NoBody)
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
		req2, err := http.NewRequest(http.MethodGet, server.URL+"/api/auth/me", http.NoBody)
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

		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/auth/me", http.NoBody)
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

type pairingTokenBody struct {
	PairingToken string `json:"pairing_token"`
}

// requestWithCookie issues method against path carrying cookie, for the
// pairing-token endpoints below (GET/POST/DELETE all need a session).
func requestWithCookie(t *testing.T, server *httptest.Server, method, path string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, http.NoBody)
	require.NoError(t, err)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// registerAndGetSessionCookie registers a fresh member account via the
// real HTTP flow (not dbtest.CreateUser's placeholder password_hash/no
// pairing token) so there's a real, decryptable pairing_token_enc to
// exercise, and returns its session cookie.
func registerAndGetSessionCookie(t *testing.T, pool *pgxpool.Pool, server *httptest.Server, username string) *http.Cookie {
	t.Helper()
	deleteUserByUsername(t, pool, username)
	body := fmt.Sprintf(`{"username":%q,"password":"hunter2"}`, username)
	resp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("register did not set a session cookie")
	return nil
}

// sessionCookieFor creates a real session row for an already-created user
// (dbtest.CreateUser) and returns the matching cookie -- for tests that
// need a specific role (dbtest.CreateUser's "member"/"admin" param),
// unlike registerAndGetSessionCookie which only ever produces members via
// the real self-service /api/auth/register flow.
func sessionCookieFor(t *testing.T, pool *pgxpool.Pool, user db.User) *http.Cookie {
	t.Helper()
	raw, hash, err := auth.GenerateSessionToken()
	require.NoError(t, err)
	dbtest.CreateSession(t, pool, db.CreateSessionParams{
		SessionHash: hash, UserID: user.ID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	return &http.Cookie{Name: sessionCookieName, Value: raw}
}

// newDeviceWorkerServer is a mock Worker implementing just enough of
// GET/POST /internal/tokens for the Manage Devices tests: an in-memory
// map of userID -> tokens, checking X-Service-Key and user_id the same
// way the real Worker handler does (see terraform/index.js).
func newDeviceWorkerServer(t *testing.T, tokensByUser map[int64][]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Key") != "test-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/tokens":
			userID, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": tokensByUser[userID]})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/internal/tokens/"):
			tokenIDStr := strings.TrimPrefix(r.URL.Path, "/internal/tokens/")
			tokenID, err1 := strconv.ParseInt(tokenIDStr, 10, 64)
			userID, err2 := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
			if err1 != nil || err2 != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			tokens := tokensByUser[userID]
			for i, tok := range tokens {
				if int64(tok["id"].(float64)) == tokenID {
					tokensByUser[userID] = append(tokens[:i], tokens[i+1:]...)
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListDevices(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("a member sees their own devices with no user_id param", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		workerServer := newDeviceWorkerServer(t, map[int64][]map[string]any{
			member.ID: {{"id": float64(1), "device_name": "laptop", "device_type": "extension", "created_at": "2026-06-01 12:00:00", "last_used_at": nil}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, member)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/devices", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Devices []struct {
				ID         int64  `json:"id"`
				DeviceName string `json:"device_name"`
			} `json:"devices"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Devices, 1)
		assert.Equal(t, "laptop", got.Devices[0].DeviceName)
	})

	t.Run("a member requesting another user's devices via user_id gets 403", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		workerServer := newDeviceWorkerServer(t, map[int64][]map[string]any{})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, member)

		resp := requestWithCookie(t, server, http.MethodGet,
			fmt.Sprintf("/api/devices?user_id=%d", other.ID), cookie)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	})

	t.Run("an admin can list another user's devices via user_id", func(t *testing.T) {
		admin := dbtest.CreateUser(t, pool, "admin")
		other := dbtest.CreateUser(t, pool, "member")
		workerServer := newDeviceWorkerServer(t, map[int64][]map[string]any{
			other.ID: {{"id": float64(2), "device_name": "phone", "device_type": "pwa", "created_at": "2026-06-01 12:00:00", "last_used_at": nil}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, admin)

		resp := requestWithCookie(t, server, http.MethodGet,
			fmt.Sprintf("/api/devices?user_id=%d", other.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Devices []struct {
				DeviceName string `json:"device_name"`
			} `json:"devices"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Devices, 1)
		assert.Equal(t, "phone", got.Devices[0].DeviceName)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/devices")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("a malformed user_id returns 400", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, member)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/devices?user_id=not-a-number", cookie)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestRevokeDevice(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("a member can revoke their own device", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		workerServer := newDeviceWorkerServer(t, map[int64][]map[string]any{
			member.ID: {{"id": float64(5), "device_name": "laptop", "device_type": "extension", "created_at": "2026-06-01 12:00:00", "last_used_at": nil}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, member)

		resp := requestWithCookie(t, server, http.MethodDelete, "/api/devices/5", cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("a member cannot revoke another user's device even by guessing the id", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		workerServer := newDeviceWorkerServer(t, map[int64][]map[string]any{
			other.ID: {{"id": float64(9), "device_name": "laptop", "device_type": "extension", "created_at": "2026-06-01 12:00:00", "last_used_at": nil}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, member)

		// No ?user_id= at all -- the member's own id is used, which
		// doesn't own token 9, so the Worker's own cross-check (not
		// just resolveTargetUserID) is what actually blocks this.
		resp := requestWithCookie(t, server, http.MethodDelete, "/api/devices/9", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("revoking a nonexistent device returns 404", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		workerServer := newDeviceWorkerServer(t, map[int64][]map[string]any{})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, member)

		resp := requestWithCookie(t, server, http.MethodDelete, "/api/devices/999", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("an admin can revoke another user's device via user_id", func(t *testing.T) {
		admin := dbtest.CreateUser(t, pool, "admin")
		other := dbtest.CreateUser(t, pool, "member")
		workerServer := newDeviceWorkerServer(t, map[int64][]map[string]any{
			other.ID: {{"id": float64(3), "device_name": "cli", "device_type": "cli", "created_at": "2026-06-01 12:00:00", "last_used_at": nil}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, admin)

		resp := requestWithCookie(t, server,
			http.MethodDelete, fmt.Sprintf("/api/devices/3?user_id=%d", other.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		req, err := http.NewRequest(http.MethodDelete, server.URL+"/api/devices/1", http.NoBody)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestPairingToken(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("GET returns the token generated at registration, matching the mirrored hash", func(t *testing.T) {
		mirrorServer, bodies := newMirrorServerCapturing(t)
		server, _ := newTestServer(t, pool, mirrorServer.URL)
		cookie := registerAndGetSessionCookie(t, pool, server, "pairing-get")

		resp := requestWithCookie(t, server, http.MethodGet, "/api/pairing-token", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got pairingTokenBody
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.True(t, strings.HasPrefix(got.PairingToken, "rcl_pair_"))

		require.Len(t, *bodies, 1, "registration must push exactly one mirror row")
		pushedHash, _ := (*bodies)[0]["pairing_token_hash"].(string)
		assert.Equal(t, auth.HashToken(got.PairingToken), pushedHash,
			"the token the dashboard decrypts must hash to exactly what was mirrored to D1")
	})

	t.Run("GET without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/pairing-token")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("regenerate issues a new token, pushes a new hash, and GET reflects it", func(t *testing.T) {
		mirrorServer, bodies := newMirrorServerCapturing(t)
		server, _ := newTestServer(t, pool, mirrorServer.URL)
		cookie := registerAndGetSessionCookie(t, pool, server, "pairing-regen")

		firstResp := requestWithCookie(t, server, http.MethodGet, "/api/pairing-token", cookie)
		var first pairingTokenBody
		require.NoError(t, json.NewDecoder(firstResp.Body).Decode(&first))

		regenResp := requestWithCookie(t, server, http.MethodPost, "/api/pairing-token/regenerate", cookie)
		assert.Equal(t, http.StatusOK, regenResp.StatusCode)
		var second pairingTokenBody
		require.NoError(t, json.NewDecoder(regenResp.Body).Decode(&second))

		assert.NotEqual(t, first.PairingToken, second.PairingToken, "regenerate must issue a genuinely new token")

		require.Len(t, *bodies, 2, "one push at registration, one at regenerate")
		lastHash, _ := (*bodies)[1]["pairing_token_hash"].(string)
		assert.Equal(t, auth.HashToken(second.PairingToken), lastHash)

		confirmResp := requestWithCookie(t, server, http.MethodGet, "/api/pairing-token", cookie)
		var confirm pairingTokenBody
		require.NoError(t, json.NewDecoder(confirmResp.Body).Decode(&confirm))
		assert.Equal(t, second.PairingToken, confirm.PairingToken, "a follow-up GET must return the regenerated token, not the original")
	})

	t.Run("regenerate without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Post(server.URL+"/api/pairing-token/regenerate", "application/json", nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("revoke clears the token, pushes a null hash, and a subsequent GET 404s", func(t *testing.T) {
		mirrorServer, bodies := newMirrorServerCapturing(t)
		server, _ := newTestServer(t, pool, mirrorServer.URL)
		cookie := registerAndGetSessionCookie(t, pool, server, "pairing-revoke")

		revokeResp := requestWithCookie(t, server, http.MethodDelete, "/api/pairing-token", cookie)
		assert.Equal(t, http.StatusNoContent, revokeResp.StatusCode)

		require.Len(t, *bodies, 2, "one push at registration, one at revoke")
		assert.Nil(t, (*bodies)[1]["pairing_token_hash"],
			"revoke must push a JSON null, not omit the field or send an empty string")

		getResp := requestWithCookie(t, server, http.MethodGet, "/api/pairing-token", cookie)
		assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
	})

	t.Run("revoke without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		req, err := http.NewRequest(http.MethodDelete, server.URL+"/api/pairing-token", http.NoBody)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("account creation still succeeds even if the mirror push fails (same guarantee as Setup/Register)", func(t *testing.T) {
		brokenMirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(brokenMirror.Close)
		server, _ := newTestServer(t, pool, brokenMirror.URL)
		cookie := registerAndGetSessionCookie(t, pool, server, "pairing-mirrorfail")

		// The pairing token still exists in Postgres and is still viewable
		// via the dashboard even though the D1 mirror push failed -- device
		// pairing for this user is broken until a resync runs, but nothing
		// about the dashboard-facing flow is blocked.
		resp := requestWithCookie(t, server, http.MethodGet, "/api/pairing-token", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}
