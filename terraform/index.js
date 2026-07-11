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

// This is intentionally minimal: only the /internal/users/mirror endpoint
// that the backend needs to push the pairing-token mirror on account
// creation, pairing-token regeneration, or pairing-token revocation.

/**
 * @typedef {Object} Env
 * @property {D1Database} DB
 * @property {string} SERVICE_SECRET
 */

export default {
  /**
   * @param {Request} request
   * @param {Env} env
   * @returns {Promise<Response>}
   */
  async fetch(request, env) {
    const url = new URL(request.url);

    if (
      request.method === "POST" &&
      url.pathname === "/internal/users/mirror"
    ) {
      return handleUserMirror(request, env);
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
