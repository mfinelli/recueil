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

package clicreds_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mfinelli/recueil/internal/clicreds"
)

func TestLoad_NotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	_, err := clicreds.Load()
	require.Error(t, err)
	assert.ErrorIs(t, err, clicreds.ErrNotFound)
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	original := &clicreds.Credentials{
		WorkerURL:  "https://worker.example.com",
		Token:      "rcl_live_abc123",
		DeviceID:   42,
		DeviceName: "my-laptop",
	}
	require.NoError(t, clicreds.Save(original))

	loaded, err := clicreds.Load()
	require.NoError(t, err)
	assert.Equal(t, original, loaded)
}

func TestSave_CreatesConfigDirectoryWithRestrictedPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions don't apply on Windows")
	}

	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)

	require.NoError(t, clicreds.Save(&clicreds.Credentials{
		WorkerURL: "https://worker.example.com", Token: "t", DeviceID: 1, DeviceName: "d",
	}))

	dirInfo, err := os.Stat(filepath.Join(xdgHome, "recueil"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	fileInfo, err := os.Stat(filepath.Join(xdgHome, "recueil", "credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
}

func TestSave_LeavesNoTempFilesBehind(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)

	require.NoError(t, clicreds.Save(&clicreds.Credentials{
		WorkerURL: "https://worker.example.com", Token: "t", DeviceID: 1, DeviceName: "d",
	}))

	entries, err := os.ReadDir(filepath.Join(xdgHome, "recueil"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "only the final credentials.json should remain, no leftover temp file")
	assert.Equal(t, "credentials.json", entries[0].Name())
}

func TestSave_OverwritesExistingCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	require.NoError(t, clicreds.Save(&clicreds.Credentials{
		WorkerURL: "https://old.example.com", Token: "old-token", DeviceID: 1, DeviceName: "d",
	}))
	require.NoError(t, clicreds.Save(&clicreds.Credentials{
		WorkerURL: "https://new.example.com", Token: "new-token", DeviceID: 2, DeviceName: "d2",
	}))

	loaded, err := clicreds.Load()
	require.NoError(t, err)
	assert.Equal(t, "https://new.example.com", loaded.WorkerURL)
	assert.Equal(t, "new-token", loaded.Token)
}

func TestConfigDir_FallsBackToHomeConfigWhenXDGUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	require.NoError(t, clicreds.Save(&clicreds.Credentials{
		WorkerURL: "https://worker.example.com", Token: "t", DeviceID: 1, DeviceName: "d",
	}))

	_, err := os.Stat(filepath.Join(home, ".config", "recueil", "credentials.json"))
	require.NoError(t, err, "should fall back to $HOME/.config/recueil per the XDG Base Directory spec's own default")
}

func TestLoad_CorruptFile(t *testing.T) {
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)

	dir := filepath.Join(xdgHome, "recueil")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("not valid json"), 0o600))

	_, err := clicreds.Load()
	require.Error(t, err)
	assert.False(t, errors.Is(err, clicreds.ErrNotFound), "a corrupt file is a different failure than a missing one")
}
