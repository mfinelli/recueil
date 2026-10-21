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
// store, with its own root and its own bind mount, kept apart from the captures
// archive because links and captures are different entities and are better off
// well separated: each has its own gc pass, backup and set of referenced files.
// One consequence is that the two roots must not overlap: `recueil gc`'s sweep of
// the archive treats every file it doesn't recognize as garbage, so link favicons
// can't live where it can reach them.
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
	"fmt"

	"github.com/mfinelli/recueil/internal/archive"
)

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
// whenever the container is recreated; the operator docs say so. The probe itself is
// archive.Store.Check, shared with the archive's own startup check.
func (s *Store) Check() error {
	return s.archive.Check("links_dir", checkHint)
}

// Favicon is what Write recorded: exactly the three values the links row
// stores (the path, the on-disk size, and the SHA-256 of the original bytes). It is
// the archive's type, shared with page favicons.
type Favicon = archive.Favicon

// Write stores data as a favicon of the given format in a new directory of its
// own and returns where it went. ext is one of "svg", "png" or "ico" -- the
// same closed set the extension sends -- and only SVG, being text, is
// zstd-compressed.
//
// If the write fails after the directory was minted, the empty directory is
// left behind for gc's empty-directory pass, exactly as a failed capture
// ingestion's is.
func (s *Store) Write(data []byte, ext string) (Favicon, error) {
	// Checked before a directory is minted, so a rejected favicon leaves nothing
	// behind.
	if err := archive.ValidateFavicon(data, ext); err != nil {
		return Favicon{}, fmt.Errorf("linkicons: %w", err)
	}

	relDir, err := s.archive.NewCapture()
	if err != nil {
		return Favicon{}, fmt.Errorf("linkicons: creating favicon directory: %w", err)
	}
	favicon, err := s.archive.WriteFavicon(relDir, data, ext)
	if err != nil {
		return Favicon{}, fmt.Errorf("linkicons: %w", err)
	}
	return favicon, nil
}
