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

-- name: CreateCollection :one
-- No upsert here, unlike UpsertTag/UpsertPage -- collections are created
-- by an explicit user action (not derived from ingestion), so a duplicate
-- name under the same parent should surface as a real conflict for the
-- caller to turn into a 409, not be silently merged into the existing row.
INSERT INTO collections (user_id, parent_id, name)
VALUES ($1, $2, $3)
RETURNING *;

-- name: RenameCollection :one
-- user_id is checked in the WHERE clause, not just the id -- the same
-- belt-and-suspenders pattern as the D1 token-revoke cross-check: a caller
-- bug that passes the wrong id can't rename another user's collection. Zero
-- rows back means either it doesn't exist or it isn't this user's.
UPDATE collections SET name = $1
WHERE id = $2 AND user_id = $3
RETURNING *;

-- name: DeleteCollection :execrows
-- Cascades to child collections (the subtree) and page_collections rows
-- via the schema's own ON DELETE CASCADE chain -- nothing else to clean up
-- here. execrows so the caller can distinguish "deleted" from "didn't
-- exist / wasn't this user's" the same way RenameCollection does.
DELETE FROM collections WHERE id = $1 AND user_id = $2;

-- name: GetCollectionByID :one
SELECT * FROM collections WHERE id = $1 AND user_id = $2;

-- name: ListCollectionsByUser :many
-- Flat list, not a tree -- the dashboard reconstructs the tree client-side
-- from (id, parent_id) the same way any adjacency-list consumer would;
-- no recursive CTE needed for a full-user listing like this one.
SELECT * FROM collections
WHERE user_id = $1
ORDER BY parent_id NULLS FIRST, name;
