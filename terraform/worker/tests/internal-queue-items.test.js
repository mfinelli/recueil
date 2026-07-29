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
  handleListQueueItems,
  handleRetryQueueItem,
  handleServiceEnqueue,
} from "../index.js";

let nextUserId = 1;

/**
 * @param {string} method
 * @param {string} path
 * @param {Record<string, string>} [headers]
 * @param {unknown} [body]
 */
function serviceRequest(
  method,
  path,
  headers = { "X-Service-Key": "test-service-secret" },
  body,
) {
  /** @type {RequestInit} */
  const init = { method, headers };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  return new Request(`https://example.com${path}`, init);
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

/**
 * @param {number} userId
 * @param {string} id
 * @param {string} url
 * @param {string} status
 * @param {number} [manualRetry]
 * @param {string | null} [claimedAt] SQLite datetime string (e.g. via
 *   `datetime('now', '-20 minutes')`, computed by the caller) -- lets
 *   tests exercise handleListQueueItems' own recency window without
 *   depending on real wall-clock timing.
 */
async function seedQueueItem(
  userId,
  id,
  url,
  status,
  manualRetry = 0,
  claimedAt = null,
) {
  await env.DB.prepare(
    `INSERT INTO queue_items (id, user_id, url, status, manual_retry, claimed_at)
     VALUES (?, ?, ?, ?, ?, ?)`,
  )
    .bind(id, userId, url, status, manualRetry, claimedAt)
    .run();
}

/** A claimed_at value clearly inside/outside handleListQueueItems' own
 * 15-minute recency window, computed directly against SQLite's own clock
 * (not JS's) so there's no risk of clock skew between the two. */
async function claimedMinutesAgo(minutesAgo) {
  const { results } = await env.DB.prepare(`SELECT datetime('now', ?) AS ts`)
    .bind(`-${minutesAgo} minutes`)
    .all();
  return results[0].ts;
}

describe("handleListQueueItems", () => {
  it("lists pending/claimed/failed items unconditionally, scoped to the requesting user", async () => {
    const userA = await seedUser();
    const userB = await seedUser();
    await seedQueueItem(
      userA,
      "a-pending",
      "https://example.com/a1",
      "pending",
    );
    await seedQueueItem(
      userA,
      "a-claimed",
      "https://example.com/a2",
      "claimed",
    );
    await seedQueueItem(userA, "a-failed", "https://example.com/a3", "failed");
    await seedQueueItem(userB, "b-failed", "https://example.com/b1", "failed");

    const response = await handleListQueueItems(
      serviceRequest("GET", `/internal/queue-items?user_id=${userA}`),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.items.map((i) => i.id).sort()).toEqual([
      "a-claimed",
      "a-failed",
      "a-pending",
    ]);
  });

  it("includes a captured item claimed within the recency window", async () => {
    const userId = await seedUser();
    const recent = await claimedMinutesAgo(5);
    await seedQueueItem(
      userId,
      "recently-captured",
      "https://example.com/rc",
      "captured",
      0,
      recent,
    );

    const response = await handleListQueueItems(
      serviceRequest("GET", `/internal/queue-items?user_id=${userId}`),
      env,
    );
    const body = await response.json();
    expect(body.items.map((i) => i.id)).toEqual(["recently-captured"]);
    expect(body.items[0].claimed_at).toBe(recent);
  });

  it("excludes a captured item claimed outside the recency window", async () => {
    const userId = await seedUser();
    const stale = await claimedMinutesAgo(20);
    await seedQueueItem(
      userId,
      "stale-captured",
      "https://example.com/sc",
      "captured",
      0,
      stale,
    );

    const response = await handleListQueueItems(
      serviceRequest("GET", `/internal/queue-items?user_id=${userId}`),
      env,
    );
    const body = await response.json();
    expect(body.items).toEqual([]);
  });

  it("returns an empty list for a user with no items", async () => {
    const userId = await seedUser();
    const response = await handleListQueueItems(
      serviceRequest("GET", `/internal/queue-items?user_id=${userId}`),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.items).toEqual([]);
  });

  it("requires the service key", async () => {
    const response = await handleListQueueItems(
      serviceRequest("GET", "/internal/queue-items?user_id=1", {}),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("rejects the wrong service key", async () => {
    const response = await handleListQueueItems(
      serviceRequest("GET", "/internal/queue-items?user_id=1", {
        "X-Service-Key": "wrong",
      }),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("rejects a missing user_id", async () => {
    const response = await handleListQueueItems(
      serviceRequest("GET", "/internal/queue-items"),
      env,
    );
    expect(response.status).toBe(400);
  });

  it("rejects a non-integer user_id", async () => {
    const response = await handleListQueueItems(
      serviceRequest("GET", "/internal/queue-items?user_id=not-a-number"),
      env,
    );
    expect(response.status).toBe(400);
  });
});

describe("handleRetryQueueItem", () => {
  it("flags a failed item for retry, scoped to the correct user", async () => {
    const userId = await seedUser();
    await seedQueueItem(
      userId,
      "retry-me",
      "https://example.com/retry",
      "failed",
    );

    const response = await handleRetryQueueItem(
      serviceRequest(
        "POST",
        `/internal/queue-items/retry-me/retry?user_id=${userId}`,
      ),
      env,
      "retry-me",
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT status, manual_retry FROM queue_items WHERE id = ?",
    )
      .bind("retry-me")
      .first();
    expect(row.status).toBe("failed");
    expect(row.manual_retry).toBe(1);
  });

  it("does not flag an item if user_id doesn't match (backend-bug safety net)", async () => {
    const userId = await seedUser();
    const otherUserId = await seedUser();
    await seedQueueItem(
      userId,
      "retry-mismatch",
      "https://example.com/x",
      "failed",
    );

    const response = await handleRetryQueueItem(
      serviceRequest(
        "POST",
        `/internal/queue-items/retry-mismatch/retry?user_id=${otherUserId}`,
      ),
      env,
      "retry-mismatch",
    );
    expect(response.status).toBe(404);

    const row = await env.DB.prepare(
      "SELECT manual_retry FROM queue_items WHERE id = ?",
    )
      .bind("retry-mismatch")
      .first();
    expect(row.manual_retry).toBe(0);
  });

  it("returns 404 for an item that isn't in the failed state", async () => {
    const userId = await seedUser();
    await seedQueueItem(
      userId,
      "still-pending",
      "https://example.com/p",
      "pending",
    );

    const response = await handleRetryQueueItem(
      serviceRequest(
        "POST",
        `/internal/queue-items/still-pending/retry?user_id=${userId}`,
      ),
      env,
      "still-pending",
    );
    expect(response.status).toBe(404);
  });

  it("returns 404 for a nonexistent item id", async () => {
    const userId = await seedUser();
    const response = await handleRetryQueueItem(
      serviceRequest(
        "POST",
        `/internal/queue-items/does-not-exist/retry?user_id=${userId}`,
      ),
      env,
      "does-not-exist",
    );
    expect(response.status).toBe(404);
  });

  it("requires the service key", async () => {
    const response = await handleRetryQueueItem(
      serviceRequest(
        "POST",
        "/internal/queue-items/retry-me/retry?user_id=1",
        {},
      ),
      env,
      "retry-me",
    );
    expect(response.status).toBe(401);
  });

  it("rejects a non-integer user_id", async () => {
    const response = await handleRetryQueueItem(
      serviceRequest(
        "POST",
        "/internal/queue-items/retry-me/retry?user_id=not-a-number",
      ),
      env,
      "retry-me",
    );
    expect(response.status).toBe(400);
  });
});

describe("handleServiceEnqueue", () => {
  it("enqueues a new item with no device token attribution", async () => {
    const userId = await seedUser();

    const response = await handleServiceEnqueue(
      serviceRequest(
        "POST",
        "/internal/queue-items",
        { "X-Service-Key": "test-service-secret" },
        {
          id: "33333333-3333-3333-3333-333333333333",
          user_id: userId,
          url: "https://example.com/recapture-me",
        },
      ),
      env,
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT user_id, url, status, added_by_token_id FROM queue_items WHERE id = ?",
    )
      .bind("33333333-3333-3333-3333-333333333333")
      .first();
    expect(row).toEqual({
      user_id: userId,
      url: "https://example.com/recapture-me",
      status: "pending",
      added_by_token_id: null,
    });
  });

  it("a retried enqueue with the same id is idempotent (no error, no duplicate row)", async () => {
    const userId = await seedUser();
    const body = {
      id: "44444444-4444-4444-4444-444444444444",
      user_id: userId,
      url: "https://example.com/a",
    };

    const r1 = await handleServiceEnqueue(
      serviceRequest(
        "POST",
        "/internal/queue-items",
        { "X-Service-Key": "test-service-secret" },
        body,
      ),
      env,
    );
    const r2 = await handleServiceEnqueue(
      serviceRequest(
        "POST",
        "/internal/queue-items",
        { "X-Service-Key": "test-service-secret" },
        body,
      ),
      env,
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

  it("requires the service key", async () => {
    const response = await handleServiceEnqueue(
      serviceRequest(
        "POST",
        "/internal/queue-items",
        {},
        { id: "x", user_id: 1, url: "https://example.com" },
      ),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("rejects the wrong service key", async () => {
    const response = await handleServiceEnqueue(
      serviceRequest(
        "POST",
        "/internal/queue-items",
        { "X-Service-Key": "wrong" },
        { id: "x", user_id: 1, url: "https://example.com" },
      ),
      env,
    );
    expect(response.status).toBe(401);
  });

  it.each([
    ["missing id", { user_id: 1, url: "https://example.com" }],
    ["missing user_id", { id: "x", url: "https://example.com" }],
    [
      "non-integer user_id",
      { id: "x", user_id: "one", url: "https://example.com" },
    ],
    ["missing url", { id: "x", user_id: 1 }],
    ["invalid url", { id: "x", user_id: 1, url: "not-a-url" }],
  ])("rejects: %s", async (name, body) => {
    const response = await handleServiceEnqueue(
      serviceRequest(
        "POST",
        "/internal/queue-items",
        { "X-Service-Key": "test-service-secret" },
        body,
      ),
      env,
    );
    expect(response.status).toBe(400);
  });
});
