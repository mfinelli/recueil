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

// Package mirror pushes backend-owned data outward to D1 via the Worker's
// service-secret-gated endpoints.
//
// PushUser is a pure one-way write: the backend is the source of truth, D1
// gets a copy, nothing here ever reads it back. The archived-pages
// bookmark-list mirror is NOT pure one-way, syncing a mirror correctly
// requires reading D1's own state back (the sync checkpoint, and the
// current page_id set for deletion reconciliation), not just writing to it.
package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// userAgent identifies every request this package sends to the Worker as
// coming from recueil's own backend, not a browser -- lets the Worker's
// Cloudflare zone bypass Browser Integrity Check for these calls
// specifically (see terraform's browser_integrity_check_bypass ruleset),
// which otherwise flags this kind of non-browser, automated traffic.
// Shared across mirror.go and archived_pages.go, both package mirror.
const userAgent = "recueil/1.0"

type Client struct {
	baseURL       string
	serviceSecret string
	httpClient    *http.Client
}

func NewClient(baseURL, serviceSecret string) *Client {
	return &Client{
		baseURL:       baseURL,
		serviceSecret: serviceSecret,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
}

type userMirrorPayload struct {
	ID               int64   `json:"id"`
	PairingTokenHash *string `json:"pairing_token_hash"`
}

// PushUser upserts a user's id/pairing_token_hash into the D1 mirror.
//
// Called on account creation, pairing-token regeneration, and
// pairing-token revocation. Pass a non-nil pairingTokenHash for
// creation/regeneration; pass nil for revocation (clears the mirrored
// hash to D1 NULL, so no submitted token can pair against this account
// until a regenerate).
//
// Failure here doesn't roll back the Postgres write (account creation,
// regenerate, and revoke are not currently retried on mirror-push
// failure); `recueil user resync` (cmd/user.go) is the intended repair
// path for drift, including the specific case of a stale mirror after
// restoring Postgres from a backup.
func (c *Client) PushUser(ctx context.Context, id int64, pairingTokenHash *string) error {
	body, err := json.Marshal(userMirrorPayload{ID: id, PairingTokenHash: pairingTokenHash})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/users/mirror", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mirror push request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mirror push failed: status %d", resp.StatusCode)
	}
	return nil
}
