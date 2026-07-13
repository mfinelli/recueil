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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
)

func TestStore_WriteAndOpen_RoundTrip(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	original := []byte(strings.Repeat("<html><body>hello world</body></html>", 1000))

	relPath, compressedSize, err := store.Write("capture-abc-123", original)
	require.NoError(t, err)
	assert.Positive(t, compressedSize)

	reader, err := store.Open(relPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, original, got)
}

func TestStore_Write_CompressesRepetitiveContent(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	// Highly repetitive text (like real HTML markup) should compress
	// substantially -- this isn't a precise ratio assertion, just a sanity
	// check that compression is actually happening, not a no-op passthrough.
	original := []byte(strings.Repeat("<div class=\"repeated-markup\">content</div>", 5000))

	_, compressedSize, err := store.Write("capture-compress-test", original)
	require.NoError(t, err)

	assert.Less(t, compressedSize, int64(len(original))/2,
		"expected meaningful compression on highly repetitive content")
}

func TestStore_Write_ShardsByKeyPrefix(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relPath, _, err := store.Write("ab34cdef-0000-0000-0000-000000000000", []byte("data"))
	require.NoError(t, err)

	assert.Equal(t, filepath.Join("ab", "34", "ab34cdef-0000-0000-0000-000000000000.html.zst"), relPath)

	// And the file genuinely exists on disk at that sharded location, not
	// just in the returned string.
	_, err = os.Stat(filepath.Join(root, relPath))
	require.NoError(t, err)
}

func TestStore_Write_FallsBackForShortIDs(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relPath, _, err := store.Write("ab", []byte("data"))
	require.NoError(t, err)

	assert.Equal(t, "ab.html.zst", relPath)
}

func TestStore_Write_CreatesDirectoriesAsNeeded(t *testing.T) {
	// A root that doesn't exist yet at all -- Write must create the full
	// sharded directory path underneath it, not assume it already exists.
	root := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")
	store := archive.New(root)

	relPath, _, err := store.Write("fresh0000-0000-0000-0000-000000000000", []byte("data"))
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(root, relPath))
	require.NoError(t, err)
}

func TestStore_Write_LeavesNoTempFilesBehind(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relPath, _, err := store.Write("clean0000-0000-0000-0000-000000000000", []byte("data"))
	require.NoError(t, err)

	dir := filepath.Dir(filepath.Join(root, relPath))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	require.Len(t, entries, 1, "exactly one file should exist: the final renamed file, no leftover temp file")
	assert.False(t, strings.HasPrefix(entries[0].Name(), ".tmp-"))
}

func TestStore_Write_OverwritesOnRetryWithSameKey(t *testing.T) {
	// a retry after a crash reuses the same key (in production,
	// content_hash -- see archive.go's package doc for why)
	// and must safely overwrite whatever's already at that path.
	root := t.TempDir()
	store := archive.New(root)

	relPath1, _, err := store.Write("retry0000-0000-0000-0000-000000000000", []byte("first attempt"))
	require.NoError(t, err)

	relPath2, _, err := store.Write("retry0000-0000-0000-0000-000000000000", []byte("second attempt, different content"))
	require.NoError(t, err)

	assert.Equal(t, relPath1, relPath2)

	reader, err := store.Open(relPath2)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "second attempt, different content", string(got))
}

func TestStore_Open_NonexistentPath(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	_, err := store.Open("nope/does/not/exist.html.zst")
	require.Error(t, err)
}
