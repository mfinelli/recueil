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

package gc_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/gc"
)

// backdate pushes relPath's modification time an hour into the past, past
// gc's own recentThreshold, so a sweep will actually consider it. Every
// file these tests create is by definition brand new, and gc deliberately
// leaves anything recently modified alone (an in-flight capture writes to
// disk before committing to Postgres, so it is legitimately unreferenced
// for a moment) -- without this, every orphan here would be correctly
// skipped and none of these tests would assert anything.
//
// Deliberately not done by making the threshold injectable and setting it
// to zero: these tests then exercise the real constant and the real
// comparison, the same reasoning that keeps dbtest on a real Postgres and
// internal/screenshot on a real sidecar.
func backdate(t *testing.T, root, relPath string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(root, relPath), old, old))
}

// writeOrphan writes a file that no Postgres row will ever reference --
// exactly what a page/capture delete leaves behind (see DeletePage/
// DeleteCapture's own doc comments) -- into its own capture directory,
// already backdated past recentThreshold.
func writeOrphan(t *testing.T, store *archive.Store, root string, content []byte) (relPath string, size int64) {
	t.Helper()
	relDir, err := store.NewCapture()
	require.NoError(t, err)
	relPath, size, err = store.WriteAsset(relDir, "favicon", "png", content, false)
	require.NoError(t, err)
	backdate(t, root, relPath)
	return relPath, size
}

func TestRunner_Run_RemovesOrphanedFilesButKeepsReferenced(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	user := dbtest.CreateUser(t, pool, "member")
	page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/gc-keeps-referenced")
	capture := dbtest.CreateCaptureWithHTML(t, pool, store, page.ID, []byte("<html>real, referenced</html>"))

	orphanRelPath, orphanSize := writeOrphan(t, store, root, []byte("orphaned bytes"))

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{})
	require.NoError(t, err)

	assert.False(t, result.DryRun)
	assert.Equal(t, 2, result.FilesScanned)
	assert.Equal(t, 1, result.FilesRemoved)
	assert.Equal(t, orphanSize, result.BytesReclaimed)
	assert.Zero(t, result.RemoveErrors)

	assert.NoFileExists(t, filepath.Join(root, orphanRelPath))

	// The referenced capture's own HTML must survive -- still openable
	// through the real Store API, not just "the bytes happen to still
	// be there."
	reader, err := store.Open(capture.HtmlPath)
	require.NoError(t, err)
	_ = reader.Close()
}

// The single most important property of the whole scheme now that captures
// no longer share directories: two captures with byte-for-byte identical
// HTML own two independent copies, so deleting one cannot take the other's
// file with it. Under the previous content-hash-addressed layout both rows
// pointed at one file and this test could not have been written.
func TestRunner_Run_IdenticalContentCapturesDoNotShareAFile(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	identical := []byte("<html>byte for byte the same</html>")

	user := dbtest.CreateUser(t, pool, "member")
	pageA := dbtest.CreatePage(t, pool, user.ID, "https://example.com/gc-identical-a")
	pageB := dbtest.CreatePage(t, pool, user.ID, "https://example.com/gc-identical-b")
	captureA := dbtest.CreateCaptureWithHTML(t, pool, store, pageA.ID, identical)
	captureB := dbtest.CreateCaptureWithHTML(t, pool, store, pageB.ID, identical)

	require.NotEqual(t, captureA.HtmlPath, captureB.HtmlPath,
		"identical content must still produce two distinct paths")

	// Delete one of them, then sweep: the other's file must be untouched.
	_, err := pool.Exec(ctx, `DELETE FROM pages WHERE id = $1`, pageA.ID)
	require.NoError(t, err)
	backdate(t, root, captureA.HtmlPath)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(ctx, gc.Options{})
	require.NoError(t, err)

	assert.Equal(t, 1, result.FilesRemoved)
	assert.NoFileExists(t, filepath.Join(root, captureA.HtmlPath))

	reader, err := store.Open(captureB.HtmlPath)
	require.NoError(t, err)
	_ = reader.Close()
}

func TestRunner_Run_LeavesRecentlyModifiedFilesAlone(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	// Written and *not* backdated: an unreferenced file this new is
	// indistinguishable from an ingestion currently in flight, which
	// writes to disk before committing to Postgres (DESIGN.md §3c).
	relDir, err := store.NewCapture()
	require.NoError(t, err)
	relPath, _, err := store.WriteAsset(relDir, "favicon", "png", []byte("in flight"), false)
	require.NoError(t, err)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{})
	require.NoError(t, err)

	assert.Equal(t, 1, result.FilesScanned)
	assert.Equal(t, 1, result.FilesSkippedRecent)
	assert.Zero(t, result.FilesRemoved)
	assert.FileExists(t, filepath.Join(root, relPath),
		"a file that may still be being written must survive the sweep")
}

// archive.Store's own temp files are the sharpest case of the above: they
// live in the walk's namespace and can never appear in the live set, so
// without the recency check a sweep would delete one mid-write and the
// writer's rename would then fail.
func TestRunner_Run_LeavesArchiveTempFilesAlone(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	relDir, err := store.NewCapture()
	require.NoError(t, err)
	tmp, err := os.CreateTemp(filepath.Join(root, relDir), ".tmp-*")
	require.NoError(t, err)
	require.NoError(t, tmp.Close())

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{})
	require.NoError(t, err)

	assert.Equal(t, 1, result.FilesSkippedRecent)
	assert.Zero(t, result.FilesRemoved)
	assert.FileExists(t, tmp.Name())
}

// NewCapture creates a capture's directory before anything is written
// into it, so an ingestion that fails early leaves a directory Walk (which
// reports regular files only) would never see.
func TestRunner_Run_PrunesEmptyCaptureDirectories(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	relDir, err := store.NewCapture()
	require.NoError(t, err)
	backdate(t, root, relDir)
	require.DirExists(t, filepath.Join(root, relDir))

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{})
	require.NoError(t, err)

	assert.Zero(t, result.FilesScanned, "an empty directory contains no files to scan")
	assert.Equal(t, 1, result.EmptyDirsRemoved)
	assert.NoDirExists(t, filepath.Join(root, relDir))

	// The shard directories above it collapse too, rather than
	// accumulating forever.
	assert.NoDirExists(t, filepath.Dir(filepath.Join(root, relDir)))
}

func TestRunner_Run_LeavesRecentlyCreatedEmptyDirectoriesAlone(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	// Not backdated: this is what a capture directory looks like in the
	// instant between NewCapture and WriteHTML.
	relDir, err := store.NewCapture()
	require.NoError(t, err)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{})
	require.NoError(t, err)

	assert.Zero(t, result.EmptyDirsRemoved)
	assert.DirExists(t, filepath.Join(root, relDir))
}

func TestRunner_Run_DryRunReportsButDoesNotDelete(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	user := dbtest.CreateUser(t, pool, "member")
	page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/gc-dry-run")
	dbtest.CreateCaptureWithHTML(t, pool, store, page.ID, []byte("<html>keep me</html>"))

	orphanRelPath, orphanSize := writeOrphan(t, store, root, []byte("bytes"))

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{DryRun: true})
	require.NoError(t, err)

	assert.True(t, result.DryRun)
	assert.Equal(t, 1, result.FilesRemoved, "dry run still identifies what WOULD be removed")
	assert.Equal(t, orphanSize, result.BytesReclaimed)
	assert.Zero(t, result.RemoveErrors, "nothing is actually attempted in dry-run mode")

	assert.FileExists(t, filepath.Join(root, orphanRelPath), "dry run must not touch disk at all")
}

func TestRunner_Run_ProtectsPagesOwnDenormalizedFaviconEvenWithoutACapturePointingAtIt(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	user := dbtest.CreateUser(t, pool, "member")
	page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/gc-protects-page-favicon")
	dbtest.CreateCaptureWithHTML(t, pool, store, page.ID, []byte("<html>current capture, no favicon of its own</html>"))

	// Simulates exactly the scenario SetPageFavicon/ListReferencedArchivePaths's
	// doc comments describe: pages.favicon_path pointing at a real
	// file that (for whatever reason -- an older capture already
	// deleted, in production) no *capture* row references anymore. GC
	// must still keep it, because ListReferencedArchivePaths includes
	// pages.favicon_path in its own right.
	faviconRelPath, _ := writeOrphan(t, store, root, []byte("favicon bytes"))
	_, err := pool.Exec(context.Background(), `UPDATE pages SET favicon_path = $1 WHERE id = $2`, faviconRelPath, page.ID)
	require.NoError(t, err)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{})
	require.NoError(t, err)

	assert.Zero(t, result.FilesRemoved, "the page's own favicon_path must count as referenced")
	assert.FileExists(t, filepath.Join(root, faviconRelPath))
}

// writeManyOrphans writes enough unreferenced files to clear gc's own
// safetyCheckMinFiles floor, so the orphan-fraction check actually
// applies. All of them are orphans, so the fraction is 100%.
func writeManyOrphans(t *testing.T, store *archive.Store, root string) []string {
	t.Helper()
	const count = 101
	paths := make([]string, 0, count)
	for i := range count {
		relPath, _ := writeOrphan(t, store, root, fmt.Appendf(nil, "orphan %d", i))
		paths = append(paths, relPath)
	}
	return paths
}

func TestRunner_Run_RefusesWhenOrphanFractionExceedsThreshold(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	paths := writeManyOrphans(t, store, root)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{})

	var tooMany *gc.TooManyOrphansError
	require.ErrorAs(t, err, &tooMany)
	assert.Equal(t, len(paths), tooMany.Orphans)
	assert.Equal(t, len(paths), tooMany.Scanned)

	assert.True(t, result.SafetyCheckTripped)
	assert.Zero(t, result.FilesRemoved, "the refusal must be clean -- nothing removed at all")

	// The check runs after the full scan but before the first deletion,
	// so every single file must still be there, not just most of them.
	for _, relPath := range paths {
		assert.FileExists(t, filepath.Join(root, relPath))
	}
}

func TestRunner_Run_ForceBypassesTheOrphanFractionThreshold(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	paths := writeManyOrphans(t, store, root)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{Force: true})
	require.NoError(t, err)

	assert.True(t, result.SafetyCheckTripped, "still reported, even though it was overridden")
	assert.Equal(t, len(paths), result.FilesRemoved)
	for _, relPath := range paths {
		assert.NoFileExists(t, filepath.Join(root, relPath))
	}
}

func TestRunner_Run_DryRunReportsTheThresholdWithoutErroring(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	paths := writeManyOrphans(t, store, root)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{DryRun: true})

	// A dry run that silently omitted the fact that the real run would
	// refuse would be actively misleading, so it reports rather than
	// errors.
	require.NoError(t, err)
	assert.True(t, result.SafetyCheckTripped)
	assert.Equal(t, len(paths), result.FilesRemoved)
	for _, relPath := range paths {
		assert.FileExists(t, filepath.Join(root, relPath))
	}
}

// Below safetyCheckMinFiles the fraction carries no information -- a
// handful of files that are all orphans is 100% and means nothing.
func TestRunner_Run_ThresholdNotAppliedToSmallArchives(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	relPath, _ := writeOrphan(t, store, root, []byte("the only file here"))

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{})
	require.NoError(t, err)

	assert.False(t, result.SafetyCheckTripped)
	assert.Equal(t, 1, result.FilesRemoved)
	assert.NoFileExists(t, filepath.Join(root, relPath))
}

func TestRunner_Run_EmptyEverything(t *testing.T) {
	pool := dbtest.Setup(t)
	store := archive.New(t.TempDir())

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), gc.Options{})
	require.NoError(t, err)

	assert.Zero(t, result.FilesScanned)
	assert.Zero(t, result.FilesRemoved)
	assert.Zero(t, result.BytesReclaimed)
	assert.Zero(t, result.EmptyDirsRemoved)
	assert.Zero(t, result.RemoveErrors)
}
