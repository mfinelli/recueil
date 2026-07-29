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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
	"github.com/mfinelli/recueil/internal/db"
	"github.com/mfinelli/recueil/internal/dbtest"
	"github.com/mfinelli/recueil/internal/gc"
)

func TestRunner_Run_RemovesOrphanedFilesButKeepsReferenced(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	user := dbtest.CreateUser(t, pool, "member")
	page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/gc-keeps-referenced")
	capture := dbtest.CreateCaptureWithHTML(t, pool, store, page.ID, []byte("<html>real, referenced</html>"))

	// A file with no corresponding row in Postgres at all -- exactly
	// what a page/capture delete leaves behind (see DeletePage/
	// DeleteCapture's own doc comments).
	orphanRelPath, orphanSize, err := store.WriteAsset("some-unrelated-html-hash", "orphaned-favicon-hash", "png", []byte("orphaned bytes"), false)
	require.NoError(t, err)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), false)
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

func TestRunner_Run_OrphanIsActuallyGoneFromDisk(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	user := dbtest.CreateUser(t, pool, "member")
	page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/gc-orphan-gone")
	dbtest.CreateCaptureWithHTML(t, pool, store, page.ID, []byte("<html>keep me</html>"))

	orphanRelPath, _, err := store.WriteAsset("another-unrelated-hash", "orphan-2", "png", []byte("bytes"), false)
	require.NoError(t, err)
	orphanAbsPath := filepath.Join(root, orphanRelPath)
	require.FileExists(t, orphanAbsPath)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	_, err = runner.Run(context.Background(), false)
	require.NoError(t, err)

	assert.NoFileExists(t, orphanAbsPath)
}

func TestRunner_Run_DryRunReportsButDoesNotDelete(t *testing.T) {
	pool := dbtest.Setup(t)
	root := t.TempDir()
	store := archive.New(root)

	user := dbtest.CreateUser(t, pool, "member")
	page := dbtest.CreatePage(t, pool, user.ID, "https://example.com/gc-dry-run")
	dbtest.CreateCaptureWithHTML(t, pool, store, page.ID, []byte("<html>keep me</html>"))

	orphanRelPath, orphanSize, err := store.WriteAsset("dry-run-unrelated-hash", "orphan-3", "png", []byte("bytes"), false)
	require.NoError(t, err)
	orphanAbsPath := filepath.Join(root, orphanRelPath)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), true)
	require.NoError(t, err)

	assert.True(t, result.DryRun)
	assert.Equal(t, 1, result.FilesRemoved, "dry run still identifies what WOULD be removed")
	assert.Equal(t, orphanSize, result.BytesReclaimed)
	assert.Zero(t, result.RemoveErrors, "nothing is actually attempted in dry-run mode")

	assert.FileExists(t, orphanAbsPath, "dry run must not touch disk at all")
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
	faviconRelPath, _, err := store.WriteAsset("favicon-owner-hash", "still-needed-favicon", "png", []byte("favicon bytes"), false)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `UPDATE pages SET favicon_path = $1 WHERE id = $2`, faviconRelPath, page.ID)
	require.NoError(t, err)

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), false)
	require.NoError(t, err)

	assert.Zero(t, result.FilesRemoved, "the page's own favicon_path must count as referenced")
	assert.FileExists(t, filepath.Join(root, faviconRelPath))
}

func TestRunner_Run_EmptyEverything(t *testing.T) {
	pool := dbtest.Setup(t)
	store := archive.New(t.TempDir())

	runner := gc.NewRunner(gc.RunnerParams{Queries: db.New(pool), Store: store})
	result, err := runner.Run(context.Background(), false)
	require.NoError(t, err)

	assert.Zero(t, result.FilesScanned)
	assert.Zero(t, result.FilesRemoved)
	assert.Zero(t, result.BytesReclaimed)
	assert.Zero(t, result.RemoveErrors)
}
