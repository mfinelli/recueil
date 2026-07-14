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
-- One row per capture event: the version history for a page.
CREATE TABLE captures (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  page_id BIGINT NOT NULL,
  source_capture_id TEXT,
  source TEXT NOT NULL DEFAULT 'extension',
  raw_url TEXT NOT NULL,
  title TEXT,
  html_path TEXT NOT NULL,
  html_compressed_size_bytes INTEGER NOT NULL,
  html_uncompressed_size_bytes INTEGER NOT NULL,
  thumbnail_path TEXT,
  reader_text TEXT,
  readability_version TEXT,
  content_hash TEXT NOT NULL,
  reader_text_hash TEXT,
  language REGCONFIG NOT NULL DEFAULT 'simple',
  captured_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT captures_pkey PRIMARY KEY (id),
  CONSTRAINT captures_page_id_fkey FOREIGN KEY (page_id)
    REFERENCES pages(id) ON DELETE CASCADE,
  CONSTRAINT captures_source_capture_id_key UNIQUE (source_capture_id),
  CONSTRAINT captures_source_check CHECK (source IN ('extension', 'manual_upload'))
);

CREATE INDEX idx_captures_page_id ON captures(page_id);

-- Full-text search over reader_text, using each capture's own detected
-- language (see internal/ingest's language detection) rather than a
-- single hardcoded configuration. coalesce(reader_text, '') means this
-- generated column -- and therefore the FTS index -- tolerates reader_text
-- being NULL (extraction pending or failed) from the start, rather than
-- needing special-casing.
ALTER TABLE captures ADD COLUMN reader_text_tsv tsvector
  GENERATED ALWAYS AS (to_tsvector(language, coalesce(reader_text, ''))) STORED;

CREATE INDEX idx_captures_fts ON captures USING GIN (reader_text_tsv);

-- +goose Down
DROP TABLE captures;
