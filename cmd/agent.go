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

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/config"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/ingest"
	"github.com/mfinelli/recueil/internal/mirror"
	"github.com/mfinelli/recueil/internal/r2"
	"github.com/mfinelli/recueil/internal/urlnorm"
)

// agentCmd is Recueil's background job runner: ingestion (pulling
// completed captures from R2/D1 into Postgres and local disk) and the D1
// bookmark-list mirror sync, both driven by a shared ticker rather than
// any push/event mechanism.
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

	// Deliberately does NOT run migrations itself, unlike `server` --
	// Postgres migrations are safe to run from multiple processes
	// concurrently (goose's own session-level advisory lock, via
	// internal/pgmigrate, serializes that), but D1 migrations have no
	// equivalent locking, and `server` and `agent` starting together in
	// compose gives no ordering guarantee. Rather than run Postgres
	// migrations here but not D1 (an asymmetry that's more confusing
	// than it's worth), the agent runs neither: it assumes `server` owns
	// migrations exclusively. If the agent starts before `server` has
	// finished migrating, its first cycle(s) will fail against a
	// not-yet-ready schema, get logged, and self-heal on the next tick
	// once `server` catches up -- the same graceful-degradation shape
	// RunOnce/SyncOnce already have for a single failed item, just at
	// the whole-cycle level.
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

	interval := time.Duration(cfg.AgentPollIntervalSeconds) * time.Second
	log.Printf("recueil agent started, poll interval: %s", interval)

	// Run one cycle immediately on startup, rather than waiting for the
	// first tick -- otherwise a freshly-deployed agent sits idle for a
	// full interval before doing anything.
	runAgentCycle(cmd.Context(), ingester, syncer, logger.Logger)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Deliberately synchronous within the select loop, not
			// spawned into its own goroutine per tick: this is what
			// prevents overlapping cycles if one ever runs longer than
			// the poll interval. time.Ticker's channel has a buffer of
			// exactly one pending tick, so a slow cycle simply means
			// some ticks are silently dropped rather than queuing up a
			// backlog -- the next cycle starts as soon as this one
			// finishes and at least one tick has fired since, not once
			// per missed interval.
			runAgentCycle(cmd.Context(), ingester, syncer, logger.Logger)
		case <-cmd.Context().Done():
			log.Println("shutting down...")
			return nil
		}
	}
}

// runAgentCycle runs one ingestion pass and one mirror-sync pass.
// Deliberately both, sequentially, on one shared tick rather than two
// independently-scheduled loops -- the simplest thing that works, and
// splitting them onto separate intervals is a natural, easy follow-up if
// one ever needs to run on a different cadence than the other. Errors
// from either are logged, not returned/propagated: a failed cycle
// shouldn't crash the agent process, it should just try again next tick
// -- the same "log and continue" philosophy RunOnce and SyncOnce already
// apply at the per-item level, just one level up.
func runAgentCycle(ctx context.Context, ingester *ingest.Ingester, syncer *mirror.Syncer, logger *slog.Logger) {
	if err := ingester.RunOnce(ctx); err != nil {
		logger.ErrorContext(ctx, "agent: ingestion cycle failed", "error", err)
	}
	if err := syncer.SyncOnce(ctx); err != nil {
		logger.ErrorContext(ctx, "agent: mirror sync cycle failed", "error", err)
	}
}
