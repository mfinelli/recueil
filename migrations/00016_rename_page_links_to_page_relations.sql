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
-- page_links becomes page_relations. "links" had come to mean three different
-- things: the saved-link entity, a page related to other pages, and (new in
-- v2) a link related to other links. Every relationship between two items is
-- now named by what it relates: page_relations (page to page), link_relations
-- (link to link) and page_link_relations (a page to a link).
--
-- A rename only: no data moves. Constraint and index names follow the table, so
-- nothing keeps the old name.
ALTER TABLE page_links RENAME TO page_relations;
ALTER TABLE page_relations RENAME CONSTRAINT page_links_pkey TO page_relations_pkey;
ALTER TABLE page_relations RENAME CONSTRAINT page_links_page_id_a_fkey TO page_relations_page_id_a_fkey;
ALTER TABLE page_relations RENAME CONSTRAINT page_links_page_id_b_fkey TO page_relations_page_id_b_fkey;
ALTER TABLE page_relations RENAME CONSTRAINT page_links_ordered_check TO page_relations_ordered_check;
ALTER INDEX idx_page_links_page_id_b RENAME TO idx_page_relations_page_id_b;

-- +goose Down
ALTER INDEX idx_page_relations_page_id_b RENAME TO idx_page_links_page_id_b;
ALTER TABLE page_relations RENAME CONSTRAINT page_relations_ordered_check TO page_links_ordered_check;
ALTER TABLE page_relations RENAME CONSTRAINT page_relations_page_id_b_fkey TO page_links_page_id_b_fkey;
ALTER TABLE page_relations RENAME CONSTRAINT page_relations_page_id_a_fkey TO page_links_page_id_a_fkey;
ALTER TABLE page_relations RENAME CONSTRAINT page_relations_pkey TO page_links_pkey;
ALTER TABLE page_relations RENAME TO page_links;
