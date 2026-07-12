// @ts-check

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

// Endpoints, in the order they were built:
// - POST /internal/users/mirror: pushes the pairing-token mirror on account
//   creation, pairing-token regeneration, or pairing-token revocation.
// - POST /pair: exchanges a pairing token for a device bearer token.
// - POST /queue, GET /queue, POST /queue/:id/claim: enqueue/poll/claim,
//   authenticated by a device's own bearer token
// - GET/DELETE /internal/tokens: service-secret-gated device-token
//   management, called by the backend on the dashboard's behalf

/**
 * @typedef {Object} Env
 * @property {D1Database} DB
 * @property {string} SERVICE_SECRET
 */

/**
 * SHA-256, hex-encoded. Used for both pairing-token verification (against
 * the D1 mirror) and bearer-token verification (against tokens.token_hash)
 * -- the same "hash at rest, compare hashes" shape as every credential in
 * this system.
 *
 * @param {string} raw
 * @returns {Promise<string>}
 */
async function sha256Hex(raw) {
  const data = new TextEncoder().encode(raw);
  const digest = await crypto.subtle.digest("SHA-256", data);
  return [...new Uint8Array(digest)]
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

/**
 * A 32-byte CSPRNG value, base64url-encoded (no padding), with a
 * human-recognizable prefix -- same shape as every other token in this
 * system (rcl_pair_, rcl_sess_, rcl_bootstrap_).
 *
 * @param {string} prefix
 * @returns {string}
 */
function generateToken(prefix) {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  const b64url = btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
  return prefix + b64url;
}

/**
 * Resolves the Authorization: Bearer header against D1's tokens table.
 * Returns null on any failure to authenticate (missing header, unknown
 * token) -- callers don't need to distinguish why. On success, touches
 * last_used_at via ctx.waitUntil so the write never adds latency to the
 * request path it's authenticating.
 *
 * @param {Request} request
 * @param {Env} env
 * @param {ExecutionContext} ctx
 * @returns {Promise<{tokenId: number, userId: number} | null>}
 */
async function authenticateDevice(request, env, ctx) {
  const header = request.headers.get("Authorization");
  if (!header || !header.startsWith("Bearer ")) return null;
  const raw = header.slice("Bearer ".length).trim();
  if (!raw) return null;

  const hash = await sha256Hex(raw);
  const row = /** @type {{id: number, user_id: number} | null} */ (
    await env.DB.prepare("SELECT id, user_id FROM tokens WHERE token_hash = ?")
      .bind(hash)
      .first()
  );
  if (!row) return null;

  ctx.waitUntil(
    env.DB.prepare(
      "UPDATE tokens SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?",
    )
      .bind(row.id)
      .run(),
  );

  return { tokenId: row.id, userId: row.user_id };
}

export default {
  /**
   * @param {Request} request
   * @param {Env} env
   * @param {ExecutionContext} ctx
   * @returns {Promise<Response>}
   */
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const { pathname } = url;
    const { method } = request;

    if (method === "POST" && pathname === "/internal/users/mirror") {
      return handleUserMirror(request, env);
    }
    if (method === "POST" && pathname === "/pair") {
      return handlePair(request, env);
    }
    if (method === "POST" && pathname === "/queue") {
      return handleEnqueue(request, env, ctx);
    }
    if (method === "GET" && pathname === "/queue") {
      return handleListQueue(request, env, ctx);
    }
    const claimMatch = pathname.match(/^\/queue\/([^/]+)\/claim$/);
    if (method === "POST" && claimMatch) {
      return handleClaimQueueItem(request, env, ctx, claimMatch[1]);
    }
    if (method === "GET" && pathname === "/internal/tokens") {
      return handleListTokens(request, env);
    }
    const tokenMatch = pathname.match(/^\/internal\/tokens\/([^/]+)$/);
    if (method === "DELETE" && tokenMatch) {
      return handleRevokeToken(request, env, tokenMatch[1]);
    }
    if (method === "POST" && pathname === "/internal/queue-items/cleanup") {
      return handleCleanupQueueItems(request, env);
    }

    return new Response("Not Found", { status: 404 });
  },
};

/**
 * @param {Request} request
 * @param {Env} env
 * @returns {Promise<Response>}
 */
export async function handleUserMirror(request, env) {
  const serviceKey = request.headers.get("X-Service-Key");
  if (!serviceKey || !env.SERVICE_SECRET || serviceKey !== env.SERVICE_SECRET) {
    return new Response("Unauthorized", { status: 401 });
  }

  /** @type {unknown} */
  let body;
  try {
    body = await request.json();
  } catch {
    return new Response("Invalid JSON", { status: 400 });
  }

  if (typeof body !== "object" || body === null) {
    return new Response("Missing or invalid fields", { status: 400 });
  }
  const { id, pairing_token_hash } = /** @type {Record<string, unknown>} */ (
    body
  );
  // pairing_token_hash is nullable: a revoke push (DELETE
  // /api/pairing-token, no reissue) sends JSON null to clear the mirrored
  // hash, so no submitted token can ever pair against a revoked account
  // until a regenerate. Anything other than a non-empty string or null is
  // rejected.
  if (
    !Number.isInteger(id) ||
    (pairing_token_hash !== null && typeof pairing_token_hash !== "string") ||
    pairing_token_hash === ""
  ) {
    return new Response("Missing or invalid fields", { status: 400 });
  }

  try {
    await env.DB.prepare(
      `INSERT INTO users (id, pairing_token_hash)
       VALUES (?, ?)
       ON CONFLICT(id) DO UPDATE SET
         pairing_token_hash = excluded.pairing_token_hash`,
    )
      .bind(id, pairing_token_hash)
      .run();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return new Response(`D1 error: ${message}`, { status: 500 });
  }

  return new Response(null, { status: 204 });
}

const DEVICE_TYPES = new Set(["extension", "pwa", "cli"]);

/**
 * POST /pair: exchanges a pairing token for a device bearer token.
 * Single-credential -- no username is submitted or needed, since a
 * pairing token hashes to exactly one account (see DESIGN.md §5).
 *
 * @param {Request} request
 * @param {Env} env
 * @returns {Promise<Response>}
 */
export async function handlePair(request, env) {
  /** @type {unknown} */
  let body;
  try {
    body = await request.json();
  } catch {
    return new Response("Invalid JSON", { status: 400 });
  }
  if (typeof body !== "object" || body === null) {
    return new Response("Missing or invalid fields", { status: 400 });
  }
  const { pairing_token, device_name, device_type } =
    /** @type {Record<string, unknown>} */ (body);

  if (
    typeof pairing_token !== "string" ||
    pairing_token === "" ||
    typeof device_name !== "string" ||
    device_name === "" ||
    typeof device_type !== "string" ||
    !DEVICE_TYPES.has(device_type)
  ) {
    return new Response("Missing or invalid fields", { status: 400 });
  }

  const pairingHash = await sha256Hex(pairing_token);
  const user = /** @type {{id: number} | null} */ (
    await env.DB.prepare("SELECT id FROM users WHERE pairing_token_hash = ?")
      .bind(pairingHash)
      .first()
  );
  if (!user) {
    return new Response("Invalid pairing token", { status: 401 });
  }

  const bearerToken = generateToken("rcl_live_");
  const bearerHash = await sha256Hex(bearerToken);

  /** @type {{id: number} | null} */
  let inserted;
  try {
    inserted = /** @type {{id: number} | null} */ (
      await env.DB.prepare(
        `INSERT INTO tokens (token_hash, user_id, device_name, device_type)
         VALUES (?, ?, ?, ?)
         RETURNING id`,
      )
        .bind(bearerHash, user.id, device_name, device_type)
        .first()
    );
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return new Response(`D1 error: ${message}`, { status: 500 });
  }

  return new Response(
    JSON.stringify({
      token: bearerToken,
      device_id: inserted?.id,
      device_name,
      device_type,
    }),
    { status: 201, headers: { "Content-Type": "application/json" } },
  );
}

/**
 * POST /queue: enqueue a URL for later capture by a device with a real
 * rendered/authenticated browser session. id is client-generated, so a retried
 * request is idempotent (INSERT ... ON CONFLICT DO NOTHING).
 *
 * @param {Request} request
 * @param {Env} env
 * @param {ExecutionContext} ctx
 * @returns {Promise<Response>}
 */
export async function handleEnqueue(request, env, ctx) {
  const auth = await authenticateDevice(request, env, ctx);
  if (!auth) return new Response("Unauthorized", { status: 401 });

  /** @type {unknown} */
  let body;
  try {
    body = await request.json();
  } catch {
    return new Response("Invalid JSON", { status: 400 });
  }
  if (typeof body !== "object" || body === null) {
    return new Response("Missing or invalid fields", { status: 400 });
  }
  const { id, url } = /** @type {Record<string, unknown>} */ (body);
  if (
    typeof id !== "string" ||
    id === "" ||
    typeof url !== "string" ||
    url === ""
  ) {
    return new Response("Missing or invalid fields", { status: 400 });
  }
  try {
    new URL(url);
  } catch {
    return new Response("Invalid url", { status: 400 });
  }

  try {
    await env.DB.prepare(
      `INSERT INTO queue_items (id, user_id, url, added_by_token_id)
       VALUES (?, ?, ?, ?)
       ON CONFLICT(id) DO NOTHING`,
    )
      .bind(id, auth.userId, url, auth.tokenId)
      .run();
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return new Response(`D1 error: ${message}`, { status: 500 });
  }

  return new Response(null, { status: 204 });
}

/**
 * GET /queue: lists this device's user's pending items, plus any claimed
 * item whose claim has gone stale (the claiming device died mid-capture --
 * see DESIGN.md lazy-reclaim-at-query-time design, no separate sweep
 * job). Listing never claims; POST /queue/:id/claim does that atomically.
 *
 * @param {Request} request
 * @param {Env} env
 * @param {ExecutionContext} ctx
 * @returns {Promise<Response>}
 */
export async function handleListQueue(request, env, ctx) {
  const auth = await authenticateDevice(request, env, ctx);
  if (!auth) return new Response("Unauthorized", { status: 401 });

  const { results } = await env.DB.prepare(
    `SELECT id, url, status, claimed_by_token_id, claimed_at, created_at
     FROM queue_items
     WHERE user_id = ?
       AND (status = 'pending'
            OR (status = 'claimed' AND claimed_at < datetime('now', '-15 minutes')))
     ORDER BY created_at ASC`,
  )
    .bind(auth.userId)
    .all();

  return new Response(JSON.stringify({ items: results }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * POST /queue/:id/claim: atomically claims a pending (or stale-claimed)
 * item via a conditional UPDATE ... RETURNING -- this, not GET /queue, is
 * where the two-devices-race-for-the-same-item risk actually lives.
 *
 * On failure to claim, distinguishes three cases rather than a uniform
 * 409: 404 (wrong id, or belongs to a different user -- collapsed
 * together so cross-user existence is never leaked), 410 (status is
 * captured/failed -- a terminal state, permanently no longer claimable),
 * or 409 (actively claimed by another device, not yet stale -- worth
 * retrying later). The extra SELECT to distinguish these only runs on the
 * failure path, never on a successful claim.
 *
 * @param {Request} request
 * @param {Env} env
 * @param {ExecutionContext} ctx
 * @param {string} itemId
 * @returns {Promise<Response>}
 */
export async function handleClaimQueueItem(request, env, ctx, itemId) {
  const auth = await authenticateDevice(request, env, ctx);
  if (!auth) return new Response("Unauthorized", { status: 401 });

  /** @type {Record<string, unknown> | null} */
  let claimed;
  try {
    claimed = /** @type {Record<string, unknown> | null} */ (
      await env.DB.prepare(
        `UPDATE queue_items
         SET status = 'claimed', claimed_by_token_id = ?, claimed_at = CURRENT_TIMESTAMP
         WHERE id = ?
           AND user_id = ?
           AND (status = 'pending'
                OR (status = 'claimed' AND claimed_at < datetime('now', '-15 minutes')))
         RETURNING id, url, status, claimed_by_token_id, claimed_at, created_at`,
      )
        .bind(auth.tokenId, itemId, auth.userId)
        .first()
    );
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return new Response(`D1 error: ${message}`, { status: 500 });
  }

  if (claimed) {
    return new Response(JSON.stringify(claimed), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }

  const existing = /** @type {{status: string} | null} */ (
    await env.DB.prepare(
      "SELECT status FROM queue_items WHERE id = ? AND user_id = ?",
    )
      .bind(itemId, auth.userId)
      .first()
  );

  if (!existing) {
    return new Response("Not Found", { status: 404 });
  }
  if (existing.status === "captured" || existing.status === "failed") {
    return new Response("Item already processed", { status: 410 });
  }
  return new Response("Item already claimed", { status: 409 });
}

/**
 * GET /internal/tokens?user_id=: lists a user's paired devices. Called by
 * the backend on the dashboard's behalf
 *
 * @param {Request} request
 * @param {Env} env
 * @returns {Promise<Response>}
 */
export async function handleListTokens(request, env) {
  const serviceKey = request.headers.get("X-Service-Key");
  if (!serviceKey || !env.SERVICE_SECRET || serviceKey !== env.SERVICE_SECRET) {
    return new Response("Unauthorized", { status: 401 });
  }

  const url = new URL(request.url);
  const userIdParam = url.searchParams.get("user_id");
  const userId = userIdParam !== null ? Number(userIdParam) : NaN;
  if (!Number.isInteger(userId)) {
    return new Response("Missing or invalid user_id", { status: 400 });
  }

  const { results } = await env.DB.prepare(
    `SELECT id, device_name, device_type, created_at, last_used_at
     FROM tokens WHERE user_id = ? ORDER BY created_at ASC`,
  )
    .bind(userId)
    .all();

  return new Response(JSON.stringify({ tokens: results }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * DELETE /internal/tokens/:id?user_id=: revokes one device's bearer
 * token. Scoped by user_id as well as id -- a sanity check against
 * backend bugs, not a substitute for the backend's own admin-vs-self
 * authorization (the Worker doesn't know about roles; the backend
 * enforces scoping before ever making this call). A mismatched user_id/id
 * pair deletes nothing rather than someone else's device.
 *
 * @param {Request} request
 * @param {Env} env
 * @param {string} tokenIdParam
 * @returns {Promise<Response>}
 */
export async function handleRevokeToken(request, env, tokenIdParam) {
  const serviceKey = request.headers.get("X-Service-Key");
  if (!serviceKey || !env.SERVICE_SECRET || serviceKey !== env.SERVICE_SECRET) {
    return new Response("Unauthorized", { status: 401 });
  }

  const url = new URL(request.url);
  const userIdParam = url.searchParams.get("user_id");
  const userId = userIdParam !== null ? Number(userIdParam) : NaN;
  const tokenId = Number(tokenIdParam);
  if (!Number.isInteger(userId) || !Number.isInteger(tokenId)) {
    return new Response("Missing or invalid fields", { status: 400 });
  }

  const result = await env.DB.prepare(
    "DELETE FROM tokens WHERE id = ? AND user_id = ?",
  )
    .bind(tokenId, userId)
    .run();

  if (result.meta.changes === 0) {
    return new Response("Not Found", { status: 404 });
  }
  return new Response(null, { status: 204 });
}

// How long a successfully captured queue_items row is kept before cleanup
// deletes it. Exists purely to support the claim endpoint's 410 semantics
// and basic auditability for a while after the fact -- not a durable
// record (that's Postgres's captures table).
const CAPTURED_RETENTION_HOURS = 72;

/**
 * POST /internal/queue-items/cleanup: deletes 'captured' queue_items older
 * than CAPTURED_RETENTION_HOURS, so the table doesn't grow unboundedly with
 * terminal-state rows that exist only to support the claim endpoint's 410
 * semantics. Called periodically by the backend's own schedule (once or
 * twice a day is plenty) -- not a Cloudflare Cron Trigger, matching the
 * same "keep the Worker dumb, let the backend own scheduling" reasoning
 * already applied to the visibility-timeout reclaim (handled at
 * query time, no sweep job either).
 *
 * Deliberately does NOT touch 'failed' items -- those are kept
 * indefinitely for now. What to do about failed items long-term is an
 * open question for later, not decided here.
 *
 * Uses claimed_at, not created_at, as the retention clock: an item can
 * sit pending for a long time before being claimed, and it's time since
 * actual completion that matters for retention, not time since the
 * original enqueue. claimed_at is a reasonable proxy for completion time
 * at this project's scale (the gap between claim and successful capture
 * is seconds to minutes) until/unless a dedicated completion timestamp
 * exists.
 *
 * @param {Request} request
 * @param {Env} env
 * @returns {Promise<Response>}
 */
export async function handleCleanupQueueItems(request, env) {
  const serviceKey = request.headers.get("X-Service-Key");
  if (!serviceKey || !env.SERVICE_SECRET || serviceKey !== env.SERVICE_SECRET) {
    return new Response("Unauthorized", { status: 401 });
  }

  const result = await env.DB.prepare(
    `DELETE FROM queue_items
     WHERE status = 'captured'
       AND claimed_at < datetime('now', ?)`,
  )
    .bind(`-${CAPTURED_RETENTION_HOURS} hours`)
    .run();

  return new Response(JSON.stringify({ deleted: result.meta.changes }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
