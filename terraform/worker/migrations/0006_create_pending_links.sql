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

-- Links saved by a device, awaiting backend pickup which is the links
-- counterpart to pending_captures, and shaped the same way: the client
-- generates the id, so a retried "save" is a recognizable no-op, and
-- claimed_at makes backend pickup an atomic claim rather than a plain read.
--
-- There is no source_capture_id counterpart downstream. captures is
-- append-only, so a retried ingestion needs a unique key to avoid writing a
-- duplicate version; a link is upserted on (user_id, normalized_url), so a
-- retry just refreshes the same row. This means that it's the claim, and not
-- an arbitary source key, that stops two agents ingesting the same row at
-- once.
--
-- A link carries no HTML, only a title and a favicon, and the favicon reaches
-- the backend one of two ways. Up to the inline size cap (the Worker's
-- LINK_FAVICON_INLINE_MAX_BYTES, which GET /config tells devices) it rides in
-- the save request itself and is parked here as base64 text in favicon_data.
-- Anything larger goes to R2 exactly as a capture's favicon does, and only its
-- key is recorded in r2_key_favicon (whose extension carries the format, as
-- r2_key_favicon on pending_captures does). favicon_ext is set in both cases,
-- and exactly one of favicon_data and r2_key_favicon accompanies it. (The
-- CHECK contstraints below enforce this behavior.)
--
-- saved_at is when the device saved the link, normalized to a UTC ISO-8601
-- string by the Worker so that ordering by it is ordering by time. Claims go
-- oldest-saved first, which makes the last of two saves of the same URL win
-- when both are waiting for the same poll.
CREATE TABLE pending_links (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  queue_item_id TEXT REFERENCES queue_items(id),
  url TEXT NOT NULL,
  title TEXT,
  favicon_ext TEXT,
  favicon_data TEXT,
  r2_key_favicon TEXT,
  saved_at TEXT NOT NULL,
  fetched_by_backend INTEGER NOT NULL DEFAULT 0,
  claimed_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT pending_links_favicon_ext_check
    CHECK (favicon_ext IS NULL OR favicon_ext IN ('svg', 'png', 'ico')),
  CONSTRAINT pending_links_favicon_shape_check
    CHECK (
      (favicon_ext IS NULL
        AND favicon_data IS NULL AND r2_key_favicon IS NULL)
      OR
      (favicon_ext IS NOT NULL
        AND (favicon_data IS NULL) <> (r2_key_favicon IS NULL))
    )
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_pending_links_user_id ON pending_links(user_id);
CREATE INDEX idx_pending_links_queue_item_id ON pending_links(queue_item_id);
CREATE INDEX idx_pending_links_fetched_by_backend ON pending_links(fetched_by_backend);
