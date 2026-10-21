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

package preflight_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/config"
	"github.com/mfinelli/recueil/internal/preflight"
)

func TestStorageDirs(t *testing.T) {
	cfg := func(t *testing.T, links bool) *config.Config {
		t.Helper()
		return &config.Config{
			ArchiveDir:  t.TempDir(),
			LinksDir:    t.TempDir(),
			EnableLinks: links,
		}
	}

	t.Run("passes when both directories are writable", func(t *testing.T) {
		assert.NoError(t, preflight.StorageDirs(cfg(t, true)))
	})

	t.Run("refuses a missing archive_dir, naming it and never creating it", func(t *testing.T) {
		c := cfg(t, true)
		c.ArchiveDir = filepath.Join(t.TempDir(), "no-such-archive")

		err := preflight.StorageDirs(c)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "archive_dir")
		assert.Contains(t, err.Error(), "bind mount", "says what to do")
		_, statErr := os.Stat(c.ArchiveDir)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("refuses an archive_dir that is a file", func(t *testing.T) {
		c := cfg(t, false)
		c.ArchiveDir = filepath.Join(t.TempDir(), "afile")
		require.NoError(t, os.WriteFile(c.ArchiveDir, []byte("x"), 0o600))

		err := preflight.StorageDirs(c)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})

	t.Run("refuses a missing links_dir while links are on", func(t *testing.T) {
		c := cfg(t, true)
		c.LinksDir = filepath.Join(t.TempDir(), "no-such-links")

		err := preflight.StorageDirs(c)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "links_dir")
	})

	t.Run("ignores links_dir while links are off", func(t *testing.T) {
		c := cfg(t, false)
		c.LinksDir = filepath.Join(t.TempDir(), "no-such-links")
		assert.NoError(t, preflight.StorageDirs(c))
	})

	t.Run("reports archive_dir first when both are wrong", func(t *testing.T) {
		c := &config.Config{
			ArchiveDir:  filepath.Join(t.TempDir(), "no-such-archive"),
			LinksDir:    filepath.Join(t.TempDir(), "no-such-links"),
			EnableLinks: true,
		}

		err := preflight.StorageDirs(c)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "archive_dir")
		assert.NotContains(t, err.Error(), "links_dir")
	})
}
