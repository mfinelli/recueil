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
import {
  handleClaimQueueItem,
  handleCleanupQueueItems,
  handleEnqueue,
  handleListQueue,
} from "../index.js";
import { sha256Hex } from "./test-helpers.js";

let nextUserId = 1;

async function seedUser() {
  const userId = nextUserId++;
  await env.DB.prepare(
    "INSERT INTO users (id, pairing_token_hash) VALUES (?, NULL)",
  )
    .bind(userId)
    .run();
  return userId;
}

async function seedToken(
  userId,
  rawToken,
  deviceName = "test-device",
  deviceType = "extension",
) {
  const hash = await sha256Hex(rawToken);
  const inserted = await env.DB.prepare(
    `INSERT INTO tokens (token_hash, user_id, device_name, device_type)
     VALUES (?, ?, ?, ?) RETURNING id`,
  )
    .bind(hash, userId, deviceName, deviceType)
    .first();
  return inserted.id;
}

async function seedUserAndToken(rawToken, deviceName, deviceType) {
  const userId = await seedUser();
  const tokenId = await seedToken(userId, rawToken, deviceName, deviceType);
  return { userId, tokenId };
}

/**
 * @param {string} method
 * @param {string} path
 * @param {string} rawToken
 * @param {unknown} [body]
 */
function authedRequest(method, path, rawToken, body) {
  /** @type {RequestInit} */
  const init = {
    method,
    headers: { Authorization: `Bearer ${rawToken}` },
  };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  return new Request(`https://example.com${path}`, init);
}

// A minimal stand-in ExecutionContext for tests that call handlers
// directly (rather than through SELF.fetch, which supplies a real one).
// waitUntil just needs to not throw; the last_used_at touch settling in
// the background is fine for a test, and every test below re-seeds its
// own token so a possibly-not-yet-applied touch never affects assertions.
const testCtx = { waitUntil: () => {} };

describe("handleEnqueue", () => {
  it("enqueues a new item and records the enqueuing token", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_enqueue-1");

    const response = await handleEnqueue(
      authedRequest("POST", "/queue", "rcl_live_enqueue-1", {
        id: "11111111-1111-1111-1111-111111111111",
        url: "https://example.com/article",
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT user_id, url, status, added_by_token_id FROM queue_items WHERE id = ?",
    )
      .bind("11111111-1111-1111-1111-111111111111")
      .first();
    expect(row).toEqual({
      user_id: userId,
      url: "https://example.com/article",
      status: "pending",
      added_by_token_id: tokenId,
    });
  });

  it("a retried enqueue with the same id is idempotent (no error, no duplicate row)", async () => {
    await seedUserAndToken("rcl_live_enqueue-2");
    const body = {
      id: "22222222-2222-2222-2222-222222222222",
      url: "https://example.com/a",
    };

    const r1 = await handleEnqueue(
      authedRequest("POST", "/queue", "rcl_live_enqueue-2", body),
      env,
      testCtx,
    );
    const r2 = await handleEnqueue(
      authedRequest("POST", "/queue", "rcl_live_enqueue-2", body),
      env,
      testCtx,
    );
    expect(r1.status).toBe(204);
    expect(r2.status).toBe(204);

    const count = await env.DB.prepare(
      "SELECT count(*) as n FROM queue_items WHERE id = ?",
    )
      .bind(body.id)
      .first();
    expect(count).toEqual({ n: 1 });
  });

  it("rejects an unknown bearer token", async () => {
    const response = await handleEnqueue(
      authedRequest("POST", "/queue", "not-a-real-token", {
        id: "x",
        url: "https://example.com",
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(401);
  });

  it("rejects a missing Authorization header", async () => {
    const response = await handleEnqueue(
      new Request("https://example.com/queue", {
        method: "POST",
        body: JSON.stringify({ id: "x", url: "https://example.com" }),
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(401);
  });

  it.each([
    ["missing id", { url: "https://example.com" }],
    ["missing url", { id: "x" }],
    ["invalid url", { id: "x", url: "not-a-url" }],
  ])("rejects: %s", async (name, body) => {
    const rawToken = `rcl_live_enqueue-invalid-${name.replace(/\s+/g, "-")}`;
    await seedUserAndToken(rawToken);
    const response = await handleEnqueue(
      authedRequest("POST", "/queue", rawToken, body),
      env,
      testCtx,
    );
    expect(response.status).toBe(400);
  });
});

describe("handleListQueue", () => {
  it("lists only this user's pending items, not another user's", async () => {
    const a = await seedUserAndToken("rcl_live_list-a");
    const b = await seedUserAndToken("rcl_live_list-b");

    await env.DB.prepare(
      "INSERT INTO queue_items (id, user_id, url, added_by_token_id) VALUES (?, ?, ?, ?)",
    )
      .bind("list-item-a", a.userId, "https://example.com/a", a.tokenId)
      .run();
    await env.DB.prepare(
      "INSERT INTO queue_items (id, user_id, url, added_by_token_id) VALUES (?, ?, ?, ?)",
    )
      .bind("list-item-b", b.userId, "https://example.com/b", b.tokenId)
      .run();

    const response = await handleListQueue(
      authedRequest("GET", "/queue", "rcl_live_list-a"),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.items.map((i) => i.id)).toEqual(["list-item-a"]);
  });

  it("includes a stale claimed item but excludes a freshly claimed one", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_stale");

    await env.DB.prepare(
      `INSERT INTO queue_items (id, user_id, url, status, claimed_by_token_id, claimed_at)
       VALUES (?, ?, ?, 'claimed', ?, datetime('now', '-20 minutes'))`,
    )
      .bind("stale-item", userId, "https://example.com/stale", tokenId)
      .run();
    await env.DB.prepare(
      `INSERT INTO queue_items (id, user_id, url, status, claimed_by_token_id, claimed_at)
       VALUES (?, ?, ?, 'claimed', ?, datetime('now', '-1 minutes'))`,
    )
      .bind("fresh-item", userId, "https://example.com/fresh", tokenId)
      .run();

    const response = await handleListQueue(
      authedRequest("GET", "/queue", "rcl_live_stale"),
      env,
      testCtx,
    );
    const body = await response.json();
    expect(body.items.map((i) => i.id)).toEqual(["stale-item"]);
  });

  it("excludes captured and failed items", async () => {
    const { userId } = await seedUserAndToken("rcl_live_terminal");
    await env.DB.prepare(
      "INSERT INTO queue_items (id, user_id, url, status) VALUES (?, ?, ?, 'captured')",
    )
      .bind("captured-item", userId, "https://example.com/c")
      .run();
    await env.DB.prepare(
      "INSERT INTO queue_items (id, user_id, url, status) VALUES (?, ?, ?, 'failed')",
    )
      .bind("failed-item", userId, "https://example.com/f")
      .run();

    const response = await handleListQueue(
      authedRequest("GET", "/queue", "rcl_live_terminal"),
      env,
      testCtx,
    );
    const body = await response.json();
    expect(body.items).toEqual([]);
  });

  it("rejects an unknown bearer token", async () => {
    const response = await handleListQueue(
      authedRequest("GET", "/queue", "not-a-real-token"),
      env,
      testCtx,
    );
    expect(response.status).toBe(401);
  });
});

describe("handleClaimQueueItem", () => {
  it("claims a pending item", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_claim-1");
    await env.DB.prepare(
      "INSERT INTO queue_items (id, user_id, url) VALUES (?, ?, ?)",
    )
      .bind("claim-item-1", userId, "https://example.com/claim1")
      .run();

    const response = await handleClaimQueueItem(
      authedRequest("POST", "/queue/claim-item-1/claim", "rcl_live_claim-1"),
      env,
      testCtx,
      "claim-item-1",
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.status).toBe("claimed");
    expect(body.claimed_by_token_id).toBe(tokenId);

    const row = await env.DB.prepare(
      "SELECT status, claimed_by_token_id FROM queue_items WHERE id = ?",
    )
      .bind("claim-item-1")
      .first();
    expect(row.status).toBe("claimed");
    expect(row.claimed_by_token_id).toBe(tokenId);
  });

  it("a second device cannot claim an item another device just claimed", async () => {
    const userId = await seedUser();
    const firstTokenId = await seedToken(userId, "rcl_live_claim-race-first");
    await seedToken(userId, "rcl_live_claim-race-second");
    await env.DB.prepare(
      "INSERT INTO queue_items (id, user_id, url) VALUES (?, ?, ?)",
    )
      .bind("claim-race-item", userId, "https://example.com/race")
      .run();

    const first = await handleClaimQueueItem(
      authedRequest(
        "POST",
        "/queue/claim-race-item/claim",
        "rcl_live_claim-race-first",
      ),
      env,
      testCtx,
      "claim-race-item",
    );
    const second = await handleClaimQueueItem(
      authedRequest(
        "POST",
        "/queue/claim-race-item/claim",
        "rcl_live_claim-race-second",
      ),
      env,
      testCtx,
      "claim-race-item",
    );

    expect(first.status).toBe(200);
    expect(second.status).toBe(409);

    const row = await env.DB.prepare(
      "SELECT claimed_by_token_id FROM queue_items WHERE id = ?",
    )
      .bind("claim-race-item")
      .first();
    expect(row.claimed_by_token_id).toBe(firstTokenId);
  });

  it("reclaims a stale-claimed item (previous claiming device died mid-capture)", async () => {
    const userId = await seedUser();
    const staleTokenId = await seedToken(userId, "rcl_live_claim-stale-old");
    const reclaimTokenId = await seedToken(
      userId,
      "rcl_live_claim-stale-reclaimer",
    );

    await env.DB.prepare(
      `INSERT INTO queue_items (id, user_id, url, status, claimed_by_token_id, claimed_at)
       VALUES (?, ?, ?, 'claimed', ?, datetime('now', '-20 minutes'))`,
    )
      .bind(
        "stale-claim-item",
        userId,
        "https://example.com/stale-claim",
        staleTokenId,
      )
      .run();

    const response = await handleClaimQueueItem(
      authedRequest(
        "POST",
        "/queue/stale-claim-item/claim",
        "rcl_live_claim-stale-reclaimer",
      ),
      env,
      testCtx,
      "stale-claim-item",
    );
    expect(response.status).toBe(200);

    const row = await env.DB.prepare(
      "SELECT claimed_by_token_id FROM queue_items WHERE id = ?",
    )
      .bind("stale-claim-item")
      .first();
    expect(row.claimed_by_token_id).toBe(reclaimTokenId);
  });

  it("returns 409 for an actively (non-stale) claimed item", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_claim-active");
    await env.DB.prepare(
      `INSERT INTO queue_items (id, user_id, url, status, claimed_by_token_id, claimed_at)
       VALUES (?, ?, ?, 'claimed', ?, datetime('now', '-1 minutes'))`,
    )
      .bind("active-claim-item", userId, "https://example.com/active", tokenId)
      .run();

    const response = await handleClaimQueueItem(
      authedRequest(
        "POST",
        "/queue/active-claim-item/claim",
        "rcl_live_claim-active",
      ),
      env,
      testCtx,
      "active-claim-item",
    );
    expect(response.status).toBe(409);
  });

  it("returns 410 for an already-captured item", async () => {
    const { userId } = await seedUserAndToken("rcl_live_claim-captured");
    await env.DB.prepare(
      "INSERT INTO queue_items (id, user_id, url, status) VALUES (?, ?, ?, 'captured')",
    )
      .bind("captured-claim-item", userId, "https://example.com/captured")
      .run();

    const response = await handleClaimQueueItem(
      authedRequest(
        "POST",
        "/queue/captured-claim-item/claim",
        "rcl_live_claim-captured",
      ),
      env,
      testCtx,
      "captured-claim-item",
    );
    expect(response.status).toBe(410);
  });

  it("returns 410 for an already-failed item", async () => {
    const { userId } = await seedUserAndToken("rcl_live_claim-failed");
    await env.DB.prepare(
      "INSERT INTO queue_items (id, user_id, url, status) VALUES (?, ?, ?, 'failed')",
    )
      .bind("failed-claim-item", userId, "https://example.com/failed")
      .run();

    const response = await handleClaimQueueItem(
      authedRequest(
        "POST",
        "/queue/failed-claim-item/claim",
        "rcl_live_claim-failed",
      ),
      env,
      testCtx,
      "failed-claim-item",
    );
    expect(response.status).toBe(410);
  });

  it("returns 404 for a nonexistent item id", async () => {
    await seedUserAndToken("rcl_live_claim-404");
    const response = await handleClaimQueueItem(
      authedRequest(
        "POST",
        "/queue/does-not-exist/claim",
        "rcl_live_claim-404",
      ),
      env,
      testCtx,
      "does-not-exist",
    );
    expect(response.status).toBe(404);
  });

  it("returns 404, not the other user's item, when a wrong-user token attempts a claim", async () => {
    const other = await seedUserAndToken("rcl_live_claim-other-owner");
    await env.DB.prepare(
      "INSERT INTO queue_items (id, user_id, url) VALUES (?, ?, ?)",
    )
      .bind("other-owner-item", other.userId, "https://example.com/other")
      .run();
    await seedUserAndToken("rcl_live_claim-wrong-user");

    const response = await handleClaimQueueItem(
      authedRequest(
        "POST",
        "/queue/other-owner-item/claim",
        "rcl_live_claim-wrong-user",
      ),
      env,
      testCtx,
      "other-owner-item",
    );
    expect(response.status).toBe(404);

    // Confirm it genuinely never touched the other user's row, not just
    // that the status code happened to be 404.
    const row = await env.DB.prepare(
      "SELECT status FROM queue_items WHERE id = ?",
    )
      .bind("other-owner-item")
      .first();
    expect(row.status).toBe("pending");
  });

  it("rejects an unknown bearer token", async () => {
    const response = await handleClaimQueueItem(
      authedRequest("POST", "/queue/x/claim", "not-a-real-token"),
      env,
      testCtx,
      "x",
    );
    expect(response.status).toBe(401);
  });
});

function serviceRequest(path) {
  return new Request(`https://example.com${path}`, {
    method: "POST",
    headers: { "X-Service-Key": "test-service-secret" },
  });
}

async function insertQueueItem(id, userId, status, hoursSinceClaimed) {
  const claimedAt =
    hoursSinceClaimed === null
      ? null
      : `datetime('now', '-${hoursSinceClaimed} hours')`;
  await env.DB.prepare(
    `INSERT INTO queue_items (id, user_id, url, status, claimed_at)
     VALUES (?, ?, ?, ?, ${claimedAt === null ? "NULL" : claimedAt})`,
  )
    .bind(id, userId, `https://example.com/${id}`, status)
    .run();
}

describe("handleCleanupQueueItems", () => {
  it("deletes a captured item claimed more than 72 hours ago", async () => {
    const userId = await seedUser();
    await insertQueueItem("cleanup-old-captured", userId, "captured", 100);

    const response = await handleCleanupQueueItems(
      serviceRequest("/internal/queue-items/cleanup"),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.deleted).toBeGreaterThanOrEqual(1);

    const row = await env.DB.prepare("SELECT id FROM queue_items WHERE id = ?")
      .bind("cleanup-old-captured")
      .first();
    expect(row).toBeNull();
  });

  it("does not delete a captured item claimed less than 72 hours ago", async () => {
    const userId = await seedUser();
    await insertQueueItem("cleanup-recent-captured", userId, "captured", 1);

    await handleCleanupQueueItems(
      serviceRequest("/internal/queue-items/cleanup"),
      env,
    );

    const row = await env.DB.prepare("SELECT id FROM queue_items WHERE id = ?")
      .bind("cleanup-recent-captured")
      .first();
    expect(row).not.toBeNull();
  });

  it("never deletes a failed item, no matter how old", async () => {
    const userId = await seedUser();
    await insertQueueItem("cleanup-old-failed", userId, "failed", 1000);

    await handleCleanupQueueItems(
      serviceRequest("/internal/queue-items/cleanup"),
      env,
    );

    const row = await env.DB.prepare("SELECT id FROM queue_items WHERE id = ?")
      .bind("cleanup-old-failed")
      .first();
    expect(row).not.toBeNull();
  });

  it("does not delete pending or claimed (non-terminal) items regardless of age", async () => {
    const userId = await seedUser();
    await insertQueueItem("cleanup-old-pending", userId, "pending", null);
    await insertQueueItem("cleanup-old-claimed", userId, "claimed", 1000);

    await handleCleanupQueueItems(
      serviceRequest("/internal/queue-items/cleanup"),
      env,
    );

    const pending = await env.DB.prepare(
      "SELECT id FROM queue_items WHERE id = ?",
    )
      .bind("cleanup-old-pending")
      .first();
    const claimed = await env.DB.prepare(
      "SELECT id FROM queue_items WHERE id = ?",
    )
      .bind("cleanup-old-claimed")
      .first();
    expect(pending).not.toBeNull();
    expect(claimed).not.toBeNull();
  });

  it("sweeps across all users, not scoped to one (this is a maintenance job, not a device operation)", async () => {
    const userA = await seedUser();
    const userB = await seedUser();
    await insertQueueItem("cleanup-multi-a", userA, "captured", 200);
    await insertQueueItem("cleanup-multi-b", userB, "captured", 200);

    const response = await handleCleanupQueueItems(
      serviceRequest("/internal/queue-items/cleanup"),
      env,
    );
    const body = await response.json();
    expect(body.deleted).toBeGreaterThanOrEqual(2);

    const rowA = await env.DB.prepare("SELECT id FROM queue_items WHERE id = ?")
      .bind("cleanup-multi-a")
      .first();
    const rowB = await env.DB.prepare("SELECT id FROM queue_items WHERE id = ?")
      .bind("cleanup-multi-b")
      .first();
    expect(rowA).toBeNull();
    expect(rowB).toBeNull();
  });

  it("requires the service key", async () => {
    const response = await handleCleanupQueueItems(
      new Request("https://example.com/internal/queue-items/cleanup", {
        method: "POST",
      }),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("rejects the wrong service key", async () => {
    const response = await handleCleanupQueueItems(
      new Request("https://example.com/internal/queue-items/cleanup", {
        method: "POST",
        headers: { "X-Service-Key": "wrong" },
      }),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("reports zero deleted when nothing qualifies", async () => {
    const response = await handleCleanupQueueItems(
      serviceRequest("/internal/queue-items/cleanup"),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.deleted).toBe(0);
  });
});
