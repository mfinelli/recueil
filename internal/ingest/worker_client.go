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

package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// WorkerClient is the backend's client for the two service-secret-gated
// Worker endpoints that make ingestion polling possible:
// GET /internal/pending-captures and
// POST /internal/pending-captures/:id/fetched. Deliberately not part of
// internal/mirror: mirror is a one-way backend-to-D1 push and explicitly
// never reads Worker/D1-owned state back -- this client does the opposite
// (reads pending_captures, and its "mark fetched" call is itself a
// read-then-acknowledge, not a data mirror push), so it doesn't belong in
// that package's stated scope.

// userAgent identifies every request this package sends to the Worker as
// coming from recueil's own backend, not a browser -- lets the Worker's
// Cloudflare zone bypass Browser Integrity Check for these calls
// specifically (see terraform's browser_integrity_check_bypass ruleset),
// which otherwise flags this kind of non-browser, automated polling
// traffic.
const userAgent = "recueil/1.0"

type WorkerClient struct {
	baseURL       string
	serviceSecret string
	httpClient    *http.Client
}

func NewWorkerClient(baseURL, serviceSecret string) *WorkerClient {
	return &WorkerClient{
		baseURL:       baseURL,
		serviceSecret: serviceSecret,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

// PendingCapture mirrors the shape returned by
// POST /internal/pending-captures -- see terraform/worker/index.js's
// handleClaimPendingCaptures. QueueItemID is nil for a direct capture.
// R2KeyFavicon is nil whenever the extension didn't find (or upload) a
// favicon for this capture -- always optional, never a reason ingestion
// itself fails (see Ingester.captureFavicon).
type PendingCapture struct {
	ID           string  `json:"id"`
	UserID       int64   `json:"user_id"`
	QueueItemID  *string `json:"queue_item_id"`
	URL          string  `json:"url"`
	R2KeyHTML    string  `json:"r2_key_html"`
	R2KeyFavicon *string `json:"r2_key_favicon"`
	CapturedAt   string  `json:"captured_at"`
	ClaimedAt    string  `json:"claimed_at"`
	CreatedAt    string  `json:"created_at"`
}

type claimPendingCapturesResponse struct {
	PendingCaptures []PendingCapture `json:"pending_captures"`
}

// ClaimPendingCaptures atomically claims up to limit captures the backend
// hasn't yet pulled from R2, oldest first, and returns them.
//
// A claim, not a plain list, and therefore a POST: two agent processes
// polling at roughly the same time would otherwise both ingest the same row,
// and the second would silently write a duplicate capture. The downstream
// source_capture_id guard does not prevent that, because ingestion clears
// that column to NULL as its final step and Postgres treats NULLs as
// distinct in a unique index -- so by the time a slower agent inserts, there
// is nothing left to conflict with.
func (c *WorkerClient) ClaimPendingCaptures(ctx context.Context, limit int) ([]PendingCapture, error) {
	url := c.baseURL + "/internal/pending-captures?limit=" + strconv.Itoa(limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("ingest: building pending-captures request: %w", err)
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ingest: claiming pending captures: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ingest: claiming pending captures: status %d", resp.StatusCode)
	}

	var parsed claimPendingCapturesResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("ingest: decoding pending-captures response: %w", err)
	}
	return parsed.PendingCaptures, nil
}

// MarkFetched marks a pending_captures row as pulled and ingested -- called
// only after the corresponding Postgres write is durable and the R2 object has
// been deleted.
func (c *WorkerClient) MarkFetched(ctx context.Context, captureID string) error {
	url := c.baseURL + "/internal/pending-captures/" + captureID + "/fetched"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("ingest: building mark-fetched request: %w", err)
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ingest: marking capture %q fetched: %w", captureID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ingest: marking capture %q fetched: status %d", captureID, resp.StatusCode)
	}
	return nil
}

// CleanupPendingCaptures sweeps pending_captures rows the backend finished
// ingesting long enough ago that nothing will look at them again, and
// returns how many were deleted. Deployment-wide, not per-user: this is
// maintenance, not an operation on behalf of any particular device.
//
// Only successfully-ingested rows are ever removed -- a row the backend has
// not managed to ingest is kept indefinitely, whether it is merely waiting
// or failing repeatedly (this table has no status column that could tell
// those apart). See the Worker's handleCleanupPendingCaptures.
func (c *WorkerClient) CleanupPendingCaptures(ctx context.Context) (int, error) {
	url := c.baseURL + "/internal/pending-captures/cleanup"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("ingest: building pending-captures cleanup request: %w", err)
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("ingest: cleaning up pending captures: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("ingest: cleaning up pending captures: status %d", resp.StatusCode)
	}

	var parsed struct {
		Deleted int `json:"deleted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, fmt.Errorf("ingest: decoding pending-captures cleanup response: %w", err)
	}
	return parsed.Deleted, nil
}
