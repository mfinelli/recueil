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

package archive_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
)

// The Store never reads, writes or removes anything outside its root, whatever path it is
// handed (internal/archive's resolve). The paths come from the database and from minted
// directories today, plus the one favicon extension that arrives with an upload, but the
// guarantee belongs to the Store rather than to each caller having checked.

// escapeSetup returns a Store rooted in a subdirectory, with a sentinel file beside the
// root (outside it) that nothing may touch.
func escapeSetup(t *testing.T) (store *archive.Store, root, outside string) {
	t.Helper()
	parent := t.TempDir()
	root = filepath.Join(parent, "archive")
	require.NoError(t, os.Mkdir(root, 0o755))
	outside = filepath.Join(parent, "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	return archive.New(root), root, outside
}

// A path that climbs out, is absolute, is empty, or names the root itself.
func badPaths(outside string) []string {
	return []string{
		"../outside.txt",
		"a/../../outside.txt",
		"a/b/../../../outside.txt",
		outside,
		"/etc/passwd",
		"",
		".",
		"..",
	}
}

func TestStore_RefusesPathsOutsideItsRoot(t *testing.T) {
	t.Run("Open and OpenRaw", func(t *testing.T) {
		store, _, outside := escapeSetup(t)
		for _, p := range badPaths(outside) {
			_, err := store.Open(p)
			assert.ErrorIs(t, err, archive.ErrInvalidPath, "Open(%q)", p)
			_, err = store.OpenRaw(p)
			assert.ErrorIs(t, err, archive.ErrInvalidPath, "OpenRaw(%q)", p)
		}
	})

	t.Run("Remove leaves what's outside alone", func(t *testing.T) {
		store, _, outside := escapeSetup(t)
		for _, p := range badPaths(outside) {
			assert.ErrorIs(t, store.Remove(p), archive.ErrInvalidPath, "Remove(%q)", p)
		}
		_, err := os.Stat(outside)
		assert.NoError(t, err, "the file outside the root is untouched")
	})

	t.Run("RemoveEmptyDir leaves the root and what's outside alone", func(t *testing.T) {
		store, root, outside := escapeSetup(t)
		for _, p := range badPaths(outside) {
			removed, err := store.RemoveEmptyDir(p)
			assert.ErrorIs(t, err, archive.ErrInvalidPath, "RemoveEmptyDir(%q)", p)
			assert.False(t, removed)
		}
		_, err := os.Stat(root)
		assert.NoError(t, err, "the root itself was not removed, though it is empty")
	})

	t.Run("WriteHTML refuses a directory that climbs out, writing nothing", func(t *testing.T) {
		store, root, outside := escapeSetup(t)
		parent := filepath.Dir(root)
		for _, dir := range []string{"../escape", "a/../../escape", outside, "/tmp/escape"} {
			_, _, err := store.WriteHTML(dir, []byte("<html></html>"))
			assert.ErrorIs(t, err, archive.ErrInvalidPath, "WriteHTML(%q)", dir)
		}
		entries, err := os.ReadDir(parent)
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		assert.ElementsMatch(t, []string{"archive", "outside.txt"}, names, "nothing was created beside the root")
	})

	t.Run("WriteAsset refuses a name or extension that could change where the file lands", func(t *testing.T) {
		store, root, _ := escapeSetup(t)
		relDir, err := store.NewCapture()
		require.NoError(t, err)

		for _, tc := range []struct{ name, ext string }{
			{"favicon", "png/../../x"},
			{"favicon", "../x"},
			{"favicon", "p.ng"},
			{"favicon", ""},
			{"../favicon", "png"},
			{"a/b", "png"},
			{"", "png"},
			{"fav icon", "png"},
			{"FAVICON", "png"},
		} {
			_, _, err := store.WriteAsset(relDir, tc.name, tc.ext, []byte("x"), false)
			assert.ErrorIs(t, err, archive.ErrInvalidPath, "WriteAsset(%q, %q)", tc.name, tc.ext)
		}

		// Only the capture directory NewCapture made exists, and it's empty.
		entries, err := os.ReadDir(filepath.Join(root, relDir))
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestStore_StillHandlesOrdinaryPaths(t *testing.T) {
	store, root, _ := escapeSetup(t)

	t.Run("a minted capture directory and its files round-trip", func(t *testing.T) {
		relDir, err := store.NewCapture()
		require.NoError(t, err)
		relPath, _, err := store.WriteHTML(relDir, []byte("<p>hello</p>"))
		require.NoError(t, err)
		asset, _, err := store.WriteAsset(relDir, "thumbnail", "png", []byte("png"), false)
		require.NoError(t, err)

		r, err := store.Open(relPath)
		require.NoError(t, err)
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, "<p>hello</p>", string(got))
		assert.FileExists(t, filepath.Join(root, asset))

		require.NoError(t, store.Remove(asset))
		assert.NoFileExists(t, filepath.Join(root, asset))
	})

	t.Run("a '..' that stays inside the root is fine", func(t *testing.T) {
		relDir, err := store.NewCapture()
		require.NoError(t, err)
		inside := filepath.Join(relDir, "sub", "..", "file.png")
		_, _, err = store.WriteAsset(filepath.Join(relDir, "sub", ".."), "file", "png", []byte("x"), false)
		require.NoError(t, err)
		r, err := store.Open(inside)
		require.NoError(t, err)
		_ = r.Close()
	})

	t.Run("a root given with a trailing slash or a dot segment still works", func(t *testing.T) {
		parent := t.TempDir()
		for _, rootArg := range []string{parent + "/", parent + "/./", filepath.Join(parent, ".")} {
			s := archive.New(rootArg)
			relDir, err := s.NewCapture()
			require.NoError(t, err)
			_, _, err = s.WriteHTML(relDir, []byte("x"))
			assert.NoError(t, err, "root %q", rootArg)
		}
	})
}
