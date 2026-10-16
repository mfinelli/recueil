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

// Package linkicons is the local disk store for link favicons. It's its own
// store, with its own root and its own bind mount, separate from the captures
// archive: `recueil gc`'s sweep of the archive treats every file it doesn't
// recognize as garbage, so link favicons must live where it can't reach them.
//
// # Same layout as the archive
//
// Within the link root the store is laid out exactly as the archive is: every
// saved favicon gets its own sharded, UUIDv7-named directory (minted by
// archive.Store.NewCapture, whose "capture" name is only historical -- it is
// the archive's generic exclusive-directory minting) holding one plain-named
// file, favicon.{ext}. Nothing is shared between links, even for byte-for-byte
// identical icons: favicons are a few KB, so deduplicating them isn't worth a
// second storage model. That also keeps deleting a link a pure database
// operation answered by the same kind of gc sweep as for pages and captures,
// with no file that more than one row might still need.
//
// A re-save of a link writes a new directory rather than overwriting the old
// one, and the old favicon, no longer referenced by the row, is left for gc.
//
// The hash recorded alongside each favicon is the SHA-256 of the original
// bytes (before the zstd that SVGs get), the same definition
// captures.favicon_hash uses.
package linkicons

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/mfinelli/recueil/internal/archive"
)

// faviconName is the fixed base name of the one file in every favicon
// directory, the same role-based naming captures use for their own favicon.
const faviconName = "favicon"

// Store writes link favicons under a root directory.
type Store struct {
	root    string
	archive *archive.Store
}

// New returns a Store rooted at root. Nothing touches the disk until Check or
// Write.
func New(root string) *Store {
	return &Store{root: root, archive: archive.New(root)}
}

// Archive returns the underlying archive.Store, for the pieces that only
// read, enumerate or remove by root-relative path (internal/gc).
func (s *Store) Archive() *archive.Store {
	return s.archive
}

// checkHint is appended to Check's errors about the directory itself: there are
// two ways out, and an operator who upgraded without reading the release notes
// needs both named.
const checkHint = " -- provide that directory (a bind mount, so its contents " +
	"survive the container being recreated), or set enable_links = false to " +
	"turn links off"

// Check verifies the root exists, is a directory and can be written to. It's
// meant for startup, so that a missing bind mount fails loudly instead of the
// first favicon silently going nowhere. It can't tell a real bind mount from a
// directory inside the container's own filesystem, which loses its contents
// whenever the container is recreated; the operator docs say so.
func (s *Store) Check() error {
	info, err := os.Stat(s.root)
	if err != nil {
		return fmt.Errorf("links_dir %q is not usable: %w%s", s.root, err, checkHint)
	}
	if !info.IsDir() {
		return fmt.Errorf("links_dir %q is not a directory%s", s.root, checkHint)
	}
	probe, err := os.CreateTemp(s.root, ".check-*")
	if err != nil {
		return fmt.Errorf("links_dir %q is not writable: %w%s", s.root, err, checkHint)
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("links_dir %q: removing write probe: %w", s.root, err)
	}
	return nil
}

// Favicon is what Write recorded: exactly the three values the links row
// stores.
type Favicon struct {
	// Path is relative to the Store's root, and is what
	// links.favicon_path holds.
	Path string
	// SizeBytes is the on-disk size (after zstd, for SVG), as
	// captures.favicon_size_bytes is.
	SizeBytes int32
	// Hash is the hex SHA-256 of the original, uncompressed bytes.
	Hash string
}

// Write stores data as a favicon of the given format in a new directory of its
// own and returns where it went. ext is one of "svg", "png" or "ico" -- the
// same closed set the extension sends -- and only SVG, being text, is
// zstd-compressed.
//
// If the write fails after the directory was minted, the empty directory is
// left behind for gc's empty-directory pass, exactly as a failed capture
// ingestion's is.
func (s *Store) Write(data []byte, ext string) (Favicon, error) {
	switch ext {
	case "svg", "png", "ico":
	default:
		return Favicon{}, fmt.Errorf("linkicons: unsupported favicon format %q", ext)
	}
	if len(data) == 0 {
		return Favicon{}, errors.New("linkicons: empty favicon")
	}

	relDir, err := s.archive.NewCapture()
	if err != nil {
		return Favicon{}, fmt.Errorf("linkicons: creating favicon directory: %w", err)
	}
	relPath, written, err := s.archive.WriteAsset(relDir, faviconName, ext, data, ext == "svg")
	if err != nil {
		return Favicon{}, fmt.Errorf("linkicons: writing favicon: %w", err)
	}
	if written > math.MaxInt32 {
		// Same reasoning as internal/ingest: absurd on its face, and not
		// worth overflowing the INTEGER column over.
		return Favicon{}, fmt.Errorf("linkicons: favicon is %d bytes on disk, over the int32 limit", written)
	}

	sum := sha256.Sum256(data)
	return Favicon{Path: relPath, SizeBytes: int32(written), Hash: hex.EncodeToString(sum[:])}, nil
}
