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

-- URLs waiting to be archived by the desktop extension. Enqueued and claimed
-- entirely by devices via their own bearer tokens -- the backend never touches
-- this table. id is a client-generated UUID (not server-assigned), for
-- idempotency on a retried enqueue (INSERT ... ON CONFLICT(id) DO NOTHING) the
-- same way pending_captures' id is, and because the enqueuing device generates
-- the id before the row exists server-side.
--
-- Visibility-timeout reclaim (a claimed item stuck because its claiming
-- device died mid-capture) is handled at query time in the claim
-- endpoint's WHERE clause, not a separate scheduled sweep.
CREATE TABLE queue_items (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  url TEXT NOT NULL,
  added_by_token_id INTEGER REFERENCES tokens(id),
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | claimed | captured | failed
  claimed_by_token_id INTEGER REFERENCES tokens(id),
  claimed_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT, WITHOUT ROWID;

-- Composite, not a bare user_id index: every poll query filters on both
-- user_id and status together (see the claim/read queries in index.js).
CREATE INDEX idx_queue_items_user_status ON queue_items(user_id, status);
CREATE INDEX idx_queue_items_added_by_token_id ON queue_items(added_by_token_id);
CREATE INDEX idx_queue_items_claimed_by_token_id ON queue_items(claimed_by_token_id);
