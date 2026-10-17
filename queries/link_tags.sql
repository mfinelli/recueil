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

-- name: AddLinkTag :exec
-- ON CONFLICT DO NOTHING: adding a tag a link already has is a no-op, not an
-- error. The caller is responsible for having verified that both the link and
-- the tag belong to the requesting user.
INSERT INTO link_tags (link_id, tag_id) VALUES ($1, $2)
ON CONFLICT (link_id, tag_id) DO NOTHING;

-- name: ListLinkTags :many
SELECT tags.id AS tag_id, tags.name AS name, tags.slug AS slug
FROM link_tags
JOIN tags ON tags.id = link_tags.tag_id
WHERE link_tags.link_id = $1
ORDER BY tags.name;

-- name: RemoveLinkTag :exec
DELETE FROM link_tags WHERE link_id = $1 AND tag_id = $2;

-- name: ListTagLinks :many
-- Links carrying a given tag, most recently saved first (ListLinks' ordering).
SELECT links.*
FROM link_tags
JOIN links ON links.id = link_tags.link_id
WHERE link_tags.tag_id = $1
ORDER BY links.created_at DESC, links.id DESC;

-- name: CountTagLinks :one
-- How many links carry a given tag, for the tag screen's "{n} links have this
-- tag" line. The caller has already checked the tag belongs to the user, and
-- tags are per-user, so every link_tags row for it is theirs.
SELECT COUNT(*) FROM link_tags WHERE tag_id = $1;
