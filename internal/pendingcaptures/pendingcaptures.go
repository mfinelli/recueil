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

// Package pendingcaptures is the backend's client for every
// service-secret-gated Worker endpoint that operates on D1's
// pending_captures table -- the short-lived rows describing a capture a
// device has finished uploading to R2 but the backend hasn't yet pulled in.
//
// One package per Worker resource, rather than per caller, which is why two
// quite different actors share it: `recueil agent` claims batches, marks
// them ingested and sweeps old ones, while the dashboard (via
// internal/httpapi) reads one user's rows to show the Queue screen's
// "awaiting ingestion" section.
//
// Deliberately not part of internal/mirror: mirror is a one-way
// backend-to-D1 push that explicitly never reads Worker/D1-owned state
// back. This client does the opposite -- it reads pending_captures, and
// even its "mark fetched" call is a read-then-acknowledge rather than a
// data mirror push -- so it doesn't belong in that package's stated scope.
package pendingcaptures

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// userAgent identifies every request this package sends to the Worker as
// coming from recueil's own backend, not a browser -- lets the Worker's
// Cloudflare zone bypass Browser Integrity Check for these calls
// specifically (see terraform's browser_integrity_check_bypass ruleset),
// which otherwise flags this kind of non-browser, automated polling
// traffic.
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
func (c *Client) ClaimPendingCaptures(ctx context.Context, limit int) ([]PendingCapture, error) {
	url := c.baseURL + "/internal/pending-captures?limit=" + strconv.Itoa(limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("pendingcaptures: building pending-captures request: %w", err)
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pendingcaptures: claiming pending captures: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pendingcaptures: claiming pending captures: status %d", resp.StatusCode)
	}

	var parsed claimPendingCapturesResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("pendingcaptures: decoding pending-captures response: %w", err)
	}
	return parsed.PendingCaptures, nil
}

// MarkFetched marks a pending_captures row as pulled and ingested -- called
// only after the corresponding Postgres write is durable and the R2 object has
// been deleted.
func (c *Client) MarkFetched(ctx context.Context, captureID string) error {
	url := c.baseURL + "/internal/pending-captures/" + captureID + "/fetched"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("pendingcaptures: building mark-fetched request: %w", err)
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pendingcaptures: marking capture %q fetched: %w", captureID, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pendingcaptures: marking capture %q fetched: status %d", captureID, resp.StatusCode)
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
func (c *Client) CleanupPendingCaptures(ctx context.Context) (int, error) {
	url := c.baseURL + "/internal/pending-captures/cleanup"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("pendingcaptures: building pending-captures cleanup request: %w", err)
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("pendingcaptures: cleaning up pending captures: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("pendingcaptures: cleaning up pending captures: status %d", resp.StatusCode)
	}

	var parsed struct {
		Deleted int `json:"deleted"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, fmt.Errorf("pendingcaptures: decoding pending-captures cleanup response: %w", err)
	}
	return parsed.Deleted, nil
}

// UserCapture is one row of ListForUser's response -- deliberately a
// narrower shape than PendingCapture, which carries R2 keys the dashboard
// has no business seeing and no use for.
//
// There is no status field because D1 has no status column for this table:
// FetchedByBackend plus ClaimedAt is the entire state, and the three
// reachable combinations are "waiting" (not fetched, unclaimed),
// "ingesting" (not fetched, claimed) and "ingested" (fetched). Notably
// absent is any failed state -- a row whose ingestion keeps failing is
// indistinguishable from one merely waiting its turn, which is the same
// reason the cleanup sweep refuses to delete either.
//
// Timestamps are real time.Time values, so this re-serializes to the
// dashboard as RFC 3339 rather than passing D1's own formats through. That
// matters more than it looks: claimed_at is written by CURRENT_TIMESTAMP,
// so it arrives as "YYYY-MM-DD HH:MM:SS" with no zone marker at all, and
// the browser's own Date parser reads that as *local* time -- every
// relative timestamp on the Queue screen would be silently wrong by the
// viewer's UTC offset. Same shape internal/queueitems.Item already uses,
// for the same reason.
type UserCapture struct {
	ID               string     `json:"id"`
	URL              string     `json:"url"`
	FetchedByBackend bool       `json:"fetched_by_backend"`
	ClaimedAt        *time.Time `json:"claimed_at"`
	CapturedAt       time.Time  `json:"captured_at"`
}

// userCaptureWire is the on-the-wire shape, before timestamp parsing.
// captured_at and claimed_at genuinely need different parsers: captured_at
// is supplied by the capturing device as RFC 3339, while claimed_at is
// D1's own CURRENT_TIMESTAMP. Two columns on one row, two formats -- worth
// being explicit about rather than hoping one parser covers both.
type userCaptureWire struct {
	ID               string `json:"id"`
	URL              string `json:"url"`
	FetchedByBackend int    `json:"fetched_by_backend"`
	ClaimedAt        string `json:"claimed_at"`
	CapturedAt       string `json:"captured_at"`
}

type listForUserResponse struct {
	PendingCaptures []userCaptureWire `json:"pending_captures"`
}

// parseD1NativeTimestamp parses a timestamp in the exact format SQLite's
// (and therefore D1's) own CURRENT_TIMESTAMP produces: "YYYY-MM-DD
// HH:MM:SS", always UTC, no 'T' separator, no offset suffix. Duplicated
// from internal/queueitems and internal/devices rather than shared, on the
// same reasoning they already record: a three-line unexported helper in a
// handful of packages isn't worth a new shared package to avoid.
func parseD1NativeTimestamp(s string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("unrecognized D1 timestamp format %q: %w", s, err)
	}
	return t, nil
}

// ListForUser returns one user's captures sitting between "a device
// finished uploading" and "the backend has ingested it", plus any ingested
// within the last 15 minutes so a row doesn't vanish from the dashboard the
// instant it moves on.
//
// A GET on the same path ClaimPendingCaptures POSTs to. The verb is the
// whole difference and it's a real one: the POST is the backend taking work
// across every user and it mutates claimed_at; this reads one user's rows
// and mutates nothing. Listing must never claim -- a person looking at a
// screen should not be able to starve the ingester.
func (c *Client) ListForUser(ctx context.Context, userID int64) ([]UserCapture, error) {
	url := c.baseURL + "/internal/pending-captures?user_id=" + strconv.FormatInt(userID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("pendingcaptures: building list request: %w", err)
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pendingcaptures: listing pending captures: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("pendingcaptures: listing pending captures: status %d", resp.StatusCode)
	}

	var parsed listForUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("pendingcaptures: decoding list response: %w", err)
	}

	out := make([]UserCapture, 0, len(parsed.PendingCaptures))
	for _, w := range parsed.PendingCaptures {
		capturedAt, err := parseD1Timestamp(w.CapturedAt)
		if err != nil {
			return nil, fmt.Errorf("pendingcaptures: capture %q captured_at: %w", w.ID, err)
		}

		// D1 serializes a NULL TEXT column as JSON null, which lands in a
		// plain string field as "". That's the one value treated as absent
		// rather than parsed -- an unclaimed capture is the normal case,
		// not a malformed row.
		var claimedAt *time.Time
		if w.ClaimedAt != "" {
			t, err := parseD1NativeTimestamp(w.ClaimedAt)
			if err != nil {
				return nil, fmt.Errorf("pendingcaptures: capture %q claimed_at: %w", w.ID, err)
			}
			claimedAt = &t
		}

		out = append(out, UserCapture{
			ID:  w.ID,
			URL: w.URL,
			// SQLite has no real boolean type; the column is an INTEGER
			// holding 0 or 1.
			FetchedByBackend: w.FetchedByBackend != 0,
			ClaimedAt:        claimedAt,
			CapturedAt:       capturedAt,
		})
	}
	return out, nil
}

// parseD1Timestamp parses the RFC 3339 timestamps a capturing device
// supplies (pending_captures.captured_at), as opposed to the ones D1 writes
// itself. Moved here alongside its only remaining caller's sibling; the
// copy in internal/ingest stays for that package's own use.
func parseD1Timestamp(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp format %q", s)
}
