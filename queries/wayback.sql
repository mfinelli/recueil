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

-- name: StartWaybackSubmission :one
-- Records a new attempt for a URL: pending, with archive.org's job id, replacing
-- whatever the previous attempt left (its error clears; last_submitted_at, the
-- last SUCCESS, is left untouched). The caller has already refused to start
-- one while another is in flight.
INSERT INTO wayback_submissions (user_id, normalized_url, status, job_id, submitted_at)
VALUES ($1, $2, 'pending', $3, NOW())
ON CONFLICT (user_id, normalized_url) DO UPDATE
SET status = 'pending',
    job_id = EXCLUDED.job_id,
    submitted_at = NOW(),
    error = NULL,
    updated_at = NOW()
RETURNING *;

-- name: GetWaybackSubmission :one
SELECT * FROM wayback_submissions WHERE user_id = $1 AND normalized_url = $2;

-- name: ListPendingWaybackSubmissions :many
-- What the agent's sweep resolves, oldest first.
SELECT * FROM wayback_submissions WHERE status = 'pending' ORDER BY submitted_at ASC;

-- name: SucceedWaybackSubmission :one
-- The job succeeded: the attempt's submitted_at becomes the last successful
-- submission. Guarded by status and job id so that a resolver looking at a row a
-- newer attempt has since replaced (the user submitted again while the sweep was
-- asking about the old job), or a second resolver looking at the same row, can't
-- clobber it: whichever is second finds nothing to update (pgx.ErrNoRows) and
-- leaves the row alone.
UPDATE wayback_submissions
SET status = 'succeeded', last_submitted_at = submitted_at, error = NULL, updated_at = NOW()
WHERE id = $1 AND status = 'pending' AND job_id = $2
RETURNING *;

-- name: FailWaybackSubmission :one
-- The attempt failed, or archive.org never reported a result. Same guard as
-- SucceedWaybackSubmission; last_submitted_at (the last success) is left alone.
UPDATE wayback_submissions
SET status = 'failed', error = $3, updated_at = NOW()
WHERE id = $1 AND status = 'pending' AND job_id = $2
RETURNING *;
