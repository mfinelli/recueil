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
  content_hash, captured_at, language, favicon_path, favicon_size_bytes,
  favicon_hash
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (source_capture_id) DO UPDATE SET
source_capture_id = EXCLUDED.source_capture_id, updated_at = NOW()
RETURNING *, (xmax = 0) AS inserted;

-- name: GetCaptureByID :one
SELECT * FROM captures WHERE id = $1;

-- name: GetCaptureByIDForUser :one
-- The dashboard-facing counterpart to GetCaptureByID: captures has no
-- user_id of its own, so ownership is checked via a join through pages
-- (same reasoning as pages.GetPageByIDForUser -- a caller shouldn't be
-- able to fetch another user's capture by guessing/incrementing an id).
SELECT captures.* FROM captures
JOIN pages ON pages.id = captures.page_id
WHERE captures.id = $1 AND pages.user_id = $2;

-- name: SetCaptureLanguage :one
-- Manual language correction: reader_text_tsv recomputes automatically as
-- part of this same UPDATE, same as readability_jobs.sql's
-- SetCaptureReadability -- it's a generated column, not separately settable,
-- so there's nothing extra to do here to keep search results consistent with
-- the corrected language. Ownership checked via the same pages join as
-- GetCaptureByIDForUser.
UPDATE captures SET language = $1, updated_at = NOW()
FROM pages
WHERE captures.page_id = pages.id AND captures.id = $2 AND pages.user_id = $3
RETURNING captures.*;

-- name: ListCapturesByPage :many
-- Version history for a page's detail view -- most recent first. Not
-- scoped by user_id (captures has no such column); the caller is
-- responsible for having already verified page ownership via
-- pages.GetPageByIDForUser before calling this with that page's id.
SELECT * FROM captures WHERE page_id = $1 ORDER BY captured_at DESC;

-- name: GetLatestCaptureByPage :one
-- Used to resolve "this page's thumbnail" (GET /api/pages/{id}/thumbnail):
-- pages, unlike favicon_path, has no denormalized thumbnail column of its
-- own (thumbnails are written async by the screenshot job well after
-- ingestion, not at UpsertPage time the way favicon_path is), so the
-- latest capture's own thumbnail_path is looked up fresh on each request
-- rather than kept in sync on pages. Same ownership-scoping caveat as
-- ListCapturesByPage above.
SELECT * FROM captures WHERE page_id = $1 ORDER BY captured_at DESC LIMIT 1;

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
