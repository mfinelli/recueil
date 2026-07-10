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
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

var ErrInvalidBootstrapToken = errors.New("invalid or expired bootstrap token")

// Valid for 1 hour. Consumed only on successful admin creation, not
// merely on validation, so a failed CreateUser (username race, DB hiccup)
// can be retried with the same token instead of requiring a restart.
const bootstrapTokenTTL = 1 * time.Hour

// BootstrapTokenHolder holds the first-admin bootstrap token entirely in
// memory. A restart before the token is used simply generates a new one;
// there's nothing left to go stale.
//
// Assumes exactly one backend process (a second replica would hold its own
// independent token, invisible to the first).
type BootstrapTokenHolder struct {
	mu        sync.Mutex
	token     string
	expiresAt time.Time
	consumed  bool
}

// NewBootstrapTokenHolder generates a new token and returns the holder plus
// the raw value to print to the backend's logs. Call once at startup when
// `users` is empty.
func NewBootstrapTokenHolder() (*BootstrapTokenHolder, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, "", err
	}
	raw := "rcl_bootstrap_" + base64.RawURLEncoding.EncodeToString(buf)
	return &BootstrapTokenHolder{
		token:     raw,
		expiresAt: time.Now().Add(bootstrapTokenTTL),
	}, raw, nil
}

// Use validates raw against the held token and, only if valid, runs fn
// (expected to create the admin user) while holding the lock (consuming
// the token only on fn's success), so a failed create can be retried with
// the same token rather than requiring a restart.
func (b *BootstrapTokenHolder) Use(raw string, fn func() error) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.consumed || time.Now().After(b.expiresAt) {
		return ErrInvalidBootstrapToken
	}
	if subtle.ConstantTimeCompare([]byte(raw), []byte(b.token)) != 1 {
		return ErrInvalidBootstrapToken
	}
	if err := fn(); err != nil {
		return err
	}
	b.consumed = true
	return nil
}
