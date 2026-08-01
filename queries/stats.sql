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

-- name: GetUserStats :one
-- A plain SUM across every capture row, not deduplicated.
--
-- Two CTEs rather than one JOIN with a correlated page-count subquery,
-- and pages.user_id qualified explicitly in *both* CTEs even though
-- each only references one `pages` table on its own: leaving either
-- bare (`user_id` instead of `pages.user_id`) makes Postgres's analyzer
-- report "column reference is ambiguous" once the two CTEs are combined
-- in the final cross join below, even though each CTE's own FROM clause
-- has only one table in scope. Both CTEs always return exactly one row
-- (COUNT is 0, SUM is NULL over an empty set, coalesced to 0 below) even
-- for a user with zero pages/captures, so the final cross join is always
-- exactly one row too.
WITH page_totals AS (
  SELECT COUNT(*) AS page_count FROM pages WHERE pages.user_id = sqlc.arg(user_id)
),
capture_totals AS (
  SELECT
    COUNT(*) AS capture_count,
    COALESCE(SUM(html_compressed_size_bytes), 0)::bigint AS html_compressed_bytes,
    COALESCE(SUM(html_uncompressed_size_bytes), 0)::bigint AS html_uncompressed_bytes,
    COALESCE(SUM(favicon_size_bytes), 0)::bigint AS favicon_bytes,
    COALESCE(SUM(thumbnail_size_bytes), 0)::bigint AS screenshot_bytes
  FROM captures
  JOIN pages ON pages.id = captures.page_id
  WHERE pages.user_id = sqlc.arg(user_id)
)
SELECT * FROM page_totals, capture_totals;

-- name: GetSystemStats :one
-- Admin-only; Same shape as GetUserStats, just without the per-user WHERE
-- filter -- literally every page/capture, system-wide.
SELECT
  (SELECT COUNT(*) FROM pages) AS page_count,
  COUNT(*) AS capture_count,
  COALESCE(SUM(html_compressed_size_bytes), 0)::bigint AS html_compressed_bytes,
  COALESCE(SUM(html_uncompressed_size_bytes), 0)::bigint AS html_uncompressed_bytes,
  COALESCE(SUM(favicon_size_bytes), 0)::bigint AS favicon_bytes,
  COALESCE(SUM(thumbnail_size_bytes), 0)::bigint AS screenshot_bytes
FROM captures;

-- name: GetTopUsersByStorage :many
-- The 5 heaviest users by total storage, for the same admin-only section
-- as GetSystemStats above. A coarser two-way breakdown than
-- GetUserStats'/GetSystemStats' three-way one (compressed HTML vs.
-- favicons-and-screenshots combined into one figure).
--
-- Inner joins throughout (not LEFT JOIN): a user with zero
-- pages/captures isn't a "top consumer" by definition, so excluding them
-- entirely is correct here, not a bug -- unlike GetUserStats, which
-- needs to represent "zero" for a single specific user rather than omit
-- rows from a ranking.
SELECT
  users.username,
  COUNT(captures.id) AS capture_count,
  COALESCE(SUM(captures.html_compressed_size_bytes), 0)::bigint AS html_compressed_bytes,
  COALESCE(SUM(
    COALESCE(captures.favicon_size_bytes, 0) + COALESCE(captures.thumbnail_size_bytes, 0)
  ), 0)::bigint AS other_bytes
FROM users
JOIN pages ON pages.user_id = users.id
JOIN captures ON captures.page_id = pages.id
GROUP BY users.id, users.username
ORDER BY
  COALESCE(SUM(captures.html_compressed_size_bytes), 0)
    + COALESCE(SUM(COALESCE(captures.favicon_size_bytes, 0) + COALESCE(captures.thumbnail_size_bytes, 0)), 0)
  DESC
LIMIT 5;
