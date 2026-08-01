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

// Package gc reclaims archive.Store disk space that DELETE /api/pages/{id}
// and DELETE /api/captures/{id} deliberately leave behind. Both of those
// endpoints intentionally orphan a deleted row's on-disk files rather than
// removing them synchronously: a delete handler acting alone would have to
// trust that its own row was the only reference, and getting that wrong
// destroys the irreplaceable half of the archive. A full sweep across
// every capture/page row at once can answer the question directly, which
// is what Run does.
//
// (Under the earlier content-hash-addressed layout a delete handler
// *could not* have answered it even in principle, since one file was
// legitimately shared by every capture with identical content. That is no
// longer true -- every capture owns its own directory now, see
// internal/archive -- so a synchronous delete is nowadays merely riskier
// than a sweep, not impossible. Keeping it a sweep means the delete path
// stays a pure database operation with no partial-failure state spanning
// two systems.)
//
// # Two things this collects
//
// Orphaned *files*, as above, and empty *directories*:
// archive.Store.NewCapture creates a capture's directory before anything
// is written into it, so an ingestion that fails early leaves a directory
// no file-oriented sweep would ever see (Walk reports regular files only).
//
// # Two safety rails
//
// Neither is about the correctness of the live-set query; both are about
// what happens when something else is wrong.
//
// recentThreshold leaves anything modified within the last 15 minutes
// alone. Ingestion writes to disk *before* committing to Postgres
// (DESIGN.md §3c), so a file that is genuinely in flight is legitimately
// absent from the live set, and a sweep running at that moment would
// delete it out from under the writer. The sharpest case is
// archive.Store's ".tmp-*" files: they are in Walk's namespace and,
// by construction, can never appear in the live set, so without this they
// would be deleted mid-write and the writer's rename would fail ENOENT.
// 15 minutes is reused deliberately from the D1 queue's claim-visibility
// timeout and internal/screenshot's claimStaleTimeout rather than
// introducing a third number for the same "stuck, or merely in progress?"
// question.
//
// maxOrphanFraction refuses the whole run if the orphan set exceeds half
// of everything scanned. The live set is built by comparing stored path
// strings against walked path strings, so any future divergence in how
// the two sides are normalized -- a leading "./", a separator difference,
// a filepath.Clean applied on one side only -- would silently produce an
// empty intersection and mark the entire archive as garbage. That failure
// is silent, total, and aimed at data no backup of Postgres alone can
// reconstruct. A run that legitimately needs to clear more than half
// (bulk-deleting a large collection, say) passes Force.
//
// The fraction is only applied once at least safetyCheckMinFiles files
// have been scanned. Below that a high fraction carries no information --
// a four-file archive with three orphans is 75% and means nothing.
package gc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/db"
)

const (
	// recentThreshold is how recently a file or directory must have been
	// modified to be left alone regardless of whether anything references
	// it. See the package doc.
	recentThreshold = 15 * time.Minute

	// maxOrphanFraction is the share of scanned files that may be
	// identified as orphaned before Run refuses to proceed without Force.
	maxOrphanFraction = 0.5

	// safetyCheckMinFiles is the scan size below which maxOrphanFraction
	// is not applied at all.
	safetyCheckMinFiles = 100
)

// TooManyOrphansError is returned by Run when the orphan set exceeded
// maxOrphanFraction and Force was not set. Nothing was removed: the check
// runs after the full scan but before the first deletion, so this is
// always a clean refusal rather than a partial sweep.
type TooManyOrphansError struct {
	Orphans   int
	Scanned   int
	Fraction  float64
	Threshold float64
}

func (e *TooManyOrphansError) Error() string {
	return fmt.Sprintf(
		"gc: refusing to remove %d of %d scanned files (%.1f%%, over the %.0f%% safety threshold) -- "+
			"nothing was deleted; re-run with --force if this is expected",
		e.Orphans, e.Scanned, e.Fraction*100, e.Threshold*100)
}

// Options controls one Run.
type Options struct {
	// DryRun performs the full scan and comparison and reports exactly
	// what would happen, without removing anything. The safety check is
	// still evaluated and reported (Result.SafetyCheckTripped) but never
	// returned as an error -- a dry run that silently omitted the fact
	// that the real run would refuse would be actively misleading.
	DryRun bool
	// Force bypasses the maxOrphanFraction check. It does not bypass
	// recentThreshold: that one prevents deleting files a writer is
	// actively using, which no operator intent makes safe.
	Force bool
}

// RunnerParams are Runner's dependencies, all required except Logger.
type RunnerParams struct {
	Queries *db.Queries
	Store   *archive.Store
	// Logger receives one warning per file this run failed to remove
	// (permissions, a concurrent modification, etc.) -- never fatal to
	// the run itself, same "log and keep going" philosophy
	// internal/ingest and internal/mirror already apply at their own
	// per-item level. Defaults to slog.Default() if nil.
	Logger *slog.Logger
}

// Runner is gc's own single entry point: NewRunner, then Run once (or
// however many times the caller likes -- Run has no internal state of its
// own between calls).
type Runner struct {
	queries *db.Queries
	store   *archive.Store
	logger  *slog.Logger
}

func NewRunner(p RunnerParams) *Runner {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{queries: p.Queries, store: p.Store, logger: logger}
}

// Result summarizes one Run.
type Result struct {
	DryRun bool
	// FilesScanned is every regular file archive.Store's Walk found
	// under its root: live, orphaned, and too-recent-to-judge alike.
	FilesScanned int
	// FilesSkippedRecent is files that nothing references but that were
	// modified within recentThreshold, so were left alone -- an
	// in-progress ingestion, most often, or archive's ".tmp-*" write
	// files. Not an error and not a backlog: they are simply judged on
	// the next run.
	FilesSkippedRecent int
	// FilesRemoved is how many orphans were actually removed from disk --
	// or, under DryRun, would have been.
	FilesRemoved int
	// BytesReclaimed is FilesRemoved's total size.
	BytesReclaimed int64
	// EmptyDirsRemoved counts capture/shard directories containing
	// nothing at all that were pruned (or, under DryRun, would have
	// been). Under DryRun this undercounts slightly compared to a real
	// run: parent directories that only *become* empty as their children
	// are removed are pruned by archive.Store.Remove during a real sweep,
	// and a dry run never gets that far.
	EmptyDirsRemoved int
	// RemoveErrors counts individual files this run failed to remove
	// (each one already logged via Logger) -- always 0 under DryRun,
	// since nothing is attempted then.
	RemoveErrors int
	// SafetyCheckTripped reports that the orphan set exceeded
	// maxOrphanFraction. Under DryRun this is informational (the run
	// still reports what it found); otherwise Run returned a
	// *TooManyOrphansError and removed nothing, unless Force was set, in
	// which case this stays true and the sweep proceeded anyway.
	SafetyCheckTripped bool
}

type orphan struct {
	relPath string
	size    int64
}

// Run performs one full sweep: read every path Postgres still references
// (ListReferencedArchivePaths, spanning both captures.*_path and pages'
// own denormalized favicon_path), walk every file the store actually
// contains, and remove whatever isn't in that live set and isn't too
// recently written to judge. Then prune directories left containing
// nothing at all.
//
// The whole scan completes before anything is removed. That ordering is
// what lets the maxOrphanFraction check (see the package doc) be a clean
// all-or-nothing refusal rather than an abort partway through a deletion
// pass.
//
// A single file's removal failing doesn't abort the run: it's logged and
// counted (Result.RemoveErrors), and the sweep continues. Only a failure
// reading the live-set query, a fundamental failure walking the store
// (permission denied, say), or the safety check tripping aborts the whole
// run -- the first two because there's no reasonable partial result to
// report, the third by design. A store root that doesn't exist at all is
// not one of those: archive.Store reports it as zero files, which is
// exactly right for an instance that has not yet ingested anything, and
// nothing can be deleted on the strength of finding nothing.
func (r *Runner) Run(ctx context.Context, opts Options) (Result, error) {
	result := Result{DryRun: opts.DryRun}

	paths, err := r.queries.ListReferencedArchivePaths(ctx)
	if err != nil {
		return result, fmt.Errorf("gc: listing referenced archive paths: %w", err)
	}
	live := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		live[p] = struct{}{}
	}

	cutoff := time.Now().Add(-recentThreshold)

	var orphans []orphan
	err = r.store.Walk(func(relPath string, sizeBytes int64, modTime time.Time) error {
		result.FilesScanned++
		if _, ok := live[relPath]; ok {
			return nil
		}
		if modTime.After(cutoff) {
			result.FilesSkippedRecent++
			return nil
		}
		orphans = append(orphans, orphan{relPath: relPath, size: sizeBytes})
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("gc: walking archive store: %w", err)
	}

	var emptyDirs []string
	err = r.store.WalkEmptyDirs(func(relPath string, modTime time.Time) error {
		if modTime.After(cutoff) {
			// Almost certainly a capture directory NewCapture just
			// minted for an ingestion still in flight.
			return nil
		}
		emptyDirs = append(emptyDirs, relPath)
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("gc: walking archive store for empty directories: %w", err)
	}

	if result.FilesScanned >= safetyCheckMinFiles {
		fraction := float64(len(orphans)) / float64(result.FilesScanned)
		if fraction > maxOrphanFraction {
			result.SafetyCheckTripped = true
			if !opts.DryRun && !opts.Force {
				return result, &TooManyOrphansError{
					Orphans:   len(orphans),
					Scanned:   result.FilesScanned,
					Fraction:  fraction,
					Threshold: maxOrphanFraction,
				}
			}
			r.logger.WarnContext(ctx, "gc: orphan set exceeds the safety threshold",
				"orphans", len(orphans), "scanned", result.FilesScanned,
				"fraction", fraction, "threshold", maxOrphanFraction,
				"dry_run", opts.DryRun, "force", opts.Force)
		}
	}

	for _, o := range orphans {
		result.FilesRemoved++
		result.BytesReclaimed += o.size
		if opts.DryRun {
			continue
		}
		if err := r.store.Remove(o.relPath); err != nil {
			r.logger.WarnContext(ctx, "gc: failed to remove orphaned file", "path", o.relPath, "error", err)
			result.RemoveErrors++
			// Correct the two tallies above: this one wasn't actually
			// removed, so it shouldn't count toward "removed" or
			// "reclaimed" -- but it was still correctly identified as
			// orphaned, which FilesScanned already reflects regardless.
			result.FilesRemoved--
			result.BytesReclaimed -= o.size
		}
	}

	for _, relPath := range emptyDirs {
		if opts.DryRun {
			result.EmptyDirsRemoved++
			continue
		}
		// RemoveEmptyDir, not Remove: it re-checks emptiness immediately
		// before removing, and reports "no longer applicable" as
		// (false, nil) rather than as an error. Both ways that can
		// happen are ordinary here -- removing an orphaned file above
		// may already have pruned the directory that held it, and a
		// concurrent NewCapture may have claimed another -- so neither
		// should show up as a removal error an operator has to explain.
		removed, err := r.store.RemoveEmptyDir(relPath)
		if err != nil {
			r.logger.WarnContext(ctx, "gc: failed to remove empty directory", "path", relPath, "error", err)
			result.RemoveErrors++
			continue
		}
		if removed {
			result.EmptyDirsRemoved++
		}
	}

	return result, nil
}
