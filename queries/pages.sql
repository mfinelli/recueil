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

-- name: UpsertPage :one
-- Get-or-create a page by (user_id, normalized_url), refreshing title to
-- the latest capture's title on every call -- pages.title is documented
-- as denormalized from the latest capture, so a plain INSERT ... ON CONFLICT
-- DO NOTHING would leave a stale title after a re-archive under a page whose
-- title has since changed.
INSERT INTO pages (user_id, normalized_url, title)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, normalized_url) DO UPDATE
SET title = $3, updated_at = NOW()
RETURNING *;

-- name: GetPageByID :one
SELECT * FROM pages WHERE id = $1;
