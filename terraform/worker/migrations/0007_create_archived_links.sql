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

-- The links counterpart of archived_pages: a lightweight copy of a Postgres
-- links row, just enough for a device to reconcile its browser bookmarks
-- against, pushed by the backend's mirror sync (links that are
-- excluded_from_mirror are never pushed here) and read by the extension's
-- "recueil links" bookmark folder.
--
-- Shaped like archived_pages minus latest_capture_at, which a link has no
-- equivalent of. raw_url is the link's own raw_url (the URL as it was saved),
-- not the normalized one archived_pages carries: that is what a bookmark should
-- open, and it is a unique key per user all the same (a distinct normalized URL
-- has a distinct raw URL). updated_at is Postgres's links.updated_at verbatim,
-- since the sync's MAX(updated_at) checkpoint only means anything if it is.
CREATE TABLE archived_links (
  link_id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  raw_url TEXT NOT NULL,
  title TEXT,
  updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_archived_links_user_id ON archived_links(user_id);

-- Backs the MAX(updated_at) checkpoint read, as idx_archived_pages_updated_at
-- does for pages.
CREATE INDEX idx_archived_links_updated_at ON archived_links(updated_at);
