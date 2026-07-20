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

// Package screenshot is the async screenshot/thumbnail job: render a
// capture's already-stored, already-inlined HTML through the shared
// headless-Chrome sidecar (internal/sidecar) and store the resulting
// fixed-viewport PNG alongside it -- a uniform size by design, not a
// full (variably-tall) page capture, so the dashboard can display
// thumbnails in a consistent grid rather than wildly different aspect
// ratios per capture. Built as a callable unit (RunOnce), same shape as
// internal/ingest, with no scheduler of its own -- cmd/agent.go's ticker
// drives it.
//
// Independent of internal/readability even though both run through the
// same sidecar and share its connection (internal/sidecar.Sidecar is
// constructed once by cmd/agent.go and handed to both): giving us
// independent failure modes, independent retry/backoff, no reason for a
// Readability.js upgrade's re-extraction to force a redundant
// re-screenshot. See internal/sidecar's own package doc for exactly
// what's shared between the two (the connection and render server) and
// what isn't (everything from here down: claiming, retry/backoff, and
// what actually happens once a tab is loaded).
//
// Claiming due jobs (ClaimDueScreenshotJobs) uses FOR UPDATE SKIP LOCKED
// and an atomic claim-and-mark-processing update, even though today's
// only deployment shape (cmd/agent.go's single ticker, one process) never
// actually needs it: it's what makes running more than one agent process
// safe later -- a horizontally-scaled or hosted deployment -- without
// this package needing to change at all when that day comes. A
// 'processing' job whose claimant crashed mid-render (and so never
// reached a terminal done/retry/failed call) becomes reclaimable again
// after claimStaleTimeout, the same stale-reclaim shape the D1 queue's
// own 15-minute claim timeout already uses. Bounded concurrency *within* one
// RunOnce call is separately plain Go concurrency (a semaphore-bounded worker
// pool over one already-claimed batch), not itself a database-level concern.
package screenshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/sidecar"
)

// defaultBatchLimit bounds how many jobs a single RunOnce call claims --
// same reasoning and same value as internal/ingest's own
// defaultBatchLimit: keep one poll cycle's work naturally bounded
// without needing its own separate cap.
const defaultBatchLimit = 50

// renderTimeout bounds a single job's page load + screenshot, generous
// enough for a large, fully-inlined (data-URI images/fonts) SingleFile
// capture to parse and paint, but short enough that one stuck tab can't
// silently hang a whole RunOnce cycle -- the semaphore-bounded worker
// pool still has to eventually free up a slot for the rest of the batch.
const renderTimeout = 60 * time.Second

// claimStaleTimeout is how long a job can sit 'processing' before
// ClaimDueScreenshotJobs treats it as abandoned (its claimant crashed
// mid-render) and lets another claim reclaim it. Matches the D1 queue's
// own 15-minute claim-visibility timeout rather than inventing a second,
// different number for what's conceptually the same "how long before we
// assume a claimant is gone" question.
const claimStaleTimeout = 15 * time.Minute

// viewportWidth and viewportHeight are the fixed dimensions every
// screenshot is taken at -- intentionally not a full-page (variable
// height) capture. Uniform thumbnails are the reason this job exists on
// the backend at all rather than in the extension: the extension's
// chrome.tabs.captureVisibleTab approach was rejected specifically because it
// depended on the user's current scroll/viewport state, producing
// inconsistent thumbnails. A full-page screenshot here would reintroduce that
// same inconsistency (heights all over the map) through a different door.
const (
	viewportWidth  = 1280
	viewportHeight = 800
)

// baseBackoff and maxBackoff bound the exponential retry delay computed
// by backoff -- see its own doc.
const (
	baseBackoff = 30 * time.Second
	maxBackoff  = 30 * time.Minute
)

// Params are Runner's dependencies, all required except Logger. Grouped
// into a struct for the same reason as internal/ingest.Params: enough
// fields that positional arguments would be a real mistake risk.
type Params struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
	Store   *archive.Store

	// Sidecar is the shared headless-Chrome connection + render server
	// (internal/sidecar) -- constructed once by cmd/agent.go and handed
	// to both this package and internal/readability, not owned by
	// either. Runner never closes it; that's cmd/agent.go's job, at
	// shutdown.
	Sidecar *sidecar.Sidecar

	// Concurrency bounds how many tabs are open against the sidecar at
	// once (config's screenshot_worker_concurrency). Values below 1 are
	// treated as 1.
	Concurrency int

	// MaxAttempts bounds the retry/backoff loop (config's
	// screenshot_max_attempts). Values below 1 are treated as 1 (no
	// retry: fail on first attempt).
	MaxAttempts int

	// Logger defaults to slog.Default() if nil.
	Logger *slog.Logger
}

// Runner has no OS resources of its own to close -- everything that
// does (the sidecar connection, the render server) lives in the shared
// *sidecar.Sidecar it holds a reference to, owned and closed by
// cmd/agent.go instead.
type Runner struct {
	pool        *pgxpool.Pool
	queries     *db.Queries
	store       *archive.Store
	sidecar     *sidecar.Sidecar
	concurrency int
	maxAttempts int
	logger      *slog.Logger
}

// New validates Params and returns a Runner. Never fails itself --
// unlike sidecar.New, there's no I/O here -- but still returns an error
// to leave room for future validation without an API-breaking signature
// change, and for symmetry with the rest of this codebase's constructors.
func New(p *Params) (*Runner, error) {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	concurrency := p.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	maxAttempts := p.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	return &Runner{
		pool:        p.Pool,
		queries:     p.Queries,
		store:       p.Store,
		sidecar:     p.Sidecar,
		concurrency: concurrency,
		maxAttempts: maxAttempts,
		logger:      logger,
	}, nil
}

// RunOnce claims one bounded batch of due screenshot_jobs rows and
// processes them with a Concurrency-bounded worker pool. A failure
// processing any single job is logged and does not stop the rest of the
// batch -- same "a capture's core validity is decoupled from any one
// enrichment step failing" principle internal/ingest's own RunOnce doc
// states, just applied to enrichment itself now rather than ingestion.
// RunOnce only returns an error when it can't even get a batch to work
// on.
func (r *Runner) RunOnce(ctx context.Context) error {
	jobs, err := r.queries.ClaimDueScreenshotJobs(ctx, db.ClaimDueScreenshotJobsParams{
		StaleBefore: pgtype.Timestamptz{Time: time.Now().Add(-claimStaleTimeout), Valid: true},
		RowLimit:    defaultBatchLimit,
	})
	if err != nil {
		return fmt.Errorf("screenshot: claiming due jobs: %w", err)
	}
	if len(jobs) == 0 {
		return nil
	}

	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(job db.ClaimDueScreenshotJobsRow) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.processOne(ctx, job); err != nil {
				r.logger.ErrorContext(ctx, "screenshot: failed to process job",
					"job_id", job.JobID, "capture_id", job.CaptureID, "error", err)
			}
		}(job)
	}
	wg.Wait()

	return nil
}

// processOne renders one capture's HTML, stores the resulting
// screenshot, and marks the job done -- or, on any failure, hands off to
// handleFailure for the retry/backoff bookkeeping. The returned error is
// purely for RunOnce's own logging; it's never propagated further than
// that.
func (r *Runner) processOne(ctx context.Context, job db.ClaimDueScreenshotJobsRow) error {
	rc, err := r.store.Open(job.HtmlPath)
	if err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("opening captured html: %w", err))
	}
	htmlData, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("reading captured html: %w", err))
	}

	shot, err := r.render(ctx, htmlData)
	if err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("rendering screenshot: %w", err))
	}

	sum := sha256.Sum256(shot)
	shotHash := hex.EncodeToString(sum[:])

	// Not compressed: png is already a compressed binary format, same
	// reasoning as ingest.go's favicon handling for ico/png -- so
	// writtenSize here is just len(shot), but going through WriteAsset's
	// own return value keeps one source of truth rather than this
	// package re-deriving it independently.
	relPath, writtenSize, err := r.store.WriteAsset(job.ContentHash, shotHash, "png", shot, false)
	if err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("writing screenshot to local archive: %w", err))
	}

	if err := r.commitDone(ctx, job, relPath, writtenSize, shotHash); err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("committing screenshot completion: %w", err))
	}

	r.logger.InfoContext(ctx, "screenshot: captured", "capture_id", job.CaptureID)
	return nil
}

// commitDone records the capture's new thumbnail_path/thumbnail_size_bytes/
// thumbnail_hash and marks the job done in one transaction -- either both
// land, or neither does, so a crash between the two can never leave a
// capture pointing at a thumbnail whose job still shows pending (which
// would make it eligible to be re-claimed and re-rendered for no reason)
// or a done job whose capture never actually got its thumbnail fields set.
func (r *Runner) commitDone(ctx context.Context, job db.ClaimDueScreenshotJobsRow, thumbnailPath string, thumbnailSizeBytes int64, thumbnailHash string) error {
	if thumbnailSizeBytes > math.MaxInt32 {
		// A multi-gigabyte thumbnail is a bug, not a real screenshot --
		// treat it as a render failure rather than overflowing the
		// int32 column.
		return fmt.Errorf("thumbnail size %d exceeds captures.thumbnail_size_bytes's int32 range", thumbnailSizeBytes)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := r.queries.WithTx(tx)

	if err := qtx.SetCaptureThumbnail(ctx, db.SetCaptureThumbnailParams{
		ID:                 job.CaptureID,
		ThumbnailPath:      pgtype.Text{String: thumbnailPath, Valid: true},
		ThumbnailSizeBytes: pgtype.Int4{Int32: int32(thumbnailSizeBytes), Valid: true},
		ThumbnailHash:      pgtype.Text{String: thumbnailHash, Valid: true},
	}); err != nil {
		return fmt.Errorf("setting capture thumbnail: %w", err)
	}
	if err := qtx.MarkScreenshotJobDone(ctx, job.JobID); err != nil {
		return fmt.Errorf("marking screenshot job done: %w", err)
	}

	return tx.Commit(ctx)
}

// handleFailure records a failed attempt: either scheduled for retry
// with a backoff-computed next_attempt_at, or, once MaxAttempts is
// exhausted, marked 'failed' permanently ("the failed row
// itself serves as the dead-letter queue"). Always returns cause,
// wrapped if the bookkeeping update itself also failed, so the caller
// has something concrete to log either way.
func (r *Runner) handleFailure(ctx context.Context, job db.ClaimDueScreenshotJobsRow, cause error) error {
	attempts := job.Attempts + 1

	if attempts >= int32(r.maxAttempts) {
		if err := r.queries.FailScreenshotJob(ctx, db.FailScreenshotJobParams{
			ID:       job.JobID,
			Attempts: attempts,
			Error:    pgtype.Text{String: cause.Error(), Valid: true},
		}); err != nil {
			return fmt.Errorf("%w (also failed marking job failed: %v)", cause, err)
		}
		return cause
	}

	if err := r.queries.RetryScreenshotJob(ctx, db.RetryScreenshotJobParams{
		ID:            job.JobID,
		Attempts:      attempts,
		NextAttemptAt: pgtype.Timestamptz{Time: time.Now().Add(backoff(attempts)), Valid: true},
		Error:         pgtype.Text{String: cause.Error(), Valid: true},
	}); err != nil {
		return fmt.Errorf("%w (also failed scheduling retry: %v)", cause, err)
	}
	return cause
}

// backoff returns the delay before the next attempt: baseBackoff doubled
// per attempt, capped at maxBackoff so a sidecar that's down for a while
// doesn't push a job arbitrarily far into the future -- it'll still be
// retried at least every maxBackoff.
func backoff(attempts int32) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 10 {
		// Avoid a pathological shift amount if MaxAttempts is ever
		// configured much higher than the original "e.g. 3" --
		// 2^10 * 30s is already ~8.5 hours, well past maxBackoff.
		return maxBackoff
	}
	d := baseBackoff * time.Duration(int64(1)<<uint(shift))
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// render serves htmlData to the sidecar and returns a fixed-viewportWidth
// x viewportHeight PNG screenshot of it -- CaptureScreenshot (the current
// viewport only), not FullScreenshot (the entire scrollable page): see
// the package doc for why uniform dimensions matter here.
func (r *Runner) render(ctx context.Context, htmlData []byte) ([]byte, error) {
	tabCtx, url, cleanup := r.sidecar.NewTab(htmlData, renderTimeout)
	defer cleanup()

	var buf []byte
	err := chromedp.Run(tabCtx,
		chromedp.EmulateViewport(viewportWidth, viewportHeight),
		chromedp.Navigate(url),
		chromedp.CaptureScreenshot(&buf),
	)
	if err != nil {
		return nil, err
	}

	return buf, nil
}
