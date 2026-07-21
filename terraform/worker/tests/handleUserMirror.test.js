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

import { env } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import { handleUserMirror } from "../index.js";

function mirrorRequest(
  body,
  headers = { "X-Service-Key": "test-service-secret" },
) {
  return new Request("https://example.com/internal/users/mirror", {
    method: "POST",
    headers,
    body: typeof body === "string" ? body : JSON.stringify(body),
  });
}

describe("handleUserMirror", () => {
  it.each([
    [
      "missing service key header",
      mirrorRequest({ id: 1, pairing_token_hash: "h" }, {}),
      401,
    ],
    [
      "wrong service key",
      mirrorRequest(
        { id: 1, pairing_token_hash: "h" },
        { "X-Service-Key": "wrong" },
      ),
      401,
    ],
    ["invalid JSON body", mirrorRequest("not json"), 400],
    ["missing pairing_token_hash key entirely", mirrorRequest({ id: 1 }), 400],
    [
      "non-integer id",
      mirrorRequest({ id: "1", pairing_token_hash: "h" }),
      400,
    ],
    ["null body", mirrorRequest(null), 400],
    [
      "empty-string pairing_token_hash is rejected (use null to revoke, not empty string)",
      mirrorRequest({ id: 1, pairing_token_hash: "" }),
      400,
    ],
    [
      "non-string, non-null pairing_token_hash",
      mirrorRequest({ id: 1, pairing_token_hash: 12345 }),
      400,
    ],
  ])("rejects: %s", async (_name, request, expectedStatus) => {
    const response = await handleUserMirror(request, env);
    expect(response.status).toBe(expectedStatus);
  });

  it("inserts a new row on first mirror push (account creation)", async () => {
    const response = await handleUserMirror(
      mirrorRequest({ id: 42, pairing_token_hash: "hash-1" }),
      env,
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT id, pairing_token_hash FROM users WHERE id = ?",
    )
      .bind(42)
      .first();
    expect(row).toEqual({ id: 42, pairing_token_hash: "hash-1" });
  });

  it("upserts on a repeat push for the same id (pairing-token regenerate)", async () => {
    await handleUserMirror(
      mirrorRequest({ id: 42, pairing_token_hash: "hash-1" }),
      env,
    );

    const response = await handleUserMirror(
      mirrorRequest({ id: 42, pairing_token_hash: "hash-2" }),
      env,
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT pairing_token_hash FROM users WHERE id = ?",
    )
      .bind(42)
      .first();
    expect(row).toEqual({ pairing_token_hash: "hash-2" });

    const count = await env.DB.prepare(
      "SELECT count(*) as n FROM users WHERE id = ?",
    )
      .bind(42)
      .first();
    expect(count).toEqual({ n: 1 });
  });

  it("a null pairing_token_hash push clears the mirror (revoke, no reissue)", async () => {
    await handleUserMirror(
      mirrorRequest({ id: 42, pairing_token_hash: "hash-1" }),
      env,
    );

    const response = await handleUserMirror(
      mirrorRequest({ id: 42, pairing_token_hash: null }),
      env,
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT pairing_token_hash FROM users WHERE id = ?",
    )
      .bind(42)
      .first();
    expect(row).toEqual({ pairing_token_hash: null });
  });

  it("accepts a null pairing_token_hash on first push too (defensive; not expected in practice)", async () => {
    const response = await handleUserMirror(
      mirrorRequest({ id: 99, pairing_token_hash: null }),
      env,
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT pairing_token_hash FROM users WHERE id = ?",
    )
      .bind(99)
      .first();
    expect(row).toEqual({ pairing_token_hash: null });
  });
});
