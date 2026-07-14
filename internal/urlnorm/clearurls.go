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

package urlnorm

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/dlclark/regexp2/v2"
)

// ClearURLs is a Go port of the ClearURLs browser extension's own
// cleaning algorithm (pureCleaning / _cleaning / removeFieldsFormURL in
// its core_js), driven by the community-maintained ruleset vendored as a
// git submodule at internal/urlnorm/clearurls-rules (see that directory's
// own pin/update process; this is a submodule rather than a package-registry
// dependency: the upstream project doesn't publish to any registry Go/JS
// tooling could consume directly).
//
// This is a *port*, not a bundled copy of the JS -- there is no existing
// Go implementation of the ClearURLs ruleset format, so every behavior
// here was verified against the actual upstream source
// (ClearURLs/Addon's core_js/pureCleaning.js and clearurls.js) rather than
// inferred from the ruleset's own documentation, which describes the data
// shape but not every matching/precedence detail. Two upstream behaviors
// are deliberately NOT ported, and are excluded even from this package's
// data structures:
//   - completeProvider ("block this request outright") -- a live-browsing
//     concept (drop a tracking-pixel request before it's ever made) that
//     doesn't apply to a URL a user already chose to archive; a bookmark
//     is definitionally not a stray tracking request.
//   - forceRedirection -- a live-tab navigation technique (rewriting a
//     browser's own main_frame object when a site defeats normal redirect
//     interception), meaningless once you're transforming an
//     already-known URL string rather than intercepting a real navigation
//     event.
type ClearURLs struct {
	providers []provider
}

// data.min.json specifically, not data.json (which upstream's own README
// calls out as outdated) or data.minify.json (a further-stripped variant
// meant to save bandwidth for the live extension's own update-fetch,
// irrelevant to a vendored, build-time-embedded copy).
//
//go:embed clearurls-rules/data.min.json
var clearURLsRulesJSON []byte

// NewClearURLs parses the embedded ClearURLs ruleset and compiles every
// provider's patterns up front. Fails loudly on the first
// unparseable/uncompilable pattern rather than skipping it silently: this
// is a pinned, deliberately-vendored artifact, not something fetched at
// runtime, so a bad pattern here means the vendored snapshot itself needs
// attention, not a runtime condition to degrade gracefully around.
func NewClearURLs() (*ClearURLs, error) {
	return newClearURLsFromJSON(clearURLsRulesJSON)
}

// newClearURLsFromJSON is NewClearURLs' actual implementation, factored out
// so it's independently testable against hand-crafted JSON (a malformed
// ruleset, a single synthetic provider) without needing to swap out the
// package's embedded data to do it.
func newClearURLsFromJSON(data []byte) (*ClearURLs, error) {
	named, err := decodeProvidersInOrder(data)
	if err != nil {
		return nil, fmt.Errorf("urlnorm: decoding ruleset: %w", err)
	}

	providers := make([]provider, 0, len(named))
	for i := range named {
		np := &named[i]
		p, err := compileProvider(np.name, &np.raw)
		if err != nil {
			return nil, fmt.Errorf("urlnorm: compiling provider %q: %w", np.name, err)
		}
		providers = append(providers, p)
	}

	return &ClearURLs{providers: providers}, nil
}

// rawProvider mirrors data.min.json's per-provider schema. completeProvider
// and forceRedirection are intentionally not fields here at all -- see
// the ClearURLs doc comment above for why neither is ported.
type rawProvider struct {
	URLPattern        string   `json:"urlPattern"`
	Rules             []string `json:"rules"`
	ReferralMarketing []string `json:"referralMarketing"`
	RawRules          []string `json:"rawRules"`
	Exceptions        []string `json:"exceptions"`
	Redirections      []string `json:"redirections"`
}

// provider is a compiled rawProvider -- every regex compiled once at
// construction time via NewClearURLs, not per-normalize-call, since this
// ruleset has 200+ providers and Normalize may run once per capture.
type provider struct {
	name         string
	urlPattern   *regexp2.Regexp
	exceptions   []*regexp2.Regexp
	redirections []*regexp2.Regexp
	rawRules     []*regexp2.Regexp
	fieldRules   []*regexp2.Regexp
}

type namedProvider struct {
	name string
	raw  rawProvider
}

// decodeProvidersInOrder decodes the top-level {"providers": {...}} object
// preserving the original file order of provider keys, which plain
// json.Unmarshal into a map would NOT do (Go map iteration order is
// randomized). Provider order matters for faithfulness: the real
// algorithm's redirection short-circuit (see (*ClearURLs).pass below)
// means which provider is checked first can change the result, exactly
// mirroring how the real extension iterates its providers array in the
// order the JSON defined it (JS object key order is insertion order).
func decodeProvidersInOrder(data []byte) ([]namedProvider, error) {
	dec := json.NewDecoder(bytes.NewReader(data))

	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}
	topKey, err := expectString(dec)
	if err != nil {
		return nil, err
	}
	if topKey != "providers" {
		return nil, fmt.Errorf("unexpected top-level key %q, want \"providers\"", topKey)
	}
	if err := expectDelim(dec, '{'); err != nil {
		return nil, err
	}

	var providers []namedProvider
	for dec.More() {
		name, err := expectString(dec)
		if err != nil {
			return nil, err
		}
		var raw rawProvider
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decoding provider %q: %w", name, err)
		}
		providers = append(providers, namedProvider{name: name, raw: raw})
	}
	if err := expectDelim(dec, '}'); err != nil { // closes "providers" object
		return nil, err
	}
	if err := expectDelim(dec, '}'); err != nil { // closes top-level object
		return nil, err
	}

	return providers, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != want {
		return fmt.Errorf("expected delimiter %q, got %v", want, tok)
	}
	return nil
}

func expectString(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected string token, got %v", tok)
	}
	return s, nil
}

func compileProvider(name string, raw *rawProvider) (provider, error) {
	urlPattern, err := regexp2.Compile(raw.URLPattern, regexp2.IgnoreCase)
	if err != nil {
		return provider{}, fmt.Errorf("urlPattern %q: %w", raw.URLPattern, err)
	}

	exceptions, err := compileEach(raw.Exceptions)
	if err != nil {
		return provider{}, fmt.Errorf("exceptions: %w", err)
	}
	redirections, err := compileEach(raw.Redirections)
	if err != nil {
		return provider{}, fmt.Errorf("redirections: %w", err)
	}
	rawRules, err := compileEach(raw.RawRules)
	if err != nil {
		return provider{}, fmt.Errorf("rawRules: %w", err)
	}

	// rules and referralMarketing are merged into one list and each
	// anchored front-and-back -- a direct port of the real extension's
	// own `new RegExp("^" + rule + "$", "gi")`, including that it's a
	// literal string concatenation rather than a defensively-grouped
	// "^(?:rule)$": a rule containing top-level alternation without its
	// own grouping (e.g. "a|b") would behave as "(^a)|(b$)", not
	// "^(a|b)$", in the real extension too. That's arguably a quirk in
	// the original, but faithfulness to the real, deployed behavior is
	// the actual goal here, not a "corrected" reimplementation that
	// diverges from what upstream's own ruleset authors have been
	// writing rules against for years.
	fieldRuleSources := make([]string, 0, len(raw.Rules)+len(raw.ReferralMarketing))
	fieldRuleSources = append(fieldRuleSources, raw.Rules...)
	fieldRuleSources = append(fieldRuleSources, raw.ReferralMarketing...)
	fieldRules := make([]*regexp2.Regexp, 0, len(fieldRuleSources))
	for _, rule := range fieldRuleSources {
		re, err := regexp2.Compile("^"+rule+"$", regexp2.IgnoreCase)
		if err != nil {
			return provider{}, fmt.Errorf("field rule %q: %w", rule, err)
		}
		fieldRules = append(fieldRules, re)
	}

	return provider{
		name:         name,
		urlPattern:   urlPattern,
		exceptions:   exceptions,
		redirections: redirections,
		rawRules:     rawRules,
		fieldRules:   fieldRules,
	}, nil
}

func compileEach(patterns []string) ([]*regexp2.Regexp, error) {
	compiled := make([]*regexp2.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp2.Compile(pattern, regexp2.IgnoreCase)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", pattern, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

// Normalize implements Step. It repeatedly runs a full pass over every
// provider until a pass produces no change -- a direct port of
// pureCleaning's own do/while loop, needed because unwrapping one
// provider's redirect-wrapper URL can reveal a URL that a *different*
// provider now matches (e.g. a Google redirect wrapping a Facebook link).
func (c *ClearURLs) Normalize(_ context.Context, rawURL string) (string, error) {
	current := rawURL
	for {
		next, err := c.pass(current)
		if err != nil {
			return "", err
		}
		if next == current {
			return next, nil
		}
		current = next
	}
}

// pass is one iteration over every provider in file order -- a port of
// _cleaning. A provider whose redirection rule matches causes the whole
// pass to return immediately with the unwrapped URL, skipping any
// remaining providers for *this* pass (they'll still get a chance on the
// next pass, against the new URL, via Normalize's outer loop above) --
// this exact short-circuit is present in the real extension's own
// _cleaning function, not an approximation of it.
func (c *ClearURLs) pass(rawURL string) (string, error) {
	current := rawURL
	for i := range c.providers {
		p := &c.providers[i]

		matched, err := p.matchURL(current)
		if err != nil {
			return "", fmt.Errorf("provider %q: %w", p.name, err)
		}
		if !matched {
			continue
		}

		result, redirected, err := p.apply(current)
		if err != nil {
			return "", fmt.Errorf("provider %q: %w", p.name, err)
		}
		if redirected {
			return result, nil
		}
		current = result
	}
	return current, nil
}

// matchURL is a port of Provider.matchURL: the provider's urlPattern
// must match, and no exception pattern may match.
func (p *provider) matchURL(rawURL string) (bool, error) {
	ok, err := p.urlPattern.MatchString(rawURL)
	if err != nil {
		return false, fmt.Errorf("urlPattern: %w", err)
	}
	if !ok {
		return false, nil
	}
	for _, exc := range p.exceptions {
		excMatch, err := exc.MatchString(rawURL)
		if err != nil {
			return false, fmt.Errorf("exceptions: %w", err)
		}
		if excMatch {
			return false, nil
		}
	}
	return true, nil
}

// apply is a port of removeFieldsFormURL: redirections first (first match
// wins, short-circuits the rest of this provider's own processing), then
// rawRules (global replace-with-empty against the whole URL string), then
// field-name stripping against both the query string and the
// fragment-treated-as-a-query-string.
func (p *provider) apply(rawURL string) (result string, redirected bool, err error) {
	for _, re := range p.redirections {
		m, err := re.FindStringMatch(rawURL)
		if err != nil {
			return "", false, fmt.Errorf("redirections: %w", err)
		}
		if m == nil {
			continue
		}
		group := m.GroupByNumber(1)
		if group == nil || group.RuneLength == 0 {
			// A redirection pattern with no (or an empty) capture group
			// is a malformed rule -- skip it rather than redirect to an
			// empty string.
			continue
		}
		decoded, err := decodeRedirectTarget(group.String())
		if err != nil {
			return "", false, fmt.Errorf("decoding redirect target: %w", err)
		}
		return decoded, true, nil
	}

	current := rawURL
	for _, re := range p.rawRules {
		current, err = re.Replace(current, "", -1, -1)
		if err != nil {
			return "", false, fmt.Errorf("rawRules: %w", err)
		}
	}

	parsed, err := url.Parse(current)
	if err != nil {
		return "", false, fmt.Errorf("parsing %q: %w", current, err)
	}

	query := parsed.Query()
	fragParams := parseFragmentParams(parsed.Fragment)

	for _, re := range p.fieldRules {
		if err := deleteMatchingKeys(query, re); err != nil {
			return "", false, fmt.Errorf("field rules (query): %w", err)
		}
		if err := deleteMatchingKeysFromMap(fragParams, re); err != nil {
			return "", false, fmt.Errorf("field rules (fragment): %w", err)
		}
	}

	parsed.RawQuery = query.Encode()
	parsed.Fragment = encodeFragmentParams(fragParams)
	parsed.RawFragment = ""

	return parsed.String(), false, nil
}

func deleteMatchingKeys(values url.Values, re *regexp2.Regexp) error {
	for key := range values {
		ok, err := re.MatchString(key)
		if err != nil {
			return err
		}
		if ok {
			values.Del(key)
		}
	}
	return nil
}

func deleteMatchingKeysFromMap(m map[string]string, re *regexp2.Regexp) error {
	for key := range m {
		ok, err := re.MatchString(key)
		if err != nil {
			return err
		}
		if ok {
			delete(m, key)
		}
	}
	return nil
}

// parseFragmentParams mirrors the real extension's own URLHashParams: the
// fragment (already without its leading "#") is parsed the same way a
// query string would be, regardless of whether it actually looks like
// one -- a direct port of that same assumption, not a Recueil addition.
//
// Simplified relative to URLHashParams in one way: duplicate fragment
// keys collapse to their last value here (a plain map), rather than
// URLHashParams' own multimap that preserves every value. Recueil's own
// canonicalization step (see canonicalize.go) drops the fragment entirely
// in the common case anyway, so this only matters for the deliberately
// unimplemented "known SPA with meaningful fragment state" exception
// which we may need to revisit if that's ever built, not a silent gap today.
func parseFragmentParams(fragment string) map[string]string {
	params := make(map[string]string)
	if fragment == "" {
		return params
	}
	for _, pair := range strings.Split(fragment, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		if key == "" {
			continue
		}
		params[key] = value
	}
	return params
}

// encodeFragmentParams serializes fragment params back into a "#"-ready
// string. Keys are sorted alphabetically for deterministic output --
// necessary because parseFragmentParams above uses a plain Go map (whose
// iteration order is randomized), and a normalized_url used as a dedup
// key must be stable across runs for identical input. This also happens
// to match the final pipeline's own "sort query params" canonicalization
// (canonicalize.go), rather than conflicting with it.
func encodeFragmentParams(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if value := params[key]; value != "" {
			parts = append(parts, key+"="+value)
		} else {
			parts = append(parts, key)
		}
	}
	return strings.Join(parts, "&")
}

// decodeRedirectTarget is a port of decodeURL: repeatedly percent-decodes
// until decoding again would no longer change the string (a direct
// fixed-point port of isEncodedURI's own "uri !== decodeURIComponent(uri)"
// check), then ensures the result starts with "http" -- upstream's own
// fix (referenced in their source as addressing a real malformed-redirect
// bug report) for redirect targets that come through schema-relative or
// otherwise malformed.
//
// url.PathUnescape, not url.QueryUnescape, is used deliberately:
// QueryUnescape additionally converts "+" to a space, which
// decodeURIComponent does not do and which would be a real behavioral
// divergence from upstream for any redirect target containing a literal
// "+".
func decodeRedirectTarget(raw string) (string, error) {
	decoded := raw
	for {
		next, err := url.PathUnescape(decoded)
		if err != nil {
			// Malformed percent-encoding -- stop unescaping further and
			// use what's been decoded so far, mirroring
			// decodeURIComponent's own throw-on-malformed-input case:
			// upstream doesn't catch that throw either, but silently
			// failing the entire normalization pipeline over one bad
			// redirect target is worse than proceeding with a
			// best-effort partial decode.
			break
		}
		if next == decoded {
			break
		}
		decoded = next
	}
	if !strings.HasPrefix(decoded, "http") {
		decoded = "http://" + decoded
	}
	return decoded, nil
}
