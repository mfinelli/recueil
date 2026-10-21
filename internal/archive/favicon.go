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

package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
)

// The favicon rules shared by every store that holds favicons: the page
// favicons in the captures archive (internal/ingest) and link favicons in their
// own store (internal/linkicons). They live here, next to WriteAsset, so there is
// one definition of what gets stored, how it is compressed, and what is recorded
// about it.

// faviconName is the fixed base name of a favicon's one file in its directory:
// favicon.{ext}, or favicon.svg.zst for the compressed SVG.
const faviconName = "favicon"

// Favicon is what WriteFavicon recorded: exactly the three values the rows that
// reference a favicon store (captures.favicon_*, links.favicon_*).
type Favicon struct {
	// Path is relative to the Store's root, and is what favicon_path holds.
	Path string
	// SizeBytes is the on-disk size (after zstd, for SVG).
	SizeBytes int32
	// Hash is the hex SHA-256 of the original, uncompressed bytes.
	Hash string
}

// ValidateFavicon reports whether data and ext are something WriteFavicon will
// store: the closed set of formats the extension and the dashboard can hand us
// (svg, png, ico), and not empty. It writes nothing, so a caller that mints a
// directory first can check before creating one.
func ValidateFavicon(data []byte, ext string) error {
	switch ext {
	case "svg", "png", "ico":
	default:
		return fmt.Errorf("unsupported favicon format %q", ext)
	}
	if len(data) == 0 {
		return errors.New("empty favicon")
	}
	return nil
}

// WriteFavicon writes data into relDir (a directory previously returned by
// NewCapture) as favicon.{ext} and returns what to record about it. Only SVG, being
// text, is zstd-compressed: PNG and ICO are already compressed, so zstd would cost
// CPU for next to no saving (see WriteAsset). The hash is of the original bytes,
// before any compression, so it identifies the icon rather than how it was stored.
func (s *Store) WriteFavicon(relDir string, data []byte, ext string) (Favicon, error) {
	if err := ValidateFavicon(data, ext); err != nil {
		return Favicon{}, err
	}

	relPath, written, err := s.WriteAsset(relDir, faviconName, ext, data, ext == "svg")
	if err != nil {
		return Favicon{}, fmt.Errorf("writing favicon: %w", err)
	}
	if written > math.MaxInt32 {
		// A multi-gigabyte favicon is absurd on its face, and not worth overflowing
		// the INTEGER column a row records its size in.
		return Favicon{}, fmt.Errorf("favicon is %d bytes on disk, over the int32 limit", written)
	}

	sum := sha256.Sum256(data)
	return Favicon{Path: relPath, SizeBytes: int32(written), Hash: hex.EncodeToString(sum[:])}, nil
}
