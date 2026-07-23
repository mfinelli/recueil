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

-- Bookmark-list mirror: a lightweight, title+URL-only copy of Postgres's
-- pages table, so the extension can browse "everything already archived"
-- without ever needing the backend to be reachable. Backend -> D1 only,
-- same direction as every other mirror table.
--
-- page_id is never D1-generated: it's always supplied explicitly from the
-- Postgres-side pages.id value on every mirror-push INSERT (same
-- reasoning as users.id), so plain INTEGER PRIMARY KEY (rowid alias, not
-- AUTOINCREMENT) is correct here.
--
-- updated_at directly mirrors Postgres pages.updated_at -- not "when this
-- D1 row was last written," which is a distinction that matters: the
-- backend always sets this explicitly to the source value on every push,
-- never lets D1 stamp its own write time. MAX(updated_at) across this
-- table *is* the incremental-sync checkpoint (see
-- GET /internal/archived-pages/last-sync) -- deliberately not a
-- separately-tracked watermark value anywhere, so there's nothing that
-- can drift from what D1 actually contains.
CREATE TABLE archived_pages (
  page_id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  raw_url TEXT NOT NULL,
  title TEXT,
  latest_capture_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_archived_pages_user_id ON archived_pages(user_id);

-- Backs both the MAX(updated_at) checkpoint read and the incremental
-- WHERE-clause pattern the backend uses on its own Postgres side; kept
-- here too since a full-table scan for MAX() would be needless at any
-- real scale.
CREATE INDEX idx_archived_pages_updated_at ON archived_pages(updated_at);
