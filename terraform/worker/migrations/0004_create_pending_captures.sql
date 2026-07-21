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

-- Completed captures awaiting backend pickup from R2. id is a
-- client-generated UUID (same reasoning as queue_items.id -- retry-safety
-- on the upload-complete notification), so STRICT + WITHOUT ROWID applies
-- here the same way it does to queue_items.
--
-- queue_item_id is nullable to support direct captures (archiving a page
-- the user is already on, never queued in the first place) -- not used by
-- anything built so far, but the column exists now so a later phase doesn't
-- need its own migration just to add it.
--
-- r2_key_favicon is nullable -- not every capture has one (not every site
-- has a favicon, and finding/uploading one is best-effort on the extension
-- side). Unlike r2_key_html, its extension isn't implicit ("page.html" is
-- always literally that): the favicon could be svg/png/ico, so the key
-- itself carries the real extension (e.g. ".../favicon.svg") for the
-- backend to read back off, rather than a separate mime/type column.
CREATE TABLE pending_captures (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  queue_item_id TEXT REFERENCES queue_items(id),
  url TEXT NOT NULL,
  r2_key_html TEXT NOT NULL,
  r2_key_favicon TEXT,
  captured_at TEXT NOT NULL,
  fetched_by_backend INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_pending_captures_user_id ON pending_captures(user_id);
CREATE INDEX idx_pending_captures_queue_item_id ON pending_captures(queue_item_id);
CREATE INDEX idx_pending_captures_fetched_by_backend ON pending_captures(fetched_by_backend);
