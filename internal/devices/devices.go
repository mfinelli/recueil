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

// Package devices is the backend's client for the Manage Devices dashboard
// screen's two Worker endpoints (GET/DELETE /internal/tokens), both
// gated by the backend↔Worker service secret. This is the same
// authenticated-as-the-backend-itself credential tier as internal/mirror
// (backend-to-D1 push) and internal/ingest.WorkerClient
// (service-secret-gated backend polling) -- distinct from
// internal/deviceapi, which authenticates as a *paired device's own*
// bearer token, not the backend. It gets its own package rather than
// living in mirror (a one-way push) or deviceapi (a different actor
// entirely) for the same reason those two are already separate: each
// service-secret-gated concern here has its own small client, not one
// shared "Worker API" grab-bag.
//
// The dashboard never talks to the Worker directly (it has no bearer
// token or service secret of its own); internal/httpapi calls this
// package, which makes the outbound authenticated call and returns the
// result.
package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// userAgent identifies every request this package sends to the Worker as
// coming from recueil's own backend, not a browser -- see mirror.userAgent
// for why this matters (Browser Integrity Check bypass).
const userAgent = "recueil/1.0"

// ErrNotFound is returned by RevokeToken when the Worker's DELETE
// responds 404 -- either the token id doesn't exist, or it exists but
// doesn't belong to the given userID. The Worker's own handler treats
// both cases identically (see terraform/index.js's handleRevokeToken),
// so this package does too; internal/httpapi maps this to a 404 for the
// dashboard rather than trying to distinguish "gone" from "never yours."
var ErrNotFound = errors.New("devices: token not found")

// Token is one paired device, as returned by GET /internal/tokens.
// Excludes token_hash -- the Worker's own SELECT never fetches it either
// (see terraform/index.js), so there's nothing to leak here even in
// principle. Doubles as this package's JSON wire type for internal/httpapi's
// own /api/devices response: there's no sensitive field to strip the way
// userResponse strips db.User's password_hash, so a separate DTO isn't
// earning its keep.
type Token struct {
	ID         int64      `json:"id"`
	DeviceName string     `json:"device_name"`
	DeviceType string     `json:"device_type"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type Client struct {
	baseURL       string
	serviceSecret string
	httpClient    *http.Client
}

func NewClient(baseURL, serviceSecret string) *Client {
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		serviceSecret: serviceSecret,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
	}
}

type tokenWirePayload struct {
	ID         int64   `json:"id"`
	DeviceName string  `json:"device_name"`
	DeviceType string  `json:"device_type"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

// ListTokens lists every device paired to userID, oldest-paired first
// (matching the Worker's own ORDER BY created_at ASC).
func (c *Client) ListTokens(ctx context.Context, userID int64) ([]Token, error) {
	url := fmt.Sprintf("%s/internal/tokens?user_id=%d", c.baseURL, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("devices: listing tokens: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("devices: listing tokens: status %d", resp.StatusCode)
	}

	var parsed struct {
		Tokens []tokenWirePayload `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("devices: decoding tokens response: %w", err)
	}

	tokens := make([]Token, 0, len(parsed.Tokens))
	for _, w := range parsed.Tokens {
		createdAt, err := parseD1NativeTimestamp(w.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("devices: parsing created_at for token %d: %w", w.ID, err)
		}
		var lastUsedAt *time.Time
		if w.LastUsedAt != nil {
			t, err := parseD1NativeTimestamp(*w.LastUsedAt)
			if err != nil {
				return nil, fmt.Errorf("devices: parsing last_used_at for token %d: %w", w.ID, err)
			}
			lastUsedAt = &t
		}
		tokens = append(tokens, Token{
			ID:         w.ID,
			DeviceName: w.DeviceName,
			DeviceType: w.DeviceType,
			CreatedAt:  createdAt,
			LastUsedAt: lastUsedAt,
		})
	}
	return tokens, nil
}

// RevokeToken deletes one device's bearer token, scoped by both tokenID
// and userID -- the same belt-and-suspenders the Worker's own handler
// documents itself (see terraform/index.js's handleRevokeToken): a
// mismatched pair deletes nothing rather than someone else's device.
// Returns ErrNotFound on the Worker's 404.
func (c *Client) RevokeToken(ctx context.Context, userID, tokenID int64) error {
	url := fmt.Sprintf("%s/internal/tokens/%d?user_id=%d", c.baseURL, tokenID, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("devices: revoking token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("devices: revoking token: status %d", resp.StatusCode)
	}
	return nil
}

// parseD1NativeTimestamp parses a timestamp in the exact format SQLite's
// (and therefore D1's) own CURRENT_TIMESTAMP default produces:
// "YYYY-MM-DD HH:MM:SS", always UTC, no 'T' separator, no offset/zone
// suffix. This is NOT the same helper as internal/ingest's parseD1Timestamp,
// which parses RFC 3339 strings a *device* generates client-side (e.g.
// captured_at) -- tokens.created_at and tokens.last_used_at are instead
// written by the Worker's own SQL (`CURRENT_TIMESTAMP`,
// `DEFAULT CURRENT_TIMESTAMP`; see terraform/index.js), which is
// SQLite-native, not RFC 3339, and would fail to parse with that helper's
// layouts.
func parseD1NativeTimestamp(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("unrecognized D1 timestamp format %q: %w", s, err)
	}
	return t, nil
}
