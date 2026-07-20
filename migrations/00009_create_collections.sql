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
-- Nested collections. Adjacency list (parent_id self-reference) rather than
-- a closure table: simpler writes, and at this project's scale a recursive
-- CTE for "this collection and all descendants" is fast enough that a
-- closure table's extra write-complexity isn't justified.
--
-- Uniqueness is per (user_id, parent_id, name), but that can't be a single
-- UNIQUE table constraint here: parent_id is nullable for top-level
-- collections, and Postgres treats NULL as distinct from itself in a unique
-- constraint, so a plain UNIQUE(user_id, parent_id, name) would silently
-- allow two top-level collections named the same thing. Two partial unique
-- indexes instead -- one per case -- since each is a normal (non-NULL)
-- unique check within its own partition.
CREATE TABLE collections (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  user_id BIGINT NOT NULL,
  parent_id BIGINT,
  name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT collections_pkey PRIMARY KEY (id),
  CONSTRAINT collections_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT collections_parent_id_fkey FOREIGN KEY (parent_id)
    REFERENCES collections(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX collections_user_id_name_top_level_key
  ON collections(user_id, name) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX collections_user_id_parent_id_name_key
  ON collections(user_id, parent_id, name) WHERE parent_id IS NOT NULL;

CREATE INDEX idx_collections_user_id ON collections(user_id);
CREATE INDEX idx_collections_parent_id ON collections(parent_id);

-- A page may be in zero, one, or many collections. Deleting a collection
-- cascades to delete child collections (the subtree, via collections'
-- own self-referencing FK above) and, separately, removes this table's
-- *membership* rows for every collection in that deleted subtree. There is
-- no dedicated "Unsorted" collection row; absence of membership rows here
-- IS the Unsorted state.
CREATE TABLE page_collections (
  page_id BIGINT NOT NULL,
  collection_id BIGINT NOT NULL,
  added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT page_collections_pkey PRIMARY KEY (page_id, collection_id),
  CONSTRAINT page_collections_page_id_fkey FOREIGN KEY (page_id)
    REFERENCES pages(id) ON DELETE CASCADE,
  CONSTRAINT page_collections_collection_id_fkey FOREIGN KEY (collection_id)
    REFERENCES collections(id) ON DELETE CASCADE
);

CREATE INDEX idx_page_collections_collection_id ON page_collections(collection_id);

-- +goose Down
DROP TABLE page_collections;
DROP TABLE collections;
