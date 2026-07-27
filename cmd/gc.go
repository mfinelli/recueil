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

var gcDryRun bool

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Reclaim disk space orphaned by deleted pages/captures",
	Long: `Removes archive files no longer referenced by any page or capture.

DELETE /api/pages/{id} and DELETE /api/captures/{id} both deliberately leave
their on-disk HTML/thumbnail/favicon files in place: those files are
content-hash addressed and can be shared by other captures or pages, so only
a full sweep across every row at once -- what this command does -- can tell
whether a given file is genuinely unreferenced.

Safe to run repeatedly; run with --dry-run first to see what would be
removed without touching disk.`,
	Args: cobra.NoArgs,
	RunE: runGC,
}

func init() {
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", false, "report what would be removed without deleting anything")
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

	result, err := runner.Run(ctx, gcDryRun)
	if err != nil {
		return fmt.Errorf("running gc: %w", err)
	}

	verb := "removed"
	if gcDryRun {
		verb = "would remove"
	}
	log.Printf("recueil gc: scanned %d files, %s %d orphaned files, %s %.2f MB",
		result.FilesScanned, verb, result.FilesRemoved, verbNoun(gcDryRun), float64(result.BytesReclaimed)/(1024*1024))
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
