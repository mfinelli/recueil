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
import { handlePair } from "../index.js";
import { sha256Hex } from "./test-helpers.js";

let nextUserId = 1;

function pairRequest(body) {
  return new Request("https://example.com/pair", {
    method: "POST",
    body: typeof body === "string" ? body : JSON.stringify(body),
  });
}

async function seedUser(pairingToken) {
  const userId = nextUserId++;
  const hash = pairingToken === null ? null : await sha256Hex(pairingToken);
  await env.DB.prepare(
    "INSERT INTO users (id, pairing_token_hash) VALUES (?, ?)",
  )
    .bind(userId, hash)
    .run();
  return userId;
}

describe("handlePair", () => {
  it("issues a bearer token for a valid pairing token", async () => {
    const userId = await seedUser("rcl_pair_test-token-1");

    const response = await handlePair(
      pairRequest({
        pairing_token: "rcl_pair_test-token-1",
        device_name: "My Laptop",
        device_type: "extension",
      }),
      env,
    );
    expect(response.status).toBe(201);

    const body = await response.json();
    expect(body.token.startsWith("rcl_live_")).toBe(true);
    expect(body.device_name).toBe("My Laptop");
    expect(body.device_type).toBe("extension");
    expect(typeof body.device_id).toBe("number");

    const expectedHash = await sha256Hex(body.token);
    const row = await env.DB.prepare(
      "SELECT user_id, device_name, device_type, token_hash FROM tokens WHERE id = ?",
    )
      .bind(body.device_id)
      .first();
    expect(row).toEqual({
      user_id: userId,
      device_name: "My Laptop",
      device_type: "extension",
      token_hash: expectedHash,
    });
  });

  it("rejects an unknown pairing token", async () => {
    const response = await handlePair(
      pairRequest({
        pairing_token: "rcl_pair_does-not-exist",
        device_name: "x",
        device_type: "cli",
      }),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("a revoked (NULL-hash) account can never be paired, even by submitting the literal word null", async () => {
    await seedUser(null);

    // A NULL column value can never match a bound, non-null lookup
    // parameter in SQL -- confirms the revoke design's core safety
    // property empirically, not just by assertion (see DESIGN.md §5).
    const response = await handlePair(
      pairRequest({
        pairing_token: "null",
        device_name: "x",
        device_type: "cli",
      }),
      env,
    );
    expect(response.status).toBe(401);
  });

  it.each([
    ["missing pairing_token", { device_name: "x", device_type: "cli" }],
    ["missing device_name", { pairing_token: "t", device_type: "cli" }],
    ["missing device_type", { pairing_token: "t", device_name: "x" }],
    [
      "invalid device_type",
      { pairing_token: "t", device_name: "x", device_type: "toaster" },
    ],
    [
      "empty pairing_token",
      { pairing_token: "", device_name: "x", device_type: "cli" },
    ],
    ["null body", null],
  ])("rejects: %s", async (_name, body) => {
    const response = await handlePair(pairRequest(body), env);
    expect(response.status).toBe(400);
  });

  it("rejects invalid JSON", async () => {
    const response = await handlePair(pairRequest("not json"), env);
    expect(response.status).toBe(400);
  });

  it('accepts device_type "shortcut" (the iOS Shortcut client)', async () => {
    const userId = await seedUser("rcl_pair_test-token-shortcut");

    const response = await handlePair(
      pairRequest({
        pairing_token: "rcl_pair_test-token-shortcut",
        device_name: "iPhone",
        device_type: "shortcut",
      }),
      env,
    );
    expect(response.status).toBe(201);

    const body = await response.json();
    expect(body.device_type).toBe("shortcut");

    const row = await env.DB.prepare(
      "SELECT user_id, device_type FROM tokens WHERE id = ?",
    )
      .bind(body.device_id)
      .first();
    expect(row).toEqual({ user_id: userId, device_type: "shortcut" });
  });

  it("the same pairing token can issue multiple independent device bearer tokens", async () => {
    const userId = await seedUser("rcl_pair_test-token-multi");

    const r1 = await handlePair(
      pairRequest({
        pairing_token: "rcl_pair_test-token-multi",
        device_name: "Device A",
        device_type: "extension",
      }),
      env,
    );
    const r2 = await handlePair(
      pairRequest({
        pairing_token: "rcl_pair_test-token-multi",
        device_name: "Device B",
        device_type: "pwa",
      }),
      env,
    );
    const b1 = await r1.json();
    const b2 = await r2.json();

    expect(b1.token).not.toBe(b2.token);
    expect(b1.device_id).not.toBe(b2.device_id);

    const count = await env.DB.prepare(
      "SELECT count(*) as n FROM tokens WHERE user_id = ?",
    )
      .bind(userId)
      .first();
    expect(count).toEqual({ n: 2 });
  });
});
