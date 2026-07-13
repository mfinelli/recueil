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

-- name: InsertCaptureIdempotent :one
-- Idempotent insert keyed by source_capture_id: a real ON CONFLICT
-- DO NOTHING RETURNING would return zero rows on a retry, which isn't
-- useful here -- the caller needs the (possibly already-existing) row's id
-- either way, to enqueue jobs against or to confirm nothing further is needed.
-- ON CONFLICT DO UPDATE with a no-op-in-substance assignment is the standard
-- Postgres idiom for "insert, or fetch the existing row" with RETURNING
-- support.
--
-- The `inserted` column (the classic `xmax = 0` trick) distinguishes a
-- genuinely new row from a retry hitting an already-ingested one: only
-- when true should the caller enqueue screenshot/readability jobs --
-- otherwise they were already enqueued on the original attempt, and
-- doing it again would create duplicate job rows.
INSERT INTO captures (
  page_id, source_capture_id, source, raw_url, title,
  html_path, html_compressed_size_bytes, html_uncompressed_size_bytes,
  content_hash, captured_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (source_capture_id) DO UPDATE SET
source_capture_id = EXCLUDED.source_capture_id, updated_at = NOW()
RETURNING *, (xmax = 0) AS inserted;

-- name: GetCaptureByID :one
SELECT * FROM captures WHERE id = $1;

-- name: GetCaptureBySourceCaptureID :one
-- The pre-check that must happen before ever touching R2: if a row already
-- exists here, an earlier attempt already committed this capture to Postgres,
-- and the caller should skip straight to R2/D1 cleanup rather than re-running
-- the whole pipeline (which would try to pull an R2 object that may already
-- be deleted).
SELECT * FROM captures WHERE source_capture_id = $1;

-- name: ClearSourceCaptureID :exec
-- Called once R2 deletion and the D1 fetched_by_backend flag update have
-- both succeeded: source_capture_id has served its only purpose (letting a
-- retry recognize this row without redoing the pipeline) and nothing else
-- ever reads it. Keyed by the row's own id, not by whatever value happens to
-- be stored there -- collision handling (see internal/ingest) may have
-- regenerated a different value than the one the caller originally submitted.
UPDATE captures SET source_capture_id = NULL, updated_at = NOW()
WHERE id = $1;
