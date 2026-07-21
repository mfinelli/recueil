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
	"bytes"
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
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
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
// mock server, not the unreachable address), an archive.Store rooted at
// a fresh t.TempDir() (empty; tests exercising capture HTML content
// write into it themselves via internal/archive directly, same as
// production would), and a fresh bootstrap token. One shared URL for
// both Worker clients mirrors production, where they're both pointed at
// the same real Worker deployment (cfg.WorkerURL).
func newTestServer(t *testing.T, pool *pgxpool.Pool, mirrorURL string) (server *httptest.Server, rawBootstrapToken string) {
	t.Helper()
	q := db.New(pool)
	m := mirror.NewClient(mirrorURL, "test-secret")
	d := devices.NewClient(mirrorURL, "test-secret")
	store := archive.New(t.TempDir())
	bootstrap, rawToken, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)

	s := httpapi.NewServer(q, pool, store, m, d, bootstrap, false, testPairingKey(t))
	logger := httplog.NewLogger("recueil-test")
	logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{})
	require.NoError(t, err)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return srv, rawToken
}

// newTestServerWithStore is newTestServer's twin for the handful of tests
// that need real on-disk archive content (GetCaptureHTML) rather than
// just a capture row -- newTestServer itself doesn't expose its internal
// Store, and changing its signature to return one would touch every one
// of its ~40 other call sites for a need only this one test area has.
func newTestServerWithStore(t *testing.T, pool *pgxpool.Pool, mirrorURL string) (*httptest.Server, *archive.Store) {
	t.Helper()
	q := db.New(pool)
	m := mirror.NewClient(mirrorURL, "test-secret")
	d := devices.NewClient(mirrorURL, "test-secret")
	store := archive.New(t.TempDir())
	bootstrap, _, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)

	s := httpapi.NewServer(q, pool, store, m, d, bootstrap, false, testPairingKey(t))
	logger := httplog.NewLogger("recueil-test")
	logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{})
	require.NoError(t, err)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return srv, store
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

func TestSetupStatus(t *testing.T) {
	pool := dbtest.Setup(t)
	dbtest.Reset(t, pool) // needs a genuinely empty table to start, same as TestSetup

	t.Run("needs_setup is true with no users, false once one exists", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)

		resp, err := http.Get(server.URL + "/api/setup-status")
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		var got struct {
			NeedsSetup bool `json:"needs_setup"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.True(t, got.NeedsSetup)

		dbtest.CreateUser(t, pool, "admin")

		resp2, err := http.Get(server.URL + "/api/setup-status")
		require.NoError(t, err)
		require.NoError(t, json.NewDecoder(resp2.Body).Decode(&got))
		assert.False(t, got.NeedsSetup)
	})
}

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

	t.Run("?user_id= is ignored -- always self-scoped, even for an admin", func(t *testing.T) {
		admin := dbtest.CreateUser(t, pool, "admin")
		other := dbtest.CreateUser(t, pool, "member")
		workerServer := newDeviceWorkerServer(t, map[int64][]map[string]any{
			admin.ID: {{"id": float64(1), "device_name": "admin-laptop", "device_type": "extension", "created_at": "2026-06-01 12:00:00", "last_used_at": nil}},
			other.ID: {{"id": float64(2), "device_name": "phone", "device_type": "pwa", "created_at": "2026-06-01 12:00:00", "last_used_at": nil}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, admin)

		// Passing another user's id shouldn't change anything -- this
		// still returns the admin's own devices, not other's. Cross-user
		// device management was reconsidered and removed; the admin
		// role has no special reach here at all, only an eventual
		// operator-only CLI command will (see ListDevices' own doc
		// comment).
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
		assert.Equal(t, "admin-laptop", got.Devices[0].DeviceName)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/devices")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
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
		// doesn't own token 9, so the Worker's own cross-check is what
		// actually blocks this.
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

	t.Run("?user_id= is ignored -- an admin cannot revoke another user's device via it", func(t *testing.T) {
		admin := dbtest.CreateUser(t, pool, "admin")
		other := dbtest.CreateUser(t, pool, "member")
		workerServer := newDeviceWorkerServer(t, map[int64][]map[string]any{
			other.ID: {{"id": float64(3), "device_name": "cli", "device_type": "cli", "created_at": "2026-06-01 12:00:00", "last_used_at": nil}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, admin)

		// Cross-user device management was reconsidered and removed --
		// the ?user_id= is simply ignored now, so this resolves to the
		// admin's own (nonexistent) device 3 and 404s, exactly as it
		// would for any other user attempting the same request.
		resp := requestWithCookie(t, server,
			http.MethodDelete, fmt.Sprintf("/api/devices/3?user_id=%d", other.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
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

func TestListPages(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("plain listing returns only the caller's pages, most recent first, with a total", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		dbtest.CreatePage(t, pool, other.ID, "https://example.com/not-mine")
		dbtest.CreatePage(t, pool, user.ID, "https://example.com/a")
		dbtest.CreatePage(t, pool, user.ID, "https://example.com/b")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/pages", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Pages []struct {
				NormalizedURL string `json:"normalized_url"`
			} `json:"pages"`
			Total int64 `json:"total"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.EqualValues(t, 2, got.Total)
		require.Len(t, got.Pages, 2)
	})

	t.Run("q= searches reader_text across a page's captures", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/searchable")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		dbtest.SetCaptureReaderText(t, pool, capture.ID, "a very particular sentence about narwhals")

		unrelated := dbtest.CreatePage(t, pool, user.ID, "https://example.com/unrelated")
		unrelatedCapture := dbtest.CreateCapture(t, pool, unrelated.ID)
		dbtest.SetCaptureReaderText(t, pool, unrelatedCapture.ID, "something about baking bread")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/pages?q=narwhals", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Pages []struct {
				ID int64 `json:"id"`
			} `json:"pages"`
			Total int64 `json:"total"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Pages, 1)
		assert.Equal(t, page.ID, got.Pages[0].ID)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/pages")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestGetPage(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("returns the page plus its capture history, most recent first", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/history")
		older := dbtest.CreateCapture(t, pool, page.ID)
		time.Sleep(10 * time.Millisecond) // ensure a distinct captured_at ordering
		newer := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ID       int64 `json:"id"`
			Captures []struct {
				ID int64 `json:"id"`
			} `json:"captures"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, page.ID, got.ID)
		require.Len(t, got.Captures, 2)
		assert.Equal(t, newer.ID, got.Captures[0].ID, "most recently captured must come first")
		assert.Equal(t, older.ID, got.Captures[1].ID)
	})

	t.Run("another user's page returns 404, not their data", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/not-yours")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, requester)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a nonexistent id returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/pages/9999999", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a non-numeric id returns 400", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/pages/not-a-number", cookie)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestPatchPage(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("toggles excluded_from_mirror for the owner", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/toggle")
		require.False(t, page.ExcludedFromMirror)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/pages/%d", page.ID),
			strings.NewReader(`{"excluded_from_mirror":true}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ExcludedFromMirror bool `json:"excluded_from_mirror"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.True(t, got.ExcludedFromMirror)
	})

	t.Run("another user's page returns 404, not a silent no-op success", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/not-patchable")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, requester)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/pages/%d", page.ID),
			strings.NewReader(`{"excluded_from_mirror":true}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("missing excluded_from_mirror returns 400", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/missing-field")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/pages/%d", page.ID),
			strings.NewReader(`{}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestGetCapture(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("returns full capture detail including reader_text for the owner", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/detail")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		dbtest.SetCaptureReaderText(t, pool, capture.ID, "the full article text")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/captures/%d", capture.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ID         int64  `json:"id"`
			ReaderText string `json:"reader_text"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, capture.ID, got.ID)
		assert.Equal(t, "the full article text", got.ReaderText)
	})

	t.Run("another user's capture returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/not-yours-capture")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, requester)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/captures/%d", capture.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a non-numeric id returns 400", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/captures/not-a-number", cookie)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/captures/1")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestGetCaptureHTML(t *testing.T) {
	pool := dbtest.Setup(t)
	htmlContent := []byte(strings.Repeat("<html><body>hello world</body></html>", 500))

	t.Run("without Accept-Encoding: zstd, streams plain decompressed HTML", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/html-plain")
		server, store := newTestServerWithStore(t, pool, unreachable)
		capture := dbtest.CreateCaptureWithHTML(t, pool, store, page.ID, htmlContent)
		cookie := sessionCookieFor(t, pool, user)

		req, err := http.NewRequest(http.MethodGet, server.URL+fmt.Sprintf("/api/captures/%d/html", capture.ID), http.NoBody)
		require.NoError(t, err)
		req.AddCookie(cookie)
		// Explicitly refuse compression so this test asserts the plain
		// path, not whichever encoding the default transport happens to
		// negotiate.
		req.Header.Set("Accept-Encoding", "identity")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Empty(t, resp.Header.Get("Content-Encoding"))
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
		assert.Equal(t, "script-src 'none'", resp.Header.Get("Content-Security-Policy"))

		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, htmlContent, got)
	})

	t.Run("with Accept-Encoding: zstd, streams the raw compressed bytes unmodified", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/html-zstd")
		server, store := newTestServerWithStore(t, pool, unreachable)
		capture := dbtest.CreateCaptureWithHTML(t, pool, store, page.ID, htmlContent)
		cookie := sessionCookieFor(t, pool, user)

		req, err := http.NewRequest(http.MethodGet, server.URL+fmt.Sprintf("/api/captures/%d/html", capture.ID), http.NoBody)
		require.NoError(t, err)
		req.AddCookie(cookie)
		req.Header.Set("Accept-Encoding", "zstd")
		// Go's http.Transport auto-negotiates/decodes gzip unless a
		// caller sets its own Accept-Encoding, which we just did --
		// but it can still choose to transparently decode other
		// encodings it doesn't recognize, so read the raw bytes off
		// the wire via a Transport with compression fully disabled.
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "zstd", resp.Header.Get("Content-Encoding"))
		assert.Equal(t, "script-src 'none'", resp.Header.Get("Content-Security-Policy"))

		rawGot, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.NotEqual(t, htmlContent, rawGot, "sanity check: response body must actually be compressed, not accidentally plain")

		decoder, err := zstd.NewReader(bytes.NewReader(rawGot))
		require.NoError(t, err)
		defer decoder.Close()
		decoded, err := io.ReadAll(decoder)
		require.NoError(t, err)
		assert.Equal(t, htmlContent, decoded)
	})

	t.Run("another user's capture returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/html-not-yours")
		server, store := newTestServerWithStore(t, pool, unreachable)
		capture := dbtest.CreateCaptureWithHTML(t, pool, store, page.ID, htmlContent)
		cookie := sessionCookieFor(t, pool, requester)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/captures/%d/html", capture.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestPatchCaptureLanguage(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("updates the language for the owner", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/lang")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		require.Equal(t, "simple", capture.Language)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/captures/%d/language", capture.ID),
			strings.NewReader(`{"language":"english"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Language string `json:"language"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, "english", got.Language)
	})

	t.Run("an invalid language name returns 400, not a raw 500", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/lang-invalid")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/captures/%d/language", capture.ID),
			strings.NewReader(`{"language":"not-a-real-config"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("another user's capture returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/lang-not-yours")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, requester)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/captures/%d/language", capture.ID),
			strings.NewReader(`{"language":"english"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestListTextSearchConfigs(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("returns the running Postgres instance's real pg_ts_config catalog", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/text-search-configs", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Languages []string `json:"languages"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Contains(t, got.Languages, "english")
		assert.Contains(t, got.Languages, "simple")
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/text-search-configs")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestListTags(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("returns only the caller's tags, alphabetically", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		_, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: other.ID, Name: "not-mine"})
		require.NoError(t, err)
		_, err = q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "zebra"})
		require.NoError(t, err)
		_, err = q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "aardvark"})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/tags", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Tags []struct {
				Name string `json:"name"`
			} `json:"tags"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Tags, 2)
		assert.Equal(t, "aardvark", got.Tags[0].Name)
		assert.Equal(t, "zebra", got.Tags[1].Name)
	})
}

func TestAddAndRemovePageTag(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("adds a tag with source manual, then removes it", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/tag-me")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		req, err := http.NewRequest(http.MethodPost, server.URL+fmt.Sprintf("/api/pages/%d/tags", page.ID),
			strings.NewReader(`{"name":"recipes"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusCreated, resp.StatusCode)

		var tag struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&tag))
		assert.Equal(t, "recipes", tag.Name)

		detailResp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		var detail struct {
			Tags []struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			} `json:"tags"`
		}
		require.NoError(t, json.NewDecoder(detailResp.Body).Decode(&detail))
		require.Len(t, detail.Tags, 1)
		assert.Equal(t, "recipes", detail.Tags[0].Name)
		assert.Equal(t, "manual", detail.Tags[0].Source)

		delResp := requestWithCookie(t, server, http.MethodDelete,
			fmt.Sprintf("/api/pages/%d/tags/%d", page.ID, tag.ID), cookie)
		assert.Equal(t, http.StatusNoContent, delResp.StatusCode)

		afterResp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		var after struct {
			Tags []struct{} `json:"tags"`
		}
		require.NoError(t, json.NewDecoder(afterResp.Body).Decode(&after))
		assert.Empty(t, after.Tags)
	})

	t.Run("tagging another user's page returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/not-your-tag-target")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, requester)

		req, err := http.NewRequest(http.MethodPost, server.URL+fmt.Sprintf("/api/pages/%d/tags", page.ID),
			strings.NewReader(`{"name":"recipes"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestCollectionsCRUD(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("create, list, rename, delete a top-level collection", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		createReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/collections",
			strings.NewReader(`{"name":"Reading List"}`))
		require.NoError(t, err)
		createReq.Header.Set("Content-Type", "application/json")
		createReq.AddCookie(cookie)
		createResp, err := http.DefaultClient.Do(createReq)
		require.NoError(t, err)
		assert.Equal(t, http.StatusCreated, createResp.StatusCode)

		var collection struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			ParentID *int64 `json:"parent_id"`
		}
		require.NoError(t, json.NewDecoder(createResp.Body).Decode(&collection))
		assert.Equal(t, "Reading List", collection.Name)
		assert.Nil(t, collection.ParentID)

		listResp := requestWithCookie(t, server, http.MethodGet, "/api/collections", cookie)
		var list struct {
			Collections []struct {
				ID int64 `json:"id"`
			} `json:"collections"`
		}
		require.NoError(t, json.NewDecoder(listResp.Body).Decode(&list))
		require.Len(t, list.Collections, 1)
		assert.Equal(t, collection.ID, list.Collections[0].ID)

		renameReq, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/collections/%d", collection.ID),
			strings.NewReader(`{"name":"Books"}`))
		require.NoError(t, err)
		renameReq.Header.Set("Content-Type", "application/json")
		renameReq.AddCookie(cookie)
		renameResp, err := http.DefaultClient.Do(renameReq)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, renameResp.StatusCode)
		var renamed struct {
			Name string `json:"name"`
		}
		require.NoError(t, json.NewDecoder(renameResp.Body).Decode(&renamed))
		assert.Equal(t, "Books", renamed.Name)

		delResp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/collections/%d", collection.ID), cookie)
		assert.Equal(t, http.StatusNoContent, delResp.StatusCode)

		getAfterDelete := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/collections/%d/pages", collection.ID), cookie)
		assert.Equal(t, http.StatusNotFound, getAfterDelete.StatusCode)
	})

	t.Run("a duplicate top-level name returns 409", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		body := `{"name":"Duplicate"}`

		firstReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/collections", strings.NewReader(body))
		require.NoError(t, err)
		firstReq.Header.Set("Content-Type", "application/json")
		firstReq.AddCookie(cookie)
		firstResp, err := http.DefaultClient.Do(firstReq)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, firstResp.StatusCode)

		secondReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/collections", strings.NewReader(body))
		require.NoError(t, err)
		secondReq.Header.Set("Content-Type", "application/json")
		secondReq.AddCookie(cookie)
		secondResp, err := http.DefaultClient.Do(secondReq)
		require.NoError(t, err)
		assert.Equal(t, http.StatusConflict, secondResp.StatusCode)
	})

	t.Run("nesting under another user's collection id is rejected", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		otherCollection, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{
			UserID: owner.ID, Name: "Owner's Collection",
		})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, requester)

		body := fmt.Sprintf(`{"name":"Sneaky","parent_id":%d}`, otherCollection.ID)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/collections", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("renaming or deleting another user's collection returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		collection, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{
			UserID: owner.ID, Name: "Not Yours",
		})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, requester)

		renameReq, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/collections/%d", collection.ID),
			strings.NewReader(`{"name":"Hijacked"}`))
		require.NoError(t, err)
		renameReq.Header.Set("Content-Type", "application/json")
		renameReq.AddCookie(cookie)
		renameResp, err := http.DefaultClient.Do(renameReq)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, renameResp.StatusCode)

		delResp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/collections/%d", collection.ID), cookie)
		assert.Equal(t, http.StatusNotFound, delResp.StatusCode)
	})
}

func TestPageCollectionMembership(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("add a page to a collection, list it, then remove it", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/collect-me")
		q := db.New(pool)
		collection, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{
			UserID: user.ID, Name: "My Collection",
		})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		addReq, err := http.NewRequest(http.MethodPost, server.URL+fmt.Sprintf("/api/pages/%d/collections", page.ID),
			strings.NewReader(fmt.Sprintf(`{"collection_id":%d}`, collection.ID)))
		require.NoError(t, err)
		addReq.Header.Set("Content-Type", "application/json")
		addReq.AddCookie(cookie)
		addResp, err := http.DefaultClient.Do(addReq)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNoContent, addResp.StatusCode)

		pagesResp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/collections/%d/pages", collection.ID), cookie)
		assert.Equal(t, http.StatusOK, pagesResp.StatusCode)
		var pages struct {
			Pages []struct {
				ID int64 `json:"id"`
			} `json:"pages"`
		}
		require.NoError(t, json.NewDecoder(pagesResp.Body).Decode(&pages))
		require.Len(t, pages.Pages, 1)
		assert.Equal(t, page.ID, pages.Pages[0].ID)

		detailResp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		var detail struct {
			Collections []struct {
				ID int64 `json:"id"`
			} `json:"collections"`
		}
		require.NoError(t, json.NewDecoder(detailResp.Body).Decode(&detail))
		require.Len(t, detail.Collections, 1)
		assert.Equal(t, collection.ID, detail.Collections[0].ID)

		remResp := requestWithCookie(t, server, http.MethodDelete,
			fmt.Sprintf("/api/pages/%d/collections/%d", page.ID, collection.ID), cookie)
		assert.Equal(t, http.StatusNoContent, remResp.StatusCode)

		afterResp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/collections/%d/pages", collection.ID), cookie)
		var after struct {
			Pages []struct{} `json:"pages"`
		}
		require.NoError(t, json.NewDecoder(afterResp.Body).Decode(&after))
		assert.Empty(t, after.Pages)
	})

	t.Run("adding to another user's collection returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		otherOwner := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/my-page-their-collection")
		q := db.New(pool)
		theirCollection, err := q.CreateCollection(context.Background(), db.CreateCollectionParams{
			UserID: otherOwner.ID, Name: "Not Yours Either",
		})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		req, err := http.NewRequest(http.MethodPost, server.URL+fmt.Sprintf("/api/pages/%d/collections", page.ID),
			strings.NewReader(fmt.Sprintf(`{"collection_id":%d}`, theirCollection.ID)))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestGetPageFavicon(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("serves the page's favicon with the right content type", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, store := newTestServerWithStore(t, pool, unreachable)

		faviconBytes := []byte("<svg>fake favicon</svg>")
		relPath, _, err := store.WriteAsset("test-html-hash", "test-favicon-hash", "svg", faviconBytes, true)
		require.NoError(t, err)

		q := db.New(pool)
		page, err := q.UpsertPage(context.Background(), db.UpsertPageParams{
			UserID: user.ID, NormalizedUrl: "https://example.com/has-favicon",
			Title:           pgtype.Text{String: "Has Favicon", Valid: true},
			LatestCaptureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
			FaviconPath:     pgtype.Text{String: relPath, Valid: true},
		})
		require.NoError(t, err)

		cookie := sessionCookieFor(t, pool, user)
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/favicon", page.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "image/svg+xml", resp.Header.Get("Content-Type"))

		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, faviconBytes, got, "must be decompressed, not the raw zstd bytes")
	})

	t.Run("a page with no favicon returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/no-favicon")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/favicon", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("another user's page returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/favicon-not-yours")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, requester)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/favicon", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestGetPageThumbnail(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("serves the latest capture's thumbnail with the right content type", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/has-thumbnail")
		server, store := newTestServerWithStore(t, pool, unreachable)
		capture := dbtest.CreateCapture(t, pool, page.ID)

		thumbnailBytes := []byte("fake png bytes")
		relPath, _, err := store.WriteAsset("test-html-hash-2", "test-thumb-hash", "png", thumbnailBytes, false)
		require.NoError(t, err)

		_, err = pool.Exec(context.Background(),
			"UPDATE captures SET thumbnail_path = $1 WHERE id = $2", relPath, capture.ID)
		require.NoError(t, err)

		cookie := sessionCookieFor(t, pool, user)
		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/thumbnail", page.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))

		got, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, thumbnailBytes, got)
	})

	t.Run("a capture with no thumbnail yet returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/no-thumbnail-yet")
		dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/thumbnail", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a page with no captures at all returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/no-captures")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/thumbnail", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("another user's page returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/thumbnail-not-yours")
		dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, requester)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/thumbnail", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
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
