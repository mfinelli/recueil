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
import { handleListTokens, handleRevokeToken } from "../index.js";
import { sha256Hex } from "./test-helpers.js";

let nextUserId = 1;

function serviceRequest(
  method,
  path,
  headers = { "X-Service-Key": "test-service-secret" },
) {
  return new Request(`https://example.com${path}`, { method, headers });
}

async function seedUser() {
  const userId = nextUserId++;
  await env.DB.prepare(
    "INSERT INTO users (id, pairing_token_hash) VALUES (?, NULL)",
  )
    .bind(userId)
    .run();
  return userId;
}

async function seedToken(userId, rawToken, deviceName, deviceType) {
  const hash = await sha256Hex(rawToken);
  const inserted = await env.DB.prepare(
    `INSERT INTO tokens (token_hash, user_id, device_name, device_type)
     VALUES (?, ?, ?, ?) RETURNING id`,
  )
    .bind(hash, userId, deviceName, deviceType)
    .first();
  return inserted.id;
}

describe("handleListTokens", () => {
  it("lists only the requested user's tokens", async () => {
    const userA = await seedUser();
    const userB = await seedUser();
    await seedToken(userA, "rcl_live_list-tokens-a1", "Laptop", "extension");
    await seedToken(userA, "rcl_live_list-tokens-a2", "Phone", "pwa");
    await seedToken(userB, "rcl_live_list-tokens-b1", "Other", "cli");

    const response = await handleListTokens(
      serviceRequest("GET", `/internal/tokens?user_id=${userA}`),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.tokens.map((t) => t.device_name).sort()).toEqual([
      "Laptop",
      "Phone",
    ]);
    // token_hash must never be exposed via this endpoint.
    expect(body.tokens[0].token_hash).toBeUndefined();
  });

  it("returns an empty list for a user with no paired devices", async () => {
    const userId = await seedUser();
    const response = await handleListTokens(
      serviceRequest("GET", `/internal/tokens?user_id=${userId}`),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.tokens).toEqual([]);
  });

  it("requires the service key", async () => {
    const response = await handleListTokens(
      serviceRequest("GET", "/internal/tokens?user_id=1", {}),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("rejects the wrong service key", async () => {
    const response = await handleListTokens(
      serviceRequest("GET", "/internal/tokens?user_id=1", {
        "X-Service-Key": "wrong",
      }),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("rejects a missing user_id", async () => {
    const response = await handleListTokens(
      serviceRequest("GET", "/internal/tokens"),
      env,
    );
    expect(response.status).toBe(400);
  });

  it("rejects a non-integer user_id", async () => {
    const response = await handleListTokens(
      serviceRequest("GET", "/internal/tokens?user_id=not-a-number"),
      env,
    );
    expect(response.status).toBe(400);
  });
});

describe("handleRevokeToken", () => {
  it("revokes a token scoped to the correct user", async () => {
    const userId = await seedUser();
    const tokenId = await seedToken(
      userId,
      "rcl_live_revoke-1",
      "Laptop",
      "extension",
    );

    const response = await handleRevokeToken(
      serviceRequest("DELETE", `/internal/tokens/${tokenId}?user_id=${userId}`),
      env,
      String(tokenId),
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare("SELECT id FROM tokens WHERE id = ?")
      .bind(tokenId)
      .first();
    expect(row).toBeNull();
  });

  it("does not revoke a token if user_id doesn't match (backend-bug safety net)", async () => {
    const userId = await seedUser();
    const otherUserId = await seedUser();
    const tokenId = await seedToken(
      userId,
      "rcl_live_revoke-mismatch",
      "Laptop",
      "extension",
    );

    const response = await handleRevokeToken(
      serviceRequest(
        "DELETE",
        `/internal/tokens/${tokenId}?user_id=${otherUserId}`,
      ),
      env,
      String(tokenId),
    );
    expect(response.status).toBe(404);

    const row = await env.DB.prepare("SELECT id FROM tokens WHERE id = ?")
      .bind(tokenId)
      .first();
    expect(row).not.toBeNull();
  });

  it("requires the service key", async () => {
    const response = await handleRevokeToken(
      serviceRequest("DELETE", "/internal/tokens/1?user_id=1", {}),
      env,
      "1",
    );
    expect(response.status).toBe(401);
  });

  it("returns 404 for a nonexistent token id", async () => {
    const userId = await seedUser();
    const response = await handleRevokeToken(
      serviceRequest("DELETE", `/internal/tokens/999999?user_id=${userId}`),
      env,
      "999999",
    );
    expect(response.status).toBe(404);
  });

  it("rejects a non-integer user_id or token id", async () => {
    const response = await handleRevokeToken(
      serviceRequest("DELETE", "/internal/tokens/abc?user_id=1"),
      env,
      "abc",
    );
    expect(response.status).toBe(400);
  });
});
