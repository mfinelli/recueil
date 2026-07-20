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
CREATE TABLE ai_jobs (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  capture_id BIGINT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  claimed_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  CONSTRAINT ai_jobs_pkey PRIMARY KEY (id),
  CONSTRAINT ai_jobs_capture_id_fkey FOREIGN KEY (capture_id)
    REFERENCES captures(id) ON DELETE CASCADE,
  CONSTRAINT ai_jobs_status_check
    CHECK (status IN ('pending', 'processing', 'done', 'failed'))
);

CREATE INDEX idx_ai_jobs_capture_id ON ai_jobs(capture_id);

-- +goose Down
DROP TABLE ai_jobs;
