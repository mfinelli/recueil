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

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/medama-io/go-useragent"
	"github.com/medama-io/go-useragent/agents"
	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/devices"
	"github.com/mfinelli/recueil/internal/mirror"
	"github.com/mfinelli/recueil/internal/queueitems"
	"github.com/mfinelli/recueil/internal/slug"
)

type Server struct {
	Queries                *db.Queries
	Pool                   *pgxpool.Pool
	Store                  *archive.Store
	Mirror                 *mirror.Client
	Devices                *devices.Client
	QueueItems             *queueitems.Client
	Bootstrap              *auth.BootstrapTokenHolder
	CookieSecure           bool
	PairingKey             auth.PairingKey
	EnableOpenRegistration bool

	// ReadabilityVersion/AIModel are this running agent's currently
	// configured values -- the same readability.Params.Version/
	// ai.Params.Model the background job runner itself was constructed
	// with, threaded through here purely for GetCaptureConfig to report.
	ReadabilityVersion string
	AIModel            string
}

func NewServer(q *db.Queries, pool *pgxpool.Pool, store *archive.Store, m *mirror.Client, d *devices.Client, qi *queueitems.Client, bootstrap *auth.BootstrapTokenHolder, cookieSecure bool, pairingKey auth.PairingKey, enableOpenRegistration bool, readabilityVersion, aiModel string) *Server {
	return &Server{
		Queries: q, Pool: pool, Store: store, Mirror: m, Devices: d, QueueItems: qi, Bootstrap: bootstrap,
		CookieSecure: cookieSecure, PairingKey: pairingKey, EnableOpenRegistration: enableOpenRegistration,
		ReadabilityVersion: readabilityVersion, AIModel: aiModel,
	}
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type setupRequest struct {
	BootstrapToken string `json:"bootstrap_token"`
	Username       string `json:"username"`
	Password       string `json:"password"`
}

type userResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type pairingTokenResponse struct {
	PairingToken string `json:"pairing_token"`
}

type setupStatusResponse struct {
	NeedsSetup       bool `json:"needs_setup"`
	OpenRegistration bool `json:"open_registration"`
}

// GET /api/setup-status: unauthenticated -- lets the dashboard's first load
// distinguish "show the setup screen" from "show the login screen" without
// guessing or having to attempt POST /api/setup speculatively just to read
// its 409. Also carries OpenRegistration (server config, not user data) so
// Login knows whether to link to /register without a second unauthenticated
// round-trip. Doesn't leak anything beyond these two booleans (not a
// username, not a count) -- an unauthenticated endpoint has no other reason
// to exist here.
func (s *Server) SetupStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.Queries.CountUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, setupStatusResponse{
		NeedsSetup:       count == 0,
		OpenRegistration: s.EnableOpenRegistration,
	})
}

// POST /api/setup: creates the first admin account, gated by the bootstrap
// token printed to the backend's logs on startup. The token is
// consumed only if CreateUser actually succeeds (see auth.BootstrapTokenHolder.Use),
// so a transient failure after a valid token can be retried without a restart.
func (s *Server) Setup(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[setupRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" || req.BootstrapToken == "" {
		writeError(w, http.StatusBadRequest, "bootstrap_token, username, and password are required")
		return
	}

	ctx := r.Context()

	// Belt-and-suspenders: even though a valid unused token implies this,
	// explicitly confirm no admin has slipped in through a race.
	count, err := s.Queries.CountUsers(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if count > 0 {
		writeError(w, http.StatusConflict, "setup has already been completed")
		return
	}

	var user db.User
	var pairingHash string
	var createErr error
	err = s.Bootstrap.Use(req.BootstrapToken, func() error {
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			return err
		}

		var pairingRaw string
		pairingRaw, pairingHash, err = auth.GeneratePairingToken()
		if err != nil {
			return err
		}
		pairingEnc, err := auth.EncryptPairingToken(s.PairingKey, pairingRaw)
		if err != nil {
			return err
		}

		user, createErr = s.Queries.CreateUser(ctx, db.CreateUserParams{
			Username:        req.Username,
			PasswordHash:    hash,
			PairingTokenEnc: pgtype.Text{String: pairingEnc, Valid: true},
			Role:            "admin",
		})
		return createErr
	})

	if errors.Is(err, auth.ErrInvalidBootstrapToken) {
		writeError(w, http.StatusUnauthorized, "invalid or expired bootstrap token")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "could not create user (username may be taken)")
		return
	}

	if err := s.Mirror.PushUser(ctx, user.ID, &pairingHash); err != nil {
		// Doesn't roll back account creation (see internal/mirror's
		// PushUser doc comment). Dashboard login works immediately; device
		// pairing for this user is broken until a resync runs.
		log.Printf("warning: failed to push pairing-token mirror for new user %d: %v", user.ID, err)
	}

	s.startSession(w, r, &user)
	writeJSON(w, http.StatusCreated, userResponse{ID: user.ID, Username: user.Username, Role: user.Role})
}

// POST /api/auth/register: open registration. Gated by
// EnableOpenRegistration (config: enable_open_registration).
func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
	if !s.EnableOpenRegistration {
		writeError(w, http.StatusForbidden, "open registration is disabled")
		return
	}

	req, err := decodeJSON[credentials](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	ctx := r.Context()
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	pairingRaw, pairingHash, err := auth.GeneratePairingToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	pairingEnc, err := auth.EncryptPairingToken(s.PairingKey, pairingRaw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	user, err := s.Queries.CreateUser(ctx, db.CreateUserParams{
		Username:        req.Username,
		PasswordHash:    hash,
		PairingTokenEnc: pgtype.Text{String: pairingEnc, Valid: true},
		Role:            "member",
	})
	if err != nil {
		writeError(w, http.StatusConflict, "username already taken")
		return
	}

	if err := s.Mirror.PushUser(ctx, user.ID, &pairingHash); err != nil {
		log.Printf("warning: failed to push pairing-token mirror for new user %d: %v", user.ID, err)
	}

	s.startSession(w, r, &user)
	writeJSON(w, http.StatusCreated, userResponse{ID: user.ID, Username: user.Username, Role: user.Role})
}

// POST /api/auth/login
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[credentials](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := r.Context()
	user, err := s.Queries.GetUserByUsername(ctx, req.Username)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	if !auth.VerifyPassword(user.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	s.startSession(w, r, &user)
	writeJSON(w, http.StatusOK, userResponse{ID: user.ID, Username: user.Username, Role: user.Role})
}

// POST /api/auth/logout
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("recueil_session"); err == nil && cookie.Value != "" {
		if err := s.Queries.DeleteSession(r.Context(), auth.HashToken(cookie.Value)); err != nil {
			log.Printf("warning: failed to delete session: %v", err)
		}
	}
	auth.ClearSessionCookie(w, s.CookieSecure)
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/auth/me
func (s *Server) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, userResponse{ID: user.ID, Username: user.Username, Role: user.Role})
}

// GET /api/pairing-token: decrypts and returns the current user's pairing
// token, so it's always viewable on the dashboard rather than only shown
// once at creation (this credential's stakes differ from a login password or
// session token, and losing it shouldn't force an immediate regenerate).
func (s *Server) GetPairingToken(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !user.PairingTokenEnc.Valid {
		writeError(w, http.StatusNotFound, "no pairing token; regenerate one")
		return
	}

	raw, err := auth.DecryptPairingToken(s.PairingKey, user.PairingTokenEnc.String)
	if err != nil {
		log.Printf("warning: failed to decrypt pairing token for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, pairingTokenResponse{PairingToken: raw})
}

// POST /api/pairing-token/regenerate: issues a new pairing token, replacing
// both the Postgres (encrypted) and D1 (hashed) copies. Any device that
// tries to pair using the previous token will fail; already-issued device
// bearer tokens are unaffected (revocation is never a live push to an
// already-paired device).
func (s *Server) RegeneratePairingToken(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx := r.Context()

	raw, hash, err := auth.GeneratePairingToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	enc, err := auth.EncryptPairingToken(s.PairingKey, raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.Queries.UpdatePairingToken(ctx, db.UpdatePairingTokenParams{
		ID:              user.ID,
		PairingTokenEnc: pgtype.Text{String: enc, Valid: true},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.Mirror.PushUser(ctx, user.ID, &hash); err != nil {
		log.Printf("warning: failed to push regenerated pairing-token mirror for user %d: %v", user.ID, err)
	}

	writeJSON(w, http.StatusOK, pairingTokenResponse{PairingToken: raw})
}

// DELETE /api/pairing-token: revokes without reissuing, blocking further
// device pairing until a regenerate. Already-issued device bearer tokens
// are unaffected, same as regenerate above.
func (s *Server) RevokePairingToken(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx := r.Context()

	if err := s.Queries.UpdatePairingToken(ctx, db.UpdatePairingTokenParams{
		ID:              user.ID,
		PairingTokenEnc: pgtype.Text{Valid: false},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.Mirror.PushUser(ctx, user.ID, nil); err != nil {
		log.Printf("warning: failed to push pairing-token revoke to mirror for user %d: %v", user.ID, err)
	}

	w.WriteHeader(http.StatusNoContent)
}

type deviceListResponse struct {
	Devices []devices.Token `json:"devices"`
}

// GET /api/devices: lists the calling user's own paired devices. Always
// self-scoped.
func (s *Server) ListDevices(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tokens, err := s.Devices.ListTokens(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list devices for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, deviceListResponse{Devices: tokens})
}

// DELETE /api/devices/{id}: revokes one of the calling user's own
// devices. Always self-scoped, same reasoning as ListDevices above. Not a
// live push -- the device keeps working until its next request to the
// Worker.
func (s *Server) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device id")
		return
	}

	if err := s.Devices.RevokeToken(r.Context(), user.ID, tokenID); err != nil {
		if errors.Is(err, devices.ErrNotFound) {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		log.Printf("warning: failed to revoke device %d for user %d: %v", tokenID, user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type sessionResponse struct {
	ID             int64  `json:"id"`
	Browser        string `json:"browser"`
	BrowserVersion string `json:"browser_version"`
	OS             string `json:"os"`
	// DeviceClass is one of "desktop"/"mobile"/"tablet"/"tv"/"bot" (see
	// sessionResponseFromSession's own doc comment), or "" if the
	// session has no user_agent at all or the go-useragent library didn't
	// recognize it. The dashboard's own icon-picking logic treats "" the
	// same as any other unrecognized value -- a generic fallback icon,
	// not an error.
	DeviceClass string    `json:"device_class"`
	CreatedAt   time.Time `json:"created_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	// IsCurrent marks the one row that matches the current request's
	// session cookie (auth.SessionIDFromContext)
	IsCurrent bool `json:"is_current"`
}

// sessionResponseFromSession parses user_agent fresh on every call
// (go-useragent's Parser is cheap -- a trie lookup, no regex, no
// network, no disk) rather than storing browser/OS/device_class as their own
// columns at session-creation time. A NULL/empty user_agent parses to every
// field empty, which the dashboard already treats as "generic/unknown," not
// an error.
func sessionResponseFromSession(parser *useragent.Parser, sess *db.Session, isCurrent bool) sessionResponse {
	resp := sessionResponse{
		ID: sess.ID, CreatedAt: sess.CreatedAt.Time, LastSeenAt: sess.LastSeenAt.Time, IsCurrent: isCurrent,
	}
	if !sess.UserAgent.Valid || sess.UserAgent.String == "" {
		return resp
	}

	ua := parser.Parse(sess.UserAgent.String)
	resp.Browser = ua.Browser().String()
	resp.BrowserVersion = ua.BrowserVersionMajor()
	resp.OS = ua.OS().String()
	switch ua.Device() {
	case agents.DeviceDesktop:
		resp.DeviceClass = "desktop"
	case agents.DeviceMobile:
		resp.DeviceClass = "mobile"
	case agents.DeviceTablet:
		resp.DeviceClass = "tablet"
	case agents.DeviceTV:
		resp.DeviceClass = "tv"
	case agents.DeviceBot:
		resp.DeviceClass = "bot"
	}
	return resp
}

type sessionListResponse struct {
	Sessions []sessionResponse `json:"sessions"`
}

// GET /api/sessions: the calling user's active (unexpired) sessions -- always
// self-scoped. Most recently active first.
func (s *Server) ListSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	currentID, _ := auth.SessionIDFromContext(r.Context())

	sessions, err := s.Queries.ListSessionsForUser(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list sessions for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	parser := useragent.NewParser()
	resp := make([]sessionResponse, len(sessions))
	for i, sess := range sessions {
		resp[i] = sessionResponseFromSession(parser, &sess, sess.ID == currentID)
	}

	writeJSON(w, http.StatusOK, sessionListResponse{Sessions: resp})
}

// DELETE /api/sessions/{id}: revokes one of the calling user's own
// *other* sessions -- but never the one this very request is
// authenticated with. Ending your own current session through this
// screen would mean the DELETE request itself succeeds and then every
// subsequent request, including whatever the dashboard tried to do
// next, starts 401ing with no session left to explain why -- confusing
// at best. Signing out (POST /api/auth/logout, the existing flow) is
// the correct, already-well-understood way to end your own current
// session; this endpoint refuses instead of trying to gracefully handle
// self-deletion's own aftermath. The dashboard hides the revoke control
// entirely for the current session's row, so reaching this 400 at all
// means either a stale tab or a direct API call, not a normal click.
func (s *Server) DeleteSession(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	sessionID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}

	if currentID, ok := auth.SessionIDFromContext(r.Context()); ok && currentID == sessionID {
		writeError(w, http.StatusBadRequest, "sign out to end your current session")
		return
	}

	rowsAffected, err := s.Queries.DeleteSessionForUser(r.Context(), db.DeleteSessionForUserParams{ID: sessionID, UserID: user.ID})
	if err != nil {
		log.Printf("warning: failed to delete session %d for user %d: %v", sessionID, user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rowsAffected == 0 {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type userSettingsResponse struct {
	// Pointer, not a plain string -- nil renders as JSON null, distinct
	// from an explicit empty-string language, matching textOrNil's own
	// convention elsewhere in this file (title/favicon_path/etc.).
	Language *string `json:"language"`
	// nil means "automatic" -- follow the browser's prefers-color-scheme.
	// "light"/"dark" are the only other possible values (enforced by the
	// theme CHECK constraint at the database level, not just here).
	Theme *string `json:"theme"`
}

func userSettingsResponseFromSettings(s *db.UserSetting) userSettingsResponse {
	return userSettingsResponse{Language: textOrNil(s.Language), Theme: textOrNil(s.Theme)}
}

// GET /api/settings: the calling user's dashboard preferences. A user who
// has never PATCHed their settings has no row in user_settings at all (see
// UpsertUserSettings's own doc comment for why there's no row-per-user
// backfill) -- that's treated identically to a row that exists with a
// field explicitly NULL, both rendering as null for that field, since
// from the API's point of view "never set" and "explicitly cleared" mean
// the same thing: no override, fall back to auto-detection (browser
// language / prefers-color-scheme respectively).
func (s *Server) GetSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	settings, err := s.Queries.GetUserSettings(r.Context(), user.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, userSettingsResponse{})
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, userSettingsResponseFromSettings(&settings))
}

// A shape check, not a fixed enum -- this only rejects values that couldn't
// possibly be a real language tag, not values that aren't (yet) translated.
var languageTagPattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)

type patchSettingsRequest struct {
	// Neither field is a pointer, unlike patchPageRequest's per-field
	// pointers -- both are always sent together, full-replace, on every
	// PATCH (this is one Settings screen with both preferences already
	// loaded into the same form, so there's never a real "update just
	// one without knowing the other" case to support). An empty string
	// clears either field back to NULL/automatic; anything else must be
	// a real language tag (Language) or exactly "light"/"dark" (Theme).
	Language string `json:"language"`
	Theme    string `json:"theme"`
}

// PATCH /api/settings: full-replace semantics for both fields (see
// patchSettingsRequest's own doc comment). Upserts rather than requiring a
// prior GET/row to exist -- a user's very first settings change is exactly
// as valid as their hundredth.
func (s *Server) PatchSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	req, err := decodeJSON[patchSettingsRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Language != "" && !languageTagPattern.MatchString(req.Language) {
		writeError(w, http.StatusBadRequest, "invalid language tag")
		return
	}
	if req.Theme != "" && req.Theme != "light" && req.Theme != "dark" {
		writeError(w, http.StatusBadRequest, "invalid theme")
		return
	}

	settings, err := s.Queries.UpsertUserSettings(r.Context(), db.UpsertUserSettingsParams{
		UserID: user.ID, Language: textOrNull(req.Language), Theme: textOrNull(req.Theme),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, userSettingsResponseFromSettings(&settings))
}

type queueItemListResponse struct {
	Items []queueitems.Item `json:"items"`
}

// GET /api/queue-items: lists the calling user's own failed queue items
// (URLs the extension/CLI tried and failed to archive). Always
// self-scoped, same reasoning as ListDevices -- this is personal data, not
// a member-vs-admin dashboard concern the way Manage Devices originally
// was before that cross-user piece was reconsidered and removed.
func (s *Server) ListFailedQueueItems(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	items, err := s.QueueItems.ListFailed(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list failed queue items for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, queueItemListResponse{Items: items})
}

// POST /api/queue-items/{id}/retry: flags one of the calling user's own
// failed queue items for another device claim attempt. Always
// self-scoped. Not a live push -- the item is picked up on some device's
// next poll of GET /queue, same as any other queue item.
func (s *Server) RetryQueueItem(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	itemID := chi.URLParam(r, "id")
	if itemID == "" {
		writeError(w, http.StatusBadRequest, "invalid queue item id")
		return
	}

	if err := s.QueueItems.Retry(r.Context(), user.ID, itemID); err != nil {
		if errors.Is(err, queueitems.ErrNotFound) {
			writeError(w, http.StatusNotFound, "queue item not found or not retryable")
			return
		}
		log.Printf("warning: failed to retry queue item %q for user %d: %v", itemID, user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// timestamptzOrNil converts a pgtype.Timestamptz into the *time.Time shape
// the dashboard's JSON responses use -- nil rather than the zero time for
// a genuinely NULL completed_at (a job that hasn't finished, one way or
// the other, yet), same reasoning as textOrNil/int8OrNil.
func timestamptzOrNil(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// failedJob is the dashboard Queue screen's combined shape for a failed
// screenshot/readability/AI job -- all three of ListFailedScreenshotJobsForUser/
// ListFailedReadabilityJobsForUser/ListFailedAIJobsForUser return this same
// row shape (id/attempts/error/completed_at from the job itself,
// page_id/raw_url/title from the capture it belongs to, via the same
// pages-ownership join GetCaptureByIDForUser already uses), so one response
// type serves all three lists rather than three near-identical DTOs.
type failedJob struct {
	ID          int64      `json:"id"`
	PageID      int64      `json:"page_id"`
	URL         string     `json:"url"`
	Title       *string    `json:"title"`
	Attempts    int32      `json:"attempts"`
	Error       *string    `json:"error"`
	CompletedAt *time.Time `json:"completed_at"`
}

// failedJobsResponse groups all three job types under their own keys
// (rather than one flat list with a job_type discriminator) so the
// dashboard's Queue screen can render them as separate sections without
// having to partition the response itself.
type failedJobsResponse struct {
	ScreenshotJobs  []failedJob `json:"screenshot_jobs"`
	ReadabilityJobs []failedJob `json:"readability_jobs"`
	AIJobs          []failedJob `json:"ai_jobs"`
}

// GET /api/jobs: lists the calling user's own failed screenshot,
// readability, and AI-enrichment jobs in one response -- these are
// backend-owned async jobs (unlike queue_items, nothing device-side ever
// claims them), so this needs no Worker round trip at all, just three
// ownership-scoped Postgres queries. Always self-scoped, same reasoning as
// ListFailedQueueItems/ListDevices.
//
// A capture whose readability extraction permanently failed never gets an
// ai_jobs row at all (see readability_jobs.sql's CreateAIJob-on-success
// comment) -- it shows up in ReadabilityJobs here, not AIJobs, since
// there's nothing in the ai_jobs table to list for it yet.
func (s *Server) ListFailedJobs(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	screenshotRows, err := s.Queries.ListFailedScreenshotJobsForUser(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list failed screenshot jobs for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	readabilityRows, err := s.Queries.ListFailedReadabilityJobsForUser(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list failed readability jobs for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	aiRows, err := s.Queries.ListFailedAIJobsForUser(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list failed AI jobs for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := failedJobsResponse{
		ScreenshotJobs:  make([]failedJob, 0, len(screenshotRows)),
		ReadabilityJobs: make([]failedJob, 0, len(readabilityRows)),
		AIJobs:          make([]failedJob, 0, len(aiRows)),
	}
	for _, j := range screenshotRows {
		resp.ScreenshotJobs = append(resp.ScreenshotJobs, failedJob{
			ID: j.ID, PageID: j.PageID, URL: j.RawUrl, Title: textOrNil(j.Title),
			Attempts: j.Attempts, Error: textOrNil(j.Error), CompletedAt: timestamptzOrNil(j.CompletedAt),
		})
	}
	for _, j := range readabilityRows {
		resp.ReadabilityJobs = append(resp.ReadabilityJobs, failedJob{
			ID: j.ID, PageID: j.PageID, URL: j.RawUrl, Title: textOrNil(j.Title),
			Attempts: j.Attempts, Error: textOrNil(j.Error), CompletedAt: timestamptzOrNil(j.CompletedAt),
		})
	}
	for _, j := range aiRows {
		resp.AIJobs = append(resp.AIJobs, failedJob{
			ID: j.ID, PageID: j.PageID, URL: j.RawUrl, Title: textOrNil(j.Title),
			Attempts: j.Attempts, Error: textOrNil(j.Error), CompletedAt: timestamptzOrNil(j.CompletedAt),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// jobKindToRetry maps the {kind} path param POST /api/jobs/{kind}/{id}/retry
// is routed with to the right ownership-scoped retry query. A single
// dispatching handler (RetryJob below) rather than three near-identical
// ones, since the only thing that differs between screenshot/readability/AI
// retry is which query to call.
var jobKindToRetry = map[string]func(s *Server, ctx context.Context, id, userID int64) (int64, error){
	"screenshot": func(s *Server, ctx context.Context, id, userID int64) (int64, error) {
		return s.Queries.ManualRetryScreenshotJobForUser(ctx, db.ManualRetryScreenshotJobForUserParams{ID: id, UserID: userID})
	},
	"readability": func(s *Server, ctx context.Context, id, userID int64) (int64, error) {
		return s.Queries.ManualRetryReadabilityJobForUser(ctx, db.ManualRetryReadabilityJobForUserParams{ID: id, UserID: userID})
	},
	"ai": func(s *Server, ctx context.Context, id, userID int64) (int64, error) {
		return s.Queries.ManualRetryAIJobForUser(ctx, db.ManualRetryAIJobForUserParams{ID: id, UserID: userID})
	},
}

// POST /api/jobs/{kind}/{id}/retry: flags one of the calling user's own
// failed screenshot/readability/AI jobs for another attempt. {kind} must be
// one of "screenshot", "readability", "ai" -- anything else is a 400, not a
// 404, since it's a caller bug (an unrecognized kind), not a
// missing-resource situation. Always self-scoped, same reasoning as
// ListFailedJobs above.
//
// Unlike RetryQueueItem, there's no device to race against here and no
// separate "flag it, some device picks it up later" step: the retry query
// resets the job straight back to 'pending' with next_attempt_at cleared,
// so the backend's own next poll of ClaimDueScreenshotJobs/
// ClaimDueReadabilityJobs/ClaimDueAIJobs picks it up immediately. attempts
// is deliberately left untouched by the query itself -- see
// ManualRetryScreenshotJobForUser's own doc comment for why a manual retry
// spends the next attempt rather than resetting the budget.
func (s *Server) RetryJob(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	kind := chi.URLParam(r, "kind")
	retry, ok := jobKindToRetry[kind]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid job kind")
		return
	}

	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}

	if _, err := retry(s, r.Context(), id, user.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "job not found or not retryable")
			return
		}
		log.Printf("warning: failed to retry %s job %d for user %d: %v", kind, id, user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// defaultPageLimit/maxPageLimit bound ?limit= for /api/pages: a sane
// default for the library view, and a ceiling that's generous for a
// personal/family archive without letting a runaway value force one
// query to return everything at once.
const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

func parseLimitOffset(r *http.Request) (limit, offset int32) {
	limit = defaultPageLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxPageLimit {
			limit = int32(n)
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n >= 0 {
			offset = int32(n)
		}
	}
	return limit, offset
}

// textOrNil converts a pgtype.Text into the *string shape the dashboard's
// JSON responses use -- nil rather than an empty string for a genuinely
// NULL column, matching how the frontend should treat "no title" /
// "no favicon" differently from "title is the empty string."
func textOrNil(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

// int4OrNil is textOrNil's twin for pgtype.Int4 -- thumbnail_size_bytes
// and favicon_size_bytes are both nullable (no thumbnail/favicon
// captured yet), same reasoning as their sibling *_path/*_hash columns.
func int4OrNil(i pgtype.Int4) *int32 {
	if !i.Valid {
		return nil
	}
	return &i.Int32
}

// stringOrNil is textOrNil's twin for a plain Go string that isn't a
// pgtype at all -- Server.ReadabilityVersion/AIModel specifically, where
// "" means "not configured" (see GetCaptureConfig's own doc comment) and
// should read as JSON null, not an empty string, same nullability
// convention as everything else in this API.
func stringOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// textOrNull is textOrNil's inverse -- internal/ingest has its own
// identically-named, identically-shaped helper, but that one's unexported
// in a different package, so this is a deliberate package-local twin, not
// a shared dependency. An empty string is treated as "not set" (NULL),
// same convention as ingest's use of it for title.
func textOrNull(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// resolveSlug decides what slug to store for a tag/collection create or
// rename: a caller-supplied slug (from the dashboard's optional,
// initially-collapsed slug field) is used as-is once validated, otherwise
// one is auto-generated from name. Returns ok=false when neither produces
// a usable value -- an explicit slug that fails slug.Valid, or an
// auto-generation attempt that came back empty because name has no Latin
// skeleton at all (see slug.Generate's own doc comment) -- so the caller
// can surface a 400 asking the person to type their own slug rather than
// writing something empty or malformed.
func resolveSlug(name string, explicit *string) (string, bool) {
	if explicit != nil {
		if !slug.Valid(*explicit) {
			return "", false
		}
		return *explicit, true
	}
	generated := slug.Generate(name)
	return generated, generated != ""
}

type pageResponse struct {
	ID                 int64     `json:"id"`
	NormalizedURL      string    `json:"normalized_url"`
	Title              *string   `json:"title"`
	LatestCaptureAt    time.Time `json:"latest_capture_at"`
	ExcludedFromMirror bool      `json:"excluded_from_mirror"`
	FaviconPath        *string   `json:"favicon_path"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func pageResponseFromPage(p *db.Page) pageResponse {
	return pageResponse{
		ID: p.ID, NormalizedURL: p.NormalizedUrl, Title: textOrNil(p.Title),
		LatestCaptureAt: p.LatestCaptureAt.Time, ExcludedFromMirror: p.ExcludedFromMirror,
		FaviconPath: textOrNil(p.FaviconPath), CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time,
	}
}

func pageResponseFromListRow(p *db.ListPagesRow) pageResponse {
	return pageResponse{
		ID: p.ID, NormalizedURL: p.NormalizedUrl, Title: textOrNil(p.Title),
		LatestCaptureAt: p.LatestCaptureAt.Time, ExcludedFromMirror: p.ExcludedFromMirror,
		FaviconPath: textOrNil(p.FaviconPath), CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time,
	}
}

func pageResponseFromSearchRow(p *db.SearchPagesRow) pageResponse {
	return pageResponse{
		ID: p.ID, NormalizedURL: p.NormalizedUrl, Title: textOrNil(p.Title),
		LatestCaptureAt: p.LatestCaptureAt.Time, ExcludedFromMirror: p.ExcludedFromMirror,
		FaviconPath: textOrNil(p.FaviconPath), CreatedAt: p.CreatedAt.Time, UpdatedAt: p.UpdatedAt.Time,
	}
}

type pageListResponse struct {
	Pages []pageResponse `json:"pages"`
	Total int64          `json:"total"`
}

// GET /api/pages: library browsing. ?q= triggers full-text search
// (matches if any of a page's captures' reader_text matches, not just
// the latest -- see queries/pages.sql's SearchPages); without it, plain
// listing ordered by latest_capture_at. ?limit=/?offset= paginate
// (default 50, max 200); the response's total reflects the full matching
// set regardless of the current page.
func (s *Server) ListPages(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit, offset := parseLimitOffset(r)
	ctx := r.Context()

	resp := pageListResponse{Pages: []pageResponse{}}

	if q := r.URL.Query().Get("q"); q != "" {
		rows, err := s.Queries.SearchPages(ctx, db.SearchPagesParams{
			UserID: user.ID, Query: q, Limit: limit, Offset: offset,
		})
		if err != nil {
			log.Printf("warning: failed to search pages for user %d: %v", user.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for i := range rows {
			resp.Total = rows[i].TotalCount
			resp.Pages = append(resp.Pages, pageResponseFromSearchRow(&rows[i]))
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	rows, err := s.Queries.ListPages(ctx, db.ListPagesParams{UserID: user.ID, Limit: limit, Offset: offset})
	if err != nil {
		log.Printf("warning: failed to list pages for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for i := range rows {
		resp.Total = rows[i].TotalCount
		resp.Pages = append(resp.Pages, pageResponseFromListRow(&rows[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

type captureSummaryResponse struct {
	ID                        int64     `json:"id"`
	Source                    string    `json:"source"`
	RawURL                    string    `json:"raw_url"`
	Title                     *string   `json:"title"`
	ThumbnailPath             *string   `json:"thumbnail_path"`
	Language                  string    `json:"language"`
	HTMLCompressedSizeBytes   int32     `json:"html_compressed_size_bytes"`
	HTMLUncompressedSizeBytes int32     `json:"html_uncompressed_size_bytes"`
	CapturedAt                time.Time `json:"captured_at"`
}

type pageTagResponse struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Source string `json:"source"`
}

type pageCollectionResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parent_id"`
}

// pageDetailResponse embeds pageResponse so its fields flatten into the
// same top-level JSON object as "captures"/"tags"/"collections" -- the
// page detail view's natural shape is "the page, plus everything
// attached to it," not a nested "page" envelope key.
type pageDetailResponse struct {
	pageResponse
	Captures    []captureSummaryResponse `json:"captures"`
	Tags        []pageTagResponse        `json:"tags"`
	Collections []pageCollectionResponse `json:"collections"`
}

// GET /api/pages/{id}: page detail plus full capture (version) history
// (most recent first), tags, and collection memberships. Captures are
// deliberately a summary per row, not the full row -- reader_text/
// ai_summary are large and belong to GET /api/captures/{id} instead.
func (s *Server) GetPage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	captures, err := s.Queries.ListCapturesByPage(ctx, page.ID)
	if err != nil {
		log.Printf("warning: failed to list captures for page %d: %v", page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	tags, err := s.Queries.ListPageTags(ctx, page.ID)
	if err != nil {
		log.Printf("warning: failed to list tags for page %d: %v", page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	collections, err := s.Queries.ListPageCollections(ctx, page.ID)
	if err != nil {
		log.Printf("warning: failed to list collections for page %d: %v", page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := pageDetailResponse{
		pageResponse: pageResponseFromPage(&page),
		Captures:     []captureSummaryResponse{},
		Tags:         []pageTagResponse{},
		Collections:  []pageCollectionResponse{},
	}
	for i := range captures {
		c := &captures[i]
		resp.Captures = append(resp.Captures, captureSummaryResponse{
			ID: c.ID, Source: c.Source, RawURL: c.RawUrl, Title: textOrNil(c.Title),
			ThumbnailPath: textOrNil(c.ThumbnailPath), Language: c.Language,
			HTMLCompressedSizeBytes: c.HtmlCompressedSizeBytes, HTMLUncompressedSizeBytes: c.HtmlUncompressedSizeBytes,
			CapturedAt: c.CapturedAt.Time,
		})
	}
	for _, t := range tags {
		resp.Tags = append(resp.Tags, pageTagResponse{ID: t.TagID, Name: t.Name, Slug: t.Slug, Source: t.Source})
	}
	for _, c := range collections {
		resp.Collections = append(resp.Collections, pageCollectionResponse{ID: c.CollectionID, Name: c.Name, ParentID: int8OrNil(c.ParentID)})
	}
	writeJSON(w, http.StatusOK, resp)
}

// serveAsset streams a small binary asset (favicon, thumbnail) off disk.
// Unlike GetCaptureHTML, no zstd/gzip content-negotiation dance -- these
// are already-binary images (PNG/ICO) or small enough SVGs that the same
// passthrough-compression treatment full HTML documents get isn't worth
// the complexity here. Store.Open already transparently decompresses a
// ".zst"-suffixed path (SVG favicons) and passes non-".zst" paths
// (PNG/ICO favicons, all thumbnails) through unmodified, so this doesn't
// need to know or care which case it's in.
func (s *Server) serveAsset(w http.ResponseWriter, relPath string) {
	reader, err := s.Store.Open(relPath)
	if err != nil {
		log.Printf("warning: failed to open asset %q: %v", relPath, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Type", contentTypeForAsset(relPath))
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("warning: failed streaming asset %q: %v", relPath, err)
	}
}

// contentTypeForAsset infers a Content-Type from an asset's stored file
// extension. Strips a trailing ".zst" first -- filepath.Ext only ever
// returns the *last* extension, which for "favicon.svg.zst" is ".zst",
// not the ".svg" that actually determines the decompressed content's
// real type.
func contentTypeForAsset(relPath string) string {
	trimmed := strings.TrimSuffix(relPath, ".zst")
	switch strings.ToLower(filepath.Ext(trimmed)) {
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return "application/octet-stream"
	}
}

// GET /api/pages/{id}/favicon: pages.favicon_path is denormalized from
// the latest capture at ingestion time (UpsertPage), so this is a direct
// read, not a join. No Cache-Control: this URL is page-identity-addressed,
// not content-addressed -- a later re-capture with a different favicon
// changes what this same URL resolves to, so caching it long-lived risks
// serving a stale icon.
func (s *Server) GetPageFavicon(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}

	page, err := s.Queries.GetPageByIDForUser(r.Context(), db.GetPageByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if !page.FaviconPath.Valid {
		writeError(w, http.StatusNotFound, "no favicon")
		return
	}

	s.serveAsset(w, page.FaviconPath.String)
}

// GET /api/pages/{id}/thumbnail: resolves the page's most recent
// capture's thumbnail (see GetLatestCaptureByPage's own doc comment for
// why this isn't a denormalized pages column the way favicon_path is).
// A capture with no thumbnail yet (screenshot job hasn't run, or
// genuinely failed) 404s the same as a page with no captures at all --
// the dashboard's grid view falls back to a placeholder either way, so
// there's no need to distinguish the two cases in the response.
func (s *Server) GetPageThumbnail(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	capture, err := s.Queries.GetLatestCaptureByPage(ctx, page.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no captures")
		return
	}
	if !capture.ThumbnailPath.Valid {
		writeError(w, http.StatusNotFound, "no thumbnail")
		return
	}

	s.serveAsset(w, capture.ThumbnailPath.String)
}

type patchPageRequest struct {
	ExcludedFromMirror *bool   `json:"excluded_from_mirror"`
	Title              *string `json:"title"`
}

// PATCH /api/pages/{id}: supports toggling excluded_from_mirror and/or
// overwriting title (a manual title override). Pointer fields
// distinguish "not provided" from an explicit false/empty, and at least
// one must be provided; if both are, each is applied as its own update
// (not one combined query), and the response reflects whichever ran
// last -- fine here since the dashboard never actually sends both in one
// request today (they're two separate pieces of UI).
func (s *Server) PatchPage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	req, err := decodeJSON[patchPageRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExcludedFromMirror == nil && req.Title == nil {
		writeError(w, http.StatusBadRequest, "excluded_from_mirror or title is required")
		return
	}

	ctx := r.Context()
	var page db.Page

	if req.ExcludedFromMirror != nil {
		page, err = s.Queries.SetPageExcludedFromMirror(ctx, db.SetPageExcludedFromMirrorParams{
			ExcludedFromMirror: *req.ExcludedFromMirror, ID: id, UserID: user.ID,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "page not found")
			return
		}
	}

	if req.Title != nil {
		trimmed := strings.TrimSpace(*req.Title)
		if trimmed == "" {
			writeError(w, http.StatusBadRequest, "title cannot be empty")
			return
		}
		page, err = s.Queries.SetPageTitle(ctx, db.SetPageTitleParams{
			Title: pgtype.Text{String: trimmed, Valid: true}, ID: id, UserID: user.ID,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "page not found")
			return
		}
	}

	writeJSON(w, http.StatusOK, pageResponseFromPage(&page))
}

// DELETE /api/pages/{id}: see DeletePage's own doc comment for exactly
// what this does and doesn't clean up (Postgres cascade vs. the D1
// mirror's self-healing sync vs. intentionally-orphaned on-disk archive
// files).
func (s *Server) DeletePage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}

	rowsAffected, err := s.Queries.DeletePage(r.Context(), db.DeletePageParams{ID: id, UserID: user.ID})
	if err != nil {
		log.Printf("warning: failed to delete page %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rowsAffected == 0 {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// POST /api/pages/{id}/recapture: re-enqueues the page's most recent
// capture's raw_url (not the normalized_url stored on the page itself --
// the raw URL is what a device would actually re-fetch) via the Worker's
// queue, the exact same queue a device's own share-sheet/extension enqueue
// feeds. This doesn't attempt any capture itself -- it's picked up
// by whichever device next polls GET /queue, same as any other queued
// URL, no different from a fresh manual add.
func (s *Server) RecapturePage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	capture, err := s.Queries.GetLatestCaptureByPage(ctx, page.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, "no captures to recapture")
		return
	}

	if err := s.QueueItems.Enqueue(ctx, user.ID, capture.RawUrl); err != nil {
		log.Printf("warning: failed to enqueue recapture for page %d: %v", page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type captureDetailResponse struct {
	ID                        int64     `json:"id"`
	PageID                    int64     `json:"page_id"`
	Source                    string    `json:"source"`
	RawURL                    string    `json:"raw_url"`
	Title                     *string   `json:"title"`
	ThumbnailPath             *string   `json:"thumbnail_path"`
	ThumbnailSizeBytes        *int32    `json:"thumbnail_size_bytes"`
	ThumbnailHash             *string   `json:"thumbnail_hash"`
	FaviconPath               *string   `json:"favicon_path"`
	FaviconSizeBytes          *int32    `json:"favicon_size_bytes"`
	FaviconHash               *string   `json:"favicon_hash"`
	ReaderText                *string   `json:"reader_text"`
	ReadabilityVersion        *string   `json:"readability_version"`
	ContentHash               string    `json:"content_hash"`
	AISummary                 *string   `json:"ai_summary"`
	AIModel                   *string   `json:"ai_model"`
	Language                  string    `json:"language"`
	HTMLCompressedSizeBytes   int32     `json:"html_compressed_size_bytes"`
	HTMLUncompressedSizeBytes int32     `json:"html_uncompressed_size_bytes"`
	CapturedAt                time.Time `json:"captured_at"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

// captureDetailResponseFromCapture maps every column captures actually
// has (GetCaptureByIDForUser is a `SELECT captures.*`) into the JSON
// shape GetCapture returns -- content_hash (the HTML archive's own
// sha256, not nullable) plus readability_version/thumbnail_size_bytes/
// thumbnail_hash/favicon_size_bytes/favicon_hash.
func captureDetailResponseFromCapture(c *db.Capture) captureDetailResponse {
	return captureDetailResponse{
		ID: c.ID, PageID: c.PageID, Source: c.Source, RawURL: c.RawUrl, Title: textOrNil(c.Title),
		ThumbnailPath: textOrNil(c.ThumbnailPath), ThumbnailSizeBytes: int4OrNil(c.ThumbnailSizeBytes), ThumbnailHash: textOrNil(c.ThumbnailHash),
		FaviconPath: textOrNil(c.FaviconPath), FaviconSizeBytes: int4OrNil(c.FaviconSizeBytes), FaviconHash: textOrNil(c.FaviconHash),
		ReaderText: textOrNil(c.ReaderText), ReadabilityVersion: textOrNil(c.ReadabilityVersion), ContentHash: c.ContentHash,
		AISummary: textOrNil(c.AiSummary), AIModel: textOrNil(c.AiModel),
		Language: c.Language, HTMLCompressedSizeBytes: c.HtmlCompressedSizeBytes, HTMLUncompressedSizeBytes: c.HtmlUncompressedSizeBytes,
		CapturedAt: c.CapturedAt.Time, CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time,
	}
}

// GET /api/captures/{id}: full capture detail including reader_text and
// AI summary -- the heavier fields GetPage's own capture-history list
// deliberately omits (see captureSummaryResponse).
func (s *Server) GetCapture(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid capture id")
		return
	}

	capture, err := s.Queries.GetCaptureByIDForUser(r.Context(), db.GetCaptureByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "capture not found")
		return
	}

	writeJSON(w, http.StatusOK, captureDetailResponseFromCapture(&capture))
}

// DELETE /api/captures/{id}: the policy is that no page is
// ever left with zero captures -- deleting a page's last remaining
// capture deletes the page itself, in the same transaction, rather than
// leaving an empty, un-browsable page behind. Ownership is re-verified
// by DeleteCapture's own USING-clause join (not just trusted from an
// earlier read), same as every other delete/update in this file.
func (s *Server) DeleteCapture(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid capture id")
		return
	}
	ctx := r.Context()

	// Read first, outside the transaction: DeleteCapture's own USING
	// join re-checks ownership at delete time regardless, but this read
	// is what gives us page_id to check afterward, and lets a
	// wrong/not-owned id 404 immediately without ever opening a
	// transaction for it.
	capture, err := s.Queries.GetCaptureByIDForUser(ctx, db.GetCaptureByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "capture not found")
		return
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		log.Printf("warning: failed to begin transaction deleting capture %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.Queries.WithTx(tx)

	rowsAffected, err := qtx.DeleteCapture(ctx, db.DeleteCaptureParams{ID: id, UserID: user.ID})
	if err != nil {
		log.Printf("warning: failed to delete capture %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rowsAffected == 0 {
		// Ownership/existence changed between the read above and here
		// (e.g. a concurrent delete) -- genuinely rare, but correct to
		// report as gone rather than silently succeeding.
		writeError(w, http.StatusNotFound, "capture not found")
		return
	}

	remaining, err := qtx.CountCapturesByPage(ctx, capture.PageID)
	if err != nil {
		log.Printf("warning: failed to count remaining captures for page %d: %v", capture.PageID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if remaining == 0 {
		if _, err := qtx.DeletePage(ctx, db.DeletePageParams{ID: capture.PageID, UserID: user.ID}); err != nil {
			log.Printf("warning: failed to delete now-capture-less page %d: %v", capture.PageID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	} else {
		// pages.favicon_path is a denormalized copy of whichever capture
		// last provided one (see SetPageFavicon's own doc comment) --
		// if the just-deleted capture was that source, the page would
		// otherwise be left pointing at a path no surviving capture
		// references anymore. Always recomputed from whatever's now
		// the page's actual latest capture (this is a no-op write when
		// the deleted capture wasn't the source in the first place, not
		// just when it was -- simpler and just as correct as detecting
		// which case this is first).
		latest, err := qtx.GetLatestCaptureByPage(ctx, capture.PageID)
		if err != nil {
			log.Printf("warning: failed to look up new latest capture for page %d: %v", capture.PageID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := qtx.SetPageFavicon(ctx, db.SetPageFaviconParams{FaviconPath: latest.FaviconPath, ID: capture.PageID}); err != nil {
			log.Printf("warning: failed to refresh favicon for page %d: %v", capture.PageID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("warning: failed to commit transaction deleting capture %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// regeneratedJobResponse is both regenerate endpoints' response shape --
// just enough for the dashboard to confirm the right job got reset,
// without re-fetching the whole capture (the reader view's own $effect
// re-fetches captures separately if it wants updated status).
type regeneratedJobResponse struct {
	JobID int64 `json:"job_id"`
}

// POST /api/captures/{id}/regenerate-summary: resets this capture's
// ai_jobs row back to pending regardless of its current status -- the
// ai.Runner's own polling loop (already running, no new processing logic
// needed here) picks it up and overwrites ai_summary/ai_model once it
// completes, same as it always does. 404s if readability itself never
// succeeded for this capture (no ai_jobs row exists yet), same status a
// bad/not-owned capture id gets; the dashboard doesn't need to tell those
// two apart, both just mean "nothing to regenerate here."
func (s *Server) RegenerateAISummary(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid capture id")
		return
	}

	jobID, err := s.Queries.RegenerateAIJobForCapture(r.Context(), db.RegenerateAIJobForCaptureParams{CaptureID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "no AI job to regenerate for this capture")
		return
	}

	writeJSON(w, http.StatusOK, regeneratedJobResponse{JobID: jobID})
}

// POST /api/captures/{id}/regenerate-readability: RegenerateAISummary's
// twin for readability_jobs -- a readability_jobs row always exists
// already (created at ingest time, unlike ai_jobs), so this 404s only for
// a genuinely bad/not-owned capture id, never "nothing to regenerate."
// Does NOT also re-queue this capture's AI job: today there's no extra state
// to track that decision by -- if the reader text changes, a stale AI summary
// is left exactly as stale as it was before this endpoint existed, and a
// separate, explicit regenerate-summary click is what actually refreshes it.
func (s *Server) RegenerateReadability(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid capture id")
		return
	}

	jobID, err := s.Queries.RegenerateReadabilityJobForCapture(r.Context(), db.RegenerateReadabilityJobForCaptureParams{CaptureID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "capture not found")
		return
	}

	writeJSON(w, http.StatusOK, regeneratedJobResponse{JobID: jobID})
}

type captureConfigResponse struct {
	ReadabilityVersion *string `json:"readability_version"`
	AIModel            *string `json:"ai_model"`
}

// GET /api/capture-config: this running agent's currently configured
// readability_version/ai_model -- what a regenerate would actually produce
// right now, for the dashboard to eventually compare against a capture's own
// already-stored readability_version/ai_model and decide whether to
// show/hide/disable its regenerate buttons. Empty string means "not
// configured" (or AI enrichment disabled entirely) and is reported as null,
// not "".
func (s *Server) GetCaptureConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, captureConfigResponse{
		ReadabilityVersion: stringOrNil(s.ReadabilityVersion),
		AIModel:            stringOrNil(s.AIModel),
	})
}

// acceptsZstd does an exact-token check against Accept-Encoding --
// deliberately stricter than chi's own middleware.Compress, which just
// does a substring Contains match (verified against its real source).
// That looseness is a reasonable tradeoff for chi's generic gzip/deflate
// negotiation, but this is the one place recueil hand-rolls its own
// Content-Encoding decision, so it can afford to be exact.
func acceptsZstd(r *http.Request) bool {
	for enc := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		if strings.TrimSpace(strings.ToLower(enc)) == "zstd" {
			return true
		}
	}
	return false
}

// GET /api/captures/{id}/html: streams the archived HTML. The HTML is
// already stored zstd-compressed on disk (internal/archive); if the
// client's own Accept-Encoding says it can handle zstd, this streams
// those bytes completely unmodified (Content-Encoding: zstd) rather than
// decompressing just to maybe recompress. Otherwise it streams the
// decompressed HTML and leans on the router's own middleware.Compress
// (whose allowed types now include text/html) to gzip it if the client
// asked for gzip instead -- verified against chi's real source that its
// WriteHeader steps aside the moment Content-Encoding is already set, so
// there's no risk of double-compressing the zstd path.
func (s *Server) GetCaptureHTML(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid capture id")
		return
	}

	capture, err := s.Queries.GetCaptureByIDForUser(r.Context(), db.GetCaptureByIDForUserParams{ID: id, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "capture not found")
		return
	}

	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Defense-in-depth, not the primary control: the extension's SingleFile
	// capture already runs with blockScripts: true (see
	// extension/src/capture-inject/bundle-entry.js), so archived HTML
	// shouldn't contain live scripts at all. But this is served
	// same-origin with the dashboard -- if anything ever did slip
	// through (a SingleFile edge case, a future config change), it would
	// otherwise run with access to the logged-in session's cookies and
	// could call the API as the user. Costs nothing to block outright.
	w.Header().Set("Content-Security-Policy", "script-src 'none'")

	if acceptsZstd(r) {
		reader, err := s.Store.OpenRaw(capture.HtmlPath)
		if err != nil {
			log.Printf("warning: failed to open raw html for capture %d: %v", id, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = reader.Close() }()

		w.Header().Set("Content-Encoding", "zstd")
		w.Header().Set("Content-Length", strconv.FormatInt(int64(capture.HtmlCompressedSizeBytes), 10))
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, reader); err != nil {
			log.Printf("warning: failed streaming raw html for capture %d: %v", id, err)
		}
		return
	}

	reader, err := s.Store.Open(capture.HtmlPath)
	if err != nil {
		log.Printf("warning: failed to open html for capture %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer func() { _ = reader.Close() }()

	w.Header().Set("Content-Length", strconv.FormatInt(int64(capture.HtmlUncompressedSizeBytes), 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("warning: failed streaming html for capture %d: %v", id, err)
	}
}

type patchCaptureLanguageRequest struct {
	Language string `json:"language"`
}

// PATCH /api/captures/{id}/language: manual language correction. An invalid
// text-search-config name surfaces as a real Postgres error from the UPDATE
// itself -- a regconfig cast performs a pg_ts_config catalog lookup -- so
// there's no need to pre-validate here. ListTextSearchConfigs is a separate
// concern (populating the dashboard's dropdown of valid values), not a
// prerequisite for this endpoint trusting Postgres's own validation.
func (s *Server) PatchCaptureLanguage(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid capture id")
		return
	}
	req, err := decodeJSON[patchCaptureLanguageRequest](r)
	if err != nil || req.Language == "" {
		writeError(w, http.StatusBadRequest, "language is required")
		return
	}

	capture, err := s.Queries.SetCaptureLanguage(r.Context(), db.SetCaptureLanguageParams{
		Language: req.Language, ID: id, UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "capture not found")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid language")
		return
	}

	writeJSON(w, http.StatusOK, captureDetailResponseFromCapture(&capture))
}

// GET /api/text-search-configs: the valid values for
// PATCH /api/captures/{id}/language's dashboard dropdown -- this
// specific running Postgres instance's own pg_ts_config catalog, not a
// hardcoded list (which configs are actually available depends on the
// Postgres version). A plain query against the raw pool, not a
// sqlc-generated one -- same reasoning internal/ingest's own
// languageConfigExists already documents: sqlc's schema analysis only
// knows about tables defined in our own migrations, not Postgres's
// built-in system catalogs, so a query referencing pg_ts_config doesn't
// fit its normal model.
func (s *Server) ListTextSearchConfigs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Pool.Query(r.Context(), "SELECT cfgname FROM pg_ts_config ORDER BY cfgname")
	if err != nil {
		log.Printf("warning: failed to list text search configs: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	configs := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			log.Printf("warning: failed to scan text search config: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		configs = append(configs, name)
	}
	if err := rows.Err(); err != nil {
		log.Printf("warning: failed iterating text search configs: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string][]string{"languages": configs})
}

type tagResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// GET /api/tags: the user's full tag vocabulary, for the tags management
// screen and for populating an "add tag" autocomplete.
func (s *Server) ListTags(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tags, err := s.Queries.ListTags(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list tags for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]tagResponse, 0, len(tags))
	for _, t := range tags {
		resp = append(resp, tagResponse{ID: t.ID, Name: t.Name, Slug: t.Slug})
	}
	writeJSON(w, http.StatusOK, map[string][]tagResponse{"tags": resp})
}

type addPageTagRequest struct {
	Name string `json:"name"`
}

// POST /api/pages/{id}/tags: gets-or-creates a tag by name (UpsertTag),
// then links it to the page with source "manual" -- the same source
// value a person applying a tag through the dashboard should carry,
// distinguishing it from the AI enrichment job's own tags.
//
// There's no slug field on this request -- it's the quick inline
// "add tag to page" flow, not the fuller tag-management screen -- so the
// slug is always auto-generated from name here. A name that can't
// transliterate to anything (see slug.Generate's own doc comment) is
// rejected with a 400 rather than silently stored empty; the same is
// true of a genuine collision.
func (s *Server) AddPageTag(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	req, err := decodeJSON[addPageTagRequest](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	candidateSlug, ok := resolveSlug(req.Name, nil)
	if !ok {
		writeError(w, http.StatusBadRequest, "could not derive a URL-friendly slug from that tag name")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: pageID, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	tag, err := s.Queries.UpsertTag(ctx, db.UpsertTagParams{UserID: user.ID, Name: req.Name, Slug: candidateSlug})
	if err != nil {
		log.Printf("warning: failed to upsert tag for user %d: %v", user.ID, err)
		writeError(w, http.StatusConflict, "a different tag already uses that URL")
		return
	}

	if err := s.Queries.AddPageTag(ctx, db.AddPageTagParams{PageID: page.ID, TagID: tag.ID, Source: "manual"}); err != nil {
		log.Printf("warning: failed to add tag %d to page %d: %v", tag.ID, page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, tagResponse{ID: tag.ID, Name: tag.Name, Slug: tag.Slug})
}

type renameTagRequest struct {
	Name string  `json:"name"`
	Slug *string `json:"slug"`
}

// PATCH /api/tags/{id}. Same resolveSlug reconciliation as
// RenameCollection: an explicit slug is validated and used as-is,
// otherwise one is re-derived from the new name. This is currently the
// only way to give a tag a custom slug after the fact -- AddPageTag's
// quick inline "add tag to page" flow always auto-generates one, never
// takes an override.
func (s *Server) RenameTag(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tag id")
		return
	}
	req, err := decodeJSON[renameTagRequest](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	candidateSlug, ok := resolveSlug(req.Name, req.Slug)
	if !ok {
		writeError(w, http.StatusBadRequest, "a valid slug is required (could not derive one automatically from that name)")
		return
	}

	tag, err := s.Queries.RenameTag(r.Context(), db.RenameTagParams{
		Name: req.Name, Slug: candidateSlug, ID: id, UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "tag not found")
			return
		}
		writeError(w, http.StatusConflict, "a tag with that name or slug already exists")
		return
	}

	writeJSON(w, http.StatusOK, tagResponse{ID: tag.ID, Name: tag.Name, Slug: tag.Slug})
}

// DELETE /api/tags/{id}: removes the tag entirely (cascading to
// page_tags), same shape as DeleteCollection -- not the same thing as
// RemovePageTag, which only unlinks the tag from one page.
func (s *Server) DeleteTag(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tag id")
		return
	}

	rowsAffected, err := s.Queries.DeleteTag(r.Context(), db.DeleteTagParams{ID: id, UserID: user.ID})
	if err != nil {
		log.Printf("warning: failed to delete tag %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rowsAffected == 0 {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/tags/{slug}/pages: pages carrying a given tag, same
// shape/ordering as ListCollectionPages. Keyed by slug, not id -- this
// is the endpoint backing the dashboard's browsable /tags/:slug URL (see
// TagDetail.svelte), so it needs to resolve the same identifier a person
// would actually have bookmarked or shared.
func (s *Server) ListTagPages(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tagSlug := chi.URLParam(r, "slug")
	ctx := r.Context()

	tag, err := s.Queries.GetTagBySlug(ctx, db.GetTagBySlugParams{Slug: tagSlug, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "tag not found")
		return
	}

	pages, err := s.Queries.ListTagPages(ctx, tag.ID)
	if err != nil {
		log.Printf("warning: failed to list pages for tag %d: %v", tag.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]pageResponse, 0, len(pages))
	for i := range pages {
		resp = append(resp, pageResponseFromPage(&pages[i]))
	}
	// Includes the tag itself, not just its pages -- the caller (the
	// TagDetail dashboard screen) needs the tag's name for its own
	// heading and has no other endpoint that would give it that
	// alongside the pages in one round trip. GetTagByID's own result is
	// already in hand from the existence check above, so this is free.
	writeJSON(w, http.StatusOK, map[string]any{
		"tag":   tagResponse{ID: tag.ID, Name: tag.Name, Slug: tag.Slug},
		"pages": resp,
	})
}

// DELETE /api/pages/{id}/tags/{tagId}
func (s *Server) RemovePageTag(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	tagID, err := strconv.ParseInt(chi.URLParam(r, "tagId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tag id")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: pageID, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	if err := s.Queries.RemovePageTag(ctx, db.RemovePageTagParams{PageID: page.ID, TagID: tagID}); err != nil {
		log.Printf("warning: failed to remove tag %d from page %d: %v", tagID, page.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// int8OrNil converts a pgtype.Int8 into the *int64 shape the dashboard's
// JSON responses use -- nil rather than 0 for a genuinely NULL
// parent_id (a top-level collection), same reasoning as textOrNil.
func int8OrNil(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

type collectionResponse struct {
	ID          int64     `json:"id"`
	ParentID    *int64    `json:"parent_id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	Description *string   `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func collectionResponseFromCollection(c *db.Collection) collectionResponse {
	return collectionResponse{
		ID:          c.ID,
		ParentID:    int8OrNil(c.ParentID),
		Name:        c.Name,
		Slug:        c.Slug,
		Description: textOrNil(c.Description),
		CreatedAt:   c.CreatedAt.Time,
		UpdatedAt:   c.UpdatedAt.Time,
	}
}

// GET /api/collections: flat list; the dashboard reconstructs the tree
// client-side from (id, parent_id), same as ListCollectionsByUser's own
// doc comment already explains.
func (s *Server) ListCollections(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rows, err := s.Queries.ListCollectionsByUser(r.Context(), user.ID)
	if err != nil {
		log.Printf("warning: failed to list collections for user %d: %v", user.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]collectionResponse, 0, len(rows))
	for i := range rows {
		resp = append(resp, collectionResponseFromCollection(&rows[i]))
	}
	writeJSON(w, http.StatusOK, map[string][]collectionResponse{"collections": resp})
}

type createCollectionRequest struct {
	Name        string  `json:"name"`
	ParentID    *int64  `json:"parent_id"`
	Slug        *string `json:"slug"`
	Description *string `json:"description"`
}

// POST /api/collections. When parent_id is given, it's verified to
// belong to this user before use -- collections.parent_id's own FK has
// no user_id check, so without this a request could nest a new
// collection under another user's collection id. A duplicate name or
// slug under the same parent (top-level or not) collides with one of the
// schema's four partial unique indexes and surfaces here as a 409.
//
// slug is optional: the dashboard's create form generates and shows a
// preview from name, only sending an explicit value once the person
// opens the (initially collapsed) slug field and edits it themselves.
// See resolveSlug for how the two are reconciled.
func (s *Server) CreateCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	req, err := decodeJSON[createCollectionRequest](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	candidateSlug, ok := resolveSlug(req.Name, req.Slug)
	if !ok {
		writeError(w, http.StatusBadRequest, "a valid slug is required (could not derive one automatically from that name)")
		return
	}
	ctx := r.Context()

	var parentID pgtype.Int8
	if req.ParentID != nil {
		if _, err := s.Queries.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: *req.ParentID, UserID: user.ID}); err != nil {
			writeError(w, http.StatusBadRequest, "parent collection not found")
			return
		}
		parentID = pgtype.Int8{Int64: *req.ParentID, Valid: true}
	}

	description := ""
	if req.Description != nil {
		description = *req.Description
	}

	collection, err := s.Queries.CreateCollection(ctx, db.CreateCollectionParams{
		UserID: user.ID, ParentID: parentID, Name: req.Name,
		Slug: candidateSlug, Description: textOrNull(description),
	})
	if err != nil {
		writeError(w, http.StatusConflict, "a collection with that name or slug already exists here")
		return
	}

	writeJSON(w, http.StatusCreated, collectionResponseFromCollection(&collection))
}

type renameCollectionRequest struct {
	Name        string  `json:"name"`
	Slug        *string `json:"slug"`
	Description *string `json:"description"`
}

// PATCH /api/collections/{id}. Takes the same three fields as create --
// name, optional slug override, optional description -- since the
// dashboard's edit form always submits all of them together in one
// round trip. Renaming re-derives the slug from the new name by default
// (see resolveSlug); a changed slug simply breaks any previously
// bookmarked deep link to this collection, which was a deliberate,
// discussed tradeoff, not an oversight -- there's no redirect/history
// mechanism here.
func (s *Server) RenameCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}
	req, err := decodeJSON[renameCollectionRequest](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	candidateSlug, ok := resolveSlug(req.Name, req.Slug)
	if !ok {
		writeError(w, http.StatusBadRequest, "a valid slug is required (could not derive one automatically from that name)")
		return
	}
	description := ""
	if req.Description != nil {
		description = *req.Description
	}

	collection, err := s.Queries.RenameCollection(r.Context(), db.RenameCollectionParams{
		Name: req.Name, Slug: candidateSlug, Description: textOrNull(description), ID: id, UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "collection not found")
			return
		}
		writeError(w, http.StatusConflict, "a collection with that name or slug already exists here")
		return
	}

	writeJSON(w, http.StatusOK, collectionResponseFromCollection(&collection))
}

// DELETE /api/collections/{id}: cascades to child collections and
// page_collections rows via the schema's own ON DELETE CASCADE chain.
func (s *Server) DeleteCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}

	rowsAffected, err := s.Queries.DeleteCollection(r.Context(), db.DeleteCollectionParams{ID: id, UserID: user.ID})
	if err != nil {
		log.Printf("warning: failed to delete collection %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if rowsAffected == 0 {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GET /api/collections/{id}/pages
func (s *Server) ListCollectionPages(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}
	ctx := r.Context()

	if _, err := s.Queries.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: id, UserID: user.ID}); err != nil {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}

	pages, err := s.Queries.ListCollectionPages(ctx, id)
	if err != nil {
		log.Printf("warning: failed to list pages for collection %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := make([]pageResponse, 0, len(pages))
	for i := range pages {
		resp = append(resp, pageResponseFromPage(&pages[i]))
	}
	writeJSON(w, http.StatusOK, map[string][]pageResponse{"pages": resp})
}

type addPageToCollectionRequest struct {
	CollectionID int64 `json:"collection_id"`
}

// POST /api/pages/{id}/collections
func (s *Server) AddPageToCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	req, err := decodeJSON[addPageToCollectionRequest](r)
	if err != nil || req.CollectionID == 0 {
		writeError(w, http.StatusBadRequest, "collection_id is required")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: pageID, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if _, err := s.Queries.GetCollectionByID(ctx, db.GetCollectionByIDParams{ID: req.CollectionID, UserID: user.ID}); err != nil {
		writeError(w, http.StatusNotFound, "collection not found")
		return
	}

	if err := s.Queries.AddPageToCollection(ctx, db.AddPageToCollectionParams{PageID: page.ID, CollectionID: req.CollectionID}); err != nil {
		log.Printf("warning: failed to add page %d to collection %d: %v", page.ID, req.CollectionID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/pages/{id}/collections/{collectionId}
func (s *Server) RemovePageFromCollection(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	pageID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	collectionID, err := strconv.ParseInt(chi.URLParam(r, "collectionId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}
	ctx := r.Context()

	page, err := s.Queries.GetPageByIDForUser(ctx, db.GetPageByIDForUserParams{ID: pageID, UserID: user.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}

	if err := s.Queries.RemovePageFromCollection(ctx, db.RemovePageFromCollectionParams{PageID: page.ID, CollectionID: collectionID}); err != nil {
		log.Printf("warning: failed to remove page %d from collection %d: %v", page.ID, collectionID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user *db.User) {
	raw, hash, err := auth.GenerateSessionToken()
	if err != nil {
		log.Printf("failed to generate session token: %v", err)
		return
	}
	_, err = s.Queries.CreateSession(r.Context(), db.CreateSessionParams{
		SessionHash: hash,
		UserID:      user.ID,
		UserAgent:   textOrNull(r.UserAgent()),
		ExpiresAt:   pgtype.Timestamptz{Time: auth.SessionExpiry(), Valid: true},
	})
	if err != nil {
		log.Printf("failed to create session: %v", err)
		return
	}
	auth.SetSessionCookie(w, raw, s.CookieSecure)
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(r.Body).Decode(&v)
	return v, err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
