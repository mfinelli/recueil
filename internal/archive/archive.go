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

// Package archive is the local, canonical disk store for everything
// belonging to a capture: the HTML itself, and now optionally a favicon,
// eventually a screenshot. Paths are returned relative to the Store's
// configured root.
//
// Every asset for one capture lives together under a single directory,
// sharded by the first four hex characters of the capture's own html
// content_hash (git's own object-store scheme, applied here for the same
// reason: a flat directory with hundreds of thousands or millions of
// entries degrades badly for `ls`, backup tools, and anything else that
// walks it) -- see CaptureDir.
//
// The HTML file's own identity is fully determined by CaptureDir(htmlHash)
// alone -- its filename inside that directory is fixed (see WriteHTML) --
// because the directory itself is already keyed by that exact content's
// hash, so any write into it is, by construction, a write of identical
// bytes. This is the same content_hash-not-capture_id reasoning
// internal/ingest already applies for Postgres's source_capture_id
// handling: keying by capture_id instead would let two different captures'
// client-generated IDs collide onto the same disk path, and the atomic
// rename below would silently overwrite one capture's data with another's.
//
// Every *other* asset (WriteAsset -- favicon today, a screenshot later) is
// instead keyed by *its own* content hash, not the html's. This matters:
// two captures can have byte-for-byte identical HTML (a static page with no
// embedded timestamps/tokens, recaptured after the site's favicon changed)
// while carrying genuinely different secondary assets. Naming a secondary
// asset by the html hash would silently reintroduce the exact
// same-key-different-content overwrite bug this package exists to avoid --
// just one level removed. Naming it by the asset's own hash instead
// preserves the same guarantee WriteHTML gets from its directory: identical
// key implies identical bytes, so a "collision" can only ever be a
// harmless no-op overwrite, never data loss for an unrelated asset.
package archive

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type Store struct {
	root string
}

func New(root string) *Store {
	return &Store{root: root}
}

// CaptureDir returns the sharded relative directory for a capture, derived
// from the capture's own html content_hash -- {hash[0:2]}/{hash[2:4]}/{hash}/.
// Every asset belonging to that capture (WriteHTML, WriteAsset) lives
// inside it. Exported since callers outside this package (e.g. a future
// dashboard handler serving "everything for this capture") may need to
// derive the same directory without going through a Write call.
//
// Falls back to no sharding for a hash shorter than 4 characters --
// shouldn't happen for a real SHA-256 hex digest (always exactly 64
// characters), but a short/malformed hash is a bad reason to panic rather
// than just place the directory directly under the root.
func CaptureDir(htmlHash string) string {
	if len(htmlHash) < 4 {
		return htmlHash
	}
	return filepath.Join(htmlHash[0:2], htmlHash[2:4], htmlHash)
}

// WriteHTML zstd-compresses data and writes it to the fixed HTML filename
// inside CaptureDir(htmlHash), returning the path relative to the Store's
// root (suitable for captures.html_path) and the compressed size in bytes
// (for captures.html_compressed_size_bytes). See the package doc for why
// the filename itself doesn't need to also encode the hash: the directory
// already does.
func (s *Store) WriteHTML(htmlHash string, data []byte) (relPath string, compressedSize int64, err error) {
	relPath = filepath.Join(CaptureDir(htmlHash), "page.html.zst")
	size, err := s.writeAtomic(relPath, data, true)
	return relPath, size, err
}

// WriteAsset writes a secondary asset belonging to the capture identified
// by htmlHash (a favicon, a screenshot) into that same capture directory,
// named by the asset's *own* content hash (assetHash) plus a real file
// extension (e.g. "svg", "png", "ico") -- not htmlHash. See the package
// doc for why that distinction matters.
//
// compress selects whether this particular asset gets zstd'd:
// already-compressed binary formats (png, ico, jpeg) gain essentially
// nothing from it and would just pay a decompress cost on every future
// read for free, while text-based formats (svg) compress well. When
// compress is true, ".zst" is appended to the stored filename (matching
// WriteHTML's own convention) so Open knows to decompress on the way back
// out purely from the path, with no separate bookkeeping.
//
// writtenSize is the actual on-disk byte count (post-compression when
// compress is true, otherwise identical to len(data)) -- the same
// "real compression-ratio numbers for the dashboard" reasoning
// html_compressed_size_bytes already exists for, now also captured for
// favicons and screenshots (captures.favicon_size_bytes,
// captures.thumbnail_size_bytes) rather than each caller re-deriving it
// from len(data) and silently getting it wrong for the compressed case.
func (s *Store) WriteAsset(htmlHash, assetHash, ext string, data []byte, compress bool) (relPath string, writtenSize int64, err error) {
	filename := assetHash + "." + ext
	if compress {
		filename += ".zst"
	}
	relPath = filepath.Join(CaptureDir(htmlHash), filename)
	writtenSize, err = s.writeAtomic(relPath, data, compress)
	return relPath, writtenSize, err
}

// writeAtomic writes data (optionally zstd-compressed) to relPath under the
// Store's root and returns the number of bytes actually written to disk.
//
// Writes go through a temp file in the same target directory, then an
// atomic rename into place -- same-directory os.Rename is atomic on a
// single filesystem, so the final path only ever holds a fully-written
// file, never a partial one, regardless of what fails partway through
// (a full disk, a crash, anything else). This matters for the crash-recovery
// story: a retry after a crash reuses the same key (the content hasn't
// changed) and therefore the same target path, and needs to safely overwrite
// whatever (possibly nothing, possibly a leftover temp file that was already
// cleaned up) is there, not risk leaving a half-written file that looks
// superficially present but isn't actually valid.
func (s *Store) writeAtomic(relPath string, data []byte, compress bool) (writtenSize int64, err error) {
	absPath := filepath.Join(s.root, relPath)
	dir := filepath.Dir(absPath)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("archive: creating directory %q: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("archive: creating temp file in %q: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	// A no-op once the rename below succeeds (nothing left at tmpPath to
	// remove); cleans up the temp file on any earlier error return.
	defer func() { _ = os.Remove(tmpPath) }()

	if compress {
		if err := writeCompressed(tmpFile, data); err != nil {
			_ = tmpFile.Close()
			return 0, err
		}
	} else if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return 0, fmt.Errorf("archive: writing: %w", err)
	}

	info, err := tmpFile.Stat()
	if err != nil {
		_ = tmpFile.Close()
		return 0, fmt.Errorf("archive: stat on temp file: %w", err)
	}
	writtenSize = info.Size()

	if err := tmpFile.Close(); err != nil {
		return 0, fmt.Errorf("archive: closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		return 0, fmt.Errorf("archive: renaming into place: %w", err)
	}

	return writtenSize, nil
}

func writeCompressed(w io.Writer, data []byte) error {
	enc, err := zstd.NewWriter(w)
	if err != nil {
		return fmt.Errorf("archive: creating zstd writer: %w", err)
	}
	if _, err := enc.Write(data); err != nil {
		_ = enc.Close()
		return fmt.Errorf("archive: compressing: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("archive: finalizing compression: %w", err)
	}
	return nil
}

// OpenRaw returns a reader for a path previously returned by WriteHTML or
// WriteAsset, same as Open, but never decompresses -- even for a ".zst"
// path, the caller gets the compressed bytes exactly as stored. For a
// caller that can pass compressed bytes straight through to its own
// consumer (e.g. an HTTP handler whose client advertised
// Accept-Encoding: zstd and can set Content-Encoding: zstd on the
// response instead of paying a decompress-then-maybe-recompress cost).
// The caller must Close the returned ReadCloser.
func (s *Store) OpenRaw(relPath string) (io.ReadCloser, error) {
	absPath := filepath.Join(s.root, relPath)

	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("archive: opening %q: %w", relPath, err)
	}
	return f, nil
}

// Open returns a reader for a path previously returned by WriteHTML or
// WriteAsset (or any path relative to the Store's root laid out the same
// way). Transparently decompresses when relPath ends in ".zst" and returns
// the raw file otherwise -- the path itself is the only source of truth
// for whether the content is compressed, matching how WriteAsset decides
// the filename in the first place, so there's no separate "was this
// compressed" bookkeeping to keep in sync. The caller must Close the
// returned ReadCloser.
func (s *Store) Open(relPath string) (io.ReadCloser, error) {
	absPath := filepath.Join(s.root, relPath)

	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("archive: opening %q: %w", relPath, err)
	}

	if !strings.HasSuffix(relPath, ".zst") {
		return f, nil
	}

	dec, err := zstd.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("archive: creating zstd reader: %w", err)
	}

	return &decodingReadCloser{dec: dec, f: f}, nil
}

// decodingReadCloser adapts *zstd.Decoder (whose Close method returns no
// error)  into a real io.ReadCloser, and closes the underlying file alongside
// it.
type decodingReadCloser struct {
	dec *zstd.Decoder
	f   *os.File
}

func (d *decodingReadCloser) Read(p []byte) (int, error) {
	return d.dec.Read(p)
}

func (d *decodingReadCloser) Close() error {
	d.dec.Close()
	return d.f.Close()
}
