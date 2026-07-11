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

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mfinelli/recueil/internal/auth"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/mirror"
)

type Server struct {
	Queries      *db.Queries
	Mirror       *mirror.Client
	Bootstrap    *auth.BootstrapTokenHolder
	CookieSecure bool
}

func NewServer(q *db.Queries, m *mirror.Client, bootstrap *auth.BootstrapTokenHolder, cookieSecure bool) *Server {
	return &Server{Queries: q, Mirror: m, Bootstrap: bootstrap, CookieSecure: cookieSecure}
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
	var createErr error
	err = s.Bootstrap.Use(req.BootstrapToken, func() error {
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			return err
		}
		user, createErr = s.Queries.CreateUser(ctx, db.CreateUserParams{
			Username:     req.Username,
			PasswordHash: hash,
			Role:         "admin",
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

	if err := s.Mirror.PushUser(ctx, user.ID, user.Username, user.PasswordHash); err != nil {
		// Doesn't roll back account creation (see internal/mirror's
		// PushUser doc comment). Dashboard login works immediately; device
		// auth for this user is broken until a resync runs.
		log.Printf("warning: failed to push credential mirror for new user %d: %v", user.ID, err)
	}

	s.startSession(w, r, user)
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

	user, err := s.Queries.CreateUser(ctx, db.CreateUserParams{
		Username:     req.Username,
		PasswordHash: hash,
		Role:         "member",
	})
	if err != nil {
		writeError(w, http.StatusConflict, "username already taken")
		return
	}

	if err := s.Mirror.PushUser(ctx, user.ID, user.Username, user.PasswordHash); err != nil {
		log.Printf("warning: failed to push credential mirror for new user %d: %v", user.ID, err)
	}

	s.startSession(w, r, user)
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

	s.startSession(w, r, user)
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

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user db.User) {
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
