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
      mirrorRequest({ id: 1, username: "a", password_hash: "h" }, {}),
      401,
    ],
    [
      "wrong service key",
      mirrorRequest(
        { id: 1, username: "a", password_hash: "h" },
        { "X-Service-Key": "wrong" },
      ),
      401,
    ],
    ["invalid JSON body", mirrorRequest("not json"), 400],
    ["missing username", mirrorRequest({ id: 1, password_hash: "h" }), 400],
    [
      "non-integer id",
      mirrorRequest({ id: "1", username: "a", password_hash: "h" }),
      400,
    ],
    ["null body", mirrorRequest(null), 400],
  ])("rejects: %s", async (_name, request, expectedStatus) => {
    const response = await handleUserMirror(request, env);
    expect(response.status).toBe(expectedStatus);
  });

  it("inserts a new row on first mirror push", async () => {
    const response = await handleUserMirror(
      mirrorRequest({ id: 42, username: "mario", password_hash: "hash-1" }),
      env,
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT id, username, password_hash FROM users WHERE id = ?",
    )
      .bind(42)
      .first();
    expect(row).toEqual({ id: 42, username: "mario", password_hash: "hash-1" });
  });

  it("upserts on a repeat push for the same id (password change / re-push)", async () => {
    await handleUserMirror(
      mirrorRequest({ id: 42, username: "mario", password_hash: "hash-1" }),
      env,
    );

    const response = await handleUserMirror(
      mirrorRequest({ id: 42, username: "mario", password_hash: "hash-2" }),
      env,
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT password_hash FROM users WHERE id = ?",
    )
      .bind(42)
      .first();
    expect(row).toEqual({ password_hash: "hash-2" });

    const count = await env.DB.prepare(
      "SELECT count(*) as n FROM users WHERE id = ?",
    )
      .bind(42)
      .first();
    expect(count).toEqual({ n: 1 });
  });
});
