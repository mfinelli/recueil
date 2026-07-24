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

-- name: GetUserSettings :one
-- No row exists for a user until their first PATCH -- this returns
-- pgx.ErrNoRows for that case, which the handler translates into
-- "language: null" (the same as an explicit NULL) rather than treating a
-- missing row as an error.
SELECT * FROM user_settings WHERE user_id = $1;

-- name: UpsertUserSettings :one
-- ON CONFLICT DO UPDATE, not CreateCollection's plain INSERT -- unlike a
-- collection, there's no meaningful "duplicate" case to reject here: a
-- user has at most one settings row, and a second PATCH is just changing
-- it, not creating a conflicting second one.
INSERT INTO user_settings (user_id, language)
VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET language = EXCLUDED.language, updated_at = NOW()
RETURNING *;
