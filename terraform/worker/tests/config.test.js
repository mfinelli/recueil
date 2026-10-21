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

import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import { handleGetConfig } from "../index.js";
import { sha256Hex } from "./test-helpers.js";

const testCtx = { waitUntil: () => {} };

// D1 state is shared across test cases within this file, so user ids must be
// unique across the whole file (the same reasoning archived-pages.test.js
// documents for its own helpers).
let nextUserId = 1;

async function seedDevice(rawToken) {
  const userId = nextUserId++;
  await env.DB.prepare(
    "INSERT INTO users (id, pairing_token_hash) VALUES (?, NULL)",
  )
    .bind(userId)
    .run();
  await env.DB.prepare(
    `INSERT INTO tokens (token_hash, user_id, device_name, device_type)
     VALUES (?, ?, 'test-device', 'extension')`,
  )
    .bind(await sha256Hex(rawToken), userId)
    .run();
}

/**
 * @param {string} [rawToken]
 */
function configRequest(rawToken) {
  /** @type {Record<string, string>} */
  const headers = {};
  if (rawToken) headers.Authorization = `Bearer ${rawToken}`;
  return new Request("https://example.com/config", { method: "GET", headers });
}

describe("GET /config", () => {
  it('reports links_enabled true when the binding is "true"', async () => {
    await seedDevice("rcl_live_config_on");
    const res = await handleGetConfig(
      configRequest("rcl_live_config_on"),
      { ...env, LINKS_ENABLED: "true" },
      testCtx,
    );
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({
      links_enabled: true,
      favicon_inline_max_bytes: 131072,
    });
  });

  it('reports links_enabled false when the binding is "false"', async () => {
    await seedDevice("rcl_live_config_off");
    const res = await handleGetConfig(
      configRequest("rcl_live_config_off"),
      { ...env, LINKS_ENABLED: "false" },
      testCtx,
    );
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({
      links_enabled: false,
      favicon_inline_max_bytes: 131072,
    });
  });

  it("treats an unset binding as off, not on", async () => {
    await seedDevice("rcl_live_config_unset");
    const withoutBinding = { ...env };
    delete withoutBinding.LINKS_ENABLED;
    const res = await handleGetConfig(
      configRequest("rcl_live_config_unset"),
      withoutBinding,
      testCtx,
    );
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({
      links_enabled: false,
      favicon_inline_max_bytes: 131072,
    });
  });

  it("returns 401 without a bearer token", async () => {
    const res = await handleGetConfig(configRequest(), env, testCtx);
    expect(res.status).toBe(401);
  });

  it("returns 401 for an unknown bearer token", async () => {
    const res = await handleGetConfig(
      configRequest("rcl_live_not_a_real_token"),
      env,
      testCtx,
    );
    expect(res.status).toBe(401);
  });
});
