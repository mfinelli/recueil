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

-- name: AddPageTag :exec
-- ON CONFLICT DO NOTHING: adding a tag that's already present on a page
-- (from either source) is a no-op, not an error.
INSERT INTO page_tags (page_id, tag_id, source) VALUES ($1, $2, $3)
ON CONFLICT (page_id, tag_id) DO NOTHING;

-- name: ListPageTags :many
SELECT tags.id AS tag_id, tags.name AS name, page_tags.source AS source
FROM page_tags
JOIN tags ON tags.id = page_tags.tag_id
WHERE page_tags.page_id = $1
ORDER BY tags.name;

-- name: RemovePageTag :exec
DELETE FROM page_tags WHERE page_id = $1 AND tag_id = $2;
