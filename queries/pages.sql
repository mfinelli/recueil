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

-- name: UpsertPage :one
-- Get-or-create a page by (user_id, normalized_url), refreshing title to
-- the latest capture's title on every call -- pages.title is documented
-- as denormalized from the latest capture, so a plain INSERT ... ON CONFLICT
-- DO NOTHING would leave a stale title after a re-archive under a page whose
-- title has since changed. favicon_path is denormalized the exact same
-- way -- always overwritten to the latest capture's value, including back
-- to NULL if that capture didn't have one, since favicon is per-capture
-- state, not something worth preserving across a capture that genuinely no
-- longer has one. latest_capture_at uses GREATEST rather than a blind
-- overwrite: ingestion order isn't strictly guaranteed to match capture order
-- (a delayed queue item could be ingested after a later one), so this
-- tolerates an out-of-order arrival without regressing the column to an
-- earlier timestamp.
INSERT INTO pages (user_id, normalized_url, title, latest_capture_at, favicon_path)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, normalized_url) DO UPDATE
SET title = $3, updated_at = NOW(),
    latest_capture_at = GREATEST(pages.latest_capture_at, $4),
    favicon_path = $5
RETURNING *;

-- name: GetPageByID :one
SELECT * FROM pages WHERE id = $1;

-- name: GetPageByIDForUser :one
-- The dashboard-facing counterpart to GetPageByID: scoped by user_id so a
-- caller can't fetch another user's page by guessing/incrementing an id.
-- GetPageByID itself is left as-is (unscoped) because its one existing
-- caller (internal/ai's tests) uses it specifically to discover a page's
-- *own* user_id in the first place -- requiring user_id as an input there
-- would be circular.
SELECT * FROM pages WHERE id = $1 AND user_id = $2;

-- name: ListPages :many
-- Plain, unfiltered library browsing -- most recently captured first.
-- COUNT(*) OVER() rides along so the dashboard gets a total for
-- pagination without a second round-trip; Postgres computes window
-- functions before LIMIT/OFFSET slicing, so this is the count of the
-- full matching set, not just the returned page.
SELECT pages.*, COUNT(*) OVER() AS total_count
FROM pages
WHERE user_id = sqlc.arg(user_id)
ORDER BY latest_capture_at DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: SearchPages :many
-- Full-text search: a page matches if ANY of its captures' reader_text
-- matches (not just the latest) -- version history means the content
-- someone remembers and is searching for might only exist in an older
-- capture, and it should still surface the page. DISTINCT ON (pages.id)
-- collapses multi-capture matches to one row per page, keeping the
-- highest-ranked capture's score (the ORDER BY inside the CTE controls
-- which row DISTINCT ON keeps); the outer query then re-sorts the
-- deduplicated set by that score, since DISTINCT ON's own ORDER BY
-- requirement (id first) doesn't leave the result in rank order.
-- plainto_tsquery uses the 'simple' config deliberately, not each
-- capture's own detected language: the query terms are the user's
-- input, not document content, and simple config avoids second-guessing
-- what language *they're* typing in.
WITH ranked AS (
  SELECT DISTINCT ON (pages.id) pages.*,
    ts_rank(captures.reader_text_tsv, plainto_tsquery('simple', sqlc.arg(query)::text)) AS rank
  FROM pages
  JOIN captures ON captures.page_id = pages.id
  WHERE pages.user_id = sqlc.arg(user_id)
    AND captures.reader_text_tsv @@ plainto_tsquery('simple', sqlc.arg(query)::text)
  ORDER BY pages.id, rank DESC
)
SELECT *, COUNT(*) OVER() AS total_count FROM ranked
ORDER BY rank DESC
LIMIT sqlc.arg('limit') OFFSET sqlc.arg('offset');

-- name: SetPageExcludedFromMirror :one
UPDATE pages SET excluded_from_mirror = $1, updated_at = NOW()
WHERE id = $2 AND user_id = $3
RETURNING *;

-- name: SearchPagesForLinking :many
-- Combined title-or-normalized_url search, one input matched against
-- both columns, for the "link this page to..." picker -- the person
-- searching may remember either. Plain ILIKE, not a tsvector/trigram
-- setup: proportionate to a personal library's scale, same reasoning as
-- collections' own recursive-CTE comment elsewhere in this codebase; a
-- pg_trgm GIN index is a drop-in upgrade later if this ever needs it.
-- No pagination/total_count (unlike ListPages/SearchPages above) -- this
-- backs a live typeahead dropdown showing a handful of matches, not a
-- browsable listing.
SELECT *
FROM pages
WHERE user_id = sqlc.arg(user_id)
  AND id != sqlc.arg(exclude_page_id)
  AND (title ILIKE '%' || sqlc.arg(query)::text || '%'
    OR normalized_url ILIKE '%' || sqlc.arg(query)::text || '%')
ORDER BY latest_capture_at DESC
LIMIT sqlc.arg('limit');

-- name: SetPageTitle :one
-- Direct overwrite, not a separate title_override column: pages.title is
-- already denormalized from the latest capture, and a later recapture will
-- overwrite whatever's set here the same way it always has -- which is a
-- deliberate choice, not an oversight.
UPDATE pages SET title = $1, updated_at = NOW()
WHERE id = $2 AND user_id = $3
RETURNING *;

-- name: SetPageNotes :one
-- Unlike SetPageTitle, an empty value is meaningful here (clearing a
-- note is a normal action, not an error).
UPDATE pages SET notes = $1, updated_at = NOW()
WHERE id = $2 AND user_id = $3
RETURNING *;

-- name: SetPageFavicon :exec
-- Called by DeleteCapture (internal/httpapi), not by any dashboard
-- endpoint of its own.
UPDATE pages SET favicon_path = $1, updated_at = NOW() WHERE id = $2;

-- name: DeletePage :execrows
-- Cascades to captures (and, transitively, their screenshot/readability/AI
-- jobs), page_tags, and page_collections rows via the schema's own
-- ON DELETE CASCADE chain -- nothing else to clean up in Postgres. The D1
-- bookmark-list mirror isn't touched synchronously here: it self-heals via
-- the existing periodic Syncer's reconcileDeletions pass (GetMirrorEligiblePageIDs
-- no longer includes this id once the row's gone), same as it already does for
-- excluded_from_mirror. The on-disk archive files (HTML/screenshots/favicons)
-- are deliberately left orphaned here for `recueil gc` to reclaim, rather
-- than removed as part of this statement: that keeps deleting a page a
-- pure database operation, with no way for a partial failure to leave
-- Postgres and the filesystem disagreeing about what still exists.
DELETE FROM pages WHERE id = $1 AND user_id = $2;

-- name: GetPagesUpdatedSince :many
-- Powers the D1 bookmark-list mirror's incremental sync
-- (internal/mirror): pages changed since the last successfully-pushed
-- checkpoint (itself read from D1's own MAX(updated_at), not tracked
-- separately here -- see internal/mirror's own docs for why). A NULL
-- since means "nothing has ever been synced," matching what the Worker's
-- GET /internal/archived-pages/last-sync returns for an empty mirror.
-- excluded_from_mirror pages are never included -- if one was already
-- synced before being excluded, GetMirrorEligiblePageIDs's deletion
-- reconciliation pass is what removes it from D1, not this query.
SELECT * FROM pages
WHERE (sqlc.narg('since')::timestamptz IS NULL OR updated_at > sqlc.narg('since'))
  AND NOT excluded_from_mirror
ORDER BY updated_at ASC;

-- name: GetMirrorEligiblePageIDs :many
-- The other half of mirror sync's deletion reconciliation: every page_id
-- Postgres currently thinks belongs in the D1 mirror (i.e. not
-- excluded_from_mirror), diffed against D1's own current set
-- (GET /internal/archived-pages/page-ids) to find what needs removing
-- from the D1 mirror -- whether because the page was deleted from
-- Postgres entirely, or because it was newly marked excluded_from_mirror.
-- Both look identical from this query's perspective: "no longer belongs
-- in the desired set" -- no special-casing needed for the exclusion case.
SELECT id FROM pages WHERE NOT excluded_from_mirror;
