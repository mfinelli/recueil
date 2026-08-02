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

// Package mcpapi is the MCP-facing surface over a user's archive: read-only
// tools (search, browse by tag/collection, fetch a page's content) for local
// MCP clients, authenticated via the api_tokens credential. A sibling to
// internal/httpapi, not a subpackage of it -- both sit over the same
// internal/db/internal/auth, mounted separately in internal/httpapi's own
// router (POST /mcp, alongside /api rather than nested under it).
//
// No write tools exist here, and none are planned -- this phase answers
// questions about the archive, it doesn't change it.
package mcpapi

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mfinelli/recueil/internal/db"
)

// defaultLimit/maxLimit bound every list-shaped tool's result size --
// dashboard-scale reasoning, just applied uniformly here rather than trusting
// each tool's caller-supplied limit unchecked.
const (
	defaultLimit = 20
	maxLimit     = 100
)

// NewHandler builds the MCP server (all tools registered) and wraps it in
// the SDK's Streamable HTTP handler. The returned http.Handler expects
// auth.RequireAPIToken to already be in front of it (mounted by
// internal/httpapi's router, not here) -- tool handlers read the
// authenticated user via auth.UserFromContext exactly like every
// internal/httpapi handler does.
func NewHandler(q *db.Queries, version string) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "recueil",
		Version: version,
	}, nil)

	registerTools(server, q)

	getServer := func(*http.Request) *mcp.Server { return server }
	return mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		// Required outright for the 2026-07-28 protocol revision (session
		// resumability is dropped from it entirely), and a reasonable
		// default regardless for a single-process backend with no
		// session-affinity problem to design around.
		Stateless: true,
		// Explicit, not left to the SDK's own default -- which as of
		// v1.6.0 is off unless opted back in via a deprecated MCPGODEBUG
		// compatibility flag. With zero trusted origins configured, this
		// still does exactly what's wanted: browser-context cross-origin
		// requests get rejected, while genuine MCP clients (which send
		// neither Sec-Fetch-Site nor Origin, being non-browser HTTP
		// clients) are unaffected.
		CrossOriginProtection: http.NewCrossOriginProtection(),
	})
}

// clampLimit applies this package's uniform default/cap to every
// list-shaped tool's caller-supplied limit -- 0 or unset takes the default,
// anything above the cap is silently clamped rather than rejected (an MCP
// client asking for too much is far more likely to be a lazy default on its
// end than a meaningful signal worth erroring over).
func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// textOrEmpty/timestamptzOrEmpty are this package's own copies of
// internal/httpapi's unexported textOrNil/timestamptzOrNil -- same
// nil-coalescing shape, just returning the JSON-friendlier zero value
// (empty string) rather than a *string/*time.Time, since tool output
// structs favor omitempty over pointer fields throughout this package.
// Not worth a shared internal/dbutil package for two three-line functions
// used by a handful of call sites in two packages.
func textOrEmpty(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
