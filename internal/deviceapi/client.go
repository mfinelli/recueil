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

package deviceapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client is a paired device's authenticated access to the Worker --
// everything that needs the bearer token Pair (above) produced.
type Client struct {
	baseURL     string
	deviceToken string
	httpClient  *http.Client
}

func NewClient(baseURL, deviceToken string) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		deviceToken: deviceToken,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Enqueue submits one URL for later capture. id is client-generated: the
// caller is responsible for generating a fresh id per URL, so a retried call
// with the same id is safely idempotent (INSERT ... ON CONFLICT DO NOTHING)
// rather than double-enqueuing.
func (c *Client) Enqueue(ctx context.Context, id, url string) error {
	body, err := json.Marshal(struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}{ID: id, URL: url})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/queue", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.deviceToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("deviceapi: enqueue request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("deviceapi: enqueue failed: status %d", resp.StatusCode)
	}
	return nil
}
