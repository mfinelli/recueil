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

-- Bearer tokens for paired devices (extension, PWA, CLI). Issued by POST
-- /pair in exchange for a valid pairing_token -- never issued any other way.
-- token_hash is SHA-256(token); the raw token is returned to the device
-- exactly once, at issuance, and never stored.
--
-- id is a plain AUTOINCREMENT integer (not client-generated, unlike
-- queue_items/pending_captures) since the Worker is the only thing that
-- ever creates a row here -- there's no client-generated-UUID retry-safety
-- concern the way there is for a device-initiated enqueue.
CREATE TABLE tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash TEXT NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id),
  device_name TEXT NOT NULL,
  device_type TEXT NOT NULL,       -- 'extension' | 'pwa' | 'cli'
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TEXT
) STRICT;

CREATE INDEX idx_tokens_user_id ON tokens(user_id);
