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

package ingest

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// languageTagPattern extracts the value of an <html lang="..."> attribute
// -- the standard HTML5 way a page declares its own content language.
// Deliberately a simple, tolerant pattern (case-insensitive, single or
// double quotes) rather than a full HTML parser, matching this package's
// existing extractTitle approach: extracting one well-known attribute
// from already-trusted, already-captured HTML doesn't need one.
var languageTagPattern = regexp.MustCompile(`(?is)<html[^>]+lang\s*=\s*["']([^"']+)["']`)

// postgresLanguageConfigs maps a BCP 47 / ISO 639-1 primary language
// subtag (the part of an <html lang="..."> value before any "-", e.g.
// "en" from "en-US") to the corresponding built-in PostgreSQL text search
// configuration name.
//
// Deliberately not exhaustive of every human language -- only languages
// Postgres ships a snowball stemmer for have an entry at all. Anything
// else (Chinese, Japanese, Korean, and any tag with no entry here)
// falls through to "simple" correctly in resolveLanguageConfig below,
// which is the right behavior for a language Postgres has no
// language-specific stemming for anyway -- there's no wrong config to
// pick for it.
//
// This is only ever a *candidate* -- resolveLanguageConfig always
// validates it against this specific Postgres instance's live
// pg_ts_config catalog before trusting it (see languageConfigExists),
// rather than assuming this Go map is itself an authoritative list of
// what the running Postgres actually supports.
var postgresLanguageConfigs = map[string]string{
	"ar": "arabic",
	"hy": "armenian",
	"eu": "basque",
	"ca": "catalan",
	"da": "danish",
	"nl": "dutch",
	"en": "english",
	"et": "estonian",
	"fi": "finnish",
	"fr": "french",
	"de": "german",
	"el": "greek",
	"hi": "hindi",
	"hu": "hungarian",
	"id": "indonesian",
	"ga": "irish",
	"it": "italian",
	"lt": "lithuanian",
	"ne": "nepali",
	"no": "norwegian",
	"nb": "norwegian",
	"nn": "norwegian",
	"pt": "portuguese",
	"ro": "romanian",
	"ru": "russian",
	"es": "spanish",
	"sv": "swedish",
	"tr": "turkish",
}

// extractLanguage parses the primary language subtag from the captured
// HTML's <html lang="..."> attribute: lowercased, with any
// region/script/variant subtag after the first "-" stripped (e.g.
// "en-US" -> "en", "pt-BR" -> "pt"). Returns "" if no lang attribute is
// present at all.
func extractLanguage(htmlBytes []byte) string {
	m := languageTagPattern.FindSubmatch(htmlBytes)
	if m == nil {
		return ""
	}
	tag := strings.ToLower(strings.TrimSpace(string(m[1])))
	primary, _, _ := strings.Cut(tag, "-")
	return primary
}

// resolveLanguageConfig maps a detected language tag to a validated
// Postgres text search configuration name, falling back to "simple" --
// no language-specific stemming, but never actively wrong for any
// language, unlike guessing -- whenever there's no tag, no mapping for
// it, or the mapped candidate doesn't actually exist on this Postgres
// instance.
func (ing *Ingester) resolveLanguageConfig(ctx context.Context, langTag string) (string, error) {
	if langTag == "" {
		return "simple", nil
	}
	candidate, ok := postgresLanguageConfigs[langTag]
	if !ok {
		return "simple", nil
	}
	exists, err := ing.languageConfigExists(ctx, candidate)
	if err != nil {
		return "", fmt.Errorf("checking language config %q: %w", candidate, err)
	}
	if !exists {
		return "simple", nil
	}
	return candidate, nil
}

// languageConfigExists asks this specific Postgres instance's actual
// pg_ts_config catalog whether a candidate config name is valid, rather
// than trusting postgresLanguageConfigs above as if it were itself an
// authoritative list -- which configs are actually available genuinely
// depends on the running Postgres version.
//
// A plain query against the raw pool, not a sqlc-generated one: sqlc's
// schema analysis only knows about tables defined in our own
// migrations, not Postgres's built-in system catalogs, so a query
// referencing pg_ts_config doesn't fit its normal model.
func (ing *Ingester) languageConfigExists(ctx context.Context, candidate string) (bool, error) {
	var exists bool
	err := ing.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_ts_config WHERE cfgname = $1)", candidate,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("querying pg_ts_config: %w", err)
	}
	return exists, nil
}
