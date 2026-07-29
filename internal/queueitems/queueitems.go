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
// screen's Worker endpoints (GET /internal/queue-items,
// POST /internal/queue-items/:id/retry) plus the recapture action's
// POST /internal/queue-items (service-secret-gated, backend-initiated
// enqueue), all gated by the backend<->Worker service secret. Same
// authenticated-as-the-backend-itself credential tier as internal/devices,
// internal/mirror, and internal/ingest.WorkerClient -- gets its own package
// for the same reason internal/devices does: each service-secret-gated
// concern here has its own small client, not one shared "Worker API"
// grab-bag.
//
// The dashboard never talks to the Worker directly; internal/httpapi calls
// this package, which makes the outbound authenticated call and returns
// the result.
package queueitems

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// userAgent identifies every request this package sends to the Worker as
// coming from recueil's own backend, not a browser -- see mirror.userAgent
// for why this matters (Browser Integrity Check bypass).
const userAgent = "recueil/1.0"

// ErrNotFound is returned by Retry when the Worker's POST responds 404 --
// either the item id doesn't exist, it doesn't belong to the given
// userID, or it's not currently in the 'failed' state (the Worker
// collapses all three into one 404 rather than distinguishing them; see
// terraform/worker/index.js's handleRetryQueueItem doc comment for why no
// 409/410 split is worth making here, unlike the device-claim endpoint).
var ErrNotFound = errors.New("queueitems: item not found or not retryable")

// Item is one queue item, as returned by GET /internal/queue-items --
// every pending/claimed/failed item unconditionally, plus 'captured' ones
// from within the Worker's own recency window. id is a client-generated
// UUID (queue_items.id is TEXT, not an integer PK, unlike tokens.id) --
// deliberately a string here, not int64. Doubles as this package's JSON
// wire type for internal/httpapi's own /api/queue-items response (same
// reasoning as devices.Token: nothing sensitive to strip, so a separate
// DTO isn't earning its keep).
type Item struct {
	ID          string     `json:"id"`
	URL         string     `json:"url"`
	Status      string     `json:"status"`
	ManualRetry bool       `json:"manual_retry"`
	ClaimedAt   *time.Time `json:"claimed_at"`
	CreatedAt   time.Time  `json:"created_at"`
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
	ClaimedAt   string `json:"claimed_at"`
	CreatedAt   string `json:"created_at"`
}

// List lists every one of userID's queue items the Worker's
// handleListQueueItems currently returns; this client has no filtering
// logic of its own, it just passes the query through and parses the response.
func (c *Client) List(ctx context.Context, userID int64) ([]Item, error) {
	reqURL := fmt.Sprintf("%s/internal/queue-items?user_id=%d", c.baseURL, userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("queueitems: listing items: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("queueitems: listing items: status %d", resp.StatusCode)
	}

	var parsed struct {
		Items []itemWirePayload `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("queueitems: decoding items response: %w", err)
	}

	items := make([]Item, 0, len(parsed.Items))
	for _, w := range parsed.Items {
		createdAt, err := parseD1NativeTimestamp(w.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("queueitems: parsing created_at for item %q: %w", w.ID, err)
		}
		claimedAt, err := parseD1NativeTimestampOrNil(w.ClaimedAt)
		if err != nil {
			return nil, fmt.Errorf("queueitems: parsing claimed_at for item %q: %w", w.ID, err)
		}
		items = append(items, Item{
			ID:          w.ID,
			URL:         w.URL,
			Status:      w.Status,
			ManualRetry: w.ManualRetry != 0,
			ClaimedAt:   claimedAt,
			CreatedAt:   createdAt,
		})
	}
	return items, nil
}

// Retry flags one failed item for another device claim attempt, scoped by
// both itemID and userID -- the same belt-and-suspenders the Worker's own
// handler documents itself (see terraform/worker/index.js's
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

// enqueuePayload mirrors terraform/worker/index.js's handleServiceEnqueue
// request shape -- id/user_id/url, same three fields the device-facing
// POST /queue takes (see handleEnqueue), just without a specific device
// bearer token in the mix.
type enqueuePayload struct {
	ID     string `json:"id"`
	UserID int64  `json:"user_id"`
	URL    string `json:"url"`
}

// Enqueue adds a fresh queue_items row for rawURL, to be picked up by
// whichever device next polls GET /queue -- exactly the same queue a
// device's own share-sheet/extension enqueue feeds, just entered on the
// backend's behalf rather than a device's. This is the dashboard's
// "recapture" action: it never attempts a capture itself, it only asks a
// device to redo one.
//
// The id is generated here (a fresh UUID, same as internal/ingest's own
// server-generated source_capture_id), not by a device: there's no client
// on the other end of this call to have generated one. added_by_token_id is
// left NULL on the Worker's side (there's no device token to attribute this
// enqueue to), which the queue_items schema already allows for exactly this
// reason.
func (c *Client) Enqueue(ctx context.Context, userID int64, rawURL string) error {
	body, err := json.Marshal(enqueuePayload{ID: uuid.NewString(), UserID: userID, URL: rawURL})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/queue-items", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("queueitems: enqueueing recapture: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("queueitems: enqueueing recapture: status %d", resp.StatusCode)
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

// parseD1NativeTimestampOrNil is parseD1NativeTimestamp's twin for a
// column that's genuinely nullable in D1 (queue_items.claimed_at is NULL
// for anything that's never been claimed -- a 'pending' item, most
// obviously). Whether D1 serializes that as JSON null or an empty string,
// it lands the same way here: itemWirePayload.ClaimedAt is a plain
// (non-pointer) string, so either one unmarshals to "" -- that's the one
// value this treats as "absent" rather than attempting to parse it.
func parseD1NativeTimestampOrNil(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := parseD1NativeTimestamp(s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
