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
-- page_relations, which are page-to-page relationships.
--
-- raw_url is what was actually open in the tab (what the dashboard opens and
-- what the browser-bookmark sync will mirror); normalized_url is the same
-- urlnorm pipeline's output pages use, and is what de-duplicates a link and
-- lets the dashboard notice the same URL is also an archived page.
-- title and the favicon are supplied by the extension from the live tab so
-- they must be nullable.
--
-- source is where the link's current title and favicon came from, as
-- captures.source is for a capture: 'extension' (read from a live browser tab,
-- the only thing that ever completes a saved link from a device) or 'manual'
-- (typed into the dashboard's Add link form). Because a re-save refreshes the
-- one row, it's the source of the LATEST save: a manually added link saved
-- again from the browser becomes 'extension', since its title and favicon now
-- come from the tab. (Adding a URL by hand that already exists is refused, not
-- a refresh, so a manual save never overwrites anything.) Not 'manual_upload'
-- as on captures: nothing is uploaded here.
--
-- excluded_from_mirror opts a link out of the browser-bookmark sync, the same
-- as pages.excluded_from_mirror.
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
  source TEXT NOT NULL DEFAULT 'extension',
  excluded_from_mirror BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT links_pkey PRIMARY KEY (id),
  CONSTRAINT links_source_check CHECK (source IN ('extension', 'manual')),
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

-- Links related to other links: one row per related pair, stored with the
-- smaller id first so a pair has exactly one representation and is visible
-- from both ends. Like page_relations it has no user_id of its own; the handlers
-- check that both links belong to the user before writing a pair.
CREATE TABLE link_relations (
  link_id_a BIGINT NOT NULL,
  link_id_b BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT link_relations_pkey PRIMARY KEY (link_id_a, link_id_b),
  CONSTRAINT link_relations_link_id_a_fkey FOREIGN KEY (link_id_a)
    REFERENCES links(id) ON DELETE CASCADE,
  CONSTRAINT link_relations_link_id_b_fkey FOREIGN KEY (link_id_b)
    REFERENCES links(id) ON DELETE CASCADE,
  -- Enforces the canonical ordering and, as a side effect, rules out a link being
  -- related to itself: link_id_a < link_id_b can never hold when they're equal.
  CONSTRAINT link_relations_ordered_check CHECK (link_id_a < link_id_b)
);

-- The primary key covers "link_id_a = X"; this covers the other half of the
-- bidirectional OR query.
CREATE INDEX idx_link_relations_link_id_b ON link_relations(link_id_b);

-- +goose Down
DROP TABLE link_relations;
DROP TABLE link_collections;
DROP TABLE link_tags;
DROP TABLE links;
