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

// Package archive is the local, canonical, zstd-compressed disk store for
// capture HTML (DESIGN.md §4). Paths are returned relative to the Store's
// configured root.
//
// Files are sharded into subdirectories by the first four hex-ish
// characters of the storage key (git's own object-store scheme, applied
// here for the same reason: a flat directory with hundreds of thousands or
// millions of entries degrades badly for `ls`, backup tools, and anything
// else that walks it).
//
// Write is keyed by content_hash (the SHA-256 of the exact bytes being
// stored), not capture_id: two captures whose client-generated capture_ids
// happened to collide would also collide on a capture_id-keyed disk path,
// and Write's atomic rename (below) silently overwrites whatever's already at
// the destination. Keying by content_hash instead means a "collision" can
// only happen for genuinely byte-identical content, in which case overwriting
// with identical bytes is a harmless no-op rather than data loss for an
// unrelated, already-successfully-stored capture. See internal/ingest for
// where this content_hash is actually computed and where the same
// reasoning is applied a second time, for the Postgres side of the same
// underlying problem.
package archive

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

type Store struct {
	root string
}

func New(root string) *Store {
	return &Store{root: root}
}

// Write zstd-compresses data and writes it to a sharded path derived from
// key, returning the path relative to the Store's root (suitable for
// captures.html_path) and the compressed size in bytes (for
// captures.html_compressed_size_bytes).
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
func (s *Store) Write(key string, data []byte) (relPath string, compressedSize int64, err error) {
	relPath = shardedPath(key)
	absPath := filepath.Join(s.root, relPath)
	dir := filepath.Dir(absPath)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("archive: creating directory %q: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", 0, fmt.Errorf("archive: creating temp file in %q: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	// A no-op once the rename below succeeds (nothing left at tmpPath to
	// remove); cleans up the temp file on any earlier error return.
	defer func() { _ = os.Remove(tmpPath) }()

	if err := writeCompressed(tmpFile, data); err != nil {
		_ = tmpFile.Close()
		return "", 0, err
	}

	info, err := tmpFile.Stat()
	if err != nil {
		_ = tmpFile.Close()
		return "", 0, fmt.Errorf("archive: stat on temp file: %w", err)
	}
	compressedSize = info.Size()

	if err := tmpFile.Close(); err != nil {
		return "", 0, fmt.Errorf("archive: closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		return "", 0, fmt.Errorf("archive: renaming into place: %w", err)
	}

	return relPath, compressedSize, nil
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

// Open returns a decompressing reader for a path previously returned by
// Write (or any path relative to the Store's root laid out the same way).
// The caller must Close the returned ReadCloser.
func (s *Store) Open(relPath string) (io.ReadCloser, error) {
	absPath := filepath.Join(s.root, relPath)

	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("archive: opening %q: %w", relPath, err)
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

// shardedPath computes the sharded relative path for a storage key:
// {key[0:2]}/{key[2:4]}/{key}.html.zst. Falls back to no sharding for a
// key shorter than 4 characters -- shouldn't happen for a real SHA-256
// hex digest (always exactly 64 characters), but a short/malformed key is
// a bad reason for Write to panic rather than just place the file
// directly under the root.
func shardedPath(key string) string {
	if len(key) < 4 {
		return key + ".html.zst"
	}
	return filepath.Join(key[0:2], key[2:4], key+".html.zst")
}
