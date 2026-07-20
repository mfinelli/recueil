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

-- name: CountJobsByStatus :many
-- One row per (job, status) combination that actually has at least one
-- row -- internal/metrics fills in zero for any combination missing from
-- the result, rather than this query needing to enumerate all of them
-- itself (simpler here; see internal/metrics's own doc on why emitting
-- every combination, including zeros, matters for Prometheus).
SELECT 'screenshot'::text AS job, status, COUNT(*) AS count FROM screenshot_jobs GROUP BY status
UNION ALL
SELECT 'readability'::text AS job, status, COUNT(*) AS count FROM readability_jobs GROUP BY status
UNION ALL
SELECT 'ai'::text AS job, status, COUNT(*) AS count FROM ai_jobs GROUP BY status;

-- name: OldestPendingJobAgeSeconds :many
-- Intentionally HAVING COUNT(*) > 0, not a plain WHERE+aggregate: without
-- it, a job type with zero pending rows would still produce exactly one
-- result row (aggregates over an empty set return one row with NULL, not
-- zero rows) -- age_seconds would be NULL, and sqlc infers this column
-- as a non-nullable float64 regardless (its nullability analysis doesn't
-- see through EXTRACT(...)::float8 over MIN() far enough to catch it),
-- so pgx would fail to scan NULL into that float64 at runtime the moment
-- any job type actually had zero pending rows -- the common case, not a
-- rare one. HAVING suppresses the row entirely instead, so a result row
-- existing at all already guarantees a real (non-NULL) value.
SELECT 'screenshot'::text AS job,
  EXTRACT(EPOCH FROM (NOW() - MIN(created_at)))::float8 AS age_seconds
FROM screenshot_jobs WHERE status = 'pending'
HAVING COUNT(*) > 0
UNION ALL
SELECT 'readability'::text AS job,
  EXTRACT(EPOCH FROM (NOW() - MIN(created_at)))::float8 AS age_seconds
FROM readability_jobs WHERE status = 'pending'
HAVING COUNT(*) > 0
UNION ALL
SELECT 'ai'::text AS job,
  EXTRACT(EPOCH FROM (NOW() - MIN(created_at)))::float8 AS age_seconds
FROM ai_jobs WHERE status = 'pending'
HAVING COUNT(*) > 0;
