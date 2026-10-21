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

-- name: AddPageLinkRelation :exec
-- Relates a page to a link. One row per pair, and relating them again is a no-op,
-- not an error. The caller has already verified both belong to the requesting user,
-- and user_id is that owner, which composite foreign keys on the table enforce: a pair
-- owned by different users is refused by Postgres. There is no ordering to
-- canonicalize, since the two ends are different kinds of item.
INSERT INTO page_link_relations (page_id, link_id, user_id)
VALUES ($1, $2, $3)
ON CONFLICT (page_id, link_id) DO NOTHING;

-- name: RemovePageLinkRelation :exec
DELETE FROM page_link_relations WHERE page_id = $1 AND link_id = $2;

-- name: ListPageRelatedLinks :many
-- The links related to a page, for its detail response: ordered as every related
-- list is, by title with untitled ones last, then URL.
SELECT links.id, links.title, links.normalized_url, links.favicon_path
FROM page_link_relations
JOIN links ON links.id = page_link_relations.link_id
WHERE page_link_relations.page_id = $1
ORDER BY links.title NULLS LAST, links.normalized_url;

-- name: ListLinkRelatedPages :many
-- The pages related to a link, for its detail response, ordered the same way.
SELECT pages.id, pages.title, pages.normalized_url, pages.favicon_path
FROM page_link_relations
JOIN pages ON pages.id = page_link_relations.page_id
WHERE page_link_relations.link_id = $1
ORDER BY pages.title NULLS LAST, pages.normalized_url;
