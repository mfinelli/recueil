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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/archive"
)

const testHint = " -- test hint"

func TestStore_Check(t *testing.T) {
	t.Run("passes for a writable directory and leaves nothing behind", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, archive.New(root).Check("archive_dir", testHint))
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("fails for a directory that doesn't exist, and never creates it", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing")

		err := archive.New(missing).Check("archive_dir", testHint)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "archive_dir", "names the setting to fix")
		assert.Contains(t, err.Error(), missing)
		assert.Contains(t, err.Error(), testHint, "says what to do about it")
		_, statErr := os.Stat(missing)
		assert.True(t, os.IsNotExist(statErr), "a missing directory is the signal a mount is missing, so it must not be created")
	})

	t.Run("fails for a path that is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "afile")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

		err := archive.New(file).Check("archive_dir", testHint)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
		assert.Contains(t, err.Error(), testHint)
	})

	t.Run("fails for a directory that can't be written to", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		root := t.TempDir()
		require.NoError(t, os.Chmod(root, 0o500))
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

		err := archive.New(root).Check("archive_dir", testHint)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "not writable")
		assert.Contains(t, err.Error(), testHint)
	})

	t.Run("names whichever setting it was asked about", func(t *testing.T) {
		err := archive.New(filepath.Join(t.TempDir(), "missing")).Check("links_dir", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "links_dir")
		assert.NotContains(t, err.Error(), "archive_dir")
	})
}
