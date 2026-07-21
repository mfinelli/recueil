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

// Package queueitems is the backend's client for the dashboard's Queue
// screen's two Worker endpoints (GET /internal/queue-items,
// POST /internal/queue-items/:id/retry), both gated by the backend<->Worker
// service secret. Same authenticated-as-the-backend-itself credential tier
// as internal/devices, internal/mirror, and internal/ingest.WorkerClient --
// gets its own package for the same reason internal/devices does (its own
// doc comment explains the general pattern): each service-secret-gated
// concern here has its own small client, not one shared "Worker API"
// grab-bag.
//
// The dashboard never talks to the Worker directly; internal/httpapi calls
// this package, which makes the outbound authenticated call and returns
// the result.
package queueitems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// userAgent identifies every request this package sends to the Worker as
// coming from recueil's own backend, not a browser -- see mirror.userAgent
// for why this matters (Browser Integrity Check bypass).
const userAgent = "recueil/1.0"

// ErrNotFound is returned by Retry when the Worker's POST responds 404 --
// either the item id doesn't exist, it doesn't belong to the given
// userID, or it's not currently in the 'failed' state (the Worker
// collapses all three into one 404 rather than distinguishing them; see
// terraform/index.js's handleRetryQueueItem doc comment for why no
// 409/410 split is worth making here, unlike the device-claim endpoint).
var ErrNotFound = errors.New("queueitems: item not found or not retryable")

// Item is one failed queue item, as returned by
// GET /internal/queue-items?status=failed. id is a client-generated UUID
// (queue_items.id is TEXT, not an integer PK, unlike tokens.id) --
// deliberately a string here, not int64. Doubles as this package's JSON
// wire type for internal/httpapi's own /api/queue-items response (same
// reasoning as devices.Token: nothing sensitive to strip, so a separate
// DTO isn't earning its keep).
type Item struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Status      string    `json:"status"`
	ManualRetry bool      `json:"manual_retry"`
	CreatedAt   time.Time `json:"created_at"`
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

type itemWirePayload struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Status      string `json:"status"`
	ManualRetry int    `json:"manual_retry"`
	CreatedAt   string `json:"created_at"`
}

// ListFailed lists every failed queue item belonging to userID, oldest
// first (matching the Worker's own ORDER BY created_at ASC).
func (c *Client) ListFailed(ctx context.Context, userID int64) ([]Item, error) {
	reqURL := fmt.Sprintf("%s/internal/queue-items?user_id=%d&status=failed", c.baseURL, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("queueitems: listing failed items: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("queueitems: listing failed items: status %d", resp.StatusCode)
	}

	var parsed struct {
		Items []itemWirePayload `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("queueitems: decoding failed-items response: %w", err)
	}

	items := make([]Item, 0, len(parsed.Items))
	for _, w := range parsed.Items {
		createdAt, err := parseD1NativeTimestamp(w.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("queueitems: parsing created_at for item %q: %w", w.ID, err)
		}
		items = append(items, Item{
			ID:          w.ID,
			URL:         w.URL,
			Status:      w.Status,
			ManualRetry: w.ManualRetry != 0,
			CreatedAt:   createdAt,
		})
	}
	return items, nil
}

// Retry flags one failed item for another device claim attempt, scoped by
// both itemID and userID -- the same belt-and-suspenders the Worker's own
// handler documents itself (see terraform/index.js's
// handleRetryQueueItem): a mismatched pair flags nothing rather than
// someone else's item. Returns ErrNotFound on the Worker's 404 (which
// also covers "not currently failed" -- see ErrNotFound's own doc
// comment).
func (c *Client) Retry(ctx context.Context, userID int64, itemID string) error {
	reqURL := fmt.Sprintf("%s/internal/queue-items/%s/retry?user_id=%d", c.baseURL, url.PathEscape(itemID), userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("queueitems: retrying item: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("queueitems: retrying item: status %d", resp.StatusCode)
	}
	return nil
}

// parseD1NativeTimestamp parses a timestamp in the exact format SQLite's
// (and therefore D1's) own CURRENT_TIMESTAMP default produces:
// "YYYY-MM-DD HH:MM:SS", always UTC, no 'T' separator, no offset/zone
// suffix. Same format, same reasoning, as internal/devices' own
// parseD1NativeTimestamp (queue_items.created_at is written the same way
// tokens.created_at is -- the Worker's own SQL default -- not by a
// device client the way internal/ingest's RFC-3339-parsing
// parseD1Timestamp is). Duplicated rather than shared: two
// three-line unexported helpers in unrelated packages isn't worth a new
// shared package to avoid.
func parseD1NativeTimestamp(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("unrecognized D1 timestamp format %q: %w", s, err)
	}
	return t, nil
}
