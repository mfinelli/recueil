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

-- name: AddPageToCollection :exec
-- ON CONFLICT DO NOTHING: adding a page to a collection it's already in is
-- a no-op, not an error -- same shape as page_tags.AddPageTag. Caller
-- (internal/httpapi) is responsible for having already verified both the
-- page and the collection belong to the requesting user; this table has no
-- user_id column of its own to check against.
INSERT INTO page_collections (page_id, collection_id) VALUES ($1, $2)
ON CONFLICT (page_id, collection_id) DO NOTHING;

-- name: RemovePageFromCollection :exec
DELETE FROM page_collections WHERE page_id = $1 AND collection_id = $2;

-- name: ListPageCollections :many
-- Collections a given page belongs to.
SELECT collections.id AS collection_id, collections.name AS name,
  collections.parent_id AS parent_id
FROM page_collections
JOIN collections ON collections.id = page_collections.collection_id
WHERE page_collections.page_id = $1
ORDER BY collections.name;

-- name: ListCollectionPages :many
-- Pages in a given collection, most recently captured first -- same
-- ordering the library view itself will default to.
SELECT pages.*
FROM page_collections
JOIN pages ON pages.id = page_collections.page_id
WHERE page_collections.collection_id = $1
ORDER BY pages.latest_capture_at DESC;
