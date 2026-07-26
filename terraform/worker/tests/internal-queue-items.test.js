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
  handleListFailedQueueItems,
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

async function seedQueueItem(userId, id, url, status, manualRetry = 0) {
  await env.DB.prepare(
    `INSERT INTO queue_items (id, user_id, url, status, manual_retry)
     VALUES (?, ?, ?, ?, ?)`,
  )
    .bind(id, userId, url, status, manualRetry)
    .run();
}

describe("handleListFailedQueueItems", () => {
  it("lists only the requested user's failed items", async () => {
    const userA = await seedUser();
    const userB = await seedUser();
    await seedQueueItem(
      userA,
      "a-failed-1",
      "https://example.com/a1",
      "failed",
    );
    await seedQueueItem(
      userA,
      "a-failed-2",
      "https://example.com/a2",
      "failed",
    );
    await seedQueueItem(
      userB,
      "b-failed-1",
      "https://example.com/b1",
      "failed",
    );

    const response = await handleListFailedQueueItems(
      serviceRequest(
        "GET",
        `/internal/queue-items?user_id=${userA}&status=failed`,
      ),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.items.map((i) => i.id).sort()).toEqual([
      "a-failed-1",
      "a-failed-2",
    ]);
  });

  it("excludes pending, claimed, and captured items", async () => {
    const userId = await seedUser();
    await seedQueueItem(
      userId,
      "pending-item",
      "https://example.com/p",
      "pending",
    );
    await seedQueueItem(
      userId,
      "claimed-item",
      "https://example.com/c",
      "claimed",
    );
    await seedQueueItem(
      userId,
      "captured-item",
      "https://example.com/cap",
      "captured",
    );

    const response = await handleListFailedQueueItems(
      serviceRequest(
        "GET",
        `/internal/queue-items?user_id=${userId}&status=failed`,
      ),
      env,
    );
    const body = await response.json();
    expect(body.items).toEqual([]);
  });

  it("returns an empty list for a user with no failed items", async () => {
    const userId = await seedUser();
    const response = await handleListFailedQueueItems(
      serviceRequest(
        "GET",
        `/internal/queue-items?user_id=${userId}&status=failed`,
      ),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.items).toEqual([]);
  });

  it("requires the service key", async () => {
    const response = await handleListFailedQueueItems(
      serviceRequest(
        "GET",
        "/internal/queue-items?user_id=1&status=failed",
        {},
      ),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("rejects the wrong service key", async () => {
    const response = await handleListFailedQueueItems(
      serviceRequest("GET", "/internal/queue-items?user_id=1&status=failed", {
        "X-Service-Key": "wrong",
      }),
      env,
    );
    expect(response.status).toBe(401);
  });

  it("rejects a missing user_id", async () => {
    const response = await handleListFailedQueueItems(
      serviceRequest("GET", "/internal/queue-items?status=failed"),
      env,
    );
    expect(response.status).toBe(400);
  });

  it("rejects a non-integer user_id", async () => {
    const response = await handleListFailedQueueItems(
      serviceRequest(
        "GET",
        "/internal/queue-items?user_id=not-a-number&status=failed",
      ),
      env,
    );
    expect(response.status).toBe(400);
  });

  it("rejects a missing or unsupported status", async () => {
    const userId = await seedUser();
    const missing = await handleListFailedQueueItems(
      serviceRequest("GET", `/internal/queue-items?user_id=${userId}`),
      env,
    );
    expect(missing.status).toBe(400);

    const unsupported = await handleListFailedQueueItems(
      serviceRequest(
        "GET",
        `/internal/queue-items?user_id=${userId}&status=pending`,
      ),
      env,
    );
    expect(unsupported.status).toBe(400);
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
