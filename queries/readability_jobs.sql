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

-- name: CreateReadabilityJob :exec
INSERT INTO readability_jobs (capture_id) VALUES ($1);

-- name: GetReadabilityJobByCaptureID :one
SELECT * FROM readability_jobs WHERE capture_id = $1;

-- name: ClaimDueReadabilityJobs :many
-- Atomically claims a batch: a job is claimable if it's 'pending' and due
-- (never attempted, or past its backoff window), or if it's stuck
-- 'processing' past staleBefore -- a prior claimant crashed mid-extraction
-- without ever reaching a terminal MarkReadabilityJobDone/
-- RetryReadabilityJob/FailReadabilityJob call. Same reasoning as
-- ClaimDueScreenshotJobs (internal/screenshot's package doc), just applied
-- to this table: FOR UPDATE SKIP LOCKED isn't needed by today's
-- single-process agent, but makes a future multi-process deployment safe
-- with no changes to this query at all.
WITH claimable AS (
  SELECT readability_jobs.id AS id
  FROM readability_jobs
  WHERE (
    readability_jobs.status = 'pending'
    AND (readability_jobs.next_attempt_at IS NULL OR readability_jobs.next_attempt_at <= NOW())
  ) OR (
    readability_jobs.status = 'processing'
    AND readability_jobs.claimed_at <= sqlc.arg(stale_before)
  )
  ORDER BY readability_jobs.created_at
  LIMIT sqlc.arg(row_limit)
  FOR UPDATE SKIP LOCKED
)
UPDATE readability_jobs
SET status = 'processing', claimed_at = NOW()
FROM claimable, captures
WHERE readability_jobs.id = claimable.id
  AND captures.id = readability_jobs.capture_id
RETURNING
  readability_jobs.id AS job_id,
  readability_jobs.capture_id AS capture_id,
  readability_jobs.attempts AS attempts,
  captures.html_path AS html_path;

-- name: SetCaptureReadability :exec
-- Overwrites reader_text/reader_text_hash/readability_version in place --
-- no history of prior extractions kept. reader_text_tsv (captures' own
-- generated tsvector column) recomputes automatically from the new reader_text
-- as part of this same UPDATE, since generated columns are just that:
-- generated, not separately settable.
UPDATE captures
SET reader_text = $2, reader_text_hash = $3, readability_version = $4, updated_at = NOW()
WHERE id = $1;

-- name: MarkReadabilityJobDone :exec
UPDATE readability_jobs SET status = 'done', completed_at = NOW(), error = NULL
WHERE id = $1;

-- name: RetryReadabilityJob :exec
-- Called on a failure that hasn't yet exhausted max attempts: bumps
-- attempts, pushes next_attempt_at out (caller computes the backoff
-- duration), and records the error for visibility. status moves back to
-- 'pending' (from 'processing', where ClaimDueReadabilityJobs left it) and
-- claimed_at is cleared, so ClaimDueReadabilityJobs's ordinary "due" branch
-- picks it up again once due, rather than its stale-reclaim branch ever
-- needing to.
UPDATE readability_jobs
SET status = 'pending', attempts = $2, next_attempt_at = $3, error = $4, claimed_at = NULL
WHERE id = $1;

-- name: FailReadabilityJob :exec
-- Called once max attempts are exhausted: status moves to 'failed'
-- permanently (no further automatic retry -- the failed row itself
-- serves as the dead-letter queue, same as screenshot_jobs).
UPDATE readability_jobs
SET status = 'failed', attempts = $2, error = $3, completed_at = NOW()
WHERE id = $1;
