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
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Canonicalize is Recueil's own additional canonicalization pass meant
// to run as a pipeline Step *after* ClearURLs (or any other
// tracking-stripping step) has already had a chance to remove tracking
// parameters and unwrap redirect wrappers -- lowercasing a host or sorting
// query params doesn't need to happen before that, and doing it after means
// this step never has to reason about tracking-parameter syntax at all,
// just plain URL structure.
//
// This is deliberately its own Step, not a hardcoded tail bolted onto
// ClearURLs, for the same reason ClearURLs itself is a Step: normalization
// needs are expected to grow (another third-party library, or a
// hand-rolled Recueil-specific ruleset), and every step -- including this
// one -- should be freely reorderable/replaceable via Pipeline composition
// rather than requiring changes to any other step.
type Canonicalize struct{}

// Normalize implements Step. Lowercases the scheme in addition to the host
// -- not explicitly listed in the design document's  canonicalization steps,
// but added here because it's both a standard, RFC 3986-sanctioned
// canonicalization (scheme is case-insensitive) and, concretely, a
// correctness requirement for canonicalizeHost's own default-port
// comparison below: url.Parse does not lowercase the scheme itself
// (confirmed against the actual stdlib source, not assumed), so comparing
// parsed.Scheme against a lowercase "https"/"http" literal would silently
// fail to strip the default port for a URL with an uppercase scheme
// without this.
func (Canonicalize) Normalize(_ context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("urlnorm: canonicalize: parsing %q: %w", rawURL, err)
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = canonicalizeHost(parsed)

	// Drop the fragment unconditionally. We may need to revisit this
	// since we carve out an exception for "a known SPA that encodes
	// meaningful route state in the fragment" but that exception is NOT
	// implemented here yet (no such list exists to check against); every
	// fragment is dropped for now. Worth revisiting together once
	// there's an actual list of sites to special-case, not a silent gap
	// in the meantime.
	parsed.Fragment = ""
	parsed.RawFragment = ""

	// url.Values.Encode() sorts keys alphabetically as a side effect of
	// how it builds the query string -- which is exactly the "stable key"
	// property that we want, not an incidental side effect this code is
	// merely tolerating.
	query := parsed.Query()
	parsed.RawQuery = query.Encode()

	// Strips a lone "/" path down to "" too, meaning
	// https://example.com and https://example.com/ normalize identically
	// -- a deliberate, not merely incidental, consequence of "strip
	// trailing slash" applied unconditionally.
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	return parsed.String(), nil
}

// canonicalizeHost lowercases the hostname and strips the port when it's
// the scheme's own default (:443 for https, :80 for http) -- anything
// else (a non-default port, no port at all) passes through unchanged
// apart from the lowercasing. Uses url.URL's own Hostname()/Port(), not a
// hand-rolled string split, specifically because those correctly handle
// IPv6 literals (e.g. "[::1]:8080"), which a naive strings.Cut(host, ":")
// would mangle.
func canonicalizeHost(parsed *url.URL) string {
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()

	isDefaultPort := (parsed.Scheme == "https" && port == "443") ||
		(parsed.Scheme == "http" && port == "80")
	if port == "" || isDefaultPort {
		return bracketIfIPv6(hostname)
	}
	return net.JoinHostPort(hostname, port)
}

// bracketIfIPv6 wraps a bare IPv6 literal (e.g. "::1") back in brackets,
// as required by URL syntax whenever it appears without a trailing port.
// url.URL.Hostname() deliberately strips brackets for IPv6 literals (its
// own documented behavior), and net.JoinHostPort only re-adds them when
// actually joining with a port -- there's no stdlib helper for the
// "host alone, no port" case, so this fills that gap. A hostname
// containing ":" is unambiguously IPv6 here: a bare domain name or IPv4
// address never contains one.
func bracketIfIPv6(hostname string) string {
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
}
