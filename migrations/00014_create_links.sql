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
-- Links: bookmark-only entries for URLs that can't be archived well (videos,
-- git repositories, multi-page sites, JavaScript-heavy apps). A link is its
-- own entity separate from pages and captures. Not to be confused with
-- page_links, which are page-to-page relationships.
--
-- raw_url is what was actually open in the tab (what the dashboard opens and
-- what the browser-bookmark sync will mirror); normalized_url is the same
-- urlnorm pipeline's output pages use, and is what de-duplicates a link and
-- lets the dashboard notice the same URL is also an archived page.
-- title and the favicon are supplied by the extension from the live tab so
-- they must be nullable.
--
-- The unique constraint's index already serves "WHERE user_id = X", so
-- unlike pages there is no separate idx_links_user_id.
CREATE TABLE links (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  user_id BIGINT NOT NULL,
  raw_url TEXT NOT NULL,
  normalized_url TEXT NOT NULL,
  title TEXT,
  favicon_path TEXT,
  favicon_size_bytes INTEGER,
  favicon_hash TEXT,
  notes TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT links_pkey PRIMARY KEY (id),
  CONSTRAINT links_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT links_user_id_normalized_url_key UNIQUE (user_id, normalized_url)
);

-- Tags and collections are the same rows pages use (one tag or collection can
-- span both entities); only the membership tables are separate. No `source`
-- column here (unlike page_tags): a link has no body text for an AI job to
-- read, so every link tag is must be manual. No user_id on either, same as
-- page_tags and page_collections: the API layer only ever accepts ids it has
-- already confirmed belong to the requesting user.
CREATE TABLE link_tags (
  link_id BIGINT NOT NULL,
  tag_id BIGINT NOT NULL,
  CONSTRAINT link_tags_pkey PRIMARY KEY (link_id, tag_id),
  CONSTRAINT link_tags_link_id_fkey FOREIGN KEY (link_id)
    REFERENCES links(id) ON DELETE CASCADE,
  CONSTRAINT link_tags_tag_id_fkey FOREIGN KEY (tag_id)
    REFERENCES tags(id) ON DELETE CASCADE
);

CREATE INDEX idx_link_tags_tag_id ON link_tags(tag_id);

CREATE TABLE link_collections (
  link_id BIGINT NOT NULL,
  collection_id BIGINT NOT NULL,
  added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT link_collections_pkey PRIMARY KEY (link_id, collection_id),
  CONSTRAINT link_collections_link_id_fkey FOREIGN KEY (link_id)
    REFERENCES links(id) ON DELETE CASCADE,
  CONSTRAINT link_collections_collection_id_fkey FOREIGN KEY (collection_id)
    REFERENCES collections(id) ON DELETE CASCADE
);

CREATE INDEX idx_link_collections_collection_id ON link_collections(collection_id);

-- +goose Down
DROP TABLE link_collections;
DROP TABLE link_tags;
DROP TABLE links;
