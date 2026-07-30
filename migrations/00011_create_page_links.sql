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
-- Explicit, bidirectional page-to-page links  -- pairwise edges.
--
-- Each relationship is stored once, as a canonically-ordered pair
-- (page_id_a < page_id_b is enforced below).
--
-- No user_id column, same as page_tags/page_collections: ownership is
-- already enforced by both referenced pages already belonging to the same
-- user (the API layer never accepts a page_id it hasn't first confirmed
-- belongs to the requesting user).
CREATE TABLE page_links (
  page_id_a BIGINT NOT NULL,
  page_id_b BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  CONSTRAINT page_links_pkey PRIMARY KEY (page_id_a, page_id_b),
  CONSTRAINT page_links_page_id_a_fkey FOREIGN KEY (page_id_a)
    REFERENCES pages(id) ON DELETE CASCADE,
  CONSTRAINT page_links_page_id_b_fkey FOREIGN KEY (page_id_b)
    REFERENCES pages(id) ON DELETE CASCADE,
  -- Enforces the canonical ordering (so the app always inserts with the
  -- smaller id first) and, as a side effect, rules out a page linking to
  -- itself: page_id_a < page_id_b can never hold when they're equal.
  CONSTRAINT page_links_ordered_check CHECK (page_id_a < page_id_b)
);

-- The primary key covers "page_id_a = X" efficiently; this covers the
-- other half of the bidirectional OR query.
CREATE INDEX idx_page_links_page_id_b ON page_links(page_id_b);

-- +goose Down
DROP TABLE page_links;
