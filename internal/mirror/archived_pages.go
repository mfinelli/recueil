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

package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ArchivedPage is one row of the D1 bookmark-list mirror -- a lightweight,
// title+URL-only copy of a Postgres pages row.
type ArchivedPage struct {
	PageID          int64
	UserID          int64
	RawURL          string
	Title           *string
	LatestCaptureAt time.Time
	UpdatedAt       time.Time
}

type archivedPageWirePayload struct {
	PageID          int64   `json:"page_id"`
	UserID          int64   `json:"user_id"`
	RawURL          string  `json:"raw_url"`
	Title           *string `json:"title,omitempty"`
	LatestCaptureAt string  `json:"latest_capture_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// GetArchivedPagesLastSync reads the sync checkpoint for the bookmark-list
// mirror: the max updated_at currently in D1's archived_pages, or nil if
// nothing has ever been pushed. See Syncer.SyncOnce for why this is read
// directly from D1's own data rather than a separately-tracked value
// anywhere on the backend side.
func (c *Client) GetArchivedPagesLastSync(ctx context.Context) (*time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/internal/archived-pages/last-sync", http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mirror: getting archived-pages last-sync: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mirror: getting archived-pages last-sync: status %d", resp.StatusCode)
	}

	var parsed struct {
		LastSync *string `json:"last_sync"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("mirror: decoding last-sync response: %w", err)
	}
	if parsed.LastSync == nil {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, *parsed.LastSync)
	if err != nil {
		return nil, fmt.Errorf("mirror: parsing last-sync timestamp %q: %w", *parsed.LastSync, err)
	}
	return &t, nil
}

// MirrorArchivedPages pushes a batch of pages to the D1 mirror in one
// request, relying on the Worker's own env.DB.batch() to apply the whole
// batch atomically (all rows land, or none do -- see Syncer.SyncOnce for
// why this is what makes the incremental sync safe without any
// application-level "stop at first failure" bookkeeping on this side).
// A no-op if pages is empty.
func (c *Client) MirrorArchivedPages(ctx context.Context, pages []ArchivedPage) error {
	if len(pages) == 0 {
		return nil
	}

	wire := make([]archivedPageWirePayload, len(pages))
	for i, p := range pages {
		wire[i] = archivedPageWirePayload{
			PageID:          p.PageID,
			UserID:          p.UserID,
			RawURL:          p.RawURL,
			Title:           p.Title,
			LatestCaptureAt: p.LatestCaptureAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt:       p.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	body, err := json.Marshal(struct {
		Pages []archivedPageWirePayload `json:"pages"`
	}{Pages: wire})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/internal/archived-pages/mirror", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mirror: pushing archived pages: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mirror: pushing archived pages: status %d", resp.StatusCode)
	}
	return nil
}

// ListArchivedPageIDs lists every page_id currently in the D1 mirror --
// the other half of deletion reconciliation, paired with the backend's
// own current Postgres page_id set (see Syncer.SyncOnce).
func (c *Client) ListArchivedPageIDs(ctx context.Context) ([]int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/internal/archived-pages/page-ids", http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mirror: listing archived page ids: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mirror: listing archived page ids: status %d", resp.StatusCode)
	}

	var parsed struct {
		PageIDs []int64 `json:"page_ids"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("mirror: decoding page-ids response: %w", err)
	}
	return parsed.PageIDs, nil
}

// DeleteArchivedPages removes a batch of page_ids from the D1 mirror. A
// no-op if pageIDs is empty.
func (c *Client) DeleteArchivedPages(ctx context.Context, pageIDs []int64) error {
	if len(pageIDs) == 0 {
		return nil
	}

	body, err := json.Marshal(struct {
		PageIDs []int64 `json:"page_ids"`
	}{PageIDs: pageIDs})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/internal/archived-pages/delete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mirror: deleting archived pages: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("mirror: deleting archived pages: status %d", resp.StatusCode)
	}
	return nil
}
