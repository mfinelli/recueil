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
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/pendinglinks"
)

// SyncerParams are Syncer's dependencies; Queries and Client are required.
type SyncerParams struct {
	Queries *db.Queries
	Client  *Client

	// LinksEnabled is the links feature flag: with it on, SyncOnce also keeps
	// the link bookmark mirror (archived_links) in step with Postgres's links
	// table.
	LinksEnabled bool
	// Logger defaults to slog.Default() if nil.
	Logger *slog.Logger
}

// Syncer keeps the D1 bookmark-list mirror (archived_pages) in step with
// Postgres's pages table -- schedule-based, not triggered on individual
// mutations: event-triggering would require every future code path that ever
// touches pages (a deletion endpoint, a re-tagging endpoint, whatever else)
// to remember to also push a D1 update. A schedule doesn't care how or where
// Postgres changed; it just asks "what's different now."
type Syncer struct {
	queries      *db.Queries
	client       *Client
	linksEnabled bool
	logger       *slog.Logger
}

func NewSyncer(p SyncerParams) *Syncer {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Syncer{queries: p.Queries, client: p.Client, linksEnabled: p.LinksEnabled, logger: logger}
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
//
// With links enabled, the same cycle runs for links against their own D1 table
// (syncLinks). The two are independent -- a failure syncing one never stops the
// other -- and both errors are reported if both fail.
func (s *Syncer) SyncOnce(ctx context.Context) error {
	var errs []error
	if err := s.syncPages(ctx); err != nil {
		errs = append(errs, err)
	}
	if s.linksEnabled {
		if err := s.syncLinks(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Syncer) syncPages(ctx context.Context) error {
	if err := s.syncIncremental(ctx); err != nil {
		return fmt.Errorf("mirror: incremental sync: %w", err)
	}
	if err := s.reconcileDeletions(ctx); err != nil {
		return fmt.Errorf("mirror: deletion reconciliation: %w", err)
	}
	return nil
}

// syncLinks is the links version of one full cycle: the incremental upsert, then
// deletion reconciliation, exactly as for pages. A Worker that doesn't have the
// archived-links routes yet (it was deployed before this backend) is not a
// failure -- it's logged and skipped, and links start syncing as soon as the
// Worker catches up.
func (s *Syncer) syncLinks(ctx context.Context) error {
	err := s.syncLinksIncremental(ctx)
	if err == nil {
		err = s.reconcileLinkDeletions(ctx)
	}
	if errors.Is(err, pendinglinks.ErrWorkerWithoutLinks) {
		s.logger.WarnContext(ctx, "mirror: the Worker has no archived-links routes yet -- deploy the updated Worker (terraform apply); links are mirrored once it's there")
		return nil
	}
	if err != nil {
		return fmt.Errorf("mirror: link sync: %w", err)
	}
	return nil
}

func (s *Syncer) syncLinksIncremental(ctx context.Context) error {
	lastSync, err := s.client.GetArchivedLinksLastSync(ctx)
	if err != nil {
		return fmt.Errorf("getting last-sync checkpoint: %w", err)
	}

	since := pgtype.Timestamptz{Valid: false}
	if lastSync != nil {
		since = pgtype.Timestamptz{Time: *lastSync, Valid: true}
	}

	links, err := s.queries.GetLinksUpdatedSince(ctx, since)
	if err != nil {
		return fmt.Errorf("querying links updated since %v: %w", lastSync, err)
	}
	if len(links) == 0 {
		return nil
	}

	entries := make([]ArchivedLink, len(links))
	for i := range links {
		l := links[i]
		var title *string
		if l.Title.Valid {
			title = &l.Title.String
		}
		entries[i] = ArchivedLink{
			LinkID:    l.ID,
			UserID:    l.UserID,
			RawURL:    l.RawUrl,
			Title:     title,
			UpdatedAt: l.UpdatedAt.Time,
		}
	}

	if err := s.client.MirrorArchivedLinks(ctx, entries); err != nil {
		return fmt.Errorf("pushing %d updated link(s): %w", len(entries), err)
	}
	return nil
}

func (s *Syncer) reconcileLinkDeletions(ctx context.Context) error {
	d1IDs, err := s.client.ListArchivedLinkIDs(ctx)
	if err != nil {
		return fmt.Errorf("listing D1 link ids: %w", err)
	}
	if len(d1IDs) == 0 {
		return nil
	}

	pgIDs, err := s.queries.GetMirrorEligibleLinkIDs(ctx)
	if err != nil {
		return fmt.Errorf("querying mirror-eligible postgres link ids: %w", err)
	}

	toDelete := idsNotIn(d1IDs, pgIDs)
	if len(toDelete) == 0 {
		return nil
	}
	if err := s.client.DeleteArchivedLinks(ctx, toDelete); err != nil {
		return fmt.Errorf("deleting %d stale D1 link(s): %w", len(toDelete), err)
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

	pgIDs, err := s.queries.GetMirrorEligiblePageIDs(ctx)
	if err != nil {
		return fmt.Errorf("querying mirror-eligible postgres page ids: %w", err)
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
