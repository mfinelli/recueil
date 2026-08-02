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

-- name: RecordAgentHeartbeat :exec
-- Upsert, not insert: cycle is the primary key, and every successful
-- cycle just moves last_success_at forward from whatever was there
-- before -- including across an agent restart.
INSERT INTO agent_heartbeats (cycle, last_success_at)
VALUES ($1, NOW())
ON CONFLICT (cycle) DO UPDATE SET last_success_at = NOW();

-- name: AgentHeartbeatAges :many
-- One row per cycle that has ever recorded a success. A cycle that never
-- has (including "the agent has never run yet") has no row at all, the
-- same absent-not-zero shape as OldestPendingJobAgeSeconds.
SELECT cycle, EXTRACT(EPOCH FROM (NOW() - last_success_at))::float8 AS age_seconds
FROM agent_heartbeats;
