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
CREATE TABLE tags (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  user_id BIGINT NOT NULL,
  name TEXT NOT NULL,
  CONSTRAINT tags_pkey PRIMARY KEY (id),
  CONSTRAINT tags_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT tags_user_id_name_key UNIQUE (user_id, name)
);

CREATE INDEX idx_tags_user_id ON tags(user_id);

-- Tags live on pages, not captures: tags describe the subject matter of
-- the URL, which doesn't change per-version. Both manually-applied and
-- AI-applied tags coexist here, distinguished by source -- including,
-- the same (page_id, tag_id) pair from both sources at once: if a user
-- already tagged a page manually and the AI job later suggests the same
-- tag name, the AI's insert is a no-op (see queries/page_tags.sql's
-- AddPageTag), not a conflict error, and whichever source got there first
-- is simply what's recorded.
CREATE TABLE page_tags (
  page_id BIGINT NOT NULL,
  tag_id BIGINT NOT NULL,
  source TEXT NOT NULL DEFAULT 'manual',
  CONSTRAINT page_tags_pkey PRIMARY KEY (page_id, tag_id),
  CONSTRAINT page_tags_page_id_fkey FOREIGN KEY (page_id)
    REFERENCES pages(id) ON DELETE CASCADE,
  CONSTRAINT page_tags_tag_id_fkey FOREIGN KEY (tag_id)
    REFERENCES tags(id) ON DELETE CASCADE,
  CONSTRAINT page_tags_source_check CHECK (source IN ('manual', 'ai'))
);

CREATE INDEX idx_page_tags_tag_id ON page_tags(tag_id);

-- +goose Down
DROP TABLE page_tags;
DROP TABLE tags;
