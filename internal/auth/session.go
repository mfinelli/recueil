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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/mfinelli/recueil/internal/db"
)

const (
	cookieName = "recueil_session"
	// 30 days: a personal/family self-hosted tool, not a bank (long-lived
	// sessions are fine), and revocation (logout, or a future "sign out
	// everywhere") is handled by deleting the DB row.
	sessionTTL = 30 * 24 * time.Hour
)

// GenerateSessionToken returns a random opaque token (to put in the cookie)
// and its SHA-256 hex hash (to store in the DB). Same shape as the device
// bearer tokens: 256 bits of entropy, so the stored hash alone isn't
// enough to reconstruct a usable token.
func GenerateSessionToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = "rcl_sess_" + base64.RawURLEncoding.EncodeToString(buf)
	hash = HashToken(raw)
	return raw, hash, nil
}

func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func SetSessionCookie(w http.ResponseWriter, raw string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
}

func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func SessionExpiry() time.Time {
	return time.Now().Add(sessionTTL)
}

type contextKey int

const userContextKey contextKey = 0

func UserFromContext(ctx context.Context) (db.User, bool) {
	u, ok := ctx.Value(userContextKey).(db.User)
	return u, ok
}

var ErrNoSession = errors.New("no valid session")

// RequireSession is HTTP middleware that resolves the session cookie against
// the DB, rejects the request with 401 if invalid/expired/absent, and
// otherwise attaches the CurrentUser to the request context.
func RequireSession(q *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, sessionID, err := resolveSession(r, q)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Best-effort activity timestamp; failure here shouldn't fail the request.
			_ = q.TouchSession(r.Context(), sessionID)

			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin builds on RequireSession, additionally rejecting non-admins
// with 403. Used for the Manage Devices screen's admin-scope actions (§5).
func RequireAdmin(q *db.Queries) func(http.Handler) http.Handler {
	requireSession := RequireSession(q)
	return func(next http.Handler) http.Handler {
		return requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok || user.Role != "admin" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

func resolveSession(r *http.Request, q *db.Queries) (db.User, int64, error) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return db.User{}, 0, ErrNoSession
	}
	row, err := q.GetSessionByHash(r.Context(), HashToken(cookie.Value))
	if err != nil {
		return db.User{}, 0, ErrNoSession
	}
	return row.User, row.Session.ID, nil
}
