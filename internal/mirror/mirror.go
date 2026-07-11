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
// service-secret-gated endpoints. Everything in this package is a one-way
// write in the same direction: the backend is the source of truth, D1 gets a
// copy, and nothing here ever reads Worker/D1-owned state back.
package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

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
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

// PushUser upserts a user's id/username/password_hash into the D1 mirror.
// Called on account creation (including the first-admin bootstrap flow) and
// on password change. Failure here doesn't roll back the Postgres write
// (account creation is not currently retried on mirror-push failure); the
// resync command planned in §14/§15 is the intended repair path for drift.
func (c *Client) PushUser(ctx context.Context, id int64, username, passwordHash string) error {
	body, err := json.Marshal(userMirrorPayload{ID: id, Username: username, PasswordHash: passwordHash})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/users/mirror", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", c.serviceSecret)

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
