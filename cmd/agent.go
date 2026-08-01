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

package cmd

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/go-chi/httplog/v2"

	"github.com/mfinelli/recueil/internal/ai"
	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/config"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/ingest"
	"github.com/mfinelli/recueil/internal/mirror"
	"github.com/mfinelli/recueil/internal/queueitems"
	"github.com/mfinelli/recueil/internal/r2"
	"github.com/mfinelli/recueil/internal/readability"
	"github.com/mfinelli/recueil/internal/screenshot"
	"github.com/mfinelli/recueil/internal/sidecar"
	"github.com/mfinelli/recueil/internal/urlnorm"
)

var (
	ReadabilityJS      string
	ReadabilityVersion string
)

// agentCmd is Recueil's background job runner, split across two
// independent schedules: a slower, Worker-free-tier-friendly one for
// ingestion (pulling completed captures from R2/D1 into Postgres and
// local disk) and the D1 bookmark-list mirror sync, and a faster,
// Postgres-only one for jobs like the screenshot job that never touch
// the Cloudflare Worker at all. Both are driven by their own ticker
// rather than any push/event mechanism -- see runWorkerCycle/
// runLocalCycle.
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Run the Recueil background job agent",
	RunE:  runAgent,
}

func init() {
	rootCmd.AddCommand(agentCmd)
}

func runAgent(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logger := httplog.NewLogger("recueil-agent", httplog.Options{
		LogLevel: slog.LevelInfo,
		JSON:     !isatty.IsTerminal(os.Stdout.Fd()),
	})

	pool, err := pgxpool.New(cmd.Context(), cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	// Does NOT run migrations itself, unlike `server` -- Postgres
	// migrations are safe to run from multiple processes concurrently
	// (goose's own session-level advisory lock, via  internal/pgmigrate,
	// serializes that), but D1 migrations have no equivalent locking, and
	// `server` and `agent` starting together in compose gives no ordering
	// guarantee. Rather than run Postgres migrations here but not D1 (an
	// asymmetry that's more confusing than it's worth), the agent runs
	// neither: it assumes `server` owns migrations exclusively. If the
	// agent starts before `server` has finished migrating, its first
	// cycle(s) will fail against a not-yet-ready schema, get logged, and
	// self-heal on the next tick once `server` catches up -- the same
	// graceful-degradation shape RunOnce/SyncOnce already have for a
	// single failed item, just at the whole-cycle level.
	queries := db.New(pool)

	r2Client, err := r2.New(r2.Config{
		AccountID:       cfg.R2AccountID,
		BucketName:      cfg.R2BucketName,
		AccessKeyID:     cfg.R2AccessKeyID,
		AccessKeySecret: cfg.R2AccessKeySecret,
	})
	if err != nil {
		return fmt.Errorf("creating r2 client: %w", err)
	}

	clearURLs, err := urlnorm.NewClearURLs()
	if err != nil {
		return fmt.Errorf("loading clearurls ruleset: %w", err)
	}
	pipeline := urlnorm.NewPipeline(clearURLs, urlnorm.Canonicalize{})

	workerClient := ingest.NewWorkerClient(cfg.WorkerURL, cfg.WorkerServiceSecret)
	ingester := ingest.New(ingest.Params{
		Pool:     pool,
		Queries:  queries,
		Worker:   workerClient,
		R2:       r2Client,
		Store:    archive.New(cfg.ArchiveDir),
		Pipeline: pipeline,
		Logger:   logger.Logger,
	})

	mirrorClient := mirror.NewClient(cfg.WorkerURL, cfg.WorkerServiceSecret)
	syncer := mirror.NewSyncer(mirror.SyncerParams{
		Queries: queries,
		Client:  mirrorClient,
	})

	// The one shared headless-Chrome connection + ephemeral render
	// server (internal/sidecar) both the screenshot and readability
	// Runners open tabs against -- constructed once, here, since it's
	// the only thing between them with real OS resources to close.
	// Neither Runner owns or closes it themselves.
	sc, err := sidecar.New(&sidecar.Params{
		SidecarURL: cfg.SidecarURL,
		RenderHost: cfg.SidecarRenderHost,
		Logger:     logger.Logger,
	})
	if err != nil {
		return fmt.Errorf("creating sidecar connection: %w", err)
	}
	defer func() {
		if err := sc.Close(); err != nil {
			logger.Logger.Error("agent: failed to close sidecar connection cleanly", "error", err)
		}
	}()

	screenshotRunner, err := screenshot.New(&screenshot.Params{
		Pool:        pool,
		Queries:     queries,
		Store:       archive.New(cfg.ArchiveDir),
		Sidecar:     sc,
		Concurrency: cfg.ScreenshotWorkerConcurrency,
		MaxAttempts: cfg.ScreenshotMaxAttempts,
		Logger:      logger.Logger,
	})
	if err != nil {
		return fmt.Errorf("creating screenshot runner: %w", err)
	}

	readabilityRunner, err := readability.New(&readability.Params{
		Pool:        pool,
		Queries:     queries,
		Store:       archive.New(cfg.ArchiveDir),
		Sidecar:     sc,
		Source:      ReadabilityJS,
		Version:     ReadabilityVersion,
		Concurrency: cfg.ReadabilityWorkerConcurrency,
		MaxAttempts: cfg.ReadabilityMaxAttempts,
		Logger:      logger.Logger,
	})
	if err != nil {
		return fmt.Errorf("creating readability runner: %w", err)
	}

	// Unlike screenshotRunner/readabilityRunner, aiRunner may be nil:
	// an empty AIBaseURL is how AI enrichment is toggled off entirely,
	// and runLocalCycle below checks for that explicitly rather than
	// constructing a Runner pointed at nothing.
	var aiRunner *ai.Runner
	if cfg.AIBaseURL != "" {
		aiRunner, err = ai.New(&ai.Params{
			Pool:           pool,
			Queries:        queries,
			BaseURL:        cfg.AIBaseURL,
			APIKey:         cfg.AIAPIKey,
			Model:          cfg.AIModel,
			Concurrency:    cfg.AIWorkerConcurrency,
			MaxAttempts:    cfg.AIMaxAttempts,
			RequestTimeout: time.Duration(cfg.AIRequestTimeoutSeconds) * time.Second,
			MaxInputChars:  cfg.AIMaxInputChars,
			Logger:         logger.Logger,
		})
		if err != nil {
			return fmt.Errorf("creating ai runner: %w", err)
		}
	}

	workerInterval := time.Duration(cfg.AgentWorkerPollIntervalSeconds) * time.Second
	localInterval := time.Duration(cfg.AgentLocalPollIntervalSeconds) * time.Second
	log.Printf("recueil agent started, worker poll interval: %s, local poll interval: %s",
		workerInterval, localInterval)

	worker := &workerCycle{
		ingester:   ingester,
		syncer:     syncer,
		worker:     workerClient,
		queueItems: queueitems.NewClient(cfg.WorkerURL, cfg.WorkerServiceSecret),
		logger:     logger.Logger,
	}

	// Run one cycle of each immediately on startup, rather than waiting
	// for their first tick -- otherwise a freshly-deployed agent sits
	// idle for a full interval before doing anything.
	worker.run(cmd.Context())
	runLocalCycle(cmd.Context(), screenshotRunner, readabilityRunner, aiRunner, logger.Logger)

	workerTicker := time.NewTicker(workerInterval)
	defer workerTicker.Stop()
	localTicker := time.NewTicker(localInterval)
	defer localTicker.Stop()

	for {
		select {
		case <-workerTicker.C:
			// Synchronous within the select loop, not spawned
			// into its own goroutine per tick: this is what
			// prevents overlapping cycles of the *same* schedule if one
			// ever runs longer than its own interval. Each ticker's
			// channel has a buffer of exactly one pending tick, so a
			// slow cycle simply means some ticks are silently dropped
			// rather than queuing up a backlog -- the next cycle starts
			// as soon as this one finishes and at least one tick has
			// fired since, not once per missed interval. The two
			// tickers are independent of *each other*, though: a slow
			// worker cycle doesn't delay the local ticker's own firing,
			// since each is just another case in the same select.
			worker.run(cmd.Context())
		case <-localTicker.C:
			runLocalCycle(cmd.Context(), screenshotRunner, readabilityRunner, aiRunner, logger.Logger)
		case <-cmd.Context().Done():
			log.Println("shutting down...")
			return nil
		}
	}
}

// cleanupInterval is how often the worker cycle also sweeps D1's own
// terminal rows (ingested pending_captures, captured queue_items). Far
// less often than the cycle itself runs, since both sweep against a
// 72-hour retention window and there is nothing to gain from checking
// every half hour.
//
// A plain elapsed-time check on the existing worker ticker rather than a
// third ticker of its own, for two reasons. The agent's tickers are split
// by *destination* -- Worker-facing work versus Postgres-only work -- and
// these sweeps are Worker-facing, so they belong on this cycle by that
// same taxonomy; a per-job ticker would quietly redefine the split as
// "one ticker per job." And a 12-hour time.Ticker restarts from zero on
// every process start, so an agent redeployed or restarted more often
// than that would sweep *never*. This check has the opposite failure --
// it sweeps shortly after each restart -- which costs two idempotent
// DELETEs against a handful of indexed rows.
const cleanupInterval = 12 * time.Hour

// workerCycle runs one ingestion pass, one mirror-sync pass, and
// (occasionally) D1's maintenance sweeps -- everything that talks to the
// Cloudflare Worker (and, through it, D1), which is why it's on the
// slower, Worker-free-tier-friendly schedule
// (AgentWorkerPollIntervalSeconds).
//
// A struct rather than a free function like runLocalCycle purely because
// this one carries state between cycles (lastCleanup); the local cycle has
// nothing to remember. Not safe for concurrent use, which is fine and
// deliberate: it's only ever called from the agent's single select loop,
// synchronously, so successive cycles can never overlap.
type workerCycle struct {
	ingester   *ingest.Ingester
	syncer     *mirror.Syncer
	worker     *ingest.WorkerClient
	queueItems *queueitems.Client
	logger     *slog.Logger

	// lastCleanup is the zero Time until the first sweep, which is what
	// makes a freshly-started agent sweep on its very first cycle.
	lastCleanup time.Time
}

// run performs one cycle. Errors from any step are logged, not
// returned/propagated: a failed cycle shouldn't crash the agent process, it
// should just try again next tick -- the same "log and continue" philosophy
// RunOnce/SyncOnce already apply at the per-item level, just one level up.
func (w *workerCycle) run(ctx context.Context) {
	if err := w.ingester.RunOnce(ctx); err != nil {
		w.logger.ErrorContext(ctx, "agent: ingestion cycle failed", "error", err)
	}
	if err := w.syncer.SyncOnce(ctx); err != nil {
		w.logger.ErrorContext(ctx, "agent: mirror sync cycle failed", "error", err)
	}

	// Last, and only sometimes: pure maintenance, so a failure here
	// mustn't delay the work anything actually waits on.
	if now := time.Now(); now.Sub(w.lastCleanup) >= cleanupInterval {
		w.lastCleanup = now
		w.runCleanup(ctx)
	}
}

// runCleanup sweeps both D1 tables that accumulate terminal rows. Each is
// independent -- one failing doesn't skip the other, since there's no
// ordering between them and no reason a Worker error on one implies
// anything about the other.
func (w *workerCycle) runCleanup(ctx context.Context) {
	if deleted, err := w.worker.CleanupPendingCaptures(ctx); err != nil {
		w.logger.ErrorContext(ctx, "agent: pending-captures cleanup failed", "error", err)
	} else if deleted > 0 {
		w.logger.InfoContext(ctx, "agent: swept ingested pending captures", "deleted", deleted)
	}

	if deleted, err := w.queueItems.Cleanup(ctx); err != nil {
		w.logger.ErrorContext(ctx, "agent: queue-items cleanup failed", "error", err)
	} else if deleted > 0 {
		w.logger.InfoContext(ctx, "agent: swept captured queue items", "deleted", deleted)
	}
}

// runLocalCycle runs the jobs that only ever touch this process's own
// Postgres instance -- the screenshot job, the readability job, and the
// AI enrichment job -- all on the same faster schedule
// (AgentLocalPollIntervalSeconds), since none of them have any
// Worker/free-tier request budget to respect. aiRunner may be nil (AI
// enrichment disabled entirely, via an empty AIBaseURL), in which case
// this cycle simply skips it -- not an error, just nothing to run.
func runLocalCycle(ctx context.Context, screenshotRunner *screenshot.Runner, readabilityRunner *readability.Runner, aiRunner *ai.Runner, logger *slog.Logger) {
	if err := screenshotRunner.RunOnce(ctx); err != nil {
		logger.ErrorContext(ctx, "agent: screenshot cycle failed", "error", err)
	}
	if err := readabilityRunner.RunOnce(ctx); err != nil {
		logger.ErrorContext(ctx, "agent: readability cycle failed", "error", err)
	}
	if aiRunner != nil {
		if err := aiRunner.RunOnce(ctx); err != nil {
			logger.ErrorContext(ctx, "agent: ai enrichment cycle failed", "error", err)
		}
	}
}
