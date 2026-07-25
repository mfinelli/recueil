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

-- name: UpsertTag :one
-- Get-or-create by (user_id, name) -- same shape as pages.UpsertPage's own
-- get-or-create. The DO UPDATE (rather than DO NOTHING) is deliberate: a
-- plain ON CONFLICT DO NOTHING RETURNING * returns zero rows on the
-- conflict path, not the existing row -- the standard workaround is a
-- harmless no-op self-update, which is what this is (there's no other
-- column worth actually changing here).
--
-- slug is only ever written on the INSERT path (a genuinely new tag): the
-- DO UPDATE clause below only touches name, so it never overwrites an
-- existing tag's established slug with a caller's freshly (re-)computed
-- candidate. Because the (user_id, name) conflict is fully absorbed by
-- the DO UPDATE, the only error this query can return is the separate
-- (user_id, slug) unique constraint firing -- i.e. a genuinely new tag
-- name whose candidate slug collides with some other, differently-named
-- tag's. Callers that run this outside of a larger transaction (the
-- manual "add tag" handler) can just treat any error as that collision
-- and surface a 409. Callers inside a larger transaction (the AI job)
-- must NOT rely on catching that error -- Postgres aborts the whole
-- transaction on any statement error, not just the one statement -- and
-- should pre-check with TagSlugTaken instead.
INSERT INTO tags (user_id, name, slug) VALUES ($1, $2, $3)
ON CONFLICT (user_id, name) DO UPDATE SET name = EXCLUDED.name
RETURNING *;

-- name: TagSlugTaken :one
-- Pre-check for callers (namely the AI tagging job, see UpsertTag's own
-- comment) that can't risk UpsertTag's insert path raising a real
-- constraint-violation error inside a shared transaction. "Taken" means
-- some *other* tag (a different name) already holds this slug; a match
-- on the same name isn't a collision, it's the ordinary get-or-create
-- path.
SELECT EXISTS (
  SELECT 1 FROM tags WHERE user_id = $1 AND slug = $2 AND name != $3
) AS taken;

-- name: ListTags :many
SELECT * FROM tags WHERE user_id = $1 ORDER BY name;

-- name: GetTagByID :one
SELECT * FROM tags WHERE id = $1 AND user_id = $2;

-- name: GetTagBySlug :one
-- Backs the dashboard's browsable /tags/:slug route (see TagDetail.svelte
-- and ListTagPages) -- the slug, not the id, is what appears in that URL,
-- same reasoning as the rest of the slug work: a person can bookmark or
-- share it. GetTagByID stays in separate use for the id-keyed edit/delete
-- API calls, which never appear in an address bar and have no reason to
-- change.
SELECT * FROM tags WHERE slug = $1 AND user_id = $2;

-- name: RenameTag :one
-- Same shape as RenameCollection: user_id checked in the WHERE clause (so
-- a caller bug passing the wrong id can't rename another user's tag,
-- same belt-and-suspenders reasoning), slug resolved by the caller (see
-- handlers.go's resolveSlug).  This is currently the only way a tag's
-- slug can be customized after creation; AddPageTag's own quick inline
-- "add tag to page" flow always auto-generates one.
UPDATE tags SET name = $1, slug = $2, updated_at = now()
WHERE id = $3 AND user_id = $4
RETURNING *;

-- name: DeleteTag :execrows
-- Cascades to page_tags rows via the schema's own ON DELETE CASCADE --
-- nothing else to clean up. execrows so the caller can distinguish
-- "deleted" from "didn't exist / wasn't this user's", same as
-- DeleteCollection.
DELETE FROM tags WHERE id = $1 AND user_id = $2;
