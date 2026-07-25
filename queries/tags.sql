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
