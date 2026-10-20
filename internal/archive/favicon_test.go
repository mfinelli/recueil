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
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
)

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestStore_WriteFavicon(t *testing.T) {
	newStore := func(t *testing.T) (*archive.Store, string, string) {
		t.Helper()
		root := t.TempDir()
		store := archive.New(root)
		relDir, err := store.NewCapture()
		require.NoError(t, err)
		return store, root, relDir
	}

	t.Run("stores PNG and ICO as they are, in the directory it was given", func(t *testing.T) {
		for _, ext := range []string{"png", "ico"} {
			store, root, relDir := newStore(t)
			data := []byte("not really an image, but opaque bytes " + ext)

			fav, err := store.WriteFavicon(relDir, data, ext)

			require.NoError(t, err, ext)
			assert.Equal(t, filepath.Join(relDir, "favicon."+ext), fav.Path)
			assert.Equal(t, int32(len(data)), fav.SizeBytes, "raw formats are stored as-is")
			assert.Equal(t, sha(data), fav.Hash)
			onDisk, err := os.ReadFile(filepath.Join(root, fav.Path))
			require.NoError(t, err)
			assert.Equal(t, data, onDisk)
		}
	})

	t.Run("compresses SVG, and the recorded size is what's on disk while the hash is of the original", func(t *testing.T) {
		store, root, relDir := newStore(t)
		data := []byte(strings.Repeat("<svg>an icon</svg>", 200))

		fav, err := store.WriteFavicon(relDir, data, "svg")

		require.NoError(t, err)
		assert.Equal(t, filepath.Join(relDir, "favicon.svg.zst"), fav.Path)
		info, err := os.Stat(filepath.Join(root, fav.Path))
		require.NoError(t, err)
		assert.Equal(t, int32(info.Size()), fav.SizeBytes, "the on-disk size")
		assert.Less(t, fav.SizeBytes, int32(len(data)), "repetitive SVG shrinks")
		assert.Equal(t, sha(data), fav.Hash, "the hash identifies the icon, not how it was stored")

		reader, err := store.Open(fav.Path)
		require.NoError(t, err)
		defer func() { _ = reader.Close() }()
		back, err := io.ReadAll(reader)
		require.NoError(t, err)
		assert.Equal(t, data, back, "reads back decompressed")
	})

	t.Run("refuses an unsupported format or empty data, writing nothing", func(t *testing.T) {
		store, root, relDir := newStore(t)

		for _, ext := range []string{"gif", "jpg", "", "SVG", "svg.zst"} {
			_, err := store.WriteFavicon(relDir, []byte("x"), ext)
			assert.ErrorContains(t, err, "unsupported favicon format", "ext %q", ext)
		}
		for _, data := range [][]byte{nil, {}} {
			_, err := store.WriteFavicon(relDir, data, "png")
			assert.ErrorContains(t, err, "empty favicon")
		}

		entries, err := os.ReadDir(filepath.Join(root, relDir))
		require.NoError(t, err)
		assert.Empty(t, entries, "nothing was written")
	})
}

func TestValidateFavicon(t *testing.T) {
	assert.NoError(t, archive.ValidateFavicon([]byte("x"), "svg"))
	assert.NoError(t, archive.ValidateFavicon([]byte("x"), "png"))
	assert.NoError(t, archive.ValidateFavicon([]byte("x"), "ico"))
	assert.Error(t, archive.ValidateFavicon([]byte("x"), "gif"))
	assert.Error(t, archive.ValidateFavicon(nil, "png"))
}
