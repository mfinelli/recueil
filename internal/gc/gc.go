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
// removing them synchronously: archive.Store's files are content-hash
// addressed and can be shared across multiple captures, or even multiple
// pages, that happen to have byte-identical content -- so a delete handler
// acting alone can never safely tell "nothing else references this file" from
// "this exact page/capture doesn't, but a sibling row does." Only a full
// sweep across every capture/page row at once can answer that question
// correctly, which is exactly what Run does.
package gc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/db"
)

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
	// FilesScanned is every regular file archive.Store's own Walk found
	// under its root, live or orphaned.
	FilesScanned int
	// FilesRemoved is how many of those were actually orphaned --
	// removed from disk when DryRun is false, or just identified and
	// left alone when it's true.
	FilesRemoved int
	// BytesReclaimed is FilesRemoved's total size -- real, already-freed
	// disk space when DryRun is false; what *would* be freed when it's true.
	BytesReclaimed int64
	// RemoveErrors counts individual files this run failed to remove
	// (each one already logged via Logger) -- always 0 when DryRun is
	// true, since nothing is actually attempted then.
	RemoveErrors int
}

// Run performs one full sweep: reads every path Postgres still references
// (ListReferencedArchivePaths, spanning both captures.*_path and pages'
// own denormalized favicon_path), then walks every file archive.Store's root
// actually contains, removing whatever isn't in that live set. dryRun performs
// the full comparison and reports exactly what would happen, without calling
// Store.Remove at all -- nothing is deleted, and RemoveErrors is
// meaningless (never touched) in that mode.
//
// A single file's removal failing doesn't abort the run: it's logged and
// counted (Result.RemoveErrors), and the sweep continues on to the next
// file. Only a failure reading the live-set query, or a fundamental
// failure walking the store's root (permission denied, root doesn't exist),
// aborts the whole run and returns an error -- there's no reasonable partial
// result to report in either of those cases.
func (r *Runner) Run(ctx context.Context, dryRun bool) (Result, error) {
	result := Result{DryRun: dryRun}

	paths, err := r.queries.ListReferencedArchivePaths(ctx)
	if err != nil {
		return result, fmt.Errorf("gc: listing referenced archive paths: %w", err)
	}
	live := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		live[p] = struct{}{}
	}

	err = r.store.Walk(func(relPath string, sizeBytes int64) error {
		result.FilesScanned++
		if _, ok := live[relPath]; ok {
			return nil
		}

		result.FilesRemoved++
		result.BytesReclaimed += sizeBytes
		if dryRun {
			return nil
		}

		if err := r.store.Remove(relPath); err != nil {
			r.logger.WarnContext(ctx, "gc: failed to remove orphaned file", "path", relPath, "error", err)
			result.RemoveErrors++
			// Correct the two tallies above: this one wasn't actually
			// removed, so it shouldn't count toward "removed" or
			// "reclaimed" -- but it was still correctly identified as
			// orphaned, which FilesScanned already reflects regardless.
			result.FilesRemoved--
			result.BytesReclaimed -= sizeBytes
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("gc: walking archive store: %w", err)
	}

	return result, nil
}
