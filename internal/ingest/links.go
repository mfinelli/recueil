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
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/linkicons"
	"github.com/mfinelli/recueil/internal/pendinglinks"
	"github.com/mfinelli/recueil/internal/urlnorm"
)

// defaultLinkBatchLimit bounds how many links one LinkIngester.RunOnce call
// processes -- the Worker's POST /internal/pending-links default, which is
// smaller than the captures one because a row can carry an inline favicon.
const defaultLinkBatchLimit = 25

// linkWorkerClient is the narrow slice of *pendinglinks.Client the
// LinkIngester calls, for the same reason Ingester depends on workerClient: its
// tests can swap in an in-memory fake and keep real Postgres, real disk and real
// URL normalization.
type linkWorkerClient interface {
	ClaimPendingLinks(ctx context.Context, limit int) ([]pendinglinks.PendingLink, error)
	MarkFetched(ctx context.Context, linkID string) error
}

// LinkParams are LinkIngester's dependencies, all required except Logger.
type LinkParams struct {
	Queries  *db.Queries
	Worker   linkWorkerClient
	R2       r2Client
	Store    *linkicons.Store
	Pipeline *urlnorm.Pipeline
	// Logger defaults to slog.Default() if nil.
	Logger *slog.Logger
}

// LinkIngester is the links counterpart of Ingester: it claims the links
// devices have saved, writes each one's favicon to the link store and upserts
// the link in Postgres. Much smaller than Ingester because a link is much
// smaller than a capture (there's no HTML to pull, multi-table transaction,
// or follow-on jobs) and for the same reason, it doesn't need any of
// Ingester's crash-recovery machinery. A link is upserted on (user_id,
// normalized_url), so a retry after a crash simply refreshes the same row;
// there is no append-only version to duplicate and so no source_capture_id
// equivalent.
type LinkIngester struct {
	queries  *db.Queries
	worker   linkWorkerClient
	r2       r2Client
	store    *linkicons.Store
	pipeline *urlnorm.Pipeline
	logger   *slog.Logger
}

func NewLinkIngester(p LinkParams) *LinkIngester {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &LinkIngester{
		queries:  p.Queries,
		worker:   p.Worker,
		r2:       p.R2,
		store:    p.Store,
		pipeline: p.Pipeline,
		logger:   logger,
	}
}

// The pending row's saved_at is used only by the Worker, to hand links out
// oldest-saved first; it's not written into the link. The row's created_at
// and updated_at are ingest time (NOW()), (the same as pages and captures)
// because updated_at is the D1 mirror's incremental-sync checkpoint and a
// device's clock has no business in it.

// RunOnce processes one bounded batch of pending links, with Ingester.RunOnce's
// contract: a failure on one link is logged and doesn't stop the rest (nothing
// marks it fetched until it succeeds, so it's simply claimed again after the
// Worker's stale-claim timeout), and an error is returned only when no batch
// could be had at all.
//
// A Worker that doesn't have the pending-links routes yet (it was deployed
// before this backend, or hasn't been redeployed since) is not an error: the
// two are upgraded separately, so it's logged and treated as an empty batch,
// and links start flowing as soon as the Worker catches up.
func (l *LinkIngester) RunOnce(ctx context.Context) error {
	pending, err := l.worker.ClaimPendingLinks(ctx, defaultLinkBatchLimit)
	if errors.Is(err, pendinglinks.ErrWorkerWithoutLinks) {
		l.logger.WarnContext(ctx, "ingest: the Worker has no pending-links routes yet -- deploy the updated Worker (terraform apply); links are ingested once it's there")
		return nil
	}
	if err != nil {
		return fmt.Errorf("ingest: claiming pending links: %w", err)
	}

	// In the order the Worker returned them: oldest saved first, which is what
	// makes the later of two saves of the same URL the one that sticks.
	for i := range pending {
		pl := &pending[i]
		if err := l.processOne(ctx, pl); err != nil {
			l.logger.ErrorContext(ctx, "ingest: failed to process link",
				"link_id", pl.ID, "url", pl.URL, "error", err)
		}
	}
	return nil
}

// faviconOutcome is what happened to the favicon a pending link declared.
type faviconOutcome int

const (
	// faviconNone: the device saw no favicon, so the link should have none.
	faviconNone faviconOutcome = iota
	// faviconStored: it was written to the link store.
	faviconStored
	// faviconFailed: one was declared but couldn't be stored. Not the same as
	// none -- see processOne.
	faviconFailed
)

// processOne ingests a single pending link: favicon to disk, then the upsert,
// then the favicon's R2 object (if it had one) is deleted, then the D1 fetched
// flag is set. The same ordering as Ingester.processOne's, and for the same
// reason: nothing is marked fetched until the data is durable, so a crash
// anywhere earlier just means the row is claimed and processed again.
func (l *LinkIngester) processOne(ctx context.Context, pl *pendinglinks.PendingLink) error {
	normalizedURL, err := l.pipeline.Normalize(ctx, pl.URL)
	if err != nil {
		return fmt.Errorf("normalizing url %q: %w", pl.URL, err)
	}

	favicon, outcome := l.storeFavicon(ctx, pl)

	title := textOrNull(derefString(pl.Title))

	switch outcome {
	case faviconFailed:
		// The device declared a favicon and we couldn't store it -- the R2
		// object was already gone on a retry, or the disk write failed. That
		// is a problem on our side, not the site's, so refresh everything else
		// but leave whatever favicon the link already has: clearing it would
		// turn a transient failure into losing data. (A save that declared
		// none, below, is the legitimate way a favicon goes away.)
		_, err = l.queries.UpsertLinkKeepingFavicon(ctx, db.UpsertLinkKeepingFaviconParams{
			UserID:        pl.UserID,
			RawUrl:        pl.URL,
			NormalizedUrl: normalizedURL,
			Title:         title,
		})
	default:
		// faviconStored sets the favicon columns; faviconNone clears them, so
		// a site that stopped serving a favicon stops showing the old one.
		present := outcome == faviconStored
		_, err = l.queries.UpsertLink(ctx, db.UpsertLinkParams{
			UserID:           pl.UserID,
			RawUrl:           pl.URL,
			NormalizedUrl:    normalizedURL,
			Title:            title,
			FaviconPath:      textOrNull(favicon.Path),
			FaviconSizeBytes: int32OrNull(favicon.SizeBytes, present),
			FaviconHash:      textOrNull(favicon.Hash),
		})
	}
	if err != nil {
		return fmt.Errorf("upserting link: %w", err)
	}

	// Best-effort, same as a capture's favicon object: it was never the
	// canonical copy, so one that fails to delete just sits in R2's temporary
	// buffer harmlessly, and isn't worth failing the cleanup over.
	if pl.R2KeyFavicon != nil && *pl.R2KeyFavicon != "" {
		if err := l.r2.Delete(ctx, *pl.R2KeyFavicon); err != nil {
			l.logger.WarnContext(ctx, "ingest: failed to delete link favicon R2 object (harmless)",
				"link_id", pl.ID, "r2_key_favicon", *pl.R2KeyFavicon, "error", err)
		}
	}

	if err := l.worker.MarkFetched(ctx, pl.ID); err != nil {
		return fmt.Errorf("marking link fetched: %w", err)
	}

	l.logger.InfoContext(ctx, "ingest: saved link", "link_id", pl.ID, "url", pl.URL)
	return nil
}

// storeFavicon gets the favicon a pending link declared onto disk. Every
// failure is logged and reported as faviconFailed rather than returned: a
// favicon is cosmetic, and never a reason to lose the link (the same principle
// as Ingester.captureFavicon). What a failed favicon does to the link's
// existing one is processOne's decision, not this function's.
func (l *LinkIngester) storeFavicon(ctx context.Context, pl *pendinglinks.PendingLink) (linkicons.Favicon, faviconOutcome) {
	if pl.FaviconExt == nil || *pl.FaviconExt == "" {
		return linkicons.Favicon{}, faviconNone
	}
	ext := *pl.FaviconExt

	var data []byte
	switch {
	case pl.FaviconData != nil:
		decoded, err := base64.StdEncoding.DecodeString(*pl.FaviconData)
		if err != nil {
			l.logger.WarnContext(ctx, "ingest: link favicon isn't valid base64, continuing without one",
				"link_id", pl.ID, "error", err)
			return linkicons.Favicon{}, faviconFailed
		}
		data = decoded

	case pl.R2KeyFavicon != nil && *pl.R2KeyFavicon != "":
		body, err := l.r2.Get(ctx, *pl.R2KeyFavicon)
		if err != nil {
			// Including the case where a previous attempt already pulled and
			// deleted this object and then crashed before marking the row
			// fetched: there is nothing left to store, and the link's existing
			// favicon is exactly the one that attempt stored.
			l.logger.WarnContext(ctx, "ingest: failed to pull link favicon from R2, continuing without one",
				"link_id", pl.ID, "r2_key_favicon", *pl.R2KeyFavicon, "error", err)
			return linkicons.Favicon{}, faviconFailed
		}
		read, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil {
			l.logger.WarnContext(ctx, "ingest: failed to read link favicon blob, continuing without one",
				"link_id", pl.ID, "r2_key_favicon", *pl.R2KeyFavicon, "error", err)
			return linkicons.Favicon{}, faviconFailed
		}
		data = read

	default:
		// A format with no bytes and no object: the Worker's CHECKs don't allow
		// it, so this is a row that didn't come from the Worker as written.
		l.logger.WarnContext(ctx, "ingest: link declares a favicon but carries neither data nor an R2 key, continuing without one",
			"link_id", pl.ID)
		return linkicons.Favicon{}, faviconFailed
	}

	favicon, err := l.store.Write(data, ext)
	if err != nil {
		l.logger.WarnContext(ctx, "ingest: failed to write link favicon, continuing without one",
			"link_id", pl.ID, "error", err)
		return linkicons.Favicon{}, faviconFailed
	}
	return favicon, faviconStored
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
