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

-- name: AddPageLink :exec
-- LEAST/GREATEST canonicalize the pair here, so callers (internal/httpapi)
-- can pass the two page ids in either order without computing the
-- ordering themselves -- same ON CONFLICT DO NOTHING shape as
-- AddPageToCollection: linking two already-linked pages again is a
-- no-op, not an error. Caller is responsible for having already verified
-- both pages belong to the requesting user; this table has no user_id
-- column of its own to check against.
--
-- Explicit ::bigint casts and sqlc.arg names: without them, sqlc can't
-- infer a type through LEAST/GREATEST (producing unusable `interface{}`
-- params) and, separately, derives both generated struct field names
-- from the same comparison column -- so named explicitly rather
-- than ending up with two same-looking fields that are easy to
-- transpose by accident.
INSERT INTO page_links (page_id_a, page_id_b)
VALUES (
  LEAST(sqlc.arg(page_a)::bigint, sqlc.arg(page_b)::bigint),
  GREATEST(sqlc.arg(page_a)::bigint, sqlc.arg(page_b)::bigint)
)
ON CONFLICT (page_id_a, page_id_b) DO NOTHING;

-- name: RemovePageLink :exec
-- Same LEAST/GREATEST canonicalization and explicit-naming reasoning as
-- AddPageLink -- the caller doesn't need to know or care which side of
-- the stored pair either id ended up on.
DELETE FROM page_links
WHERE page_id_a = LEAST(sqlc.arg(page_a)::bigint, sqlc.arg(page_b)::bigint)
  AND page_id_b = GREATEST(sqlc.arg(page_a)::bigint, sqlc.arg(page_b)::bigint);

-- name: ListPageLinks :many
-- Every page linked to the given one, from either side of the stored
-- pair -- this is what makes the link bidirectional at read time: the
-- CASE picks whichever id in the pair *isn't* the page being looked up,
-- so the same row is visible (as "the other page") from both ends
-- without the row itself needing to say which side is "this" page.
SELECT pages.id, pages.title, pages.normalized_url, pages.favicon_path
FROM page_links
JOIN pages ON pages.id = CASE
  WHEN page_links.page_id_a = sqlc.arg(page_id) THEN page_links.page_id_b
  ELSE page_links.page_id_a
END
WHERE page_links.page_id_a = sqlc.arg(page_id)
  OR page_links.page_id_b = sqlc.arg(page_id)
ORDER BY pages.title NULLS LAST, pages.normalized_url;
