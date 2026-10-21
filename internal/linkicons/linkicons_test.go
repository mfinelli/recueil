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

package linkicons_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/linkicons"
)

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestStore_Write(t *testing.T) {
	t.Run("a png is stored raw in its own directory", func(t *testing.T) {
		root := t.TempDir()
		store := linkicons.New(root)
		data := []byte("\x89PNG not really, but bytes are bytes")

		fav, err := store.Write(data, "png")
		require.NoError(t, err)

		assert.Equal(t, hashOf(data), fav.Hash)
		assert.Equal(t, "favicon.png", filepath.Base(fav.Path))
		assert.Equal(t, int32(len(data)), fav.SizeBytes, "raw formats are stored as-is")

		onDisk, err := os.ReadFile(filepath.Join(root, fav.Path))
		require.NoError(t, err)
		assert.Equal(t, data, onDisk)
	})

	t.Run("the layout is the archive's: shard/shard/uuid/favicon.ext", func(t *testing.T) {
		fav, err := linkicons.New(t.TempDir()).Write([]byte("x"), "ico")
		require.NoError(t, err)

		parts := strings.Split(filepath.ToSlash(fav.Path), "/")
		require.Len(t, parts, 4)
		id := parts[2]
		assert.Equal(t, id[len(id)-4:len(id)-2], parts[0])
		assert.Equal(t, id[len(id)-2:], parts[1])
		assert.Equal(t, "favicon.ico", parts[3])
	})

	t.Run("an svg is zstd-compressed, with the hash of the original bytes", func(t *testing.T) {
		root := t.TempDir()
		store := linkicons.New(root)
		data := []byte(strings.Repeat(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>`, 40))

		fav, err := store.Write(data, "svg")
		require.NoError(t, err)

		assert.Equal(t, hashOf(data), fav.Hash, "hash is over the uncompressed bytes")
		assert.Equal(t, "favicon.svg.zst", filepath.Base(fav.Path))
		info, err := os.Stat(filepath.Join(root, fav.Path))
		require.NoError(t, err)
		assert.Equal(t, int32(info.Size()), fav.SizeBytes, "size is the on-disk size")
		assert.Less(t, fav.SizeBytes, int32(len(data)), "repetitive svg should shrink")

		// And it round-trips through the same Open the archive uses, which
		// decompresses by the .zst suffix alone.
		r, err := store.Archive().Open(fav.Path)
		require.NoError(t, err)
		defer func() { _ = r.Close() }()
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, data, got)
	})

	t.Run("identical bytes still get independent files", func(t *testing.T) {
		root := t.TempDir()
		store := linkicons.New(root)
		data := []byte("the same icon on two links")

		first, err := store.Write(data, "png")
		require.NoError(t, err)
		second, err := store.Write(data, "png")
		require.NoError(t, err)

		assert.NotEqual(t, first.Path, second.Path)
		assert.Equal(t, first.Hash, second.Hash)

		var files []string
		require.NoError(t, store.Archive().Walk(func(rel string, _ int64, _ time.Time) error {
			files = append(files, rel)
			return nil
		}))
		assert.ElementsMatch(t, []string{first.Path, second.Path}, files)
	})

	t.Run("rejects a format outside svg, png and ico", func(t *testing.T) {
		store := linkicons.New(t.TempDir())
		for _, ext := range []string{"gif", "jpg", "", "PNG", "../png"} {
			_, err := store.Write([]byte("x"), ext)
			assert.Error(t, err, "ext %q", ext)
		}
	})

	t.Run("rejects an empty favicon, and writes nothing", func(t *testing.T) {
		root := t.TempDir()
		_, err := linkicons.New(root).Write(nil, "png")
		assert.Error(t, err)
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

// The probe itself is tested in internal/archive (Store.Check, shared with the archive's
// own startup check); these only pin what is specific to the links store: the setting
// it names and the way out its hint offers, so the wrapper can't drift.
func TestStore_Check(t *testing.T) {
	t.Run("passes for a writable directory and leaves nothing behind", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, linkicons.New(root).Check())
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("names links_dir and offers both ways out", func(t *testing.T) {
		err := linkicons.New(filepath.Join(t.TempDir(), "missing")).Check()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "links_dir")
		assert.Contains(t, err.Error(), "enable_links = false", "names the way out, not just the problem")
		assert.Contains(t, err.Error(), "bind mount")
	})
}
