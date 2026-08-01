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
// belonging to a capture: the HTML itself, a favicon, a screenshot.
// Paths are returned relative to the Store's configured root.
// Walk/WalkEmptyDirs/Remove (for internal/gc) round this out with a way to
// enumerate and delete by that same relative path, without leaking the root
// itself or the sharding scheme's directory depth to callers outside this
// package.
//
// # One capture, one directory
//
// Every capture gets its own directory, minted by NewCapture and shared
// with no other capture -- even one whose HTML is byte-for-byte identical.
//
// # Uniqueness is enforced, not assumed
//
// NewCapture mints a UUIDv7 and creates its leaf directory with a plain
// os.Mkdir, not MkdirAll -- so an already-existing directory surfaces as
// EEXIST and the id is regenerated, rather than a second capture silently
// writing over a first. That check has to happen here, at mkdir time,
// because the disk write precedes the Postgres commit:
// a database constraint alone would only reject the row *after*
// writeAtomic had already overwritten the other capture's bytes, which is
// exactly the silent-overwrite failure cites as its reason not to key
// disk paths by a client-generated id.
//
// The collision this guards against cannot realistically happen: UUIDv7
// carries 74 random bits (12 rand_a + 62 rand_b), and two ids would have
// to collide across all of them within the same millisecond. The guard is
// here because it is nearly free, not because the risk is real.
// captures.html_path additionally carries a UNIQUE constraint, which is
// the same invariant restated where the database can enforce it (see
// migration 00004) -- belt-and-suspenders, not the primary mechanism.
//
// # Filenames within a capture directory
//
// Because the directory belongs to exactly one capture, there is exactly
// one HTML file, at most one favicon and at most one screenshot inside it
// -- so each gets a plain, predictable name (page.html.zst, favicon.svg,
// thumbnail.png). A re-render (a retried screenshot job, a re-extraction
// after a Readability.js upgrade) therefore overwrites in place through the
// same atomic rename, leaving no superseded file behind for gc to collect.
package archive

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
)

// htmlFilename is the fixed name of the HTML file inside every capture
// directory -- see the package doc on why no capture's filenames need to
// encode a hash to stay distinct from another's.
const htmlFilename = "page.html.zst"

// newCaptureAttempts bounds NewCapture's regenerate-on-collision loop.
// Reaching the bound means either a genuine (unreachable) run of UUIDv7
// collisions or, far more likely, something structurally wrong with the
// archive root -- either way an error, not an infinite retry.
const newCaptureAttempts = 5

type Store struct {
	root string
}

func New(root string) *Store {
	return &Store{root: root}
}

// captureDir returns the sharded relative directory for a capture id --
// {id[32:34]}/{id[34:36]}/{id}/. The shard is taken from the *trailing*
// characters of the UUID rather than the leading ones because UUIDv7's
// leading bits are a millisecond timestamp: sharding on those would drop
// every capture made in the same period into one bucket and defeat the
// point. The last group of a v7 id is entirely rand_b, so it distributes
// uniformly.
//
// Three levels are kept (the same as git's object-store shape, for the same
// reason: a flat directory with hundreds of thousands of entries degrades
// badly for ls, backup tools, and anything else that walks it).
//
// Falls back to no sharding for an id too short to slice -- unreachable
// for a real UUID (always 36 characters), but a malformed id is a bad
// reason to panic rather than just place the directory under the root.
func captureDir(id string) string {
	if len(id) < 4 {
		return id
	}
	return filepath.Join(id[len(id)-4:len(id)-2], id[len(id)-2:], id)
}

// NewCapture mints a fresh capture directory, creates it on disk, and
// returns its path relative to the Store's root -- the value every
// subsequent WriteHTML/WriteAsset call for that capture takes, and the
// value filepath.Dir(captures.html_path) recovers later for a capture
// whose row already exists (see internal/screenshot).
//
// The directory is created exclusively (see the package doc): if it
// somehow already exists, the id is regenerated rather than reused, so no
// two captures can ever share one. The directory is created eagerly, up
// front, which means an ingestion that fails before writing anything
// leaves an empty directory behind -- harmless, invisible to Walk (which
// reports only regular files), and collected by internal/gc's own
// empty-directory pass via WalkEmptyDirs.
func (s *Store) NewCapture() (relDir string, err error) {
	for range newCaptureAttempts {
		id, err := uuid.NewV7()
		if err != nil {
			return "", fmt.Errorf("archive: generating capture id: %w", err)
		}

		relDir := captureDir(id.String())
		absDir := filepath.Join(s.root, relDir)

		// MkdirAll for the shard levels (shared by many captures, so
		// already-exists is the normal case), then a plain Mkdir for the
		// leaf, where already-exists is the collision signal.
		if err := os.MkdirAll(filepath.Dir(absDir), 0o755); err != nil {
			return "", fmt.Errorf("archive: creating shard directories for %q: %w", relDir, err)
		}
		if err := os.Mkdir(absDir, 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return "", fmt.Errorf("archive: creating capture directory %q: %w", relDir, err)
		}

		return relDir, nil
	}

	return "", fmt.Errorf("archive: could not mint an unused capture directory in %d attempts", newCaptureAttempts)
}

// WriteHTML zstd-compresses data and writes it to the fixed HTML filename
// inside relDir (a directory previously returned by NewCapture, or
// recovered via filepath.Dir of an existing capture's html_path),
// returning the path relative to the Store's root (suitable for
// captures.html_path) and the compressed size in bytes (for
// captures.html_compressed_size_bytes).
func (s *Store) WriteHTML(relDir string, data []byte) (relPath string, compressedSize int64, err error) {
	relPath = filepath.Join(relDir, htmlFilename)
	size, err := s.writeAtomic(relPath, data, true)
	return relPath, size, err
}

// WriteAsset writes a secondary asset belonging to the capture stored at
// relDir -- name is the asset's role ("favicon", "thumbnail"), ext its
// real file extension ("svg", "png", "ico"). Because relDir belongs to
// exactly one capture there is at most one asset of each role in it, so
// the role name alone is enough to identify the file; see the package doc
// for why this no longer needs to encode the asset's content hash.
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
func (s *Store) WriteAsset(relDir, name, ext string, data []byte, compress bool) (relPath string, writtenSize int64, err error) {
	filename := name + "." + ext
	if compress {
		filename += ".zst"
	}
	relPath = filepath.Join(relDir, filename)
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
// (a full disk, a crash, anything else). This is what makes an
// overwrite-in-place safe for the re-render cases described in the package
// doc: a reader either sees the whole old file or the whole new one.
//
// The MkdirAll here is not what establishes a capture directory's
// uniqueness -- NewCapture's exclusive Mkdir is (see the package doc). It
// stays because a caller may legitimately write into a directory that
// already exists (every call after the first for a given capture), and
// because a Store pointed at a not-yet-created root should still work.
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

// Walk calls fn once for every regular file under the Store's root, with
// relPath in the same root-relative shape WriteHTML/WriteAsset return and
// Open/OpenRaw/Remove accept, plus the file's size and modification time.
// Purely a read-only directory listing -- deciding which of those files
// are still referenced by anything, and actually removing the ones that
// aren't, is entirely the caller's job (internal/gc): archive has no
// business knowing what Postgres still points at, only how its own files
// are laid out on disk. modTime is reported for the same reason: gc needs
// it to leave in-flight writes alone, but what counts as "in flight" is
// gc's policy to set, not this package's.
//
// A root that doesn't exist yet is not an error -- a Store that has never
// been written to has zero files, which is exactly what an empty walk
// reports. If fn returns an error, the walk stops immediately and that
// error is returned; a nil error from fn continues to the next file.
func (s *Store) Walk(fn func(relPath string, sizeBytes int64, modTime time.Time) error) error {
	if _, err := os.Stat(s.root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return filepath.WalkDir(s.root, func(absPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(s.root, absPath)
		if err != nil {
			return err
		}
		return fn(relPath, info.Size(), info.ModTime())
	})
}

// WalkEmptyDirs calls fn once for every directory under the Store's root
// that currently contains nothing at all, with its root-relative path and
// modification time. The root itself is never reported, however empty --
// removing it would be a different and much more surprising operation than
// pruning a shard directory.
//
// This exists because Walk deliberately reports only regular files, which
// leaves one real category of garbage invisible to it: NewCapture creates
// a capture's directory eagerly, before anything is written into it, so an
// ingestion that fails early leaves an empty directory no file-oriented
// sweep would ever see. As with Walk, this reports and never removes --
// what to do about a given empty directory, and how recently-created is
// too recent to touch, is internal/gc's decision.
func (s *Store) WalkEmptyDirs(fn func(relPath string, modTime time.Time) error) error {
	if _, err := os.Stat(s.root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return filepath.WalkDir(s.root, func(absPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || absPath == s.root {
			return nil
		}

		entries, err := os.ReadDir(absPath)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(s.root, absPath)
		if err != nil {
			return err
		}
		return fn(relPath, info.ModTime())
	})
}

// Remove deletes whatever is at relPath -- a regular file, or an already-
// empty directory (as reported by WalkEmptyDirs) -- then climbs back up
// removing each now-empty parent directory in turn, collapsing
// captureDir's three levels of sharding back down once nothing inside
// them is left, rather than accumulating empty directory entries forever
// as captures are garbage-collected over the life of an instance (see
// pruneEmptyParents). Climbing never goes above the Store's own root.
func (s *Store) Remove(relPath string) error {
	absPath := filepath.Join(s.root, relPath)
	if err := os.Remove(absPath); err != nil {
		return fmt.Errorf("archive: removing %q: %w", relPath, err)
	}

	s.pruneEmptyParents(absPath)
	return nil
}

// RemoveEmptyDir removes the directory at relPath, but only if it is
// still empty, then prunes its now-empty parents the same way Remove
// does. removed reports whether it actually did anything.
//
// The emptiness re-check is the point of having this separate from
// Remove. A caller sweeping the store (internal/gc) collects candidates
// during a walk and removes them afterward, and in between: removing an
// orphaned file may already have pruned the directory that held it, and a
// concurrent NewCapture may have claimed another. Neither is a failure --
// both simply mean the candidate no longer applies -- so both come back
// as (false, nil) rather than as errors the caller would have to
// recognize and discard. Doing this check here rather than in the caller
// also keeps the Store's root, which is what "relPath is relative to"
// actually means, from having to be exposed for it.
func (s *Store) RemoveEmptyDir(relPath string) (removed bool, err error) {
	absPath := filepath.Join(s.root, relPath)

	entries, err := os.ReadDir(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("archive: reading directory %q: %w", relPath, err)
	}
	if len(entries) > 0 {
		return false, nil
	}

	if err := os.Remove(absPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("archive: removing directory %q: %w", relPath, err)
	}

	s.pruneEmptyParents(absPath)
	return true, nil
}

// pruneEmptyParents climbs up from absPath removing each parent directory
// that is now empty, stopping at the Store's own root. os.Remove refuses
// to remove a non-empty directory, which is exactly the signal to stop
// climbing -- silently treated as "stop here," not an error, since "some
// sibling capture's directory is still in here" is the expected, common
// case, not a failure.
func (s *Store) pruneEmptyParents(absPath string) {
	dir := filepath.Dir(absPath)
	for dir != s.root && strings.HasPrefix(dir, s.root) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
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
