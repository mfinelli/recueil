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
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/devices"
	"github.com/mfinelli/recueil/internal/mirror"
)

type Server struct {
	Queries      *db.Queries
	Mirror       *mirror.Client
	Devices      *devices.Client
	Bootstrap    *auth.BootstrapTokenHolder
	CookieSecure bool
	PairingKey   auth.PairingKey
}

func NewServer(q *db.Queries, m *mirror.Client, d *devices.Client, bootstrap *auth.BootstrapTokenHolder, cookieSecure bool, pairingKey auth.PairingKey) *Server {
	return &Server{Queries: q, Mirror: m, Devices: d, Bootstrap: bootstrap, CookieSecure: cookieSecure, PairingKey: pairingKey}
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

// POST /api/auth/register: open registration.
func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
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

// resolveTargetUserID decides whose devices a request is allowed to act
// on: a member can only ever target themselves; an admin may target any user
// via ?user_id=, defaulting to themselves when omitted (so the same admin
// account viewing their own devices doesn't need the query param at
// all). ok is false for a member explicitly requesting someone else's
// user_id (403) or a malformed user_id value (400) -- the caller
// distinguishes the two via badRequest.
func resolveTargetUserID(r *http.Request, user db.User) (target int64, ok bool, badRequest bool) {
	raw := r.URL.Query().Get("user_id")
	if raw == "" {
		return user.ID, true, false
	}
	requested, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, true
	}
	if user.Role != "admin" && requested != user.ID {
		return 0, false, false
	}
	return requested, true, false
}

type deviceListResponse struct {
	Devices []devices.Token `json:"devices"`
}

// GET /api/devices: lists the target user's paired devices (see
// resolveTargetUserID for the member-vs-admin ?user_id= scoping).
func (s *Server) ListDevices(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	targetUserID, ok, badRequest := resolveTargetUserID(r, user)
	if badRequest {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "cannot view another user's devices")
		return
	}

	tokens, err := s.Devices.ListTokens(r.Context(), targetUserID)
	if err != nil {
		log.Printf("warning: failed to list devices for user %d: %v", targetUserID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, deviceListResponse{Devices: tokens})
}

// DELETE /api/devices/{id}: revokes one device (see resolveTargetUserID
// for the member-vs-admin ?user_id= scoping). This is not a live push --
// the device keeps working until its next request to the Worker.
func (s *Server) RevokeDevice(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	targetUserID, ok, badRequest := resolveTargetUserID(r, user)
	if badRequest {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "cannot revoke another user's device")
		return
	}

	tokenID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid device id")
		return
	}

	if err := s.Devices.RevokeToken(r.Context(), targetUserID, tokenID); err != nil {
		if errors.Is(err, devices.ErrNotFound) {
			writeError(w, http.StatusNotFound, "device not found")
			return
		}
		log.Printf("warning: failed to revoke device %d for user %d: %v", tokenID, targetUserID, err)
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
