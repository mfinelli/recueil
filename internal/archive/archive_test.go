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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
)

// newCapture is the two-line preamble almost every test here needs: mint a
// capture directory and fail loudly if that didn't work, since nothing
// downstream is meaningful without it.
func newCapture(t *testing.T, store *archive.Store) string {
	t.Helper()
	relDir, err := store.NewCapture()
	require.NoError(t, err)
	return relDir
}

func TestStore_NewCapture_CreatesTheDirectoryOnDisk(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relDir := newCapture(t, store)

	assert.DirExists(t, filepath.Join(root, relDir))
	entries, err := os.ReadDir(filepath.Join(root, relDir))
	require.NoError(t, err)
	assert.Empty(t, entries, "a freshly minted capture directory starts empty")
}

func TestStore_NewCapture_ShardsByTrailingCharacters(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relDir := newCapture(t, store)

	parts := strings.Split(relDir, string(filepath.Separator))
	require.Len(t, parts, 3, "three levels of sharding: {shard}/{shard}/{id}")

	id := parts[2]
	assert.Len(t, id, 36, "a full UUID string")

	// Trailing, not leading: UUIDv7's leading bits are a millisecond
	// timestamp, so sharding on those would drop everything captured in
	// the same period into one bucket. The last group is entirely
	// rand_b.
	assert.Equal(t, id[len(id)-4:len(id)-2], parts[0])
	assert.Equal(t, id[len(id)-2:], parts[1])
}

func TestStore_NewCapture_NeverReturnsTheSameDirectoryTwice(t *testing.T) {
	store := archive.New(t.TempDir())

	// Enough iterations to make a same-millisecond batch certain, which
	// is where a v7 id has only its random bits to distinguish it.
	const iterations = 500
	seen := make(map[string]struct{}, iterations)
	for range iterations {
		relDir := newCapture(t, store)
		_, dup := seen[relDir]
		require.False(t, dup, "NewCapture returned a directory it had already returned: %s", relDir)
		seen[relDir] = struct{}{}
	}
}

// The property the whole layout exists for: identical content does not
// collapse two captures onto one directory the way the previous
// content-hash-addressed scheme did.
func TestStore_NewCapture_IdenticalContentStillGetsSeparateDirectories(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)
	identical := []byte("<html>byte for byte the same</html>")

	pathA, _, err := store.WriteHTML(newCapture(t, store), identical)
	require.NoError(t, err)
	pathB, _, err := store.WriteHTML(newCapture(t, store), identical)
	require.NoError(t, err)

	assert.NotEqual(t, pathA, pathB)
	assert.FileExists(t, filepath.Join(root, pathA))
	assert.FileExists(t, filepath.Join(root, pathB))

	// Not just distinct paths -- two genuinely independent files, so
	// removing one leaves the other entirely intact.
	require.NoError(t, store.Remove(pathA))
	assert.NoFileExists(t, filepath.Join(root, pathA))
	assert.FileExists(t, filepath.Join(root, pathB))
}

// The EEXIST branch inside NewCapture cannot be forced from outside the
// package without injecting the id generator, and injecting it purely to
// reach a branch guarding an unreachable event isn't worth the seam. What
// this checks instead is the property that branch exists to protect: an
// already-populated capture directory is never handed back out and
// written into, which is exactly how one capture's bytes would come to
// overwrite another's.
func TestStore_NewCapture_NeverHandsBackAnAlreadyPopulatedDirectory(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	first := newCapture(t, store)
	marker := filepath.Join(root, first, "already-here")
	require.NoError(t, os.WriteFile(marker, []byte("do not clobber me"), 0o644))

	for range 20 {
		next := newCapture(t, store)
		require.NotEqual(t, first, next)
	}

	content, err := os.ReadFile(marker)
	require.NoError(t, err)
	assert.Equal(t, "do not clobber me", string(content))
}

func TestStore_NewCapture_CreatesRootWhenItDoesNotExist(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not", "created", "yet")
	store := archive.New(root)

	relDir := newCapture(t, store)
	assert.DirExists(t, filepath.Join(root, relDir))
}

func TestStore_WriteHTMLAndOpen_RoundTrip(t *testing.T) {
	store := archive.New(t.TempDir())
	original := []byte("<html><body>hello, archive</body></html>")

	relPath, compressedSize, err := store.WriteHTML(newCapture(t, store), original)
	require.NoError(t, err)
	assert.Positive(t, compressedSize)

	reader, err := store.Open(relPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	roundTripped, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, original, roundTripped)
}

func TestStore_WriteHTML_UsesAFixedFilenameInsideTheCaptureDirectory(t *testing.T) {
	store := archive.New(t.TempDir())
	relDir := newCapture(t, store)

	relPath, _, err := store.WriteHTML(relDir, []byte("<html></html>"))
	require.NoError(t, err)

	// The directory identifies the capture, so the filename doesn't have
	// to -- there is exactly one HTML file in here.
	assert.Equal(t, filepath.Join(relDir, "page.html.zst"), relPath)
}

func TestStore_OpenRaw_ReturnsCompressedBytesUnmodified(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)
	original := []byte(strings.Repeat("<p>compress me</p>", 200))

	relPath, compressedSize, err := store.WriteHTML(newCapture(t, store), original)
	require.NoError(t, err)

	reader, err := store.OpenRaw(relPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()

	raw, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, compressedSize, int64(len(raw)))
	assert.NotEqual(t, original, raw, "OpenRaw must not decompress")
}

func TestStore_OpenRaw_NonexistentPath(t *testing.T) {
	store := archive.New(t.TempDir())

	_, err := store.OpenRaw(filepath.Join("does", "not", "exist.html.zst"))
	require.Error(t, err)
}

func TestStore_Open_NonexistentPath(t *testing.T) {
	store := archive.New(t.TempDir())

	_, err := store.Open(filepath.Join("does", "not", "exist.html.zst"))
	require.Error(t, err)
}

func TestStore_WriteHTML_CompressesRepetitiveContent(t *testing.T) {
	store := archive.New(t.TempDir())
	original := []byte(strings.Repeat("<div class=\"row\">the same thing again</div>", 500))

	_, compressedSize, err := store.WriteHTML(newCapture(t, store), original)
	require.NoError(t, err)

	assert.Less(t, compressedSize, int64(len(original))/4,
		"zstd should compress this kind of markup dramatically")
}

func TestStore_WriteHTML_LeavesNoTempFilesBehind(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)
	relDir := newCapture(t, store)

	_, _, err := store.WriteHTML(relDir, []byte("data"))
	require.NoError(t, err)

	entries, err := os.ReadDir(filepath.Join(root, relDir))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "page.html.zst", entries[0].Name())
}

func TestStore_WriteHTML_OverwritesInPlaceForTheSameCapture(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)
	relDir := newCapture(t, store)

	relPath1, _, err := store.WriteHTML(relDir, []byte("first attempt"))
	require.NoError(t, err)
	relPath2, _, err := store.WriteHTML(relDir, []byte("second attempt, different content"))
	require.NoError(t, err)
	require.Equal(t, relPath1, relPath2)

	reader, err := store.Open(relPath2)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, "second attempt, different content", string(content),
		"the second write wins cleanly, with no partial file left behind")

	entries, err := os.ReadDir(filepath.Join(root, relDir))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "an overwrite leaves nothing superseded behind for gc to find")
}

func TestStore_WriteAsset_LivesAlongsideHTMLInTheSameCaptureDir(t *testing.T) {
	store := archive.New(t.TempDir())
	relDir := newCapture(t, store)

	htmlPath, _, err := store.WriteHTML(relDir, []byte("<html></html>"))
	require.NoError(t, err)
	faviconPath, _, err := store.WriteAsset(relDir, "favicon", "png", []byte("fake-png-bytes"), false)
	require.NoError(t, err)

	assert.Equal(t, filepath.Dir(htmlPath), filepath.Dir(faviconPath))
	assert.Equal(t, filepath.Join(relDir, "favicon.png"), faviconPath)
}

func TestStore_WriteAsset_DistinctRolesCoexist(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)
	relDir := newCapture(t, store)

	faviconPath, _, err := store.WriteAsset(relDir, "favicon", "png", []byte("favicon bytes"), false)
	require.NoError(t, err)
	thumbnailPath, _, err := store.WriteAsset(relDir, "thumbnail", "png", []byte("thumbnail bytes"), false)
	require.NoError(t, err)

	require.NotEqual(t, faviconPath, thumbnailPath)
	assert.FileExists(t, filepath.Join(root, faviconPath))
	assert.FileExists(t, filepath.Join(root, thumbnailPath))
}

// A re-render (a retried screenshot job, a re-extraction after a
// Readability.js upgrade) replaces the asset rather than accumulating a
// second copy that gc would later have to notice.
func TestStore_WriteAsset_ReRenderOverwritesRatherThanAccumulating(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)
	relDir := newCapture(t, store)

	firstPath, _, err := store.WriteAsset(relDir, "thumbnail", "png", []byte("first render"), false)
	require.NoError(t, err)
	secondPath, _, err := store.WriteAsset(relDir, "thumbnail", "png", []byte("second render, different bytes"), false)
	require.NoError(t, err)
	require.Equal(t, firstPath, secondPath)

	entries, err := os.ReadDir(filepath.Join(root, relDir))
	require.NoError(t, err)
	assert.Len(t, entries, 1)

	content, err := os.ReadFile(filepath.Join(root, secondPath))
	require.NoError(t, err)
	assert.Equal(t, "second render, different bytes", string(content))
}

func TestStore_WriteAsset_CompressTrueAppendsZstExtensionAndCompresses(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)
	original := []byte(strings.Repeat(`<svg><rect width="10" height="10"/></svg>`, 200))

	relPath, writtenSize, err := store.WriteAsset(newCapture(t, store), "favicon", "svg", original, true)
	require.NoError(t, err)

	assert.True(t, strings.HasSuffix(relPath, "favicon.svg.zst"))
	assert.Less(t, writtenSize, int64(len(original)))

	info, err := os.Stat(filepath.Join(root, relPath))
	require.NoError(t, err)
	assert.Equal(t, info.Size(), writtenSize,
		"writtenSize must be the real on-disk size, not len(data)")

	reader, err := store.Open(relPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	roundTripped, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, original, roundTripped)
}

func TestStore_WriteAsset_CompressFalseStoresRawBytes(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)
	original := []byte("\x89PNG\r\n\x1a\n already-compressed bytes")

	relPath, writtenSize, err := store.WriteAsset(newCapture(t, store), "favicon", "png", original, false)
	require.NoError(t, err)

	assert.False(t, strings.HasSuffix(relPath, ".zst"))
	assert.Equal(t, int64(len(original)), writtenSize)

	onDisk, err := os.ReadFile(filepath.Join(root, relPath))
	require.NoError(t, err)
	assert.Equal(t, original, onDisk, "stored byte-for-byte, no compression applied")

	reader, err := store.Open(relPath)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	roundTripped, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, original, roundTripped, "Open must not try to decompress a non-.zst path")
}

func TestStore_Walk_FindsEveryRegularFile(t *testing.T) {
	store := archive.New(t.TempDir())
	relDir := newCapture(t, store)

	htmlPath, _, err := store.WriteHTML(relDir, []byte("<html></html>"))
	require.NoError(t, err)
	assetPath, _, err := store.WriteAsset(relDir, "favicon", "png", []byte("png-bytes"), false)
	require.NoError(t, err)

	found := map[string]int64{}
	err = store.Walk(func(relPath string, sizeBytes int64, _ time.Time) error {
		found[relPath] = sizeBytes
		return nil
	})
	require.NoError(t, err)

	assert.Len(t, found, 2)
	assert.Contains(t, found, htmlPath)
	assert.Contains(t, found, assetPath)
	assert.Positive(t, found[htmlPath])
}

func TestStore_Walk_ReportsModTime(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relPath, _, err := store.WriteHTML(newCapture(t, store), []byte("<html></html>"))
	require.NoError(t, err)

	backdated := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(root, relPath), backdated, backdated))

	var got time.Time
	require.NoError(t, store.Walk(func(_ string, _ int64, modTime time.Time) error {
		got = modTime
		return nil
	}))

	assert.WithinDuration(t, backdated, got, time.Second)
}

func TestStore_Walk_EmptyStore(t *testing.T) {
	store := archive.New(t.TempDir())

	calls := 0
	require.NoError(t, store.Walk(func(string, int64, time.Time) error {
		calls++
		return nil
	}))
	assert.Zero(t, calls)
}

// A Store pointed at a directory that doesn't exist yet has zero files,
// which is exactly what an empty walk reports -- gc running before the
// first capture ever lands shouldn't be an error.
func TestStore_Walk_RootDoesNotExist(t *testing.T) {
	store := archive.New(filepath.Join(t.TempDir(), "never", "created"))

	calls := 0
	require.NoError(t, store.Walk(func(string, int64, time.Time) error {
		calls++
		return nil
	}))
	assert.Zero(t, calls)
}

func TestStore_Walk_PropagatesCallbackError(t *testing.T) {
	store := archive.New(t.TempDir())
	_, _, err := store.WriteHTML(newCapture(t, store), []byte("<html></html>"))
	require.NoError(t, err)

	sentinel := io.ErrUnexpectedEOF
	err = store.Walk(func(string, int64, time.Time) error { return sentinel })
	assert.ErrorIs(t, err, sentinel)
}

func TestStore_WalkEmptyDirs_FindsACaptureDirWithNothingInIt(t *testing.T) {
	store := archive.New(t.TempDir())

	// One capture that got as far as writing, one that didn't -- the
	// second is exactly what an ingestion failing right after
	// NewCapture leaves behind.
	written := newCapture(t, store)
	_, _, err := store.WriteHTML(written, []byte("<html></html>"))
	require.NoError(t, err)
	empty := newCapture(t, store)

	var found []string
	require.NoError(t, store.WalkEmptyDirs(func(relPath string, _ time.Time) error {
		found = append(found, relPath)
		return nil
	}))

	assert.Equal(t, []string{empty}, found,
		"only the directory containing nothing at all, and never the shard dirs above a populated one")
}

func TestStore_WalkEmptyDirs_NeverReportsTheRootItself(t *testing.T) {
	store := archive.New(t.TempDir())

	calls := 0
	require.NoError(t, store.WalkEmptyDirs(func(string, time.Time) error {
		calls++
		return nil
	}))
	assert.Zero(t, calls, "an empty root is not something to prune")
}

func TestStore_Remove_DeletesTheFile(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relPath, _, err := store.WriteHTML(newCapture(t, store), []byte("<html></html>"))
	require.NoError(t, err)

	require.NoError(t, store.Remove(relPath))
	assert.NoFileExists(t, filepath.Join(root, relPath))
}

func TestStore_Remove_PrunesNowEmptyShardDirectories(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relDir := newCapture(t, store)
	relPath, _, err := store.WriteHTML(relDir, []byte("<html></html>"))
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(root, relDir))

	require.NoError(t, store.Remove(relPath))

	assert.NoDirExists(t, filepath.Join(root, relDir))
	// Both shard levels above it collapse too.
	assert.NoDirExists(t, filepath.Dir(filepath.Join(root, relDir)))
	assert.NoDirExists(t, filepath.Dir(filepath.Dir(filepath.Join(root, relDir))))
	assert.DirExists(t, root, "pruning never climbs past the store's own root")
}

func TestStore_Remove_StopsPruningAtANonEmptySiblingDirectory(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	relDir := newCapture(t, store)
	relPath, _, err := store.WriteHTML(relDir, []byte("<html></html>"))
	require.NoError(t, err)

	// A sibling capture inside the same leaf shard directory, forced
	// rather than hoped for: NewCapture's ids are random, so waiting for
	// two to land in the same bucket by chance would be flaky.
	siblingDir := filepath.Join(filepath.Dir(relDir), "sibling-capture-id")
	require.NoError(t, os.MkdirAll(filepath.Join(root, siblingDir), 0o755))
	_, _, err = store.WriteAsset(siblingDir, "favicon", "png", []byte("x"), false)
	require.NoError(t, err)

	require.NoError(t, store.Remove(relPath))

	assert.NoDirExists(t, filepath.Join(root, relDir), "the emptied capture dir goes")
	assert.DirExists(t, filepath.Join(root, filepath.Dir(relDir)),
		"but its parent stays, because the sibling capture is still in there")
}

func TestStore_Remove_NonexistentPath(t *testing.T) {
	store := archive.New(t.TempDir())

	err := store.Remove(filepath.Join("does", "not", "exist.html.zst"))
	require.Error(t, err)
}

func TestStore_RemoveEmptyDir_RemovesAndPrunes(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)
	relDir := newCapture(t, store)

	removed, err := store.RemoveEmptyDir(relDir)
	require.NoError(t, err)
	assert.True(t, removed)
	assert.NoDirExists(t, filepath.Join(root, relDir))
	assert.NoDirExists(t, filepath.Dir(filepath.Join(root, relDir)))
}

// Both ways a collected candidate can stop applying between the walk and
// the removal, neither of which is a failure.
func TestStore_RemoveEmptyDir_NotApplicableCasesAreNotErrors(t *testing.T) {
	root := t.TempDir()
	store := archive.New(root)

	t.Run("directory is gone", func(t *testing.T) {
		relDir := newCapture(t, store)
		require.NoError(t, os.Remove(filepath.Join(root, relDir)))

		removed, err := store.RemoveEmptyDir(relDir)
		require.NoError(t, err)
		assert.False(t, removed)
	})

	t.Run("directory is no longer empty", func(t *testing.T) {
		relDir := newCapture(t, store)
		_, _, err := store.WriteHTML(relDir, []byte("<html>a capture landed here after all</html>"))
		require.NoError(t, err)

		removed, err := store.RemoveEmptyDir(relDir)
		require.NoError(t, err)
		assert.False(t, removed)
		assert.DirExists(t, filepath.Join(root, relDir))
	})
}
