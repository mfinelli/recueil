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

-- name: UpsertLink :one
-- Get-or-create a link by (user_id, normalized_url). Unlike a page there is no
-- version history: saving the same URL again refreshes the one row, and the
-- refresh is faithful to what the extension just saw in the live tab. raw_url,
-- title and the three favicon columns are all replaced by the new values,
-- including with NULL -- a site that has stopped serving a favicon (or a
-- title) shouldn't keep showing the old one, the same way pages.favicon_path
-- follows the latest capture. The favicon columns always move together. When
-- the favicon_path changes (or goes NULL), the old file is simply no longer
-- referenced and is left for gc to reclaim. notes are the user's own and are
-- never touched by a save. source follows the latest save too (it says where
-- the current title and favicon came from).
INSERT INTO links (user_id, raw_url, normalized_url, title, favicon_path, favicon_size_bytes, favicon_hash, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id, normalized_url) DO UPDATE
SET raw_url = EXCLUDED.raw_url,
    title = EXCLUDED.title,
    favicon_path = EXCLUDED.favicon_path,
    favicon_size_bytes = EXCLUDED.favicon_size_bytes,
    favicon_hash = EXCLUDED.favicon_hash,
    source = EXCLUDED.source,
    updated_at = NOW()
RETURNING *;

-- name: CreateLinkIfAbsent :one
-- The create-only counterpart of UpsertLink, for adding a link by hand: if the
-- user already has a link for this normalized_url nothing is written and no row
-- comes back (pgx.ErrNoRows), so the caller can refuse with a 409 instead of
-- overwriting what's there. An extension save refreshes an existing link; a
-- manual add must not, because "no title or favicon given" there means "I
-- didn't provide one", not "the site has none", and a refresh would wipe them.
INSERT INTO links (user_id, raw_url, normalized_url, title, favicon_path, favicon_size_bytes, favicon_hash, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id, normalized_url) DO NOTHING
RETURNING *;

-- name: GetLinkByIDForUser :one
-- Scoped by user_id so a caller can't fetch another user's link by guessing
-- an id.
SELECT * FROM links WHERE id = $1 AND user_id = $2;

-- name: GetLinkByNormalizedURL :one
-- Backs the same-URL hint between a link and an archived page, and lookups by
-- URL generally.
SELECT * FROM links WHERE user_id = $1 AND normalized_url = $2;

-- name: ListLinks :many
-- Most recently saved first (created_at, not updated_at: re-saving an existing
-- link refreshes it in place rather than moving it to the top). id breaks
-- ties so paging is stable. COUNT(*) OVER() rides along for pagination, same
-- as ListPages. tag_id and collection_id, when set, narrow the list to links
-- carrying that tag / directly in that collection (a collection's own links, not
-- its sub-collections', like its page list); both together are ANDed, and the
-- caller has already checked they belong to the user. matching_page_id is the
-- same-URL hint (an archived page with
-- this link's normalized_url, if any), joined in here so a list needs no
-- query per row; pages are unique per (user_id, normalized_url), so the join
-- never multiplies a row. (A LEFT JOIN rather than a scalar subquery so sqlc
-- types the column as nullable.)
SELECT links.*,
  pages.id AS matching_page_id,
  COUNT(*) OVER() AS total_count
FROM links
LEFT JOIN pages
  ON pages.user_id = links.user_id
  AND pages.normalized_url = links.normalized_url
WHERE links.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(tag_id)::bigint IS NULL
    OR EXISTS (SELECT 1 FROM link_tags
      WHERE link_tags.link_id = links.id
        AND link_tags.tag_id = sqlc.narg(tag_id)::bigint))
  AND (sqlc.narg(collection_id)::bigint IS NULL
    OR EXISTS (SELECT 1 FROM link_collections
      WHERE link_collections.link_id = links.id
        AND link_collections.collection_id = sqlc.narg(collection_id)::bigint))
ORDER BY links.created_at DESC, links.id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: SearchLinks :many
-- ListLinks restricted to links whose title, URL (raw or normalized) or notes
-- contain the pattern, case-insensitively. The caller builds the pattern
-- (already wrapped in %...% and with LIKE's own metacharacters escaped with a
-- backslash, Postgres's default ILIKE escape). A link has no body text, so
-- there's no full-text search to run, only this substring match; same ordering,
-- pagination and matching_page_id as ListLinks.
SELECT links.*,
  pages.id AS matching_page_id,
  COUNT(*) OVER() AS total_count
FROM links
LEFT JOIN pages
  ON pages.user_id = links.user_id
  AND pages.normalized_url = links.normalized_url
WHERE links.user_id = sqlc.arg(user_id)
  AND (links.title ILIKE sqlc.arg(pattern)
    OR links.raw_url ILIKE sqlc.arg(pattern)
    OR links.normalized_url ILIKE sqlc.arg(pattern)
    OR links.notes ILIKE sqlc.arg(pattern))
  AND (sqlc.narg(tag_id)::bigint IS NULL
    OR EXISTS (SELECT 1 FROM link_tags
      WHERE link_tags.link_id = links.id
        AND link_tags.tag_id = sqlc.narg(tag_id)::bigint))
  AND (sqlc.narg(collection_id)::bigint IS NULL
    OR EXISTS (SELECT 1 FROM link_collections
      WHERE link_collections.link_id = links.id
        AND link_collections.collection_id = sqlc.narg(collection_id)::bigint))
ORDER BY links.created_at DESC, links.id DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: GetPageRefByNormalizedURL :one
-- The same-URL hint for a single link: the id and title of the user's archived
-- page with this normalized_url, if there is one (pgx.ErrNoRows if not). The
-- title rides along so the link's detail screen can name the page in its
-- callout without a second request.
SELECT id, title FROM pages WHERE user_id = $1 AND normalized_url = $2;

-- name: SetLinkTitle :one
-- A direct overwrite, not a separate title_override column, exactly as
-- SetPageTitle: links.title is whatever the extension last saw in the live tab,
-- and the next save of this link replaces whatever is set here.
-- updated_at moves so the bookmark mirror picks the edit up.
UPDATE links SET title = $1, updated_at = NOW()
WHERE id = $2 AND user_id = $3
RETURNING *;

-- name: SetLinkExcludedFromMirror :one
-- Opts a link in or out of the browser-bookmark sync, as SetPageExcludedFromMirror
-- does for a page. updated_at moves so the incremental sync notices the change.
UPDATE links SET excluded_from_mirror = $1, updated_at = NOW()
WHERE id = $2 AND user_id = $3
RETURNING *;

-- name: SetLinkNotes :one
-- An empty value is meaningful (clearing a note is a normal action), same as
-- SetPageNotes.
UPDATE links SET notes = $1, updated_at = NOW()
WHERE id = $2 AND user_id = $3
RETURNING *;

-- name: DeleteLink :execrows
-- Cascades to link_tags and link_collections via the schema's own ON DELETE
-- CASCADE. execrows so the caller can tell "deleted" from "didn't exist / wasn't
-- this user's". The link's favicon file is deliberately left for gc, the same
-- way DeletePage leaves a page's files.
DELETE FROM links WHERE id = $1 AND user_id = $2;

-- name: ListReferencedLinkFaviconPaths :many
-- The live set for the link favicon store's gc pass: every root-relative path
-- some link still points at. Anything the sweep finds under the link store's
-- root that isn't in this set gets removed -- a deleted link's favicon, or one
-- a re-save has replaced or cleared.
SELECT favicon_path::text AS path FROM links WHERE favicon_path IS NOT NULL;

-- name: UpsertLinkKeepingFavicon :one
-- UpsertLink for a save that declared a favicon the backend then couldn't
-- store (the R2 object was already gone on a retry, or the disk write failed):
-- everything else is refreshed, but the favicon columns are left exactly as
-- they are, and a brand-new row simply has none. The faithful rule (a save
-- with no favicon clears the old one) is for a site that stopped serving one;
-- this is a transient failure on our side, and it must not turn into losing a
-- favicon the link already had.
INSERT INTO links (user_id, raw_url, normalized_url, title, source)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, normalized_url) DO UPDATE
SET raw_url = EXCLUDED.raw_url,
    title = EXCLUDED.title,
    source = EXCLUDED.source,
    updated_at = NOW()
RETURNING *;

-- name: GetLinksUpdatedSince :many
-- Powers the D1 link bookmark mirror's incremental sync (internal/mirror), as
-- GetPagesUpdatedSince does for pages: links changed since the last
-- successfully-pushed checkpoint (itself read from D1's own MAX(updated_at)).
-- A NULL since means "nothing has ever been synced". excluded_from_mirror links
-- are never included -- one that was already synced before being excluded is
-- removed from D1 by the deletion-reconciliation pass
-- (GetMirrorEligibleLinkIDs), not by this query.
SELECT * FROM links
WHERE (sqlc.narg('since')::timestamptz IS NULL OR updated_at > sqlc.narg('since'))
  AND NOT excluded_from_mirror
ORDER BY updated_at ASC;

-- name: GetMirrorEligibleLinkIDs :many
-- The other half of the link mirror's deletion reconciliation: every link id
-- Postgres currently thinks belongs in D1 (not excluded_from_mirror), diffed
-- against the set in D1 to find what to remove, whether the link was deleted
-- or newly excluded -- the two look identical from here.
SELECT id FROM links WHERE NOT excluded_from_mirror;
