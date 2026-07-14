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
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mfinelli/recueil/internal/db"
)

// SyncerParams are Syncer's dependencies, all required.
type SyncerParams struct {
	Queries *db.Queries
	Client  *Client
}

// Syncer keeps the D1 bookmark-list mirror (archived_pages) in step with
// Postgres's pages table -- schedule-based, not triggered on individual
// mutations: event-triggering would require every future code path that ever
// touches pages (a deletion endpoint, a re-tagging endpoint, whatever else)
// to remember to also push a D1 update. A schedule doesn't care how or where
// Postgres changed; it just asks "what's different now."
type Syncer struct {
	queries *db.Queries
	client  *Client
}

func NewSyncer(p SyncerParams) *Syncer {
	return &Syncer{queries: p.Queries, client: p.Client}
}

// SyncOnce runs one full sync cycle: an incremental upsert of whatever
// changed in Postgres since the last sync, followed by deletion
// reconciliation (removing from D1 whatever no longer exists in
// Postgres -- the only way a schedule-based sync can ever notice a
// deletion, since a deleted row was never "updated," it's just gone).
//
// The incremental checkpoint is read directly from D1's own data
// (MAX(updated_at) across archived_pages, via GetArchivedPagesLastSync) --
// deliberately not a separately-tracked watermark value stored anywhere
// on the backend. There's nothing here that can drift from what D1
// actually contains, since the checkpoint and the data are the same read.
//
// This is safe against a partial-batch failure without any
// application-level "stop at first failure" ordering logic, because the
// Worker's own env.DB.batch() call (behind MirrorArchivedPages) is
// atomic: either every page in the incremental batch lands, and D1's new
// MAX(updated_at) correctly reflects all of them, or none do, and the
// next cycle's "WHERE updated_at > lastSync" query picks up the exact
// same unchanged set again -- a safe, idempotent retry either way.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	if err := s.syncIncremental(ctx); err != nil {
		return fmt.Errorf("mirror: incremental sync: %w", err)
	}
	if err := s.reconcileDeletions(ctx); err != nil {
		return fmt.Errorf("mirror: deletion reconciliation: %w", err)
	}
	return nil
}

func (s *Syncer) syncIncremental(ctx context.Context) error {
	lastSync, err := s.client.GetArchivedPagesLastSync(ctx)
	if err != nil {
		return fmt.Errorf("getting last-sync checkpoint: %w", err)
	}

	since := pgtype.Timestamptz{Valid: false}
	if lastSync != nil {
		since = pgtype.Timestamptz{Time: *lastSync, Valid: true}
	}

	pages, err := s.queries.GetPagesUpdatedSince(ctx, since)
	if err != nil {
		return fmt.Errorf("querying pages updated since %v: %w", lastSync, err)
	}
	if len(pages) == 0 {
		return nil
	}

	entries := make([]ArchivedPage, len(pages))
	for i := range pages {
		p := pages[i]
		var title *string
		if p.Title.Valid {
			title = &p.Title.String
		}
		entries[i] = ArchivedPage{
			PageID:          p.ID,
			UserID:          p.UserID,
			RawURL:          p.NormalizedUrl,
			Title:           title,
			LatestCaptureAt: p.LatestCaptureAt.Time,
			UpdatedAt:       p.UpdatedAt.Time,
		}
	}

	if err := s.client.MirrorArchivedPages(ctx, entries); err != nil {
		return fmt.Errorf("pushing %d updated page(s): %w", len(entries), err)
	}
	return nil
}

func (s *Syncer) reconcileDeletions(ctx context.Context) error {
	d1IDs, err := s.client.ListArchivedPageIDs(ctx)
	if err != nil {
		return fmt.Errorf("listing D1 page ids: %w", err)
	}
	if len(d1IDs) == 0 {
		return nil
	}

	pgIDs, err := s.queries.GetAllPageIDs(ctx)
	if err != nil {
		return fmt.Errorf("querying all postgres page ids: %w", err)
	}

	toDelete := idsNotIn(d1IDs, pgIDs)
	if len(toDelete) == 0 {
		return nil
	}

	if err := s.client.DeleteArchivedPages(ctx, toDelete); err != nil {
		return fmt.Errorf("deleting %d stale D1 page(s): %w", len(toDelete), err)
	}
	return nil
}

// idsNotIn returns the ids in candidates that are not present in known --
// i.e. what's in D1 but no longer in Postgres, and therefore stale.
func idsNotIn(candidates, known []int64) []int64 {
	knownSet := make(map[int64]struct{}, len(known))
	for _, id := range known {
		knownSet[id] = struct{}{}
	}

	var result []int64
	for _, id := range candidates {
		if _, ok := knownSet[id]; !ok {
			result = append(result, id)
		}
	}
	return result
}
