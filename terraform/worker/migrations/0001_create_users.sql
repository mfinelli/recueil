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

-- Mirrors Postgres users.id only, for device pairing, without ever
-- exposing the backend or anything password-derived. Does NOT include `role`
-- (authorization is a backend/dashboard concern only) or `username` (pairing
-- is single-credential -- a device submits only the pairing token, so the
-- Worker never looks a user up by name).
--
-- pairing_token_hash is nullable: a revoked user (DELETE /api/pairing-token,
-- no reissue) has this cleared to NULL until they regenerate. A NULL column
-- value can never match a bound, non-null lookup parameter, so no
-- special-casing is needed in the pairing-lookup query for a revoked user
-- to correctly fail to pair.
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  pairing_token_hash TEXT UNIQUE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT;
