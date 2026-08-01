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
	"errors"
	"fmt"
	"io/fs"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/config"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/gc"
	"github.com/mfinelli/recueil/internal/pgmigrate"
)

var (
	gcDryRun bool
	gcForce  bool
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Reclaim disk space orphaned by deleted pages/captures",
	Long: `Removes archive files no longer referenced by any page or capture.

DELETE /api/pages/{id} and DELETE /api/captures/{id} both deliberately leave
their on-disk HTML/thumbnail/favicon files in place, so that deleting stays a
pure database operation with no partial-failure state spanning both Postgres
and the filesystem. This command is the sweep that reclaims them. It also
prunes capture directories left containing nothing at all, which happens when
an ingestion fails after creating its directory but before writing into it.

Anything modified in the last 15 minutes is left alone regardless, since an
in-flight capture writes to disk before committing to Postgres and is
legitimately unreferenced until it does.

If more than half of the scanned files come back unreferenced, the run stops
without deleting anything and reports what it found -- that pattern is far
more likely to mean something is wrong with the sweep than that the archive is
genuinely half garbage. Use --force when it really is expected.

Safe to run repeatedly; run with --dry-run first to see what would be
removed without touching disk.`,
	Args: cobra.NoArgs,
	RunE: runGC,
}

func init() {
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", false, "report what would be removed without deleting anything")
	gcCmd.Flags().BoolVar(&gcForce, "force", false, "proceed even if more than half of the scanned files are unreferenced")
	rootCmd.AddCommand(gcCmd)
}

func runGC(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	ctx := cmd.Context()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	// Same reasoning as `recueil user create`'s migrations step:
	// works standalone even against a database the server has never
	// been started against yet (e.g. right after restoring a backup) --
	// goose no-ops if everything's already current.
	postgresMigrations, err := fs.Sub(PostgresMigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("preparing embedded postgres migrations: %w", err)
	}
	if err := pgmigrate.Run(ctx, pool, postgresMigrations); err != nil {
		return fmt.Errorf("applying postgres migrations: %w", err)
	}

	queries := db.New(pool)
	store := archive.New(cfg.ArchiveDir)
	runner := gc.NewRunner(gc.RunnerParams{Queries: queries, Store: store})

	if gcDryRun {
		log.Println("recueil gc: dry run -- nothing will actually be deleted")
	}

	result, err := runner.Run(ctx, gc.Options{DryRun: gcDryRun, Force: gcForce})
	if err != nil {
		// TooManyOrphansError already explains itself in full, including
		// what to do about it -- wrapping it in "running gc: ..." would
		// just prefix noise onto an already-actionable message.
		var tooMany *gc.TooManyOrphansError
		if errors.As(err, &tooMany) {
			return err
		}
		return fmt.Errorf("running gc: %w", err)
	}

	verb := "removed"
	if gcDryRun {
		verb = "would remove"
	}
	log.Printf("recueil gc: scanned %d files, %s %d orphaned files, %s %.2f MB",
		result.FilesScanned, verb, result.FilesRemoved, verbNoun(gcDryRun), float64(result.BytesReclaimed)/(1024*1024))
	if result.EmptyDirsRemoved > 0 {
		log.Printf("recueil gc: %s %d empty directories", verb, result.EmptyDirsRemoved)
	}
	if result.FilesSkippedRecent > 0 {
		log.Printf("recueil gc: left %d unreferenced but recently-modified files alone (in-flight captures) -- they are judged on the next run",
			result.FilesSkippedRecent)
	}
	// Only reachable here with --dry-run or --force: without either, a
	// tripped check returns an error above and never gets this far.
	if result.SafetyCheckTripped {
		if gcDryRun {
			log.Println("recueil gc: over half of the scanned files are unreferenced -- a real run would stop here; use --force if that is expected")
		} else {
			log.Println("recueil gc: over half of the scanned files were unreferenced -- proceeded anyway because --force was given")
		}
	}
	if result.RemoveErrors > 0 {
		log.Printf("recueil gc: %d files failed to remove -- see warnings above", result.RemoveErrors)
	}

	return nil
}

// verbNoun avoids an awkward "removed/would remove ... reclaimed/would
// reclaim" repeat of the same dry-run-or-not branch runGC's own log line
// already made once for "removed" -- kept as its own tiny function only so
// that single log.Printf call doesn't need a multi-line if/else just to
// pick between two five-word phrases.
func verbNoun(dryRun bool) string {
	if dryRun {
		return "would reclaim"
	}
	return "reclaimed"
}
