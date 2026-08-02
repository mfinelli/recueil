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
CREATE TABLE api_tokens (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  user_id BIGINT NOT NULL,
  token_hash TEXT NOT NULL, -- SHA-256 hex of the opaque rcl_api_... token
  name TEXT NOT NULL, -- user-supplied label, e.g. "Claude Desktop"
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_used_at TIMESTAMPTZ,
  CONSTRAINT api_tokens_pkey PRIMARY KEY (id),
  CONSTRAINT api_tokens_token_hash_key UNIQUE (token_hash),
  CONSTRAINT api_tokens_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX idx_api_tokens_user_id ON api_tokens(user_id);

-- +goose Down
DROP TABLE api_tokens;
