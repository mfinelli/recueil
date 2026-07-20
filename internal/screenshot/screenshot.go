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
// headless-Chrome sidecar and store the resulting fixed-viewport PNG
// alongside it -- a uniform size by design, not a full (variably-tall) page
// capture, so the dashboard can display thumbnails in a consistent grid
// rather than wildly different aspect ratios per capture. Built as a callable
// unit (RunOnce), same shape as internal/ingest, with no scheduler of its own
// -- cmd/agent.go's ticker drives it.
//
// Independent of internal's readability job even though both run through the
// same sidecar and could, in principle, share a single page load: giving us
// independent failure modes, independent retry/backoff, no reason for a
// Readability.js upgrade's re-extraction to force a redundant re-screenshot.
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
//
// # Serving HTML to the sidecar
//
// The sidecar is a separate process -- often a separate container --
// with no filesystem access to the agent's local archive, so a job's
// decompressed HTML is served over a tiny ephemeral HTTP server this
// package runs for the lifetime of the Runner, one random-token path per
// in-flight render. See Params' SidecarURL/RenderHost doc (mirrored in
// internal/config) for how the two different deployment shapes (sidecar
// and agent in the same compose network vs. agent running directly on
// the operator's own machine) each configure reachability.
package screenshot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/db"
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

// sidecarPingTimeout bounds New's one-shot startup reachability check --
// see pingSidecar's own doc.
const sidecarPingTimeout = 5 * time.Second

// Params are Runner's dependencies, all required except Logger. Grouped
// into a struct for the same reason as internal/ingest.Params: enough
// fields that positional arguments would be a real mistake risk.
type Params struct {
	Pool    *pgxpool.Pool
	Queries *db.Queries
	Store   *archive.Store

	// SidecarURL is the agent's own outbound address for the shared
	// headless-Chrome sidecar (config's screenshot_sidecar_url) -- an
	// http(s) base URL, not a raw ws:// one: chromedp.NewRemoteAllocator
	// fetches /json/version itself and swaps in the real
	// webSocketDebuggerUrl, so this package never has to.
	SidecarURL string

	// RenderHost is the hostname the sidecar should use to reach back
	// into this Runner's own ephemeral render server (config's
	// screenshot_render_host) -- the opposite direction from
	// SidecarURL. See the package doc and internal/config's own doc
	// comment for the two deployment shapes this needs to cover.
	RenderHost string

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

// Runner holds a long-lived connection to the sidecar (one
// RemoteAllocator, reused across every job -- opening a new tab per job
// rather than cold-starting a browser process each time, and a long-lived
// ephemeral HTTP server for serving HTML to it. Both are real OS resources,
// unlike internal/ingest.Ingester or mirror.Syncer, which is why -- unlike
// either of those -- this type has a Close.
type Runner struct {
	pool        *pgxpool.Pool
	queries     *db.Queries
	store       *archive.Store
	renderHost  string
	concurrency int
	maxAttempts int
	logger      *slog.Logger

	allocCancel context.CancelFunc
	allocCtx    context.Context

	listener net.Listener
	server   *http.Server

	mu      sync.Mutex
	pending map[string][]byte
}

// New pings the sidecar once to confirm it's actually reachable, then
// starts the Runner's ephemeral render server and dials the sidecar for
// real. The caller is responsible for calling Close when done (typically
// via defer at the same call site cmd/agent.go already closes its
// Postgres pool from).
//
// Failing loudly here (rather than lazily discovering an unreachable
// sidecar on the first job's render call) is deliberate: it's what lets
// an orchestrator's restart-until-healthy policy -- Docker Compose's
// restart policy, systemd's Restart=on-failure, a Kubernetes liveness
// probe, whatever the deployment uses -- actually do its job. Without
// this, a misconfigured or not-yet-ready sidecar would leave the agent
// process running indefinitely with every screenshot job silently
// failing and retrying forever, instead of the process itself exiting
// non-zero so something notices and restarts it once the sidecar catches
// up.
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

	if err := pingSidecar(p.SidecarURL); err != nil {
		return nil, fmt.Errorf("screenshot: sidecar not reachable at startup: %w", err)
	}

	// Bound to every interface: this is a container-to-container (or
	// container-to-host) connection, entirely separate from whatever
	// hostname RenderHost tells the *sidecar* to use to reach back in.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("screenshot: starting render server listener: %w", err)
	}

	r := &Runner{
		pool:        p.Pool,
		queries:     p.Queries,
		store:       p.Store,
		renderHost:  p.RenderHost,
		concurrency: concurrency,
		maxAttempts: maxAttempts,
		logger:      logger,
		listener:    ln,
		pending:     make(map[string][]byte),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", r.handleRender)
	r.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := r.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("screenshot: render server stopped unexpectedly", "error", err)
		}
	}()

	// Intentionally parented on context.Background(), not any per-call
	// ctx passed to RunOnce: this connection is meant to outlive any
	// single RunOnce cycle, same lifetime as the Runner itself, torn
	// down only by an explicit Close.
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), p.SidecarURL)
	r.allocCtx = allocCtx
	r.allocCancel = allocCancel

	return r, nil
}

// pingSidecar performs one bounded-time HTTP GET against the sidecar's
// /json/version endpoint -- the same endpoint chromedp's own
// RemoteAllocator uses internally to discover the real
// webSocketDebuggerUrl -- purely to confirm something's actually
// listening and answering there before New commits to starting the
// render server and dialing for real.
func pingSidecar(sidecarURL string) error {
	client := http.Client{Timeout: sidecarPingTimeout}

	resp, err := client.Get(strings.TrimRight(sidecarURL, "/") + "/json/version")
	if err != nil {
		return fmt.Errorf("GET /json/version: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /json/version: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// Close tears down the sidecar connection and the render server. Safe to
// call once, at shutdown.
func (r *Runner) Close() error {
	r.allocCancel()
	return r.server.Close()
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

// render serves htmlData to the sidecar over the ephemeral render
// server and returns a fixed-viewportWidth x viewportHeight PNG
// screenshot of it -- CaptureScreenshot (the current viewport only), not
// FullScreenshot (the entire scrollable page): see the package doc for why
// uniform dimensions matter here.
func (r *Runner) render(ctx context.Context, htmlData []byte) ([]byte, error) {
	token, cleanup := r.registerHTML(htmlData)
	defer cleanup()

	tabCtx, cancelTab := chromedp.NewContext(r.allocCtx)
	defer cancelTab()
	tabCtx, cancelTimeout := context.WithTimeout(tabCtx, renderTimeout)
	defer cancelTimeout()

	url := fmt.Sprintf("%s/%s", r.baseURL(), token)

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

// baseURL is the http address the sidecar should navigate to for this
// Runner's render server, combining the configured, deployment-specific
// RenderHost with the port the ephemeral listener actually got assigned
// -- never configured directly (see Params.RenderHost's doc for why the
// port doesn't need to be).
func (r *Runner) baseURL() string {
	port := r.listener.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("http://%s:%d", r.renderHost, port)
}

// registerHTML makes htmlData servable at a fresh random-token path and
// returns a cleanup func that removes it again. Called once per render,
// so a stuck/never-fetched job (the sidecar crashing mid-render, say)
// leaks at most one entry until this same job is retried and the
// original token is simply never looked up again -- not indefinitely,
// since cleanup still runs via defer in render regardless of whether
// the fetch ever actually happened.
func (r *Runner) registerHTML(htmlData []byte) (token string, cleanup func()) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	token = hex.EncodeToString(buf)

	r.mu.Lock()
	r.pending[token] = htmlData
	r.mu.Unlock()

	return token, func() {
		r.mu.Lock()
		delete(r.pending, token)
		r.mu.Unlock()
	}
}

func (r *Runner) handleRender(w http.ResponseWriter, req *http.Request) {
	token := strings.TrimPrefix(req.URL.Path, "/")

	r.mu.Lock()
	data, ok := r.pending[token]
	r.mu.Unlock()

	if !ok {
		http.NotFound(w, req)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
