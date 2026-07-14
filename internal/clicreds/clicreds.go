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

// Package clicreds is where `recueil auth` stores, and `recueil enqueue`
// reads, this device's pairing result -- a dedicated file under
// $XDG_CONFIG_HOME/recueil, deliberately not a field inside a general
// config.toml a user might hand-edit: `auth` rewriting part of a file the
// user also edits risks clobbering their formatting/comments (nothing
// this project uses for TOML writing round-trips cleanly), and a bearer
// credential arguably deserves its own tighter-scoped file rather than
// sharing a general settings file's permissions.
//
// worker_url is stored alongside the token, not as a separate setting
// living elsewhere: a token is only ever meaningful for the specific
// Worker that issued it, so the two are one unit that's always captured,
// stored, and read together, not independent values that happen to be
// related.
package clicreds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned by Load when no credentials file exists yet --
// distinguishable via errors.Is from any other read failure, so callers
// (recueil enqueue) can print "run `recueil auth` first" specifically,
// rather than a generic file-system error.
var ErrNotFound = errors.New("clicreds: no credentials file found")

const credentialsFilename = "credentials.json"

// Credentials is exactly what a successful `POST /pair` produced, plus
// the Worker URL it was paired against.
type Credentials struct {
	WorkerURL  string `json:"worker_url"`
	Token      string `json:"token"`
	DeviceID   int64  `json:"device_id"`
	DeviceName string `json:"device_name"`
}

// Load reads the stored credentials. Returns ErrNotFound (wrapped) if
// `recueil auth` has never been run.
func Load() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("clicreds: reading %s: %w", path, err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("clicreds: parsing %s: %w", path, err)
	}
	return &creds, nil
}

// Save writes credentials, creating the config directory if needed.
// Written via a temp file in the same directory, then an atomic rename
// into place -- same reasoning as internal/archive's own Write: a crash
// or error partway through must never leave a half-written, superficially
// present-but-invalid credentials file at the real path.
func Save(creds *Credentials) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	// 0700: this directory holds a bearer credential.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("clicreds: creating config directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("clicreds: encoding credentials: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, ".tmp-"+credentialsFilename+"-*")
	if err != nil {
		return fmt.Errorf("clicreds: creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once renamed; cleans up on any earlier error return

	// 0600 before writing any content: avoid a window where the file
	// exists with default (world-readable-by-default-umask) permissions
	// before the chmod below tightens them.
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("clicreds: setting temp file permissions: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("clicreds: writing temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("clicreds: closing temp file: %w", err)
	}

	path := filepath.Join(dir, credentialsFilename)
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("clicreds: renaming into place: %w", err)
	}
	return nil
}

func credentialsPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, credentialsFilename), nil
}

// configDir resolves $XDG_CONFIG_HOME/recueil, falling back to
// $HOME/.config/recueil -- the Base Directory spec's own documented
// default when XDG_CONFIG_HOME is unset.
func configDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "recueil"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("clicreds: resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", "recueil"), nil
}
