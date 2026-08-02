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
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/mfinelli/recueil/internal/db"
)

// apiTokenPrefix mirrors the human-recognizable prefix convention used for
// every other token in this system (rcl_sess_, rcl_pair_, rcl_live_,
// rcl_bootstrap_).
const apiTokenPrefix = "rcl_api_"

// GenerateAPIToken returns a random opaque token (shown to the user exactly
// once, at creation) and its SHA-256 hex hash (stored in api_tokens).
// Same shape and entropy as GenerateSessionToken -- reuses HashToken rather
// than introducing a second hashing scheme for what's structurally the same
// kind of credential.
func GenerateAPIToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = apiTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	hash = HashToken(raw)
	return raw, hash, nil
}

var ErrNoAPIToken = errors.New("no valid api token")

// RequireAPIToken is HTTP middleware that resolves an `Authorization:
// Bearer rcl_api_...` header against the DB, rejects the request with 401
// if missing/invalid, and otherwise attaches the CurrentUser to the request
// context via the same userContextKey RequireSession uses -- so handlers
// that only call auth.UserFromContext work unmodified regardless of which
// of the two middlewares authenticated the request. Structurally parallel
// to RequireSession, but there is no equivalent of SessionIDFromContext:
// nothing about api-token-authenticated requests needs to distinguish
// "this token" from the user's other ones the way session revocation does.
func RequireAPIToken(q *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, tokenID, err := resolveAPIToken(r, q)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Synchronous, unlike the D1 device-token fire-and-forget
			if err := q.TouchApiToken(r.Context(), tokenID); err != nil {
				// Best-effort, same as TouchSession -- a failed
				// activity-timestamp write shouldn't fail the request.
				_ = err
			}

			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func resolveAPIToken(r *http.Request, q *db.Queries) (db.User, int64, error) {
	authz := r.Header.Get("Authorization")
	raw, ok := strings.CutPrefix(authz, "Bearer ")
	if !ok || raw == "" {
		return db.User{}, 0, ErrNoAPIToken
	}

	row, err := q.GetApiTokenByHash(r.Context(), HashToken(raw))
	if err != nil {
		return db.User{}, 0, ErrNoAPIToken
	}
	return row.User, row.ApiToken.ID, nil
}
