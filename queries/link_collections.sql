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

-- name: AddLinkToCollection :exec
-- ON CONFLICT DO NOTHING, same shape as AddPageToCollection. The caller is
-- responsible for having verified that both the link and the collection belong
-- to the requesting user.
INSERT INTO link_collections (link_id, collection_id) VALUES ($1, $2)
ON CONFLICT (link_id, collection_id) DO NOTHING;

-- name: RemoveLinkFromCollection :exec
DELETE FROM link_collections WHERE link_id = $1 AND collection_id = $2;

-- name: ListLinkCollections :many
-- Collections a given link belongs to.
SELECT collections.id AS collection_id, collections.name AS name,
  collections.parent_id AS parent_id
FROM link_collections
JOIN collections ON collections.id = link_collections.collection_id
WHERE link_collections.link_id = $1
ORDER BY collections.name;

-- name: ListCollectionLinks :many
-- Links in a given collection, most recently saved first.
SELECT links.*
FROM link_collections
JOIN links ON links.id = link_collections.link_id
WHERE link_collections.collection_id = $1
ORDER BY links.created_at DESC, links.id DESC;

-- name: CountCollectionLinks :one
-- How many links are directly in a given collection (not its sub-collections,
-- like ListCollectionLinks and the collection's page list), for the collection
-- screen's "{n} links in this collection" line. The caller has already checked
-- the collection belongs to the user.
SELECT COUNT(*) FROM link_collections WHERE collection_id = $1;
