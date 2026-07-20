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

-- name: CreateAIJob :exec
-- Called by the readability job itself, in the same transaction as
-- marking itself done.
INSERT INTO ai_jobs (capture_id) VALUES ($1);

-- name: GetAIJobByCaptureID :one
SELECT * FROM ai_jobs WHERE capture_id = $1;

-- name: ClaimDueAIJobs :many
-- Simpler than ClaimDueScreenshotJobs/ClaimDueReadabilityJobs: no
-- readiness join against captures is needed, since an ai_jobs row only
-- ever exists once its precondition (reader_text set) is already met.
-- Otherwise identical shape: FOR UPDATE SKIP LOCKED + stale-reclaim, for
-- the same future-multi-process-safety reasoning as the other two jobs.
-- Joins pages (not just captures) because tags live on pages, not
-- captures, and tags.user_id needs the owning user too.
WITH claimable AS (
  SELECT ai_jobs.id AS id
  FROM ai_jobs
  WHERE (
    ai_jobs.status = 'pending'
    AND (ai_jobs.next_attempt_at IS NULL OR ai_jobs.next_attempt_at <= NOW())
  ) OR (
    ai_jobs.status = 'processing'
    AND ai_jobs.claimed_at <= sqlc.arg(stale_before)
  )
  ORDER BY ai_jobs.created_at
  LIMIT sqlc.arg(row_limit)
  FOR UPDATE SKIP LOCKED
)
UPDATE ai_jobs
SET status = 'processing', claimed_at = NOW()
FROM claimable, captures, pages
WHERE ai_jobs.id = claimable.id
  AND captures.id = ai_jobs.capture_id
  AND pages.id = captures.page_id
RETURNING
  ai_jobs.id AS job_id,
  ai_jobs.capture_id AS capture_id,
  ai_jobs.attempts AS attempts,
  captures.reader_text AS reader_text,
  pages.id AS page_id,
  pages.user_id AS user_id;

-- name: SetCaptureAI :exec
UPDATE captures
SET ai_summary = $2, ai_model = $3, updated_at = NOW()
WHERE id = $1;

-- name: MarkAIJobDone :exec
UPDATE ai_jobs SET status = 'done', completed_at = NOW(), error = NULL
WHERE id = $1;

-- name: RetryAIJob :exec
UPDATE ai_jobs
SET status = 'pending', attempts = $2, next_attempt_at = $3, error = $4, claimed_at = NULL
WHERE id = $1;

-- name: FailAIJob :exec
UPDATE ai_jobs
SET status = 'failed', attempts = $2, error = $3, completed_at = NOW()
WHERE id = $1;
