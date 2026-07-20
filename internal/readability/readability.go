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

// Package readability is the async reader-text extraction job: render a
// capture's already-stored, already-inlined HTML through the shared
// headless-Chrome sidecar (internal/sidecar), inject the vendored
// Readability.js into the loaded page, and run it against the real DOM
// Chromium just rendered -- the actual upstream Readability.js
// library, in a real browser, just no longer in the original capturing
// tab. Built as a callable unit (RunOnce), same shape as
// internal/screenshot and internal/ingest, with no scheduler of its own
// -- cmd/agent.go's ticker drives it.
//
// Independent of internal/screenshot even though both run through the
// same sidecar and share its connection (internal/sidecar.Sidecar is
// constructed once by cmd/agent.go and handed to both): independent
// failure modes, independent retry/backoff, no reason for a
// Readability.js upgrade's re-extraction to force a redundant
// re-screenshot. See internal/sidecar's own package doc for exactly
// what's shared between the two and what isn't.
//
// Claiming due jobs, retry/backoff, and the atomic FOR UPDATE SKIP
// LOCKED claim-and-mark-processing shape are all identical in spirit to
// internal/screenshot's own -- see its package doc for the fuller
// reasoning, not repeated here.
package readability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
// same value and reasoning as internal/screenshot's own.
const defaultBatchLimit = 50

// renderTimeout bounds a single job's page load + injection + parse.
// Same value as internal/screenshot's own renderTimeout -- Readability's
// own parse() is synchronous DOM-walking, not meaningfully slower than a
// screenshot's paint for the same already-static, already-inlined page.
const renderTimeout = 60 * time.Second

// claimStaleTimeout is how long a job can sit 'processing' before
// ClaimDueReadabilityJobs treats it as abandoned and lets another claim
// reclaim it. Same value and reasoning as internal/screenshot's own.
const claimStaleTimeout = 15 * time.Minute

// baseBackoff and maxBackoff bound the exponential retry delay computed
// by backoff -- see its own doc. Same values as internal/screenshot's own.
const (
	baseBackoff = 30 * time.Second
	maxBackoff  = 30 * time.Minute
)

// parseScript runs after Source has already been injected (so the global
// Readability constructor exists) -- cloning the document first is
// Readability's own documented recommendation, since parse() mutates the
// DOM it's given destructively; that DOM is discarded along with the tab
// either way, but cloning costs nothing and matches upstream's own usage
// example rather than deviating from it for no reason.
const parseScript = `(() => {
  const article = new Readability(document.cloneNode(true)).parse();
  return article;
})()`

// Params are Runner's dependencies, all required except Logger.
type Params struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
	Store   *archive.Store

	// Sidecar is the shared headless-Chrome connection + render server
	// (internal/sidecar) -- constructed once by cmd/agent.go and handed
	// to both this package and internal/screenshot, not owned by
	// either. Runner never closes it; that's cmd/agent.go's job, at
	// shutdown.
	Sidecar *sidecar.Sidecar

	// Source is the raw Readability.js source injected into every loaded
	// page.
	Source string

	// Version identifies which vendored Readability.js produced a given
	// extraction (captures.readability_version). Empty outside of
	// `make`-built binaries, in which case readability_version is
	// simply stored as NULL rather than an empty string.
	Version string

	// Concurrency bounds how many tabs are open against the sidecar at
	// once (config's readability_worker_concurrency). Values below 1
	// are treated as 1.
	Concurrency int

	// MaxAttempts bounds the retry/backoff loop (config's
	// readability_max_attempts). Values below 1 are treated as 1 (no
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
	source      string
	version     string
	concurrency int
	maxAttempts int
	logger      *slog.Logger
}

// article is Readability.js's parse() output, decoded back into Go.
// Every field is a plain JSON-safe string/number, so this needs no
// custom unmarshaling -- but note parse() itself can return null (a page
// it judges isn't extractable), which is exactly why callers bind into
// a *article, not a bare article: JSON null correctly leaves the pointer
// nil, letting render tell "no article" apart from "something went
// wrong decoding a real one."
type article struct {
	Title         string `json:"title"`
	Byline        string `json:"byline"`
	Dir           string `json:"dir"`
	Lang          string `json:"lang"`
	Content       string `json:"content"`
	TextContent   string `json:"textContent"`
	Length        int    `json:"length"`
	Excerpt       string `json:"excerpt"`
	SiteName      string `json:"siteName"`
	PublishedTime string `json:"publishedTime"`
}

// New validates Params and returns a Runner. Never fails itself -- see
// internal/screenshot.New's own doc for why it still returns an error.
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
		source:      p.Source,
		version:     p.Version,
		concurrency: concurrency,
		maxAttempts: maxAttempts,
		logger:      logger,
	}, nil
}

// RunOnce claims one bounded batch of due readability_jobs rows and
// processes them with a Concurrency-bounded worker pool. See
// internal/screenshot.Runner.RunOnce's own doc -- identical shape and
// reasoning, just against a different table.
func (r *Runner) RunOnce(ctx context.Context) error {
	jobs, err := r.queries.ClaimDueReadabilityJobs(ctx, db.ClaimDueReadabilityJobsParams{
		StaleBefore: pgtype.Timestamptz{Time: time.Now().Add(-claimStaleTimeout), Valid: true},
		RowLimit:    defaultBatchLimit,
	})
	if err != nil {
		return fmt.Errorf("readability: claiming due jobs: %w", err)
	}
	if len(jobs) == 0 {
		return nil
	}

	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(job db.ClaimDueReadabilityJobsRow) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := r.processOne(ctx, job); err != nil {
				r.logger.ErrorContext(ctx, "readability: failed to process job",
					"job_id", job.JobID, "capture_id", job.CaptureID, "error", err)
			}
		}(job)
	}
	wg.Wait()

	return nil
}

// processOne extracts one capture's reader text, stores it, and marks
// the job done -- or, on any failure (including Readability judging the
// page not extractable at all), hands off to handleFailure. The returned
// error is purely for RunOnce's own logging.
func (r *Runner) processOne(ctx context.Context, job db.ClaimDueReadabilityJobsRow) error {
	rc, err := r.store.Open(job.HtmlPath)
	if err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("opening captured html: %w", err))
	}
	htmlData, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("reading captured html: %w", err))
	}

	art, err := r.render(ctx, htmlData)
	if err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("extracting reader text: %w", err))
	}

	sum := sha256.Sum256([]byte(art.TextContent))
	textHash := hex.EncodeToString(sum[:])

	if err := r.commitDone(ctx, job, art.TextContent, textHash); err != nil {
		return r.handleFailure(ctx, job, fmt.Errorf("committing readability completion: %w", err))
	}

	r.logger.InfoContext(ctx, "readability: extracted", "capture_id", job.CaptureID, "length", len(art.TextContent))
	return nil
}

// commitDone records the capture's new reader_text/reader_text_hash/
// readability_version and marks the job done in one transaction -- same
// "either both land or neither does" reasoning as
// internal/screenshot.Runner.commitDone.
func (r *Runner) commitDone(ctx context.Context, job db.ClaimDueReadabilityJobsRow, textContent, textHash string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := r.queries.WithTx(tx)

	if err := qtx.SetCaptureReadability(ctx, db.SetCaptureReadabilityParams{
		ID:                 job.CaptureID,
		ReaderText:         pgtype.Text{String: textContent, Valid: true},
		ReaderTextHash:     pgtype.Text{String: textHash, Valid: true},
		ReadabilityVersion: pgtype.Text{String: r.version, Valid: r.version != ""},
	}); err != nil {
		return fmt.Errorf("setting capture readability: %w", err)
	}
	if err := qtx.MarkReadabilityJobDone(ctx, job.JobID); err != nil {
		return fmt.Errorf("marking readability job done: %w", err)
	}

	return tx.Commit(ctx)
}

// handleFailure records a failed attempt: either scheduled for retry, or,
// once MaxAttempts is exhausted, marked 'failed' permanently. Identical
// shape to internal/screenshot.Runner.handleFailure -- see its own doc.
func (r *Runner) handleFailure(ctx context.Context, job db.ClaimDueReadabilityJobsRow, cause error) error {
	attempts := job.Attempts + 1

	if attempts >= int32(r.maxAttempts) {
		if err := r.queries.FailReadabilityJob(ctx, db.FailReadabilityJobParams{
			ID:       job.JobID,
			Attempts: attempts,
			Error:    pgtype.Text{String: cause.Error(), Valid: true},
		}); err != nil {
			return fmt.Errorf("%w (also failed marking job failed: %v)", cause, err)
		}
		return cause
	}

	if err := r.queries.RetryReadabilityJob(ctx, db.RetryReadabilityJobParams{
		ID:            job.JobID,
		Attempts:      attempts,
		NextAttemptAt: pgtype.Timestamptz{Time: time.Now().Add(backoff(attempts)), Valid: true},
		Error:         pgtype.Text{String: cause.Error(), Valid: true},
	}); err != nil {
		return fmt.Errorf("%w (also failed scheduling retry: %v)", cause, err)
	}
	return cause
}

// backoff is identical to internal/screenshot's own -- see its doc. Not
// shared: a few lines of pure arithmetic isn't worth a shared dependency
// for, unlike the sidecar connection/render server internal/sidecar
// actually factors out.
func backoff(attempts int32) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 10 {
		return maxBackoff
	}
	d := baseBackoff * time.Duration(int64(1)<<uint(shift))
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// render serves htmlData to the sidecar, injects Source (defining the
// global Readability constructor), and runs parseScript against the
// resulting DOM.
func (r *Runner) render(ctx context.Context, htmlData []byte) (*article, error) {
	tabCtx, url, cleanup := r.sidecar.NewTab(htmlData, renderTimeout)
	defer cleanup()

	var art *article
	err := chromedp.Run(tabCtx,
		chromedp.Navigate(url),
		chromedp.Evaluate(r.source, nil),
		chromedp.Evaluate(parseScript, &art),
	)
	if err != nil {
		return nil, err
	}
	if art == nil {
		return nil, errors.New("Readability.parse() returned null: page judged not extractable")
	}

	return art, nil
}
