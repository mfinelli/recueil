-- recueil: self-hosted webpage bookmarker and archiver
-- Copyright © 2026 Mario Finelli
--
-- This program is free software: you can redistribute it and/or modify
-- it under the terms of the GNU Affero General Public License as published by
-- the Free Software Foundation, either version 3 of the License, or
-- (at your option) any later version.
--
-- This program is distributed in the hope that it will be useful,
-- but WITHOUT ANY WARRANTY; without even the implied warranty of
-- MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
-- GNU Affero General Public License for more details.
--
-- You should have received a copy of the GNU Affero General Public License
-- along with this program. If not, see <https://www.gnu.org/licenses/>.

-- +goose Up
-- Wayback Machine submissions: on-demand requests to have the Internet Archive
-- capture a URL, tracked so the dashboard can show that it was done and when.
--
-- Keyed by (user, normalized URL): the Wayback Machine captures URLs, so a
-- page and a link with the same URL share one submission, the archive's
-- per-URL daily limit applies to it as one thing, and the record survives a
-- page being deleted and captured again. One row per URL holds the state of
-- its latest attempt; there is no history and no job queue, because a failed
-- attempt is simply retried by hand.
--
-- An attempt starts 'pending' with archive.org's job id (it does the capturing
-- itself, so nothing here is "working" meanwhile) and is resolved by the agent's
-- regular sweep asking archive.org how the job went; nothing else does, and the
-- dashboard only reads the row. last_submitted_at is when the latest SUCCESSFUL
-- attempt was submitted -- what the screen shows -- and survives a later failed
-- attempt, which only changes status and error.
CREATE TABLE wayback_submissions (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  user_id BIGINT NOT NULL,
  normalized_url TEXT NOT NULL,
  status TEXT NOT NULL,
  job_id TEXT,
  submitted_at TIMESTAMPTZ NOT NULL,
  last_submitted_at TIMESTAMPTZ,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT wayback_submissions_pkey PRIMARY KEY (id),
  CONSTRAINT wayback_submissions_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT wayback_submissions_status_check
    CHECK (status IN ('pending', 'succeeded', 'failed')),
  CONSTRAINT wayback_submissions_user_url_key UNIQUE (user_id, normalized_url)
);

-- The agent's sweep reads only the unresolved ones.
CREATE INDEX idx_wayback_submissions_pending ON wayback_submissions(submitted_at)
  WHERE status = 'pending';

-- +goose Down
DROP TABLE wayback_submissions;
