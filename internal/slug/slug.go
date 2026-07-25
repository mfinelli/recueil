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

// Package slug generates and validates the URL-facing slugs stored
// alongside tag and collection names. Slugs are their own column (not
// derived on the fly), so this package is deliberately small and pure --
// no DB access, no knowledge of tags vs collections -- callers own the
// uniqueness check and the decision of what to do when Generate can't
// produce anything usable.
package slug

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// MaxLength caps generated and user-supplied slugs at 63 characters,
// matching the conventional DNS-label length limit that most slug/URL
// segment conventions borrow.
const MaxLength = 63

// validPattern is the syntax a slug must satisfy regardless of where it
// came from (auto-generated or typed by hand into the dashboard's slug
// field): lowercase alphanumeric segments joined by single hyphens, no
// leading/trailing/doubled hyphens.
var validPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Generate produces a best-effort slug from name: NFKD-decompose (so an
// accented Latin letter like "é" splits into "e" plus a separate
// combining-mark rune), drop the combining marks, lowercase, then
// collapse every run of characters outside [a-z0-9] into a single
// hyphen and trim any leading/trailing hyphen.
//
// This only transliterates the Latin-alphabet case (accents, ligatures
// dropped to their base letter). Scripts that don't decompose into a
// Latin base at all -- CJK, Cyrillic, Arabic, emoji, and so on -- strip
// down to nothing and Generate returns "". That's deliberate rather than
// an oversight: NFKD decomposition is not a transliteration/romanization
// system, and bolting one on (e.g. Cyrillic "б" -> "b") would need a
// per-script mapping table this package doesn't have. Callers must
// treat "" as "could not auto-generate" and prompt for a manual slug
// rather than assuming Generate always succeeds.
func Generate(name string) string {
	decomposed := norm.NFKD.String(name)

	var stripped strings.Builder
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		stripped.WriteRune(r)
	}
	lowered := strings.ToLower(stripped.String())

	var out strings.Builder
	prevHyphen := true // starts true so a leading run of junk is dropped, not hyphenated
	for _, r := range lowered {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			prevHyphen = false
			continue
		}
		if !prevHyphen {
			out.WriteByte('-')
			prevHyphen = true
		}
	}

	result := strings.TrimSuffix(out.String(), "-")
	if len(result) > MaxLength {
		// Every rune in result is a single-byte ASCII character at this
		// point (only a-z, 0-9, and '-' survive the loop above), so a
		// plain byte-index slice can't split a multi-byte rune.
		result = strings.TrimRight(result[:MaxLength], "-")
	}
	return result
}

// Valid reports whether s satisfies the slug syntax rules on its own,
// independent of whether it happens to be free (that's a database
// uniqueness check, not this package's concern). Used to validate a
// user-supplied slug from the dashboard's slug field, not Generate's own
// output (which always already satisfies this by construction, empty
// string aside).
func Valid(s string) bool {
	return s != "" && len(s) <= MaxLength && validPattern.MatchString(s)
}
