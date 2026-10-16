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

// Package pendinglinks is the backend's client for the service-secret-gated
// Worker endpoints that operate on D1's pending_links table: the short-lived
// rows describing a link a device has saved but the backend hasn't yet pulled
// in. The links counterpart of internal/pendingcaptures, and shaped like it
// -- an atomic claim, a fetched flag, and a sweep -- minus the per-user
// listing, which nothing needs yet.
package pendinglinks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// userAgent identifies every request this package sends to the Worker as
// coming from recueil's own backend, not a browser -- see
// internal/pendingcaptures for why that matters to the Worker's zone.
const userAgent = "recueil/1.0"

// ErrWorkerWithoutLinks means the Worker answered 404 to a pending-links
// route, i.e. it predates the links feature and doesn't have them. The Worker
// and backend are deployed separately (terraform apply versus a new image), so
// a backend upgraded first will see this until the Worker catches up; callers
// treat it as "nothing to do yet" rather than as a failure.
//
// The routes are deliberately never 404 for any other reason: the Worker
// leaves them ungated by its links flag, so with links off they simply return
// nothing.
var ErrWorkerWithoutLinks = errors.New("pendinglinks: the Worker has no pending-links routes (it predates the links feature)")

type Client struct {
	baseURL       string
	serviceSecret string
	httpClient    *http.Client
}

func NewClient(baseURL, serviceSecret string) *Client {
	return &Client{
		baseURL:       baseURL,
		serviceSecret: serviceSecret,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// PendingLink mirrors the shape returned by POST /internal/pending-links -- see
// terraform/worker/index.js's handleClaimPendingLinks.
//
// The favicon, when there is one, arrives one of two ways, never both:
// FaviconData holds it inline as standard base64, or R2KeyFavicon names the R2
// object a device uploaded to (the format is FaviconExt either way). All three
// are nil when the device saw no favicon. Title is nil when it saw none.
// QueueItemID is nil for a direct save.
type PendingLink struct {
	ID           string  `json:"id"`
	UserID       int64   `json:"user_id"`
	QueueItemID  *string `json:"queue_item_id"`
	URL          string  `json:"url"`
	Title        *string `json:"title"`
	FaviconExt   *string `json:"favicon_ext"`
	FaviconData  *string `json:"favicon_data"`
	R2KeyFavicon *string `json:"r2_key_favicon"`
	SavedAt      string  `json:"saved_at"`
	ClaimedAt    string  `json:"claimed_at"`
	CreatedAt    string  `json:"created_at"`
}

type claimPendingLinksResponse struct {
	PendingLinks []PendingLink `json:"pending_links"`
}

// ClaimPendingLinks atomically claims up to limit links the backend hasn't yet
// ingested and returns them oldest-SAVED first. That order is load-bearing: a
// link save is an upsert, so when the same URL was saved twice before a poll,
// processing them in saved order is what makes the later one win.
//
// A claim, not a plain list, and therefore a POST, for the same reason as
// pendingcaptures.Client.ClaimPendingCaptures: two agents polling together
// would otherwise both ingest the same row.
func (c *Client) ClaimPendingLinks(ctx context.Context, limit int) ([]PendingLink, error) {
	url := c.baseURL + "/internal/pending-links?limit=" + strconv.Itoa(limit)
	resp, err := c.post(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("pendinglinks: claiming pending links: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := statusError(resp); err != nil {
		return nil, fmt.Errorf("pendinglinks: claiming pending links: %w", err)
	}

	var parsed claimPendingLinksResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("pendinglinks: decoding pending-links response: %w", err)
	}
	return parsed.PendingLinks, nil
}

// MarkFetched marks a pending_links row as pulled and ingested -- called only
// after the link is durably in Postgres (and its favicon, if any, on disk). The
// Worker also drops the row's favicon payload at this point.
func (c *Client) MarkFetched(ctx context.Context, linkID string) error {
	url := c.baseURL + "/internal/pending-links/" + linkID + "/fetched"
	resp, err := c.post(ctx, url)
	if err != nil {
		return fmt.Errorf("pendinglinks: marking link %q fetched: %w", linkID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := statusError(resp); err != nil {
		return fmt.Errorf("pendinglinks: marking link %q fetched: %w", linkID, err)
	}
	return nil
}

// CleanupPendingLinks sweeps pending_links rows the backend finished ingesting
// long enough ago that nothing will look at them again, and returns how many
// were deleted. Deployment-wide, not per-user. Only ingested rows are ever
// removed; see the Worker's handleCleanupPendingLinks.
func (c *Client) CleanupPendingLinks(ctx context.Context) (int, error) {
	resp, err := c.post(ctx, c.baseURL+"/internal/pending-links/cleanup")
	if err != nil {
		return 0, fmt.Errorf("pendinglinks: cleaning up pending links: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := statusError(resp); err != nil {
		return 0, fmt.Errorf("pendinglinks: cleaning up pending links: %w", err)
	}

	var parsed struct {
		Deleted int `json:"deleted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, fmt.Errorf("pendinglinks: decoding pending-links cleanup response: %w", err)
	}
	return parsed.Deleted, nil
}

func (c *Client) post(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)
	return c.httpClient.Do(req)
}

// statusError is nil for any 2xx, ErrWorkerWithoutLinks for a 404, and a plain
// status error otherwise.
func statusError(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return ErrWorkerWithoutLinks
	default:
		return fmt.Errorf("status %d", resp.StatusCode)
	}
}
