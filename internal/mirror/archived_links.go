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

	"github.com/mfinelli/recueil/internal/pendinglinks"
)

// The link bookmark mirror's half of the D1 client: the same four operations
// as archived_pages.go has for pages -- the checkpoint, a batch upsert, the
// id list for deletion reconciliation, a batch delete -- against the Worker's
// /internal/archived-links routes. Shares one small request helper instead of
// repeating the request plumbing four times.
//
// A 404 from any of them means the Worker predates the links feature (the
// routes are never gated by the Worker's flag, so there is no other reason for
// one) and is reported as pendinglinks.ErrWorkerWithoutLinks, which Syncer
// treats as "nothing to do yet" rather than a failure: the Worker and backend
// are deployed separately.

// ArchivedLink is one row of the D1 link bookmark mirror -- a title+URL-only
// copy of a Postgres links row. RawURL is the link's own raw_url (what a
// bookmark should open), not its normalized one.
type ArchivedLink struct {
	LinkID    int64
	UserID    int64
	RawURL    string
	Title     *string
	UpdatedAt time.Time
}

type archivedLinkWirePayload struct {
	LinkID    int64   `json:"link_id"`
	UserID    int64   `json:"user_id"`
	RawURL    string  `json:"raw_url"`
	Title     *string `json:"title,omitempty"`
	UpdatedAt string  `json:"updated_at"`
}

// linksRequest sends one service-secret-gated request to a Worker
// /internal/archived-links route and decodes its JSON response into out (nil to
// ignore the body).
func (c *Client) linksRequest(ctx context.Context, method, path string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Service-Key", c.serviceSecret)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return pendinglinks.ErrWorkerWithoutLinks
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetArchivedLinksLastSync reads the sync checkpoint for the link mirror: the max
// updated_at currently in D1's archived_links, or nil if nothing has ever been
// pushed. Read from D1's data, as GetArchivedPagesLastSync is, so the
// checkpoint can't drift from what D1 actually contains.
func (c *Client) GetArchivedLinksLastSync(ctx context.Context) (*time.Time, error) {
	var parsed struct {
		LastSync *string `json:"last_sync"`
	}
	if err := c.linksRequest(ctx, http.MethodGet, "/internal/archived-links/last-sync", nil, &parsed); err != nil {
		return nil, fmt.Errorf("mirror: getting archived-links last-sync: %w", err)
	}
	if parsed.LastSync == nil {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, *parsed.LastSync)
	if err != nil {
		return nil, fmt.Errorf("mirror: parsing archived-links last-sync timestamp %q: %w", *parsed.LastSync, err)
	}
	return &t, nil
}

// MirrorArchivedLinks pushes a batch of links to the D1 mirror in one request,
// relying on the Worker's env.DB.batch() to apply the whole batch atomically
// (see Syncer.SyncOnce for why that is what makes the incremental sync safe). A
// no-op if links is empty.
func (c *Client) MirrorArchivedLinks(ctx context.Context, links []ArchivedLink) error {
	if len(links) == 0 {
		return nil
	}
	wire := make([]archivedLinkWirePayload, len(links))
	for i, l := range links {
		wire[i] = archivedLinkWirePayload{
			LinkID:    l.LinkID,
			UserID:    l.UserID,
			RawURL:    l.RawURL,
			Title:     l.Title,
			UpdatedAt: l.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	body := struct {
		Links []archivedLinkWirePayload `json:"links"`
	}{Links: wire}
	if err := c.linksRequest(ctx, http.MethodPost, "/internal/archived-links/mirror", body, nil); err != nil {
		return fmt.Errorf("mirror: pushing archived links: %w", err)
	}
	return nil
}

// ListArchivedLinkIDs lists every link_id currently in the D1 mirror -- the other
// half of deletion reconciliation, paired with the backend's own current set.
func (c *Client) ListArchivedLinkIDs(ctx context.Context) ([]int64, error) {
	var parsed struct {
		LinkIDs []int64 `json:"link_ids"`
	}
	if err := c.linksRequest(ctx, http.MethodGet, "/internal/archived-links/link-ids", nil, &parsed); err != nil {
		return nil, fmt.Errorf("mirror: listing archived link ids: %w", err)
	}
	return parsed.LinkIDs, nil
}

// DeleteArchivedLinks removes a batch of link_ids from the D1 mirror. A no-op if
// linkIDs is empty.
func (c *Client) DeleteArchivedLinks(ctx context.Context, linkIDs []int64) error {
	if len(linkIDs) == 0 {
		return nil
	}
	body := struct {
		LinkIDs []int64 `json:"link_ids"`
	}{LinkIDs: linkIDs}
	if err := c.linksRequest(ctx, http.MethodPost, "/internal/archived-links/delete", body, nil); err != nil {
		return fmt.Errorf("mirror: deleting archived links: %w", err)
	}
	return nil
}
