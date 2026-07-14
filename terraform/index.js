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
// - POST /internal/queue-items/cleanup: service-secret-gated retention sweep
// - POST /captures/upload-urls, POST /queue/:id/complete,
//   POST /queue/:id/fail: presigned R2 upload issuance and the
//   claimed-item-to-captured/failed transition
// - GET /internal/pending-captures, POST
//   /internal/pending-captures/:id/fetched: service-secret-gated backend
//   ingestion polling (list unfetched captures; mark one as pulled)
// - GET /internal/archived-pages/last-sync, POST
//   /internal/archived-pages/mirror, GET /internal/archived-pages/page-ids,
//   POST /internal/archived-pages/delete: service-secret-gated bookmark-list
//   mirror sync (checkpoint read, incremental upsert, full ID list for
//   deletion reconciliation, batch delete) -- all four deliberately dumb:
//   the backend computes what changed and what to delete, the Worker only
//   ever executes exactly what it's told

/**
 * @typedef {Object} Env
 * @property {D1Database} DB
 * @property {string} SERVICE_SECRET
 * @property {string} R2_ACCOUNT_ID
 * @property {string} R2_BUCKET_NAME
 * @property {string} R2_ACCESS_KEY_ID
 * @property {string} R2_ACCESS_KEY_SECRET
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

// Hand-rolled AWS SigV4 query-string ("presigned URL") signing for R2's
// S3-compatible API, used by handleGetUploadUrls below. Any dependency would
// conflict with the "plain JS, no build step, no dependencies"
// constraint that governs everything else here. The algorithm itself is a fixed
// public spec (AWS Signature Version 4), fully implementable against Web Crypto's
// crypto.subtle, standard in the workerd runtime.
//
// R2's S3-compatible endpoint accepts SigV4 exactly like S3 does, with
// region "auto" and service "s3" (Cloudflare's R2 API docs). This
// implementation is verified two ways in terraform/tests/r2-presign.test.js:
// against AWS's own published presigned-URL worked example, and against
// the official @smithy/signature-v4 signer (the library aws-sdk-js v3
// itself uses) for arbitrary R2-shaped requests -- a test-only dependency,
// never shipped here, that catches any drift from the real spec without
// relying on a hand-typed copy of the algorithm to check itself.

const R2_SIGV4_REGION = "auto";
const R2_SIGV4_SERVICE = "s3";
const R2_SIGV4_ALGORITHM = "AWS4-HMAC-SHA256";

/**
 * @param {string} str
 * @returns {Uint8Array}
 */
function utf8(str) {
  return new TextEncoder().encode(str);
}

/**
 * @param {ArrayBuffer} buf
 * @returns {string}
 */
function hex(buf) {
  return [...new Uint8Array(buf)]
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

/**
 * HMAC-SHA256, returning the raw signature bytes (needed for signing-key
 * derivation, where each step's output becomes the next step's key -- only
 * the final signature is ever hex-encoded).
 *
 * @param {Uint8Array | ArrayBuffer} key
 * @param {string} message
 * @returns {Promise<ArrayBuffer>}
 */
export async function hmacRaw(key, message) {
  const cryptoKey = await crypto.subtle.importKey(
    "raw",
    key,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  return crypto.subtle.sign("HMAC", cryptoKey, utf8(message));
}

/**
 * Derives the SigV4 signing key: HMAC chain of
 * date -> region -> service -> "aws4_request", each keyed by the previous
 * step's output, starting from "AWS4" + the raw secret key. region/service
 * are parameters (rather than hardcoded to R2's "auto"/"s3") purely so
 * tests can reproduce AWS's own published presigned-URL worked example
 * (which uses "us-east-1"/"s3") against this exact function, not a
 * re-typed copy of it -- presignR2Url below always calls this with R2's
 * fixed values.
 *
 * @param {string} secretAccessKey
 * @param {string} dateStamp - YYYYMMDD
 * @param {string} region
 * @param {string} service
 * @returns {Promise<ArrayBuffer>}
 */
export async function deriveSigningKey(
  secretAccessKey,
  dateStamp,
  region,
  service,
) {
  const kDate = await hmacRaw(utf8(`AWS4${secretAccessKey}`), dateStamp);
  const kRegion = await hmacRaw(kDate, region);
  const kService = await hmacRaw(kRegion, service);
  return hmacRaw(kService, "aws4_request");
}

/**
 * RFC 3986 URI encoding. encodeURIComponent already escapes "/" (as
 * %2F), but leaves !'()* and ~ untouched (or, for ~, correctly leaves it
 * unescaped -- it's an RFC 3986 unreserved character). AWS's spec requires
 * !'()* to also be percent-encoded, which encodeURIComponent doesn't do on
 * its own.
 *
 * @param {string} str
 * @returns {string}
 */
export function uriEncode(str) {
  return encodeURIComponent(str).replace(
    /[!'()*]/g,
    (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase(),
  );
}

/**
 * Canonical-URI-encodes a path: each segment is URI-encoded independently
 * (so a literal "/" separator is never itself escaped), matching AWS's
 * requirement that canonical URI encoding preserve path separators.
 *
 * @param {string} path - must start with "/"
 * @returns {string}
 */
export function encodePath(path) {
  return path
    .split("/")
    .map((segment) => uriEncode(segment))
    .join("/");
}

/**
 * @typedef {Object} PresignParams
 * @property {string} accountId - Cloudflare account ID (R2 S3 host is
 *   `<accountId>.r2.cloudflarestorage.com`)
 * @property {string} bucketName
 * @property {string} accessKeyId
 * @property {string} secretAccessKey
 * @property {string} key - object key within the bucket, no leading slash
 * @property {"GET" | "PUT"} method
 * @property {number} expiresInSeconds
 * @property {string} [checksumSha256Base64] - base64-encoded SHA-256 of the
 *   exact bytes about to be uploaded. When supplied, it's bound into the
 *   signature as a required `x-amz-checksum-sha256` header (R2's "Flexible
 *   Checksums" feature -- a genuinely separate mechanism from the SigV4
 *   payload-hash slot below): the caller's real PUT request must resend
 *   this exact header/value, and R2 independently verifies the uploaded
 *   bytes against it, rejecting the upload on any mismatch. This is what
 *   actually catches transfer corruption or a swapped body -- not the
 *   payload-hash slot itself (see below). Every capture path this project
 *   plans always has the content in hand before requesting a presigned
 *   URL (SingleFile/Readability extraction finishes in-browser first), so
 *   omitting this should be the exception, not the default.
 * @property {Date} [now] - injectable for deterministic tests; defaults to
 *   the real current time
 */

/**
 * Builds a SigV4 presigned URL for R2's S3-compatible API. Query-string
 * ("presigned URL") auth, not header auth -- the whole point is that the
 * fake extension script (and eventually the real one) can PUT straight to
 * this URL with no credentials of its own, no custom headers required to
 * merely authenticate, and no Authorization header to construct.
 *
 * Two genuinely distinct mechanisms are in play here, worth not
 * conflating (a real point of confusion working this out -- see
 * terraform/tests/r2-presign.test.js and the PresignParams doc above):
 * - The SigV4 "payload hash" slot (the last line of the canonical
 *   request) is a *signing* input, not a content-integrity check. R2's
 *   own documented presigned-URL examples leave it as the literal string
 *   "UNSIGNED-PAYLOAD" even for uploads, since the payload isn't known at
 *   signing time in the general case -- and per AWS's own guidance,
 *   presigned URLs "typically don't need to include the content hash in
 *   the signature calculation" at all. This implementation always uses
 *   that literal, matching R2's convention.
 * - Real content-integrity verification -- R2 rejecting an upload whose
 *   bytes don't match what the caller declared -- is a separate feature
 *   ("Flexible Checksums"), via an `x-amz-checksum-sha256` *header*
 *   (distinct name from `x-amz-content-sha256` above), base64-encoded,
 *   bound into the signature as a normal signed header. That's what
 *   checksumSha256Base64 wires up below.
 *
 * @param {PresignParams} params
 * @returns {Promise<string>}
 */
export async function presignR2Url({
  accountId,
  bucketName,
  accessKeyId,
  secretAccessKey,
  key,
  method,
  expiresInSeconds,
  checksumSha256Base64,
  now,
}) {
  const host = `${accountId}.r2.cloudflarestorage.com`;
  const date = now ?? new Date();
  // YYYYMMDDTHHMMSSZ, stripping the "-", ":" separators and millisecond
  // fraction that Date#toISOString includes but AWS's amz-date format
  // doesn't allow.
  const amzDate = date
    .toISOString()
    .replace(/[-:]/g, "")
    .replace(/\.\d{3}/, "");
  const dateStamp = amzDate.slice(0, 8);
  const credentialScope = `${dateStamp}/${R2_SIGV4_REGION}/${R2_SIGV4_SERVICE}/aws4_request`;
  const credential = `${accessKeyId}/${credentialScope}`;

  const canonicalUri = encodePath(`/${bucketName}/${key}`);

  /** @type {[string, string][]} */
  const queryPairs = [
    ["X-Amz-Algorithm", R2_SIGV4_ALGORITHM],
    ["X-Amz-Credential", credential],
    ["X-Amz-Date", amzDate],
    ["X-Amz-Expires", String(expiresInSeconds)],
    // SignedHeaders' *value* (not the query param set itself) changes
    // below when a checksum is supplied -- "host" alone otherwise, or
    // "host;x-amz-checksum-sha256" when binding a checksum. Header names
    // are sorted alphabetically per SigV4's canonical-headers rule, and
    // "host" < "x-amz-checksum-sha256" already.
    [
      "X-Amz-SignedHeaders",
      checksumSha256Base64 ? "host;x-amz-checksum-sha256" : "host",
    ],
  ];
  // AWS requires canonical query params sorted by (encoded) key.
  queryPairs.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  const canonicalQueryString = queryPairs
    .map(([k, v]) => `${uriEncode(k)}=${uriEncode(v)}`)
    .join("&");

  const canonicalHeaders = checksumSha256Base64
    ? `host:${host}\nx-amz-checksum-sha256:${checksumSha256Base64}\n`
    : `host:${host}\n`;
  const signedHeaders = checksumSha256Base64
    ? "host;x-amz-checksum-sha256"
    : "host";
  // UNSIGNED-PAYLOAD, always -- see the function doc above for why this
  // is a signing-input default, not the content-integrity mechanism.
  const payloadHash = "UNSIGNED-PAYLOAD";

  const canonicalRequest = [
    method,
    canonicalUri,
    canonicalQueryString,
    canonicalHeaders,
    signedHeaders,
    payloadHash,
  ].join("\n");

  const stringToSign = [
    R2_SIGV4_ALGORITHM,
    amzDate,
    credentialScope,
    await sha256Hex(canonicalRequest),
  ].join("\n");

  const signingKey = await deriveSigningKey(
    secretAccessKey,
    dateStamp,
    R2_SIGV4_REGION,
    R2_SIGV4_SERVICE,
  );
  const signature = hex(await hmacRaw(signingKey, stringToSign));

  return `https://${host}${canonicalUri}?${canonicalQueryString}&X-Amz-Signature=${signature}`;
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
    if (method === "GET" && pathname === "/internal/pending-captures") {
      return handleListPendingCaptures(request, env);
    }
    const fetchedMatch = pathname.match(
      /^\/internal\/pending-captures\/([^/]+)\/fetched$/,
    );
    if (method === "POST" && fetchedMatch) {
      return handleMarkPendingCaptureFetched(request, env, fetchedMatch[1]);
    }
    if (method === "POST" && pathname === "/captures/upload-urls") {
      return handleGetUploadUrls(request, env, ctx);
    }
    const completeMatch = pathname.match(/^\/queue\/([^/]+)\/complete$/);
    if (method === "POST" && completeMatch) {
      return handleCompleteQueueItem(request, env, ctx, completeMatch[1]);
    }
    const failMatch = pathname.match(/^\/queue\/([^/]+)\/fail$/);
    if (method === "POST" && failMatch) {
      return handleFailQueueItem(request, env, ctx, failMatch[1]);
    }
    if (method === "GET" && pathname === "/internal/archived-pages/last-sync") {
      return handleGetArchivedPagesLastSync(request, env);
    }
    if (method === "POST" && pathname === "/internal/archived-pages/mirror") {
      return handleMirrorArchivedPages(request, env);
    }
    if (method === "GET" && pathname === "/internal/archived-pages/page-ids") {
      return handleListArchivedPageIDs(request, env);
    }
    if (method === "POST" && pathname === "/internal/archived-pages/delete") {
      return handleDeleteArchivedPages(request, env);
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

// Bounds how many rows a single poll can return -- an unbounded SELECT
// against a table that only grows between polls would be a bad default,
// even at this project's modest personal/family scale. The backend can
// always poll again immediately if there's more to fetch; there's no
// on-demand path here, so this only needs to be "reasonable," not tuned.
const DEFAULT_PENDING_CAPTURES_LIMIT = 50;
const MAX_PENDING_CAPTURES_LIMIT = 200;

/**
 * GET /internal/pending-captures?limit=: lists captures the backend hasn't
 * yet pulled from R2 (fetched_by_backend = 0), oldest first. Service-secret
 * gated, cross-user -- this is the backend's own ingestion sweep across the
 * whole deployment, not a per-device or per-user operation, so it takes
 * no user_id the way the device-facing queue endpoints do (same shape as
 * handleCleanupQueueItems above).
 *
 * @param {Request} request
 * @param {Env} env
 * @returns {Promise<Response>}
 */
export async function handleListPendingCaptures(request, env) {
  const serviceKey = request.headers.get("X-Service-Key");
  if (!serviceKey || !env.SERVICE_SECRET || serviceKey !== env.SERVICE_SECRET) {
    return new Response("Unauthorized", { status: 401 });
  }

  const url = new URL(request.url);
  const limitParam = url.searchParams.get("limit");
  let limit = DEFAULT_PENDING_CAPTURES_LIMIT;
  if (limitParam !== null) {
    const parsed = Number(limitParam);
    if (
      !Number.isInteger(parsed) ||
      parsed < 1 ||
      parsed > MAX_PENDING_CAPTURES_LIMIT
    ) {
      return new Response("Invalid limit", { status: 400 });
    }
    limit = parsed;
  }

  const { results } = await env.DB.prepare(
    `SELECT id, user_id, queue_item_id, url, r2_key_html, r2_key_favicon, captured_at, created_at
     FROM pending_captures
     WHERE fetched_by_backend = 0
     ORDER BY created_at ASC
     LIMIT ?`,
  )
    .bind(limit)
    .all();

  return new Response(JSON.stringify({ pending_captures: results }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * POST /internal/pending-captures/:id/fetched: marks a pending_captures row
 * as pulled and ingested. Called only after the backend has durably written
 * the capture to Postgres and deleted the R2 objects -- per the
 * crash-recovery ordering (disk write, then DB commit, then R2 delete, then
 * this D1 flag update), a crash before this call simply means the row shows
 * up in the next poll again, which is safe: ingestion itself is idempotent
 * via source_capture_id, so re-processing an already-ingested row is a no-op
 * on the Postgres side.
 *
 * Deliberately not scoped to a queue item or a user -- this is the
 * backend's own bookkeeping on a row it already knows about; the device
 * that originally created the row is not involved in this call at all.
 *
 * @param {Request} request
 * @param {Env} env
 * @param {string} captureId
 * @returns {Promise<Response>}
 */
export async function handleMarkPendingCaptureFetched(request, env, captureId) {
  const serviceKey = request.headers.get("X-Service-Key");
  if (!serviceKey || !env.SERVICE_SECRET || serviceKey !== env.SERVICE_SECRET) {
    return new Response("Unauthorized", { status: 401 });
  }

  const result = await env.DB.prepare(
    "UPDATE pending_captures SET fetched_by_backend = 1 WHERE id = ?",
  )
    .bind(captureId)
    .run();

  if (result.meta.changes === 0) {
    return new Response("Not Found", { status: 404 });
  }
  return new Response(null, { status: 204 });
}

// How long a presigned upload URL remains valid. Comfortably more than
// enough time for a device to PUT a large inlined-HTML capture over a slow
// connection, without leaving signed URLs usable indefinitely.
const UPLOAD_URL_EXPIRY_SECONDS = 900;

// A lowercase 64-character hex string -- what crypto.subtle.digest("SHA-256",
// ...) produces once hex-encoded, and the format this codebase already uses
// elsewhere (sha256Hex above). Required shape for content_sha256_html in
// handleGetUploadUrls's request body.
const HEX_SHA256_PATTERN = /^[0-9a-f]{64}$/;

/**
 * Converts a lowercase hex SHA-256 digest to base64, the encoding R2's
 * Flexible Checksums feature (`x-amz-checksum-sha256`) expects -- distinct
 * from the hex convention used everywhere else in this codebase, so the
 * conversion happens once, here, rather than asking every caller to know
 * both encodings.
 *
 * @param {string} hexDigest - exactly 64 lowercase hex characters
 * @returns {string}
 */
function hexToBase64(hexDigest) {
  const bytes = new Uint8Array(hexDigest.length / 2);
  for (let i = 0; i < bytes.length; i++) {
    bytes[i] = parseInt(hexDigest.slice(i * 2, i * 2 + 2), 16);
  }
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary);
}

/**
 * Builds the deterministic R2 object key for a given user + capture, so the
 * Worker never has to trust a client-supplied key at completion time (see
 * handleCompleteQueueItem) -- the key is entirely a function of who's
 * authenticated and which capture_id they supplied, both already verified.
 *
 * @param {number} userId
 * @param {string} captureId
 * @returns {string}
 */
function captureObjectKey(userId, captureId) {
  return `pending/${userId}/${captureId}/page.html`;
}

// The only favicon formats the extension is expected to ever hand us:
// link-level selection (svg > png > ico, preferring whatever the page itself
// declares, falling back to the conventional /favicon.{svg,png,ico} root
// paths) rather than parsing any particular file's contents. Validated here
// so a malformed/unexpected extension fails loudly at presign time rather
// than producing a pending_captures row the backend can't make sense of later.
const FAVICON_EXTENSIONS = new Set(["svg", "png", "ico"]);

/**
 * Builds the deterministic R2 object key for a capture's favicon, mirroring
 * captureObjectKey above -- same reasoning, plus the extension itself
 * becomes part of the key (".../favicon.svg" vs ".../favicon.png") since,
 * unlike the HTML object, a favicon's format isn't fixed. This lets the
 * backend recover the real extension by reading the key back
 * (path/filepath.Ext in internal/ingest) instead of needing a separate
 * mime/type column anywhere.
 *
 * @param {number} userId
 * @param {string} captureId
 * @param {string} ext - one of FAVICON_EXTENSIONS; not validated here,
 *   callers must validate before calling (see handleGetUploadUrls)
 * @returns {string}
 */
function faviconObjectKey(userId, captureId, ext) {
  return `pending/${userId}/${captureId}/favicon.${ext}`;
}

/**
 * POST /captures/upload-urls: issues a presigned R2 PUT URL for a capture's
 * HTML, and, when the extension found one, a second presigned URL for its
 * favicon. Purely stateless signing, no D1 read or write -- capture_id is
 * client-generated (the same client-generated-UUID idempotency pattern as
 * queue_items.id and pending_captures.id) so nothing needs to be reserved
 * server-side before a device starts uploading.
 *
 * content_sha256_html is required: every capture path this project plans
 * has the exact HTML bytes in hand before ever talking to the Worker
 * (SingleFile capture finishes fully in-browser first), so there's no
 * legitimate case for skipping the checksum binding described in
 * presignR2Url's doc above -- R2 will reject the upload if the real bytes
 * don't hash to what's declared here, catching transfer corruption or a
 * swapped body, not just authenticating that some device was allowed to
 * write to this key.
 *
 * favicon_ext and content_sha256_favicon are both optional, but only
 * together: a capture without a discoverable favicon simply omits both,
 * and no favicon upload URL is issued at all.
 *
 * Deliberately not scoped to a queue item: pending_captures.queue_item_id
 * is nullable to support direct captures, so upload-URL issuance shouldn't
 * require one either. Phase 3's fake extension always supplies one via the
 * queue, but a future direct-capture path can call this same endpoint
 * unchanged.
 *
 * @param {Request} request
 * @param {Env} env
 * @param {ExecutionContext} ctx
 * @returns {Promise<Response>}
 */
export async function handleGetUploadUrls(request, env, ctx) {
  const auth = await authenticateDevice(request, env, ctx);
  if (!auth) return new Response("Unauthorized", { status: 401 });

  if (
    !env.R2_ACCOUNT_ID ||
    !env.R2_BUCKET_NAME ||
    !env.R2_ACCESS_KEY_ID ||
    !env.R2_ACCESS_KEY_SECRET
  ) {
    // A misconfigured deployment (operator never provisioned the R2 S3 API
    // credential -- see manual-step note) should fail loudly here, not produce
    // a broken presigned URL a device then fails to PUT against with a
    // confusing error.
    return new Response("R2 credentials not configured", { status: 500 });
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
  const {
    capture_id,
    content_sha256_html,
    favicon_ext,
    content_sha256_favicon,
  } = /** @type {Record<string, unknown>} */ (body);
  if (
    typeof capture_id !== "string" ||
    capture_id === "" ||
    typeof content_sha256_html !== "string" ||
    !HEX_SHA256_PATTERN.test(content_sha256_html)
  ) {
    return new Response("Missing or invalid fields", { status: 400 });
  }

  // favicon_ext and content_sha256_favicon are a pair -- both present or
  // both absent. One without the other is a malformed request, not a
  // "favicon optional, treat as absent" case: a caller that supplies an
  // extension but no checksum (or vice versa) almost certainly has a bug
  // worth surfacing rather than silently dropping.
  const faviconRequested =
    favicon_ext !== undefined || content_sha256_favicon !== undefined;
  if (faviconRequested) {
    if (
      typeof favicon_ext !== "string" ||
      !FAVICON_EXTENSIONS.has(favicon_ext) ||
      typeof content_sha256_favicon !== "string" ||
      !HEX_SHA256_PATTERN.test(content_sha256_favicon)
    ) {
      return new Response("Missing or invalid fields", { status: 400 });
    }
  }

  const key = captureObjectKey(auth.userId, capture_id);
  const checksumHtmlBase64 = hexToBase64(content_sha256_html);

  const uploadUrlHtml = await presignR2Url({
    accountId: env.R2_ACCOUNT_ID,
    bucketName: env.R2_BUCKET_NAME,
    accessKeyId: env.R2_ACCESS_KEY_ID,
    secretAccessKey: env.R2_ACCESS_KEY_SECRET,
    method: /** @type {"PUT"} */ ("PUT"),
    expiresInSeconds: UPLOAD_URL_EXPIRY_SECONDS,
    key,
    checksumSha256Base64: checksumHtmlBase64,
  });

  /** @type {Record<string, unknown>} */
  const responseBody = {
    capture_id,
    upload_url_html: uploadUrlHtml,
    r2_key_html: key,
    expires_in_seconds: UPLOAD_URL_EXPIRY_SECONDS,
    // The caller's real PUT MUST include this exact header (name and
    // value) or R2 will reject the request: it's bound into the
    // signature, and R2 separately verifies the uploaded bytes against it.
    required_headers_html: { "x-amz-checksum-sha256": checksumHtmlBase64 },
  };

  if (faviconRequested) {
    const faviconKey = faviconObjectKey(
      auth.userId,
      capture_id,
      /** @type {string} */ (favicon_ext),
    );
    const checksumFaviconBase64 = hexToBase64(
      /** @type {string} */ (content_sha256_favicon),
    );

    responseBody.upload_url_favicon = await presignR2Url({
      accountId: env.R2_ACCOUNT_ID,
      bucketName: env.R2_BUCKET_NAME,
      accessKeyId: env.R2_ACCESS_KEY_ID,
      secretAccessKey: env.R2_ACCESS_KEY_SECRET,
      method: /** @type {"PUT"} */ ("PUT"),
      expiresInSeconds: UPLOAD_URL_EXPIRY_SECONDS,
      key: faviconKey,
      checksumSha256Base64: checksumFaviconBase64,
    });
    responseBody.r2_key_favicon = faviconKey;
    responseBody.required_headers_favicon = {
      "x-amz-checksum-sha256": checksumFaviconBase64,
    };
  }

  return new Response(JSON.stringify(responseBody), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * POST /queue/:id/complete: notifies the Worker that a claimed queue item's
 * capture has finished uploading to R2, writing the corresponding
 * pending_captures row and transitioning the queue item to 'captured'.
 *
 * Idempotency: capture_id is the pending_captures primary key,
 * client-generated once and reused on any retry. A retry after a
 * crash (the row already exists) is detected up front and short-circuits
 * to success -- deliberately *before* re-checking the queue item's status,
 * since by the time a retry happens the first attempt may have already
 * flipped it to 'captured', which would otherwise look like a stale/
 * terminal-item 410 rather than the successful no-op it actually is.
 *
 * r2_key_html is never taken from the request body -- it's recomputed from
 * auth.userId + capture_id (see captureObjectKey), the same deterministic
 * scheme handleGetUploadUrls used to issue the presigned URL. This means a
 * device can't claim an arbitrary R2 key belongs to this capture; it can
 * only ever reference the key it was actually presigned to upload to.
 * favicon_ext (optional) gets the same treatment via faviconObjectKey --
 * the caller declares *whether* it uploaded a favicon and in what format,
 * but never the key itself.
 *
 * @param {Request} request
 * @param {Env} env
 * @param {ExecutionContext} ctx
 * @param {string} itemId
 * @returns {Promise<Response>}
 */
export async function handleCompleteQueueItem(request, env, ctx, itemId) {
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
  const { capture_id, captured_at, favicon_ext } =
    /** @type {Record<string, unknown>} */ (body);
  if (
    typeof capture_id !== "string" ||
    capture_id === "" ||
    typeof captured_at !== "string" ||
    captured_at === ""
  ) {
    return new Response("Missing or invalid fields", { status: 400 });
  }
  if (
    favicon_ext !== undefined &&
    (typeof favicon_ext !== "string" || !FAVICON_EXTENSIONS.has(favicon_ext))
  ) {
    return new Response("Missing or invalid fields", { status: 400 });
  }

  const existing = await env.DB.prepare(
    "SELECT id FROM pending_captures WHERE id = ?",
  )
    .bind(capture_id)
    .first();
  if (existing) {
    // Already recorded by an earlier attempt -- nothing left to do.
    return new Response(null, { status: 204 });
  }

  const queueItem = /** @type {{
    id: string,
    user_id: number,
    url: string,
    status: string,
    claimed_by_token_id: number | null,
  } | null} */ (
    await env.DB.prepare(
      "SELECT id, user_id, url, status, claimed_by_token_id FROM queue_items WHERE id = ? AND user_id = ?",
    )
      .bind(itemId, auth.userId)
      .first()
  );
  if (!queueItem) {
    return new Response("Not Found", { status: 404 });
  }
  if (queueItem.status === "captured" || queueItem.status === "failed") {
    return new Response("Item already processed", { status: 410 });
  }
  if (
    queueItem.status !== "claimed" ||
    queueItem.claimed_by_token_id !== auth.tokenId
  ) {
    return new Response("Item not claimed by this device", { status: 409 });
  }

  const r2KeyHtml = captureObjectKey(auth.userId, capture_id);
  const r2KeyFavicon =
    typeof favicon_ext === "string"
      ? faviconObjectKey(auth.userId, capture_id, favicon_ext)
      : null;

  try {
    await env.DB.batch([
      env.DB.prepare(
        `INSERT INTO pending_captures
           (id, user_id, queue_item_id, url, r2_key_html, r2_key_favicon, captured_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
      ).bind(
        capture_id,
        auth.userId,
        itemId,
        queueItem.url,
        r2KeyHtml,
        r2KeyFavicon,
        captured_at,
      ),
      env.DB.prepare(
        "UPDATE queue_items SET status = 'captured' WHERE id = ?",
      ).bind(itemId),
    ]);
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return new Response(`D1 error: ${message}`, { status: 500 });
  }

  return new Response(
    JSON.stringify({
      id: capture_id,
      r2_key_html: r2KeyHtml,
      r2_key_favicon: r2KeyFavicon,
    }),
    { status: 201, headers: { "Content-Type": "application/json" } },
  );
}

/**
 * POST /queue/:id/fail: transitions a claimed queue item to 'failed' --
 * the capture attempt didn't produce anything to upload (the tab crashed,
 * the page turned out to require login, etc.), so there's no
 * pending_captures row to write, only a status change.
 *
 * Shares the claim endpoint's three-way failure semantics (404/410/409 --
 * see handleClaimQueueItem) rather than inventing new ones: a device can
 * only fail an item it currently holds the claim on.
 *
 * @param {Request} request
 * @param {Env} env
 * @param {ExecutionContext} ctx
 * @param {string} itemId
 * @returns {Promise<Response>}
 */
export async function handleFailQueueItem(request, env, ctx, itemId) {
  const auth = await authenticateDevice(request, env, ctx);
  if (!auth) return new Response("Unauthorized", { status: 401 });

  const queueItem = /** @type {{
    status: string,
    claimed_by_token_id: number | null,
  } | null} */ (
    await env.DB.prepare(
      "SELECT status, claimed_by_token_id FROM queue_items WHERE id = ? AND user_id = ?",
    )
      .bind(itemId, auth.userId)
      .first()
  );
  if (!queueItem) {
    return new Response("Not Found", { status: 404 });
  }
  if (queueItem.status === "captured" || queueItem.status === "failed") {
    return new Response("Item already processed", { status: 410 });
  }
  if (
    queueItem.status !== "claimed" ||
    queueItem.claimed_by_token_id !== auth.tokenId
  ) {
    return new Response("Item not claimed by this device", { status: 409 });
  }

  await env.DB.prepare("UPDATE queue_items SET status = 'failed' WHERE id = ?")
    .bind(itemId)
    .run();

  return new Response(null, { status: 204 });
}

/**
 * GET /internal/archived-pages/last-sync: returns the sync checkpoint for
 * the bookmark-list mirror -- the max updated_at currently in
 * archived_pages, or null if nothing has ever been pushed. Deliberately
 * derived directly from D1's own data rather than a separately-tracked
 * watermark value anywhere: there's nothing that can drift from what D1
 * actually contains, since the "checkpoint" and "the data" are the same
 * read. The backend uses this to compute
 * `WHERE pages.updated_at > last_sync` on its own Postgres side; a null
 * response means "sync everything."
 *
 * @param {Request} request
 * @param {Env} env
 * @returns {Promise<Response>}
 */
export async function handleGetArchivedPagesLastSync(request, env) {
  const serviceKey = request.headers.get("X-Service-Key");
  if (!serviceKey || !env.SERVICE_SECRET || serviceKey !== env.SERVICE_SECRET) {
    return new Response("Unauthorized", { status: 401 });
  }

  const row = /** @type {{ max_updated_at: string | null } | null} */ (
    await env.DB.prepare(
      "SELECT MAX(updated_at) AS max_updated_at FROM archived_pages",
    ).first()
  );

  return new Response(
    JSON.stringify({ last_sync: row?.max_updated_at ?? null }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );
}

/**
 * @typedef {Object} ArchivedPageMirrorEntry
 * @property {number} page_id
 * @property {number} user_id
 * @property {string} raw_url
 * @property {string | null} [title]
 * @property {string} latest_capture_at
 * @property {string} updated_at
 */

/**
 * POST /internal/archived-pages/mirror: batch upsert into archived_pages.
 * Deliberately dumb -- the backend decides which pages changed and in
 * what order to send them (ascending updated_at, per DESIGN.md's own
 * reasoning about not letting a partial-batch failure leave a gap below
 * the new observed max); this endpoint just executes the upsert for
 * whatever it's given, in the order given, via a single env.DB.batch()
 * call so the whole batch commits atomically or not at all.
 *
 * updated_at is always taken verbatim from the request body -- never
 * D1's own CURRENT_TIMESTAMP -- since it needs to directly mirror
 * Postgres's pages.updated_at for the last-sync checkpoint above to mean
 * anything.
 *
 * @param {Request} request
 * @param {Env} env
 * @returns {Promise<Response>}
 */
export async function handleMirrorArchivedPages(request, env) {
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
  if (typeof body !== "object" || body === null || !("pages" in body)) {
    return new Response("Missing or invalid fields", { status: 400 });
  }
  const { pages } = /** @type {{ pages: unknown }} */ (body);
  if (!Array.isArray(pages)) {
    return new Response("Missing or invalid fields", { status: 400 });
  }

  /** @type {ArchivedPageMirrorEntry[]} */
  const entries = [];
  for (const page of pages) {
    if (
      typeof page !== "object" ||
      page === null ||
      typeof (/** @type {Record<string, unknown>} */ (page).page_id) !==
        "number" ||
      typeof (/** @type {Record<string, unknown>} */ (page).user_id) !==
        "number" ||
      typeof (/** @type {Record<string, unknown>} */ (page).raw_url) !==
        "string" ||
      typeof (
        /** @type {Record<string, unknown>} */ (page).latest_capture_at
      ) !== "string" ||
      typeof (/** @type {Record<string, unknown>} */ (page).updated_at) !==
        "string"
    ) {
      return new Response("Missing or invalid fields", { status: 400 });
    }
    entries.push(/** @type {ArchivedPageMirrorEntry} */ (page));
  }

  const statements = entries.map((page) =>
    env.DB.prepare(
      `INSERT INTO archived_pages
         (page_id, user_id, raw_url, title, latest_capture_at, updated_at)
       VALUES (?, ?, ?, ?, ?, ?)
       ON CONFLICT(page_id) DO UPDATE SET
         user_id = excluded.user_id,
         raw_url = excluded.raw_url,
         title = excluded.title,
         latest_capture_at = excluded.latest_capture_at,
         updated_at = excluded.updated_at`,
    ).bind(
      page.page_id,
      page.user_id,
      page.raw_url,
      page.title ?? null,
      page.latest_capture_at,
      page.updated_at,
    ),
  );

  if (statements.length > 0) {
    await env.DB.batch(statements);
  }

  return new Response(JSON.stringify({ upserted: entries.length }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * GET /internal/archived-pages/page-ids: lists every page_id currently in
 * the D1 mirror. Deliberately dumb -- computing which of these no longer
 * exist in Postgres (and therefore need deleting) is the backend's own
 * job, using this raw list plus its own current pages table; the Worker
 * never compares anything itself.
 *
 * @param {Request} request
 * @param {Env} env
 * @returns {Promise<Response>}
 */
export async function handleListArchivedPageIDs(request, env) {
  const serviceKey = request.headers.get("X-Service-Key");
  if (!serviceKey || !env.SERVICE_SECRET || serviceKey !== env.SERVICE_SECRET) {
    return new Response("Unauthorized", { status: 401 });
  }

  const { results } = await env.DB.prepare(
    "SELECT page_id FROM archived_pages",
  ).all();

  return new Response(
    JSON.stringify({
      page_ids: results.map(
        (row) => /** @type {{ page_id: number }} */ (row).page_id,
      ),
    }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );
}

/**
 * POST /internal/archived-pages/delete: batch delete from archived_pages
 * by page_id -- the other half of deletion reconciliation, executing
 * exactly the ids the backend already determined no longer exist in
 * Postgres.
 *
 * @param {Request} request
 * @param {Env} env
 * @returns {Promise<Response>}
 */
export async function handleDeleteArchivedPages(request, env) {
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
  if (typeof body !== "object" || body === null || !("page_ids" in body)) {
    return new Response("Missing or invalid fields", { status: 400 });
  }
  const { page_ids } = /** @type {{ page_ids: unknown }} */ (body);
  if (
    !Array.isArray(page_ids) ||
    !page_ids.every((id) => typeof id === "number")
  ) {
    return new Response("Missing or invalid fields", { status: 400 });
  }

  if (page_ids.length > 0) {
    const statements = page_ids.map((id) =>
      env.DB.prepare("DELETE FROM archived_pages WHERE page_id = ?").bind(id),
    );
    await env.DB.batch(statements);
  }

  return new Response(JSON.stringify({ deleted: page_ids.length }), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}
