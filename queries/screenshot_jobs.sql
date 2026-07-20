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

-- name: CreateScreenshotJob :exec
INSERT INTO screenshot_jobs (capture_id) VALUES ($1);

-- name: GetScreenshotJobByCaptureID :one
SELECT * FROM screenshot_jobs WHERE capture_id = $1;

-- name: ClaimDueScreenshotJobs :many
-- Atomically claims a batch: a job is claimable if it's 'pending' and due
-- (never attempted, or past its backoff window), or if it's stuck
-- 'processing' past staleBefore -- a prior claimant crashed mid-render
-- without ever reaching a terminal MarkScreenshotJobDone/
-- RetryScreenshotJob/FailScreenshotJob call, the same stale-reclaim shape
-- the D1 queue's own 15-minute claim timeout already uses. FOR UPDATE SKIP
-- LOCKED in the CTE means a second agent process racing for the same batch
-- simply skips whatever the first has already locked, rather than blocking
-- on it -- this is what makes running more than one agent process safe, not
-- just theoretically possible.
--
-- The claiming UPDATE and the join back to captures (for html_path/
-- content_hash) happen in one statement rather than a claim-then-fetch
-- pair, so the row lock is held only for this one brief statement's
-- duration -- never across the actual render, which can take up to
-- renderTimeout.
WITH claimable AS (
  SELECT screenshot_jobs.id AS id
  FROM screenshot_jobs
  WHERE (
    screenshot_jobs.status = 'pending'
    AND (screenshot_jobs.next_attempt_at IS NULL OR screenshot_jobs.next_attempt_at <= NOW())
  ) OR (
    screenshot_jobs.status = 'processing'
    AND screenshot_jobs.claimed_at <= sqlc.arg(stale_before)
  )
  ORDER BY screenshot_jobs.created_at
  LIMIT sqlc.arg(row_limit)
  FOR UPDATE SKIP LOCKED
)
UPDATE screenshot_jobs
SET status = 'processing', claimed_at = NOW()
FROM claimable, captures
WHERE screenshot_jobs.id = claimable.id
  AND captures.id = screenshot_jobs.capture_id
RETURNING
  screenshot_jobs.id AS job_id,
  screenshot_jobs.capture_id AS capture_id,
  screenshot_jobs.attempts AS attempts,
  captures.html_path AS html_path,
  captures.content_hash AS content_hash;

-- name: SetCaptureThumbnail :exec
UPDATE captures
SET thumbnail_path = $2, thumbnail_size_bytes = $3, thumbnail_hash = $4, updated_at = NOW()
WHERE id = $1;

-- name: MarkScreenshotJobDone :exec
UPDATE screenshot_jobs SET status = 'done', completed_at = NOW(), error = NULL
WHERE id = $1;

-- name: RetryScreenshotJob :exec
-- Called on a failure that hasn't yet exhausted max attempts: bumps
-- attempts, pushes next_attempt_at out (caller computes the backoff
-- duration), and records the error for visibility. status moves back to
-- 'pending' (from 'processing', where ClaimDueScreenshotJobs left it) and
-- claimed_at is cleared, so ClaimDueScreenshotJobs's ordinary "due" branch
-- picks it up again once due, rather than its stale-reclaim branch ever
-- needing to.
UPDATE screenshot_jobs
SET status = 'pending', attempts = $2, next_attempt_at = $3, error = $4, claimed_at = NULL
WHERE id = $1;

-- name: FailScreenshotJob :exec
-- Called once max attempts are exhausted: status moves to 'failed'
-- permanently.
UPDATE screenshot_jobs
SET status = 'failed', attempts = $2, error = $3, completed_at = NOW()
WHERE id = $1;
