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

func TestStore_WriteHTMLAndOpen_RoundTrip(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	original := []byte(strings.Repeat("<html><body>hello world</body></html>", 1000))

	relPath, compressedSize, err := store.WriteHTML("capture-abc-123", original)
	require.NoError(t, err)
	assert.Positive(t, compressedSize)

	reader, err := store.Open(relPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, original, got)
}

func TestStore_WriteHTML_CompressesRepetitiveContent(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	// Highly repetitive text (like real HTML markup) should compress
	// substantially -- this isn't a precise ratio assertion, just a sanity
	// check that compression is actually happening, not a no-op passthrough.
	original := []byte(strings.Repeat("<div class=\"repeated-markup\">content</div>", 5000))

	_, compressedSize, err := store.WriteHTML("capture-compress-test", original)
	require.NoError(t, err)

	assert.Less(t, compressedSize, int64(len(original))/2,
		"expected meaningful compression on highly repetitive content")
}

func TestStore_WriteHTML_ShardsByHashPrefix(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relPath, _, err := store.WriteHTML("ab34cdef-0000-0000-0000-000000000000", []byte("data"))
	require.NoError(t, err)

	assert.Equal(t,
		filepath.Join("ab", "34", "ab34cdef-0000-0000-0000-000000000000", "page.html.zst"),
		relPath)

	// And the file genuinely exists on disk at that sharded location, not
	// just in the returned string.
	_, err = os.Stat(filepath.Join(root, relPath))
	require.NoError(t, err)
}

func TestStore_WriteHTML_FallsBackForShortHashes(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relPath, _, err := store.WriteHTML("ab", []byte("data"))
	require.NoError(t, err)

	assert.Equal(t, filepath.Join("ab", "page.html.zst"), relPath)
}

func TestStore_WriteHTML_CreatesDirectoriesAsNeeded(t *testing.T) {
	// A root that doesn't exist yet at all -- WriteHTML must create the
	// full sharded directory path underneath it, not assume it already
	// exists.
	root := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")
	store := archive.New(root)

	relPath, _, err := store.WriteHTML("fresh0000-0000-0000-0000-000000000000", []byte("data"))
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(root, relPath))
	require.NoError(t, err)
}

func TestStore_WriteHTML_LeavesNoTempFilesBehind(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relPath, _, err := store.WriteHTML("clean0000-0000-0000-0000-000000000000", []byte("data"))
	require.NoError(t, err)

	dir := filepath.Dir(filepath.Join(root, relPath))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	require.Len(t, entries, 1, "exactly one file should exist: the final renamed file, no leftover temp file")
	assert.False(t, strings.HasPrefix(entries[0].Name(), ".tmp-"))
}

func TestStore_WriteHTML_OverwritesOnRetryWithSameHash(t *testing.T) {
	// a retry after a crash reuses the same html content hash (the
	// content hasn't changed) and therefore the same target path, and
	// must safely overwrite whatever's already there -- see archive.go's
	// package doc for why this is always safe for WriteHTML specifically.
	root := t.TempDir()
	store := archive.New(root)

	relPath1, _, err := store.WriteHTML("retry0000-0000-0000-0000-000000000000", []byte("first attempt"))
	require.NoError(t, err)

	relPath2, _, err := store.WriteHTML("retry0000-0000-0000-0000-000000000000", []byte("second attempt, different content"))
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

	_, err := store.Open("nope/does/not/exist/page.html.zst")
	require.Error(t, err)
}

func TestStore_WriteAsset_LivesAlongsideHTMLInSameCaptureDir(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	htmlHash := "cafe0000-0000-0000-0000-000000000000"
	htmlPath, _, err := store.WriteHTML(htmlHash, []byte("<html></html>"))
	require.NoError(t, err)

	faviconPath, _, err := store.WriteAsset(htmlHash, "favicon-hash-111", "png", []byte("fake-png-bytes"), false)
	require.NoError(t, err)

	assert.Equal(t, filepath.Dir(htmlPath), filepath.Dir(faviconPath),
		"the html file and its favicon should live in the same capture directory")
	assert.Equal(t, filepath.Join(archive.CaptureDir(htmlHash), "favicon-hash-111.png"), faviconPath)
}

func TestStore_WriteAsset_KeyedByItsOwnHashNotHTMLHash(t *testing.T) {
	// Two captures with byte-identical HTML (same htmlHash) but different
	// favicons must not collide on disk -- each favicon is keyed by its
	// own content hash, not the shared html hash. This is the specific
	// bug this design avoids; see archive.go's package doc.
	root := t.TempDir()
	store := archive.New(root)

	htmlHash := "shared0000-0000-0000-0000-000000000000"

	path1, _, err := store.WriteAsset(htmlHash, "favicon-old", "png", []byte("old favicon bytes"), false)
	require.NoError(t, err)

	path2, _, err := store.WriteAsset(htmlHash, "favicon-new", "svg", []byte("<svg>new favicon</svg>"), true)
	require.NoError(t, err)

	assert.NotEqual(t, path1, path2)

	// Both must still be readable independently -- writing the second
	// must not have clobbered the first.
	r1, err := store.Open(path1)
	require.NoError(t, err)
	defer func() { _ = r1.Close() }()
	got1, err := io.ReadAll(r1)
	require.NoError(t, err)
	assert.Equal(t, "old favicon bytes", string(got1))

	r2, err := store.Open(path2)
	require.NoError(t, err)
	defer func() { _ = r2.Close() }()
	got2, err := io.ReadAll(r2)
	require.NoError(t, err)
	assert.Equal(t, "<svg>new favicon</svg>", string(got2))
}

func TestStore_WriteAsset_CompressTrueAppendsZstExtensionAndCompresses(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	original := []byte(strings.Repeat("<svg><!-- repeated --></svg>", 5000))

	relPath, writtenSize, err := store.WriteAsset("html0000-0000-0000-0000-000000000000", "favicon-svg", "svg", original, true)
	require.NoError(t, err)

	assert.True(t, strings.HasSuffix(relPath, "favicon-svg.svg.zst"))

	reader, err := store.Open(relPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, original, got)

	info, err := os.Stat(filepath.Join(root, relPath))
	require.NoError(t, err)
	assert.Equal(t, info.Size(), writtenSize,
		"the returned writtenSize should match the actual on-disk (compressed) size")
	assert.Less(t, writtenSize, int64(len(original))/2,
		"expected meaningful compression on highly repetitive content")
}

func TestStore_WriteAsset_CompressFalseStoresRawBytes(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	original := []byte("not-actually-a-png-but-treated-as-opaque-bytes")

	relPath, _, err := store.WriteAsset("html0000-0000-0000-0000-000000000000", "favicon-png", "png", original, false)
	require.NoError(t, err)

	assert.True(t, strings.HasSuffix(relPath, "favicon-png.png"),
		"uncompressed assets keep a plain extension, no .zst suffix")

	// Stored byte-for-byte with no zstd framing -- readable directly off
	// disk without going through Store.Open at all.
	onDisk, err := os.ReadFile(filepath.Join(root, relPath))
	require.NoError(t, err)
	assert.Equal(t, original, onDisk)

	reader, err := store.Open(relPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	got, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, original, got)
}

func TestCaptureDir_ShardsByHashPrefix(t *testing.T) {
	assert.Equal(t,
		filepath.Join("ab", "34", "ab34cdef-0000-0000-0000-000000000000"),
		archive.CaptureDir("ab34cdef-0000-0000-0000-000000000000"))
}

func TestCaptureDir_FallsBackForShortHashes(t *testing.T) {
	assert.Equal(t, "ab", archive.CaptureDir("ab"))
}
