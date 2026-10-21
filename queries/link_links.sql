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

-- name: AddLinkLink :exec
-- Relates two links, as AddPageLink does for pages: LEAST/GREATEST canonicalize the
-- pair, so callers can pass the two ids in either order, and relating two already
-- related links again is a no-op, not an error. The caller has already verified
-- both links belong to the requesting user; this table has no user_id of its own.
-- The explicit ::bigint casts and sqlc.arg names are for the same reasons given on
-- AddPageLink (sqlc can't infer a type through LEAST/GREATEST, and would give both
-- generated fields the same name).
INSERT INTO link_links (link_id_a, link_id_b)
VALUES (
  LEAST(sqlc.arg(link_a)::bigint, sqlc.arg(link_b)::bigint),
  GREATEST(sqlc.arg(link_a)::bigint, sqlc.arg(link_b)::bigint)
)
ON CONFLICT (link_id_a, link_id_b) DO NOTHING;

-- name: RemoveLinkLink :exec
-- Same canonicalization as AddLinkLink: the caller doesn't need to know which side
-- of the stored pair either id ended up on.
DELETE FROM link_links
WHERE link_id_a = LEAST(sqlc.arg(link_a)::bigint, sqlc.arg(link_b)::bigint)
  AND link_id_b = GREATEST(sqlc.arg(link_a)::bigint, sqlc.arg(link_b)::bigint);

-- name: ListLinkLinks :many
-- Every link related to the given one, from either side of the stored pair: the
-- CASE picks whichever id in the pair isn't the link being looked up, so the same
-- row is visible (as "the other link") from both ends. Ordered as page links are,
-- by title with untitled ones last, then URL.
SELECT links.id, links.title, links.normalized_url, links.favicon_path
FROM link_links
JOIN links ON links.id = CASE
  WHEN link_links.link_id_a = sqlc.arg(link_id) THEN link_links.link_id_b
  ELSE link_links.link_id_a
END
WHERE link_links.link_id_a = sqlc.arg(link_id)
  OR link_links.link_id_b = sqlc.arg(link_id)
ORDER BY links.title NULLS LAST, links.normalized_url;
