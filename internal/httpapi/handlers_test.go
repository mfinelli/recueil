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
	"testing/fstest"
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
	"github.com/mfinelli/recueil/internal/queueitems"
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
// the same real Worker deployment (cfg.WorkerURL). Also wires fixed
// "test-readability-version"/"test-ai-model" strings (real production
// values a `make`-built binary/enabled AI enrichment would report) --
// GetCaptureConfig's own tests assert against these exact values, and
// every other one of this helper's ~40 other callers is unaffected by
// what these two are, same as they're unaffected by the exact bootstrap
// token value.
func newTestServer(t *testing.T, pool *pgxpool.Pool, mirrorURL string) (server *httptest.Server, rawBootstrapToken string) {
	t.Helper()
	q := db.New(pool)
	m := mirror.NewClient(mirrorURL, "test-secret")
	d := devices.NewClient(mirrorURL, "test-secret")
	qi := queueitems.NewClient(mirrorURL, "test-secret")
	store := archive.New(t.TempDir())
	bootstrap, rawToken, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)

	// EnableOpenRegistration is true here (unlike the real default) so the
	// ~40 existing callers of this helper, including TestRegister's own
	// happy-path coverage, keep exercising the real /api/auth/register
	// flow unchanged. TestRegisterDisabledByDefault covers the
	// default-false gate directly against its own server.
	s := httpapi.NewServer(q, pool, store, m, d, qi, bootstrap, false, testPairingKey(t), true, "test-readability-version", "test-ai-model")
	logger := httplog.NewLogger("recueil-test")
	logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{}, nil)
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
	qi := queueitems.NewClient(mirrorURL, "test-secret")
	store := archive.New(t.TempDir())
	bootstrap, _, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)

	s := httpapi.NewServer(q, pool, store, m, d, qi, bootstrap, false, testPairingKey(t), true, "test-readability-version", "test-ai-model")
	logger := httplog.NewLogger("recueil-test")
	logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{}, nil)
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

func TestNewRouter_DashboardSPA(t *testing.T) {
	pool := dbtest.Setup(t)

	// A real fs.FS (testing/fstest.MapFS), not a hand-rolled fake --
	// consistent with this project's general preference for exercising
	// real implementations over mocks wherever practical.
	dashboard := fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<html>shell</html>")},
		"assets/app.js":    &fstest.MapFile{Data: []byte("console.log('hi')")},
		"assets/app.js.gz": &fstest.MapFile{Data: []byte("not actually gzipped, just a distinct file")},
	}

	q := db.New(pool)
	m := mirror.NewClient(unreachable, "test-secret")
	d := devices.NewClient(unreachable, "test-secret")
	qi := queueitems.NewClient(unreachable, "test-secret")
	store := archive.New(t.TempDir())
	bootstrap, _, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)
	s := httpapi.NewServer(q, pool, store, m, d, qi, bootstrap, false, testPairingKey(t), false, "test-readability-version", "test-ai-model")
	logger := httplog.NewLogger("recueil-test")
	logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{}, dashboard)
	require.NoError(t, err)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	t.Run("serves index.html at the root", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "<html>shell</html>", string(body))
	})

	t.Run("serves a real built asset directly, not the index.html fallback", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/assets/app.js")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "console.log('hi')", string(body))
	})

	t.Run("falls back to index.html for a client-side route that isn't a real file", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/pages/5")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "svelte-spa-router's own client-side routing needs the shell, not a 404")
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, "<html>shell</html>", string(body))
	})

	t.Run("/api routes still take priority over the dashboard catch-all", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/api/auth/me")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		// Unauthenticated: a real API 401 from the auth middleware, not
		// the dashboard's index.html served as a false-positive 200.
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

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

	// newTestServer wires EnableOpenRegistration=true (see its own doc
	// comment) -- this just confirms SetupStatus actually surfaces the
	// value it was given rather than hardcoding it, not the config
	// default itself (that's TestRegisterDisabledByDefault's job).
	t.Run("open_registration reflects the server's configured value", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)

		resp, err := http.Get(server.URL + "/api/setup-status")
		require.NoError(t, err)
		var got struct {
			OpenRegistration bool `json:"open_registration"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.True(t, got.OpenRegistration)
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

// TestRegisterDisabledByDefault covers EnableOpenRegistration's real
// default (false) directly, since newTestServer itself always passes
// true so its ~40 other callers, including TestRegister above, keep
// exercising the enabled path unchanged.
func TestRegisterDisabledByDefault(t *testing.T) {
	pool := dbtest.Setup(t)
	q := db.New(pool)
	m := mirror.NewClient(unreachable, "test-secret")
	d := devices.NewClient(unreachable, "test-secret")
	qi := queueitems.NewClient(unreachable, "test-secret")
	store := archive.New(t.TempDir())
	bootstrap, _, err := auth.NewBootstrapTokenHolder()
	require.NoError(t, err)

	s := httpapi.NewServer(q, pool, store, m, d, qi, bootstrap, false, testPairingKey(t), false, "test-readability-version", "test-ai-model")
	logger := httplog.NewLogger("recueil-test")
	logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{}, nil)
	require.NoError(t, err)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	body := `{"username":"register-disabled","password":"hunter2"}`
	resp, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.False(t, hasSessionCookie(resp))
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
		dbtest.CreateSession(t, pool, &db.CreateSessionParams{
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
		dbtest.CreateSession(t, pool, &db.CreateSessionParams{
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

// requestWithCookieBody is requestWithCookie's twin for PATCH/POST calls
// that need a JSON body (PatchPage's title/excluded_from_mirror fields,
// specifically) -- avoids repeating the same NewRequest/Content-Type/
// AddCookie dance across several subtests below.
func requestWithCookieBody(t *testing.T, server *httptest.Server, method, path string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
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
func sessionCookieFor(t *testing.T, pool *pgxpool.Pool, user *db.User) *http.Cookie {
	t.Helper()
	raw, hash, err := auth.GenerateSessionToken()
	require.NoError(t, err)
	dbtest.CreateSession(t, pool, &db.CreateSessionParams{
		SessionHash: hash, UserID: user.ID, ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	return &http.Cookie{Name: sessionCookieName, Value: raw}
}

// newDeviceWorkerServer is a mock Worker implementing just enough of
// GET/POST /internal/tokens for the Manage Devices tests: an in-memory
// map of userID -> tokens, checking X-Service-Key and user_id the same
// way the real Worker handler does (see terraform/worker/index.js).
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

// newQueueItemsWorkerServer is newDeviceWorkerServer's twin for the
// dashboard's Queue screen: a mock Worker standing in for
// GET /internal/queue-items?status=failed and
// POST /internal/queue-items/:id/retry. itemsByUser is mutated in place
// (retry flips manual_retry to true) the same way newDeviceWorkerServer's
// tokensByUser is mutated by revoke.
func newQueueItemsWorkerServer(t *testing.T, itemsByUser map[int64][]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Key") != "test-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/queue-items":
			userID, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": itemsByUser[userID]})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/internal/queue-items/") && strings.HasSuffix(r.URL.Path, "/retry"):
			itemID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/queue-items/"), "/retry")
			userID, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			items := itemsByUser[userID]
			for i, item := range items {
				if item["id"] == itemID && item["status"] == "failed" {
					items[i]["manual_retry"] = float64(1)
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
		cookie := sessionCookieFor(t, pool, &member)

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
		cookie := sessionCookieFor(t, pool, &admin)

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
		cookie := sessionCookieFor(t, pool, &member)

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
		cookie := sessionCookieFor(t, pool, &member)

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
		cookie := sessionCookieFor(t, pool, &member)

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
		cookie := sessionCookieFor(t, pool, &admin)

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

// createSessionWithUA is sessionCookieFor's twin for tests that need
// either a specific User-Agent string (to exercise real parsing) or the
// created session's own id (to build /api/sessions/{id} URLs, or to
// compare against a response's is_current) -- sessionCookieFor exposes
// neither, just the cookie.
func createSessionWithUA(t *testing.T, pool *pgxpool.Pool, user *db.User, userAgent string) (*http.Cookie, db.Session) {
	t.Helper()
	raw, hash, err := auth.GenerateSessionToken()
	require.NoError(t, err)
	sess := dbtest.CreateSession(t, pool, &db.CreateSessionParams{
		SessionHash: hash, UserID: user.ID,
		UserAgent: pgtype.Text{String: userAgent, Valid: userAgent != ""},
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	return &http.Cookie{Name: sessionCookieName, Value: raw}, sess
}

func TestListSessions(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("lists all of the caller's own sessions, most recently active first", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		_, older := createSessionWithUA(t, pool, &user, "")
		// ListSessionsForUser orders by last_seen_at DESC with no
		// tiebreaker -- same caveat as other tests in this file that
		// depend on ordering (e.g. TestRecapturePage's own "more than
		// one capture" test).
		time.Sleep(2 * time.Millisecond)
		newerCookie, newer := createSessionWithUA(t, pool, &user, "")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodGet, "/api/sessions", newerCookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Sessions []struct {
				ID int64 `json:"id"`
			} `json:"sessions"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Sessions, 2)
		assert.Equal(t, newer.ID, got.Sessions[0].ID)
		assert.Equal(t, older.ID, got.Sessions[1].ID)
	})

	t.Run("marks the current session's own row is_current, and no others", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		_, other := createSessionWithUA(t, pool, &user, "")
		currentCookie, current := createSessionWithUA(t, pool, &user, "")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodGet, "/api/sessions", currentCookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Sessions []struct {
				ID        int64 `json:"id"`
				IsCurrent bool  `json:"is_current"`
			} `json:"sessions"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		byID := map[int64]bool{}
		for _, s := range got.Sessions {
			byID[s.ID] = s.IsCurrent
		}
		assert.True(t, byID[current.ID])
		assert.False(t, byID[other.ID])
	})

	t.Run("parses a real user agent into browser/os/device_class", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie, sess := createSessionWithUA(t, pool, &user,
			"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Safari/537.36")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodGet, "/api/sessions", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Sessions []struct {
				ID             int64  `json:"id"`
				Browser        string `json:"browser"`
				BrowserVersion string `json:"browser_version"`
				OS             string `json:"os"`
				DeviceClass    string `json:"device_class"`
			} `json:"sessions"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Sessions, 1)
		assert.Equal(t, sess.ID, got.Sessions[0].ID)
		assert.Equal(t, "Chrome", got.Sessions[0].Browser)
		assert.Equal(t, "118", got.Sessions[0].BrowserVersion)
		assert.Equal(t, "Windows", got.Sessions[0].OS)
		assert.Equal(t, "desktop", got.Sessions[0].DeviceClass)
	})

	t.Run("a missing/unrecognized user agent returns empty fields, not an error", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		cookie, _ := createSessionWithUA(t, pool, &user, "")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodGet, "/api/sessions", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Sessions []struct {
				Browser     string `json:"browser"`
				OS          string `json:"os"`
				DeviceClass string `json:"device_class"`
			} `json:"sessions"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Sessions, 1)
		assert.Empty(t, got.Sessions[0].Browser)
		assert.Empty(t, got.Sessions[0].OS)
		assert.Empty(t, got.Sessions[0].DeviceClass)
	})

	t.Run("another user's sessions never appear", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		_, _ = createSessionWithUA(t, pool, &other, "")
		cookie, _ := createSessionWithUA(t, pool, &user, "")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodGet, "/api/sessions", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Sessions []struct {
				ID int64 `json:"id"`
			} `json:"sessions"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Len(t, got.Sessions, 1, "only the caller's own session should be listed")
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/sessions")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestDeleteSession(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("a member can revoke one of their own other sessions", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		currentCookie, _ := createSessionWithUA(t, pool, &user, "")
		_, other := createSessionWithUA(t, pool, &user, "")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/sessions/%d", other.ID), currentCookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		remaining, err := db.New(pool).ListSessionsForUser(context.Background(), user.ID)
		require.NoError(t, err)
		require.Len(t, remaining, 1)
		assert.NotEqual(t, other.ID, remaining[0].ID)
	})

	t.Run("refuses to delete the caller's own current session", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		currentCookie, current := createSessionWithUA(t, pool, &user, "")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/sessions/%d", current.ID), currentCookie)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		remaining, err := db.New(pool).ListSessionsForUser(context.Background(), user.ID)
		require.NoError(t, err)
		require.Len(t, remaining, 1, "the current session must still be there")
	})

	t.Run("a member cannot revoke another user's session even by guessing the id", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		currentCookie, _ := createSessionWithUA(t, pool, &user, "")
		_, otherSession := createSessionWithUA(t, pool, &other, "")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/sessions/%d", otherSession.ID), currentCookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)

		remaining, err := db.New(pool).ListSessionsForUser(context.Background(), other.ID)
		require.NoError(t, err)
		assert.Len(t, remaining, 1, "the other user's session should be untouched")
	})

	t.Run("revoking a nonexistent session id returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		currentCookie, _ := createSessionWithUA(t, pool, &user, "")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodDelete, "/api/sessions/9999999", currentCookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a non-numeric id returns 400", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		currentCookie, _ := createSessionWithUA(t, pool, &user, "")

		server, _ := newTestServer(t, pool, unreachable)
		resp := requestWithCookie(t, server, http.MethodDelete, "/api/sessions/not-a-number", currentCookie)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		req, err := http.NewRequest(http.MethodDelete, server.URL+"/api/sessions/1", http.NoBody)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestListQueueItems(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("lists pending/claimed/failed items unconditionally, passing claimed_at through", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		workerServer := newQueueItemsWorkerServer(t, map[int64][]map[string]any{
			member.ID: {
				{"id": "item-pending", "url": "https://example.com/p", "status": "pending", "manual_retry": float64(0), "claimed_at": nil, "created_at": "2026-06-01 12:00:00"},
				{"id": "item-claimed", "url": "https://example.com/c", "status": "claimed", "manual_retry": float64(0), "claimed_at": "2026-06-01 12:05:00", "created_at": "2026-06-01 12:00:01"},
				{"id": "item-failed", "url": "https://example.com/f", "status": "failed", "manual_retry": float64(0), "claimed_at": nil, "created_at": "2026-06-01 12:00:02"},
			},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/queue-items", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Items []struct {
				ID        string  `json:"id"`
				Status    string  `json:"status"`
				ClaimedAt *string `json:"claimed_at"`
			} `json:"items"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Items, 3)

		byID := map[string]string{}
		for _, item := range got.Items {
			byID[item.ID] = item.Status
		}
		assert.Equal(t, "pending", byID["item-pending"])
		assert.Equal(t, "claimed", byID["item-claimed"])
		assert.Equal(t, "failed", byID["item-failed"])

		for _, item := range got.Items {
			if item.ID == "item-pending" || item.ID == "item-failed" {
				assert.Nil(t, item.ClaimedAt, "never-claimed items should have a null claimed_at")
			}
			if item.ID == "item-claimed" {
				require.NotNil(t, item.ClaimedAt)
				assert.Contains(t, *item.ClaimedAt, "2026-06-01T12:05:00")
			}
		}
	})

	// The Worker's own recency-window filtering for 'captured' items
	// (terraform/worker/index.js's own handleListQueueItems) is tested
	// there directly, against a real D1 -- this mock Worker just returns
	// whatever it's told, so there's nothing more to verify about that
	// filtering from this side. This confirms the passthrough itself
	// doesn't drop/misrender a 'captured' item the Worker does decide to
	// include.
	t.Run("passes a recently-captured item through unchanged", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		workerServer := newQueueItemsWorkerServer(t, map[int64][]map[string]any{
			member.ID: {{"id": "item-captured", "url": "https://example.com/cap", "status": "captured", "manual_retry": float64(0), "claimed_at": "2026-06-01 12:05:00", "created_at": "2026-06-01 12:00:00"}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/queue-items", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Items []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"items"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Items, 1)
		assert.Equal(t, "captured", got.Items[0].Status)
	})

	t.Run("?user_id= is ignored -- always self-scoped, even for an admin", func(t *testing.T) {
		admin := dbtest.CreateUser(t, pool, "admin")
		other := dbtest.CreateUser(t, pool, "member")
		workerServer := newQueueItemsWorkerServer(t, map[int64][]map[string]any{
			admin.ID: {{"id": "admin-item", "url": "https://example.com/admin", "status": "failed", "manual_retry": float64(0), "created_at": "2026-06-01 12:00:00"}},
			other.ID: {{"id": "other-item", "url": "https://example.com/other", "status": "failed", "manual_retry": float64(0), "created_at": "2026-06-01 12:00:00"}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &admin)

		resp := requestWithCookie(t, server, http.MethodGet,
			fmt.Sprintf("/api/queue-items?user_id=%d", other.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.Items, 1)
		assert.Equal(t, "admin-item", got.Items[0].ID)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/queue-items")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestRetryQueueItem(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("a member can retry their own failed item", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		workerServer := newQueueItemsWorkerServer(t, map[int64][]map[string]any{
			member.ID: {{"id": "retry-me", "url": "https://example.com/r", "status": "failed", "manual_retry": float64(0), "created_at": "2026-06-01 12:00:00"}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodPost, "/api/queue-items/retry-me/retry", cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("a member cannot retry another user's item even by guessing the id", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		workerServer := newQueueItemsWorkerServer(t, map[int64][]map[string]any{
			other.ID: {{"id": "not-yours", "url": "https://example.com/n", "status": "failed", "manual_retry": float64(0), "created_at": "2026-06-01 12:00:00"}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &member)

		// No ?user_id= at all -- the member's own id is used, which
		// doesn't own this item, so the Worker's own cross-check is
		// what actually blocks this.
		resp := requestWithCookie(t, server, http.MethodPost, "/api/queue-items/not-yours/retry", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("retrying a nonexistent item returns 404", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		workerServer := newQueueItemsWorkerServer(t, map[int64][]map[string]any{})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodPost, "/api/queue-items/does-not-exist/retry", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("?user_id= is ignored -- an admin cannot retry another user's item via it", func(t *testing.T) {
		admin := dbtest.CreateUser(t, pool, "admin")
		other := dbtest.CreateUser(t, pool, "member")
		workerServer := newQueueItemsWorkerServer(t, map[int64][]map[string]any{
			other.ID: {{"id": "cross-user", "url": "https://example.com/c", "status": "failed", "manual_retry": float64(0), "created_at": "2026-06-01 12:00:00"}},
		})
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &admin)

		resp := requestWithCookie(t, server, http.MethodPost,
			fmt.Sprintf("/api/queue-items/cross-user/retry?user_id=%d", other.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/queue-items/x/retry", http.NoBody)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

// seedFailedJob inserts a 'failed' row directly into one of the three job
// tables (screenshot_jobs/readability_jobs/ai_jobs -- all three share the
// same shape, see queries/*_jobs.sql), returning its id. table is always a
// fixed string literal at call sites, never request-derived, so building
// the query with fmt.Sprintf here carries no injection risk.
func seedFailedJob(t *testing.T, pool *pgxpool.Pool, table string, captureID int64, attempts int32, errMsg string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		fmt.Sprintf(`INSERT INTO %s (capture_id, status, attempts, error, completed_at)
		             VALUES ($1, 'failed', $2, $3, NOW()) RETURNING id`, table),
		captureID, attempts, errMsg,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

// seedJobWithStatus is seedFailedJob's twin for tests exercising ListJobs'
// recency-window filtering -- seedFailedJob always hardcodes
// status='failed' and completed_at=NOW(), neither of which fits a
// pending/processing job (no completed_at at all) or a 'done' job at a
// specific, test-controlled distance from now.
func seedJobWithStatus(t *testing.T, pool *pgxpool.Pool, table string, captureID int64, status string, completedAt *time.Time) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		fmt.Sprintf(`INSERT INTO %s (capture_id, status, completed_at) VALUES ($1, $2, $3) RETURNING id`, table),
		captureID, status, completedAt,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

func TestListJobs(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("lists the calling user's own failed jobs, grouped by kind", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, member.ID, "https://example.com/page")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		seedFailedJob(t, pool, "screenshot_jobs", capture.ID, 3, "render timeout")
		seedFailedJob(t, pool, "readability_jobs", capture.ID, 3, "extraction failed")
		seedFailedJob(t, pool, "ai_jobs", capture.ID, 3, "model unavailable")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/jobs", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ScreenshotJobs  []struct{ URL string } `json:"screenshot_jobs"`
			ReadabilityJobs []struct{ URL string } `json:"readability_jobs"`
			AIJobs          []struct{ URL string } `json:"ai_jobs"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.ScreenshotJobs, 1)
		require.Len(t, got.ReadabilityJobs, 1)
		require.Len(t, got.AIJobs, 1)
		assert.Equal(t, capture.RawUrl, got.ScreenshotJobs[0].URL)
		assert.Equal(t, capture.RawUrl, got.ReadabilityJobs[0].URL)
		assert.Equal(t, capture.RawUrl, got.AIJobs[0].URL)
	})

	t.Run("excludes another user's failed jobs", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		other := dbtest.CreateUser(t, pool, "member")
		otherPage := dbtest.CreatePage(t, pool, other.ID, "https://example.com/other")
		otherCapture := dbtest.CreateCapture(t, pool, otherPage.ID)
		seedFailedJob(t, pool, "screenshot_jobs", otherCapture.ID, 3, "render timeout")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/jobs", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ScreenshotJobs []struct{ URL string } `json:"screenshot_jobs"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Empty(t, got.ScreenshotJobs)
	})

	t.Run("includes pending and processing jobs unconditionally", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, member.ID, "https://example.com/pending-processing")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		seedJobWithStatus(t, pool, "screenshot_jobs", capture.ID, "pending", nil)
		seedJobWithStatus(t, pool, "readability_jobs", capture.ID, "processing", nil)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/jobs", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ScreenshotJobs  []struct{ Status string } `json:"screenshot_jobs"`
			ReadabilityJobs []struct{ Status string } `json:"readability_jobs"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.ScreenshotJobs, 1)
		assert.Equal(t, "pending", got.ScreenshotJobs[0].Status)
		require.Len(t, got.ReadabilityJobs, 1)
		assert.Equal(t, "processing", got.ReadabilityJobs[0].Status)
	})

	t.Run("includes a done job completed within the recency window", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, member.ID, "https://example.com/recently-done")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		recent := time.Now().Add(-5 * time.Minute)
		seedJobWithStatus(t, pool, "screenshot_jobs", capture.ID, "done", &recent)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/jobs", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ScreenshotJobs []struct{ Status string } `json:"screenshot_jobs"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Len(t, got.ScreenshotJobs, 1)
		assert.Equal(t, "done", got.ScreenshotJobs[0].Status)
	})

	t.Run("excludes a done job completed outside the recency window", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, member.ID, "https://example.com/stale-done")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		stale := time.Now().Add(-20 * time.Minute)
		seedJobWithStatus(t, pool, "screenshot_jobs", capture.ID, "done", &stale)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/jobs", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ScreenshotJobs []struct{ Status string } `json:"screenshot_jobs"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Empty(t, got.ScreenshotJobs)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/jobs")
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestRetryJob(t *testing.T) {
	pool := dbtest.Setup(t)

	for _, kind := range []string{"screenshot", "readability", "ai"} {
		table := kind + "_jobs"
		t.Run(kind+": a member can retry their own failed job", func(t *testing.T) {
			member := dbtest.CreateUser(t, pool, "member")
			page := dbtest.CreatePage(t, pool, member.ID, "https://example.com/"+kind)
			capture := dbtest.CreateCapture(t, pool, page.ID)
			jobID := seedFailedJob(t, pool, table, capture.ID, 3, "boom")

			server, _ := newTestServer(t, pool, unreachable)
			cookie := sessionCookieFor(t, pool, &member)

			resp := requestWithCookie(t, server, http.MethodPost,
				fmt.Sprintf("/api/jobs/%s/%d/retry", kind, jobID), cookie)
			assert.Equal(t, http.StatusNoContent, resp.StatusCode)

			var status string
			var attempts int32
			err := pool.QueryRow(context.Background(),
				fmt.Sprintf("SELECT status, attempts FROM %s WHERE id = $1", table), jobID,
			).Scan(&status, &attempts)
			require.NoError(t, err)
			assert.Equal(t, "pending", status)
			// attempts is deliberately left untouched by a manual retry --
			// see ManualRetry*JobForUser's own doc comment.
			assert.Equal(t, int32(3), attempts)
		})

		t.Run(kind+": a member cannot retry another user's job even by guessing the id", func(t *testing.T) {
			member := dbtest.CreateUser(t, pool, "member")
			other := dbtest.CreateUser(t, pool, "member")
			otherPage := dbtest.CreatePage(t, pool, other.ID, "https://example.com/other-"+kind)
			otherCapture := dbtest.CreateCapture(t, pool, otherPage.ID)
			jobID := seedFailedJob(t, pool, table, otherCapture.ID, 3, "boom")

			server, _ := newTestServer(t, pool, unreachable)
			cookie := sessionCookieFor(t, pool, &member)

			resp := requestWithCookie(t, server, http.MethodPost,
				fmt.Sprintf("/api/jobs/%s/%d/retry", kind, jobID), cookie)
			assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		})

		t.Run(kind+": retrying a job that isn't failed returns 404", func(t *testing.T) {
			member := dbtest.CreateUser(t, pool, "member")
			page := dbtest.CreatePage(t, pool, member.ID, "https://example.com/notfailed-"+kind)
			capture := dbtest.CreateCapture(t, pool, page.ID)
			var jobID int64
			err := pool.QueryRow(context.Background(),
				fmt.Sprintf("INSERT INTO %s (capture_id, status) VALUES ($1, 'pending') RETURNING id", table),
				capture.ID,
			).Scan(&jobID)
			require.NoError(t, err)

			server, _ := newTestServer(t, pool, unreachable)
			cookie := sessionCookieFor(t, pool, &member)

			resp := requestWithCookie(t, server, http.MethodPost,
				fmt.Sprintf("/api/jobs/%s/%d/retry", kind, jobID), cookie)
			assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		})
	}

	t.Run("retrying a nonexistent job returns 404", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodPost, "/api/jobs/screenshot/999999/retry", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("an unrecognized kind returns 400", func(t *testing.T) {
		member := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &member)

		resp := requestWithCookie(t, server, http.MethodPost, "/api/jobs/bogus/1/retry", cookie)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/jobs/screenshot/1/retry", http.NoBody)
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
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a nonexistent id returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/pages/9999999", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a non-numeric id returns 400", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/pages/not-a-number", cookie)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestGetSettings(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("no row yet returns language: null, not a 404/500", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/settings", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Language *string `json:"language"`
			Theme    *string `json:"theme"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Nil(t, got.Language)
		assert.Nil(t, got.Theme)
	})

	t.Run("returns a previously set language", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+"/api/settings",
			strings.NewReader(`{"language":"fr"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		_, err = http.DefaultClient.Do(req)
		require.NoError(t, err)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/settings", cookie)
		var got struct {
			Language *string `json:"language"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.NotNil(t, got.Language)
		assert.Equal(t, "fr", *got.Language)
	})

	t.Run("returns a previously set theme", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, "/api/settings", cookie, `{"theme":"dark"}`)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		resp = requestWithCookie(t, server, http.MethodGet, "/api/settings", cookie)
		var got struct {
			Theme *string `json:"theme"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.NotNil(t, got.Theme)
		assert.Equal(t, "dark", *got.Theme)
	})
}

func TestPatchSettings(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("sets the language on first patch (no prior row)", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+"/api/settings",
			strings.NewReader(`{"language":"en"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Language *string `json:"language"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.NotNil(t, got.Language)
		assert.Equal(t, "en", *got.Language)
	})

	t.Run("empty string clears back to null", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		setReq, err := http.NewRequest(http.MethodPatch, server.URL+"/api/settings",
			strings.NewReader(`{"language":"fr"}`))
		require.NoError(t, err)
		setReq.Header.Set("Content-Type", "application/json")
		setReq.AddCookie(cookie)
		_, err = http.DefaultClient.Do(setReq)
		require.NoError(t, err)

		clearReq, err := http.NewRequest(http.MethodPatch, server.URL+"/api/settings",
			strings.NewReader(`{"language":""}`))
		require.NoError(t, err)
		clearReq.Header.Set("Content-Type", "application/json")
		clearReq.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(clearReq)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Language *string `json:"language"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Nil(t, got.Language)
	})

	t.Run("a malformed language tag is rejected", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+"/api/settings",
			strings.NewReader(`{"language":"not a language tag"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("one user's settings never affect another's", func(t *testing.T) {
		userA := dbtest.CreateUser(t, pool, "member")
		userB := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookieA := sessionCookieFor(t, pool, &userA)
		cookieB := sessionCookieFor(t, pool, &userB)

		req, err := http.NewRequest(http.MethodPatch, server.URL+"/api/settings",
			strings.NewReader(`{"language":"fr"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookieA)
		_, err = http.DefaultClient.Do(req)
		require.NoError(t, err)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/settings", cookieB)
		var got struct {
			Language *string `json:"language"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Nil(t, got.Language)
	})

	t.Run("sets the theme on first patch (no prior row)", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, "/api/settings", cookie, `{"theme":"light"}`)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Theme *string `json:"theme"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.NotNil(t, got.Theme)
		assert.Equal(t, "light", *got.Theme)
	})

	t.Run("an empty theme clears back to null (automatic)", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		setResp := requestWithCookieBody(t, server, http.MethodPatch, "/api/settings", cookie, `{"theme":"dark"}`)
		require.Equal(t, http.StatusOK, setResp.StatusCode)

		clearResp := requestWithCookieBody(t, server, http.MethodPatch, "/api/settings", cookie, `{"theme":""}`)
		assert.Equal(t, http.StatusOK, clearResp.StatusCode)

		var got struct {
			Theme *string `json:"theme"`
		}
		require.NoError(t, json.NewDecoder(clearResp.Body).Decode(&got))
		assert.Nil(t, got.Theme)
	})

	t.Run("a theme value other than light/dark/empty is rejected", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, "/api/settings", cookie, `{"theme":"solarized"}`)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("language and theme can be set together in one request", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, "/api/settings", cookie,
			`{"language":"fr","theme":"dark"}`)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Language *string `json:"language"`
			Theme    *string `json:"theme"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.NotNil(t, got.Language)
		assert.Equal(t, "fr", *got.Language)
		require.NotNil(t, got.Theme)
		assert.Equal(t, "dark", *got.Theme)
	})

	t.Run("patching only theme doesn't clear an already-set language, and vice versa is NOT true -- both are always full-replace", func(t *testing.T) {
		// This test's name is the point: patchSettingsRequest's two
		// fields are NOT independent pointers -- sending {"theme":
		// "dark"} without "language" clears language back to null too.
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		setBoth := requestWithCookieBody(t, server, http.MethodPatch, "/api/settings", cookie,
			`{"language":"fr","theme":"light"}`)
		require.Equal(t, http.StatusOK, setBoth.StatusCode)

		themeOnly := requestWithCookieBody(t, server, http.MethodPatch, "/api/settings", cookie, `{"theme":"dark"}`)
		require.Equal(t, http.StatusOK, themeOnly.StatusCode)

		var got struct {
			Language *string `json:"language"`
			Theme    *string `json:"theme"`
		}
		require.NoError(t, json.NewDecoder(themeOnly.Body).Decode(&got))
		assert.Nil(t, got.Language, "omitting language in this second request cleared it, by design")
		require.NotNil(t, got.Theme)
		assert.Equal(t, "dark", *got.Theme)
	})
}

func TestPatchPage(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("toggles excluded_from_mirror for the owner", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/toggle")
		require.False(t, page.ExcludedFromMirror)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &requester)

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
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/pages/%d", page.ID),
			strings.NewReader(`{}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("overwrites title for the owner", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/retitle")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"title":"My New Title"}`)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Title string `json:"title"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, "My New Title", got.Title)
	})

	t.Run("trims whitespace from a title before saving", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/retitle-trim")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"title":"  Padded Title  "}`)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Title string `json:"title"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, "Padded Title", got.Title)
	})

	t.Run("a blank/whitespace-only title returns 400 and doesn't change the page", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/retitle-blank")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"title":"   "}`)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

		unchanged, err := db.New(pool).GetPageByID(context.Background(), page.ID)
		require.NoError(t, err)
		assert.Equal(t, page.Title, unchanged.Title)
	})

	t.Run("another user's page returns 404 for a title update too", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/retitle-not-yours")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"title":"Hijacked"}`)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("sets notes for the owner", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/notes")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"notes":"**Worth** revisiting"}`)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Notes *string `json:"notes"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.NotNil(t, got.Notes)
		assert.Equal(t, "**Worth** revisiting", *got.Notes)
	})

	t.Run("trims whitespace from notes before saving", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/notes-trim")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"notes":"  padded note  "}`)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Notes *string `json:"notes"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.NotNil(t, got.Notes)
		assert.Equal(t, "padded note", *got.Notes)
	})

	t.Run("an empty/whitespace-only notes value clears it to null, not an error", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/notes-clear")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		setResp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"notes":"a note to clear"}`)
		assert.Equal(t, http.StatusOK, setResp.StatusCode)

		clearResp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"notes":"   "}`)
		assert.Equal(t, http.StatusOK, clearResp.StatusCode)

		var got struct {
			Notes *string `json:"notes"`
		}
		require.NoError(t, json.NewDecoder(clearResp.Body).Decode(&got))
		assert.Nil(t, got.Notes)

		persisted, err := db.New(pool).GetPageByID(context.Background(), page.ID)
		require.NoError(t, err)
		assert.False(t, persisted.Notes.Valid)
	})

	t.Run("another user's page returns 404 for a notes update too", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/notes-not-yours")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"notes":"Hijacked"}`)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("both fields in one request applies both -- RETURNING * on the second UPDATE reflects the first's write too", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/retitle-both")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookieBody(t, server, http.MethodPatch, fmt.Sprintf("/api/pages/%d", page.ID),
			cookie, `{"excluded_from_mirror":true,"title":"Both At Once"}`)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Title              string `json:"title"`
			ExcludedFromMirror bool   `json:"excluded_from_mirror"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, "Both At Once", got.Title)
		// Both updates run as two sequential UPDATEs against the same row
		// (not two independent copies), so by the time the second one's
		// own RETURNING * fires, it already reflects the first's write --
		// the response ends up complete either way, not just whichever
		// field that specific query happened to touch.
		assert.True(t, got.ExcludedFromMirror)

		persisted, err := db.New(pool).GetPageByID(context.Background(), page.ID)
		require.NoError(t, err)
		assert.True(t, persisted.ExcludedFromMirror)
		assert.Equal(t, "Both At Once", persisted.Title.String)
	})
}

func TestDeletePage(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("deletes the page and cascades to its captures/tags/collections", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/to-delete")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		q := db.New(pool)
		ctx := context.Background()
		tag, err := q.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: "reading", Slug: "reading"})
		require.NoError(t, err)
		require.NoError(t, q.AddPageTag(ctx, db.AddPageTagParams{PageID: page.ID, TagID: tag.ID, Source: "manual"}))
		collection, err := q.CreateCollection(ctx, db.CreateCollectionParams{UserID: user.ID, Name: "Articles", Slug: "articles"})
		require.NoError(t, err)
		require.NoError(t, q.AddPageToCollection(ctx, db.AddPageToCollectionParams{PageID: page.ID, CollectionID: collection.ID}))

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		_, err = q.GetPageByID(ctx, page.ID)
		assert.Error(t, err, "page row should be gone")
		_, err = q.GetCaptureByID(ctx, capture.ID)
		assert.Error(t, err, "capture should have cascaded away with the page")
		tags, err := q.ListPageTags(ctx, page.ID)
		require.NoError(t, err)
		assert.Empty(t, tags, "page_tags row should have cascaded away")
		collections, err := q.ListPageCollections(ctx, page.ID)
		require.NoError(t, err)
		assert.Empty(t, collections, "page_collections row should have cascaded away")
	})

	t.Run("another user's page returns 404, not a silent no-op success", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/not-yours-delete")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/pages/%d", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)

		// Confirm it's genuinely still there, not a false-positive 404
		// from some unrelated bug.
		_, err := db.New(pool).GetPageByID(context.Background(), page.ID)
		assert.NoError(t, err)
	})

	t.Run("a nonexistent page id returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, "/api/pages/9999999", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/no-session-delete")
		server, _ := newTestServer(t, pool, unreachable)

		req, err := http.NewRequest(http.MethodDelete, server.URL+fmt.Sprintf("/api/pages/%d", page.ID), http.NoBody)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

// newEnqueueWorkerServer is newQueueItemsWorkerServer's twin for the
// recapture action's own Worker call: a mock implementing just
// POST /internal/queue-items (see terraform/worker/index.js's
// handleServiceEnqueue). Every accepted call is appended to *enqueued in
// call order, so a test can assert on exactly what was posted without the
// mock needing any real D1-backed queue_items state of its own.
type enqueuedItem struct {
	ID     string `json:"id"`
	UserID int64  `json:"user_id"`
	URL    string `json:"url"`
}

func newEnqueueWorkerServer(t *testing.T, enqueued *[]enqueuedItem) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Key") != "test-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/internal/queue-items" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		var item enqueuedItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		*enqueued = append(*enqueued, item)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRecapturePage(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("enqueues the latest capture's raw_url, not pages.normalized_url", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/recapture-me")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		var enqueued []enqueuedItem
		workerServer := newEnqueueWorkerServer(t, &enqueued)
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/pages/%d/recapture", page.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		require.Len(t, enqueued, 1)
		assert.Equal(t, user.ID, enqueued[0].UserID)
		assert.Equal(t, capture.RawUrl, enqueued[0].URL)
		assert.NotEqual(t, page.NormalizedUrl, enqueued[0].URL)
		assert.NotEmpty(t, enqueued[0].ID)
	})

	t.Run("re-enqueues the most recent capture when there's more than one", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/recapture-versioned")
		dbtest.CreateCapture(t, pool, page.ID)
		// GetLatestCaptureByPage orders by captured_at DESC with no
		// tiebreaker column -- a small sleep guarantees the second
		// capture's timestamp is strictly later, not relying on however
		// much real wall-clock time two back-to-back calls happen to
		// take on their own.
		time.Sleep(2 * time.Millisecond)
		latest := dbtest.CreateCapture(t, pool, page.ID)

		var enqueued []enqueuedItem
		workerServer := newEnqueueWorkerServer(t, &enqueued)
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/pages/%d/recapture", page.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		require.Len(t, enqueued, 1)
		assert.Equal(t, latest.RawUrl, enqueued[0].URL)
	})

	t.Run("a page with no captures at all returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/no-captures-yet")

		var enqueued []enqueuedItem
		workerServer := newEnqueueWorkerServer(t, &enqueued)
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/pages/%d/recapture", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Empty(t, enqueued)
	})

	t.Run("another user's page returns 404, not a silent no-op success", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/not-yours-recapture")
		dbtest.CreateCapture(t, pool, page.ID)

		var enqueued []enqueuedItem
		workerServer := newEnqueueWorkerServer(t, &enqueued)
		server, _ := newTestServer(t, pool, workerServer.URL)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/pages/%d/recapture", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Empty(t, enqueued)
	})

	t.Run("a Worker enqueue failure surfaces as a 500", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/recapture-worker-down")
		dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/pages/%d/recapture", page.ID), cookie)
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Post(server.URL+"/api/pages/1/recapture", "application/json", http.NoBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
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
		cookie := sessionCookieFor(t, pool, &user)

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

	// dbtest.CreateCapture leaves readability_version/thumbnail_*/
	// favicon_* all NULL (no screenshot/readability job has actually run
	// against this test row), and content_hash is always set (it's
	// captured at ingest time, not by a later job) -- these two subtests
	// cover both the "field genuinely wasn't tracked before" case (now
	// null, not silently missing from the JSON) and the "field has a
	// real value" case together, rather than repeating the whole
	// request/decode dance four times over for each individually.
	t.Run("previously-omitted nullable fields are null when unset", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/detail-nulls")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/captures/%d", capture.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ReadabilityVersion *string `json:"readability_version"`
			ContentHash        string  `json:"content_hash"`
			ThumbnailSizeBytes *int32  `json:"thumbnail_size_bytes"`
			ThumbnailHash      *string `json:"thumbnail_hash"`
			FaviconSizeBytes   *int32  `json:"favicon_size_bytes"`
			FaviconHash        *string `json:"favicon_hash"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Nil(t, got.ReadabilityVersion)
		assert.Equal(t, capture.ContentHash, got.ContentHash)
		assert.NotEmpty(t, got.ContentHash)
		assert.Nil(t, got.ThumbnailSizeBytes)
		assert.Nil(t, got.ThumbnailHash)
		assert.Nil(t, got.FaviconSizeBytes)
		assert.Nil(t, got.FaviconHash)
	})

	t.Run("previously-omitted nullable fields surface their real values once set", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/detail-values")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		q := db.New(pool)
		ctx := context.Background()
		require.NoError(t, q.SetCaptureReadability(ctx, db.SetCaptureReadabilityParams{
			ID: capture.ID, ReaderText: pgtype.Text{String: "text", Valid: true},
			ReaderTextHash:     pgtype.Text{String: "text-hash", Valid: true},
			ReadabilityVersion: pgtype.Text{String: "1.2.3", Valid: true},
		}))
		_, err := pool.Exec(ctx, `UPDATE captures SET
			thumbnail_size_bytes = 12345, thumbnail_hash = $1,
			favicon_size_bytes = 678, favicon_hash = $2
			WHERE id = $3`, "thumb-hash", "favicon-hash", capture.ID)
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/captures/%d", capture.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ReadabilityVersion *string `json:"readability_version"`
			ThumbnailSizeBytes *int32  `json:"thumbnail_size_bytes"`
			ThumbnailHash      *string `json:"thumbnail_hash"`
			FaviconSizeBytes   *int32  `json:"favicon_size_bytes"`
			FaviconHash        *string `json:"favicon_hash"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.NotNil(t, got.ReadabilityVersion)
		assert.Equal(t, "1.2.3", *got.ReadabilityVersion)
		require.NotNil(t, got.ThumbnailSizeBytes)
		assert.EqualValues(t, 12345, *got.ThumbnailSizeBytes)
		require.NotNil(t, got.ThumbnailHash)
		assert.Equal(t, "thumb-hash", *got.ThumbnailHash)
		require.NotNil(t, got.FaviconSizeBytes)
		assert.EqualValues(t, 678, *got.FaviconSizeBytes)
		require.NotNil(t, got.FaviconHash)
		assert.Equal(t, "favicon-hash", *got.FaviconHash)
	})

	t.Run("another user's capture returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/not-yours-capture")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/captures/%d", capture.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a non-numeric id returns 400", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/captures/%d/html", capture.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestDeleteCapture(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("deletes one of several captures, leaving the page and the rest alone", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/delete-one-of-many")
		keep := dbtest.CreateCapture(t, pool, page.ID)
		toDelete := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/captures/%d", toDelete.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		q := db.New(pool)
		ctx := context.Background()
		_, err := q.GetCaptureByID(ctx, toDelete.ID)
		assert.Error(t, err, "deleted capture should be gone")
		_, err = q.GetCaptureByID(ctx, keep.ID)
		assert.NoError(t, err, "the other capture should be untouched")
		_, err = q.GetPageByID(ctx, page.ID)
		assert.NoError(t, err, "page should still exist -- it still has one capture left")
	})

	t.Run("deleting a page's last capture deletes the page too", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/delete-last-capture")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/captures/%d", capture.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		q := db.New(pool)
		ctx := context.Background()
		_, err := q.GetCaptureByID(ctx, capture.ID)
		assert.Error(t, err, "capture should be gone")
		_, err = q.GetPageByID(ctx, page.ID)
		assert.Error(t, err, "page with zero captures left should be gone too")
	})

	t.Run("deleting the page's current favicon-source capture refreshes pages.favicon_path from the new latest capture", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/delete-refreshes-favicon")
		older := dbtest.CreateCapture(t, pool, page.ID)
		// GetLatestCaptureByPage orders by captured_at DESC with no
		// tiebreaker column (same caveat as TestRecapturePage's own
		// "more than one capture" test) -- the sleep guarantees newer
		// really is later.
		time.Sleep(2 * time.Millisecond)
		newer := dbtest.CreateCapture(t, pool, page.ID)

		q := db.New(pool)
		ctx := context.Background()
		_, err := pool.Exec(ctx, `UPDATE captures SET favicon_path = $1 WHERE id = $2`, "archive/older-favicon.png", older.ID)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `UPDATE captures SET favicon_path = $1 WHERE id = $2`, "archive/newer-favicon.png", newer.ID)
		require.NoError(t, err)
		// Simulates UpsertPage's own denormalization at ingest time --
		// the page's favicon currently reflects the newer (about to be
		// deleted) capture's own favicon, not the older one's.
		_, err = pool.Exec(ctx, `UPDATE pages SET favicon_path = $1 WHERE id = $2`, "archive/newer-favicon.png", page.ID)
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/captures/%d", newer.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		refreshedPage, err := q.GetPageByID(ctx, page.ID)
		require.NoError(t, err)
		require.True(t, refreshedPage.FaviconPath.Valid)
		assert.Equal(t, "archive/older-favicon.png", refreshedPage.FaviconPath.String,
			"page's favicon should now come from the sole remaining capture, not the deleted one")
	})

	t.Run("deleting an older, non-favicon-source capture leaves the page's favicon untouched", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/delete-doesnt-disturb-favicon")
		older := dbtest.CreateCapture(t, pool, page.ID)
		time.Sleep(2 * time.Millisecond)
		newer := dbtest.CreateCapture(t, pool, page.ID)

		q := db.New(pool)
		ctx := context.Background()
		_, err := pool.Exec(ctx, `UPDATE captures SET favicon_path = $1 WHERE id = $2`, "archive/newer-favicon.png", newer.ID)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `UPDATE pages SET favicon_path = $1 WHERE id = $2`, "archive/newer-favicon.png", page.ID)
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/captures/%d", older.ID), cookie)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)

		refreshedPage, err := q.GetPageByID(ctx, page.ID)
		require.NoError(t, err)
		require.True(t, refreshedPage.FaviconPath.Valid)
		assert.Equal(t, "archive/newer-favicon.png", refreshedPage.FaviconPath.String,
			"still recomputed from GetLatestCaptureByPage, but that's unaffected by deleting the older capture, so it's a no-op write to the same value")
	})

	t.Run("another user's capture returns 404, and nothing is deleted", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/delete-not-yours")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/captures/%d", capture.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)

		_, err := db.New(pool).GetCaptureByID(context.Background(), capture.ID)
		assert.NoError(t, err, "capture should be untouched")
	})

	t.Run("a nonexistent capture id returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, "/api/captures/9999999", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a non-numeric id returns 400", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, "/api/captures/not-a-number", cookie)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		req, err := http.NewRequest(http.MethodDelete, server.URL+"/api/captures/1", http.NoBody)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestRegenerateAISummary(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("resets an ai_jobs row to pending regardless of its current status", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/regen-summary")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		q := db.New(pool)
		ctx := context.Background()
		require.NoError(t, q.CreateAIJob(ctx, capture.ID))
		job, err := q.GetAIJobByCaptureID(ctx, capture.ID)
		require.NoError(t, err)
		// Simulate a job that already ran, failed a couple of times, and
		// permanently failed -- exactly the state a "regenerate" click
		// should be able to reset from, unlike ManualRetryAIJobForUser
		// which only works from 'failed' too, but this endpoint isn't
		// that one.
		require.NoError(t, q.FailAIJob(ctx, db.FailAIJobParams{ID: job.ID, Attempts: 3, Error: pgtype.Text{String: "boom", Valid: true}}))

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/captures/%d/regenerate-summary", capture.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			JobID int64 `json:"job_id"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, job.ID, got.JobID)

		reset, err := q.GetAIJobByCaptureID(ctx, capture.ID)
		require.NoError(t, err)
		assert.Equal(t, "pending", reset.Status)
		assert.Equal(t, int32(0), reset.Attempts)
		assert.False(t, reset.Error.Valid)
		assert.False(t, reset.CompletedAt.Valid)
		assert.False(t, reset.ClaimedAt.Valid)
	})

	t.Run("a capture whose readability never succeeded (no ai_jobs row yet) returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/regen-summary-no-job")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/captures/%d/regenerate-summary", capture.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("another user's capture returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/regen-summary-not-yours")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		require.NoError(t, db.New(pool).CreateAIJob(context.Background(), capture.ID))

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/captures/%d/regenerate-summary", capture.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Post(server.URL+"/api/captures/1/regenerate-summary", "application/json", http.NoBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestRegenerateReadability(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("resets a readability_jobs row to pending regardless of its current status", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/regen-readability")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		q := db.New(pool)
		ctx := context.Background()
		// dbtest.CreateCapture doesn't create a readability_jobs row
		// itself (unlike real ingestion) -- create one directly, then
		// mark it done, to prove regenerate works from a *successful*
		// prior run too, not just from 'failed' the way
		// ManualRetryReadabilityJobForUser is restricted to.
		require.NoError(t, q.CreateReadabilityJob(ctx, capture.ID))
		job, err := q.GetReadabilityJobByCaptureID(ctx, capture.ID)
		require.NoError(t, err)
		require.NoError(t, q.MarkReadabilityJobDone(ctx, job.ID))

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/captures/%d/regenerate-readability", capture.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			JobID int64 `json:"job_id"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, job.ID, got.JobID)

		reset, err := q.GetReadabilityJobByCaptureID(ctx, capture.ID)
		require.NoError(t, err)
		assert.Equal(t, "pending", reset.Status)
		assert.False(t, reset.CompletedAt.Valid)
	})

	t.Run("doesn't touch this capture's ai_jobs row", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/regen-readability-no-ai-requeue")
		capture := dbtest.CreateCapture(t, pool, page.ID)

		q := db.New(pool)
		ctx := context.Background()
		require.NoError(t, q.CreateReadabilityJob(ctx, capture.ID))
		require.NoError(t, q.CreateAIJob(ctx, capture.ID))
		aiJob, err := q.GetAIJobByCaptureID(ctx, capture.ID)
		require.NoError(t, err)
		require.NoError(t, q.MarkAIJobDone(ctx, aiJob.ID))

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/captures/%d/regenerate-readability", capture.ID), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		stillDone, err := q.GetAIJobByCaptureID(ctx, capture.ID)
		require.NoError(t, err)
		assert.Equal(t, "done", stillDone.Status, "regenerate-readability must not requeue the AI job")
	})

	t.Run("another user's capture returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/regen-readability-not-yours")
		capture := dbtest.CreateCapture(t, pool, page.ID)
		require.NoError(t, db.New(pool).CreateReadabilityJob(context.Background(), capture.ID))

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodPost, fmt.Sprintf("/api/captures/%d/regenerate-readability", capture.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a nonexistent capture id returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodPost, "/api/captures/9999999/regenerate-readability", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Post(server.URL+"/api/captures/1/regenerate-readability", "application/json", http.NoBody)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
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
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &requester)

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
		cookie := sessionCookieFor(t, pool, &user)

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

func TestGetCaptureConfig(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("reports this server's own configured readability_version/ai_model", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		// newTestServer wires "test-readability-version"/"test-ai-model"
		// (see its own doc comment) -- this just confirms GetCaptureConfig
		// actually surfaces whatever it was given rather than hardcoding
		// anything, same reasoning as SetupStatus's own
		// open_registration-reflects-configured-value test.
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/capture-config", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ReadabilityVersion *string `json:"readability_version"`
			AIModel            *string `json:"ai_model"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.NotNil(t, got.ReadabilityVersion)
		assert.Equal(t, "test-readability-version", *got.ReadabilityVersion)
		require.NotNil(t, got.AIModel)
		assert.Equal(t, "test-ai-model", *got.AIModel)
	})

	t.Run("reports null, not empty string, for an unconfigured value", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		m := mirror.NewClient(unreachable, "test-secret")
		d := devices.NewClient(unreachable, "test-secret")
		qi := queueitems.NewClient(unreachable, "test-secret")
		store := archive.New(t.TempDir())
		bootstrap, _, err := auth.NewBootstrapTokenHolder()
		require.NoError(t, err)

		// Deliberately "", "" here -- a dev build (no `make`-injected
		// readability_version) with AI enrichment disabled entirely
		// (cmd/server.go's own empty-AIBaseURL-means-disabled reasoning).
		s := httpapi.NewServer(q, pool, store, m, d, qi, bootstrap, false, testPairingKey(t), true, "", "")
		logger := httplog.NewLogger("recueil-test")
		logger.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
		r, err := httpapi.NewRouter(s, pool, q, logger, httpapi.BuildInfo{}, nil)
		require.NoError(t, err)
		server := httptest.NewServer(r)
		t.Cleanup(server.Close)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, "/api/capture-config", cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			ReadabilityVersion *string `json:"readability_version"`
			AIModel            *string `json:"ai_model"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Nil(t, got.ReadabilityVersion)
		assert.Nil(t, got.AIModel)
	})

	t.Run("without a session cookie returns 401", func(t *testing.T) {
		server, _ := newTestServer(t, pool, unreachable)
		resp, err := http.Get(server.URL + "/api/capture-config")
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
		_, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: other.ID, Name: "not-mine", Slug: "not-mine"})
		require.NoError(t, err)
		_, err = q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "zebra", Slug: "zebra"})
		require.NoError(t, err)
		_, err = q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "aardvark", Slug: "aardvark"})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

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
		cookie := sessionCookieFor(t, pool, &user)

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
			Slug string `json:"slug"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&tag))
		assert.Equal(t, "recipes", tag.Name)
		assert.Equal(t, "recipes", tag.Slug)

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
		cookie := sessionCookieFor(t, pool, &requester)

		req, err := http.NewRequest(http.MethodPost, server.URL+fmt.Sprintf("/api/pages/%d/tags", page.ID),
			strings.NewReader(`{"name":"recipes"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a tag whose slug collides with a differently-named tag returns 409", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/tag-collision")
		q := db.New(pool)
		_, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "Go!", Slug: "go"})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		// "Go!" and "Go?" transliterate to the same slug -- this is
		// deliberately NOT auto-suffixed for the manual add-tag flow
		// (see UpsertTag's own doc comment): the person sees an error
		// and can pick a different name, rather than getting a silently
		// suffixed slug they never asked for.
		req, err := http.NewRequest(http.MethodPost, server.URL+fmt.Sprintf("/api/pages/%d/tags", page.ID),
			strings.NewReader(`{"name":"Go?"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})
}

func TestRenameTag(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("renames and re-derives the slug by default", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "recipes", Slug: "recipes"})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/tags/%d", tag.ID),
			strings.NewReader(`{"name":"Cooking"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var renamed struct {
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&renamed))
		assert.Equal(t, "Cooking", renamed.Name)
		assert.Equal(t, "cooking", renamed.Slug)
	})

	t.Run("an explicit slug is honored", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "recipes", Slug: "recipes"})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/tags/%d", tag.ID),
			strings.NewReader(`{"name":"Cooking","slug":"food"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var renamed struct {
			Slug string `json:"slug"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&renamed))
		assert.Equal(t, "food", renamed.Slug)
	})

	t.Run("renaming to a slug already used by another tag returns 409", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		_, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "existing", Slug: "existing"})
		require.NoError(t, err)
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "renaming-me", Slug: "renaming-me"})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/tags/%d", tag.ID),
			strings.NewReader(`{"name":"Something Else","slug":"existing"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})

	t.Run("renaming another user's tag returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: owner.ID, Name: "not-yours", Slug: "not-yours"})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		req, err := http.NewRequest(http.MethodPatch, server.URL+fmt.Sprintf("/api/tags/%d", tag.ID),
			strings.NewReader(`{"name":"Hijacked"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestListTagPages(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("returns pages carrying the tag, excluding untagged pages", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		tagged1 := dbtest.CreatePage(t, pool, user.ID, "https://example.com/tagged-one")
		tagged2 := dbtest.CreatePage(t, pool, user.ID, "https://example.com/tagged-two")
		untagged := dbtest.CreatePage(t, pool, user.ID, "https://example.com/untagged")
		q := db.New(pool)
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "recipes", Slug: "recipes"})
		require.NoError(t, err)
		require.NoError(t, q.AddPageTag(context.Background(), db.AddPageTagParams{PageID: tagged1.ID, TagID: tag.ID, Source: "manual"}))
		require.NoError(t, q.AddPageTag(context.Background(), db.AddPageTagParams{PageID: tagged2.ID, TagID: tag.ID, Source: "manual"}))

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/tags/%s/pages", tag.Slug), cookie)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var got struct {
			Tag struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
				Slug string `json:"slug"`
			} `json:"tag"`
			Pages []struct {
				ID int64 `json:"id"`
			} `json:"pages"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, tag.ID, got.Tag.ID)
		assert.Equal(t, "recipes", got.Tag.Name)
		assert.Equal(t, "recipes", got.Tag.Slug)
		require.Len(t, got.Pages, 2)
		gotIDs := []int64{got.Pages[0].ID, got.Pages[1].ID}
		assert.Contains(t, gotIDs, tagged1.ID)
		assert.Contains(t, gotIDs, tagged2.ID)
		assert.NotContains(t, gotIDs, untagged.ID)
	})

	t.Run("another user's tag slug returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: owner.ID, Name: "not-yours", Slug: "not-yours"})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/tags/%s/pages", tag.Slug), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})
}

func TestDeleteTag(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("deletes the tag and unlinks it from tagged pages", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/delete-tag-target")
		q := db.New(pool)
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: user.ID, Name: "recipes", Slug: "recipes"})
		require.NoError(t, err)
		require.NoError(t, q.AddPageTag(context.Background(), db.AddPageTagParams{PageID: page.ID, TagID: tag.ID, Source: "manual"}))

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		delResp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/tags/%d", tag.ID), cookie)
		assert.Equal(t, http.StatusNoContent, delResp.StatusCode)

		pageTags, err := q.ListPageTags(context.Background(), page.ID)
		require.NoError(t, err)
		assert.Empty(t, pageTags)

		listResp := requestWithCookie(t, server, http.MethodGet, "/api/tags", cookie)
		var list struct {
			Tags []struct{ ID int64 } `json:"tags"`
		}
		require.NoError(t, json.NewDecoder(listResp.Body).Decode(&list))
		assert.Empty(t, list.Tags)
	})

	t.Run("deleting a nonexistent tag returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodDelete, "/api/tags/999999", cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("deleting another user's tag returns 404 and leaves it intact", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		q := db.New(pool)
		tag, err := q.UpsertTag(context.Background(), db.UpsertTagParams{UserID: owner.ID, Name: "not-yours", Slug: "not-yours"})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

		resp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/tags/%d", tag.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)

		_, err = q.GetTagByID(context.Background(), db.GetTagByIDParams{ID: tag.ID, UserID: owner.ID})
		assert.NoError(t, err, "the other user's tag should still exist")
	})
}

func TestCollectionsCRUD(t *testing.T) {
	pool := dbtest.Setup(t)

	t.Run("create, list, rename, delete a top-level collection", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

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
			Slug     string `json:"slug"`
			ParentID *int64 `json:"parent_id"`
		}
		require.NoError(t, json.NewDecoder(createResp.Body).Decode(&collection))
		assert.Equal(t, "Reading List", collection.Name)
		assert.Equal(t, "reading-list", collection.Slug)
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
			Slug string `json:"slug"`
		}
		require.NoError(t, json.NewDecoder(renameResp.Body).Decode(&renamed))
		assert.Equal(t, "Books", renamed.Name)
		assert.Equal(t, "books", renamed.Slug, "renaming re-derives the slug from the new name by default")

		delResp := requestWithCookie(t, server, http.MethodDelete, fmt.Sprintf("/api/collections/%d", collection.ID), cookie)
		assert.Equal(t, http.StatusNoContent, delResp.StatusCode)

		getAfterDelete := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/collections/%d/pages", collection.ID), cookie)
		assert.Equal(t, http.StatusNotFound, getAfterDelete.StatusCode)
	})

	t.Run("a duplicate top-level name returns 409", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

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

	t.Run("an explicit slug is honored instead of the auto-generated one", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/collections",
			strings.NewReader(`{"name":"Reading List","slug":"reading"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var collection struct {
			Slug string `json:"slug"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&collection))
		assert.Equal(t, "reading", collection.Slug)
	})

	t.Run("an invalid explicit slug returns 400", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/collections",
			strings.NewReader(`{"name":"Reading List","slug":"Not Valid!"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})

	t.Run("a slug collision under a different name still returns 409", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		firstReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/collections",
			strings.NewReader(`{"name":"Go!"}`))
		require.NoError(t, err)
		firstReq.Header.Set("Content-Type", "application/json")
		firstReq.AddCookie(cookie)
		firstResp, err := http.DefaultClient.Do(firstReq)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, firstResp.StatusCode)

		// "Go!" and "Go?" are different names but transliterate to the
		// same slug ("go") -- a genuine slug-only collision, distinct
		// from the plain duplicate-name case above.
		secondReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/collections",
			strings.NewReader(`{"name":"Go?"}`))
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
			UserID: owner.ID, Name: "Owner's Collection", Slug: "owners-collection",
		})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

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
			UserID: owner.ID, Name: "Not Yours", Slug: "not-yours",
		})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

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
			UserID: user.ID, Name: "My Collection", Slug: "my-collection",
		})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

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
			UserID: otherOwner.ID, Name: "Not Yours Either", Slug: "not-yours-either",
		})
		require.NoError(t, err)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

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

		cookie := sessionCookieFor(t, pool, &user)
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
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/favicon", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("another user's page returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/favicon-not-yours")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

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

		cookie := sessionCookieFor(t, pool, &user)
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
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/thumbnail", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("a page with no captures at all returns 404", func(t *testing.T) {
		user := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/no-captures")

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &user)

		resp := requestWithCookie(t, server, http.MethodGet, fmt.Sprintf("/api/pages/%d/thumbnail", page.ID), cookie)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	})

	t.Run("another user's page returns 404", func(t *testing.T) {
		owner := dbtest.CreateUser(t, pool, "member")
		requester := dbtest.CreateUser(t, pool, "member")
		page := dbtest.CreatePage(t, pool, owner.ID, "https://example.com/thumbnail-not-yours")
		dbtest.CreateCapture(t, pool, page.ID)

		server, _ := newTestServer(t, pool, unreachable)
		cookie := sessionCookieFor(t, pool, &requester)

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
