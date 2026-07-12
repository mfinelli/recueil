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
  handleCompleteQueueItem,
  handleFailQueueItem,
  handleGetUploadUrls,
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

async function seedToken(userId, rawToken) {
  const hash = await sha256Hex(rawToken);
  const inserted = await env.DB.prepare(
    `INSERT INTO tokens (token_hash, user_id, device_name, device_type)
     VALUES (?, ?, 'test-device', 'extension') RETURNING id`,
  )
    .bind(hash, userId)
    .first();
  return inserted.id;
}

async function seedUserAndToken(rawToken) {
  const userId = await seedUser();
  const tokenId = await seedToken(userId, rawToken);
  return { userId, tokenId };
}

async function seedQueueItem(id, userId, url, status, claimedByTokenId) {
  await env.DB.prepare(
    `INSERT INTO queue_items (id, user_id, url, status, claimed_by_token_id)
     VALUES (?, ?, ?, ?, ?)`,
  )
    .bind(id, userId, url, status, claimedByTokenId ?? null)
    .run();
}

function authedRequest(method, path, rawToken, body) {
  const init = {
    method,
    headers: { Authorization: `Bearer ${rawToken}` },
  };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  return new Request(`https://example.com${path}`, init);
}

const testCtx = { waitUntil: () => {} };

const FAKE_HTML_SHA256 = "a".repeat(64); // well-formed lowercase hex, not a real digest of anything

// Mirrors index.js's own hexToBase64 (kept private there) rather than
// relying on Node's Buffer, which isn't a declared global in this
// worker-runtime test environment.
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

describe("handleGetUploadUrls", () => {
  it("returns a presigned URL and a deterministic r2 key for a capture id", async () => {
    const { userId } = await seedUserAndToken("rcl_live_upload-urls-1");

    const response = await handleGetUploadUrls(
      authedRequest("POST", "/captures/upload-urls", "rcl_live_upload-urls-1", {
        capture_id: "capture-abc",
        content_sha256_html: FAKE_HTML_SHA256,
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.capture_id).toBe("capture-abc");
    expect(body.r2_key_html).toBe(`pending/${userId}/capture-abc/page.html`);
    expect(body.upload_url_html).toMatch(/^https:\/\//);
    expect(new URL(body.upload_url_html).pathname).toBe(
      `/test-bucket/${body.r2_key_html}`,
    );
    expect(typeof body.expires_in_seconds).toBe("number");
  });

  it("binds the checksum into the signature (SignedHeaders includes it)", async () => {
    await seedUserAndToken("rcl_live_upload-urls-checksum-1");
    const response = await handleGetUploadUrls(
      authedRequest(
        "POST",
        "/captures/upload-urls",
        "rcl_live_upload-urls-checksum-1",
        {
          capture_id: "capture-checksum-1",
          content_sha256_html: FAKE_HTML_SHA256,
        },
      ),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
    const body = await response.json();

    const htmlUrl = new URL(body.upload_url_html);
    expect(htmlUrl.searchParams.get("X-Amz-SignedHeaders")).toBe(
      "host;x-amz-checksum-sha256",
    );
    expect(body.required_headers_html).toEqual({
      "x-amz-checksum-sha256": hexToBase64(FAKE_HTML_SHA256),
    });
  });

  it("produces a different signature (and thus a different URL) for a different checksum, all else equal", async () => {
    await seedUserAndToken("rcl_live_upload-urls-checksum-diff");
    const requestBody = (hash) => ({
      capture_id: "capture-checksum-diff",
      content_sha256_html: hash,
    });
    const r1 = await handleGetUploadUrls(
      authedRequest(
        "POST",
        "/captures/upload-urls",
        "rcl_live_upload-urls-checksum-diff",
        requestBody(FAKE_HTML_SHA256),
      ),
      env,
      testCtx,
    );
    const r2 = await handleGetUploadUrls(
      authedRequest(
        "POST",
        "/captures/upload-urls",
        "rcl_live_upload-urls-checksum-diff",
        requestBody("c".repeat(64)),
      ),
      env,
      testCtx,
    );
    const b1 = await r1.json();
    const b2 = await r2.json();
    expect(b1.upload_url_html).not.toBe(b2.upload_url_html);
  });

  it("scopes object keys by user id, so two users' identical capture ids never collide", async () => {
    const a = await seedUserAndToken("rcl_live_upload-urls-user-a");
    const b = await seedUserAndToken("rcl_live_upload-urls-user-b");

    const ra = await handleGetUploadUrls(
      authedRequest(
        "POST",
        "/captures/upload-urls",
        "rcl_live_upload-urls-user-a",
        {
          capture_id: "same-capture-id",
          content_sha256_html: FAKE_HTML_SHA256,
        },
      ),
      env,
      testCtx,
    );
    const rb = await handleGetUploadUrls(
      authedRequest(
        "POST",
        "/captures/upload-urls",
        "rcl_live_upload-urls-user-b",
        {
          capture_id: "same-capture-id",
          content_sha256_html: FAKE_HTML_SHA256,
        },
      ),
      env,
      testCtx,
    );
    const ba = await ra.json();
    const bb = await rb.json();
    expect(ba.r2_key_html).not.toBe(bb.r2_key_html);
    expect(ba.r2_key_html).toBe(
      `pending/${a.userId}/same-capture-id/page.html`,
    );
    expect(bb.r2_key_html).toBe(
      `pending/${b.userId}/same-capture-id/page.html`,
    );
  });

  it("rejects an unknown bearer token", async () => {
    const response = await handleGetUploadUrls(
      authedRequest("POST", "/captures/upload-urls", "not-a-real-token", {
        capture_id: "x",
        content_sha256_html: FAKE_HTML_SHA256,
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(401);
  });

  it("rejects a missing capture_id", async () => {
    await seedUserAndToken("rcl_live_upload-urls-invalid");
    const response = await handleGetUploadUrls(
      authedRequest(
        "POST",
        "/captures/upload-urls",
        "rcl_live_upload-urls-invalid",
        { content_sha256_html: FAKE_HTML_SHA256 },
      ),
      env,
      testCtx,
    );
    expect(response.status).toBe(400);
  });

  it.each([
    ["missing content_sha256_html", { capture_id: "x" }],
    [
      "content_sha256_html too short",
      { capture_id: "x", content_sha256_html: "abc123" },
    ],
    [
      "content_sha256_html uppercase (not lowercase hex)",
      { capture_id: "x", content_sha256_html: "A".repeat(64) },
    ],
  ])("rejects: %s", async (name, body) => {
    const rawToken = `rcl_live_upload-urls-bad-${name.replace(/\s+/g, "-")}`;
    await seedUserAndToken(rawToken);
    const response = await handleGetUploadUrls(
      authedRequest("POST", "/captures/upload-urls", rawToken, body),
      env,
      testCtx,
    );
    expect(response.status).toBe(400);
  });
});

describe("handleCompleteQueueItem", () => {
  it("writes a pending_captures row and marks the queue item captured", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_complete-1");
    await seedQueueItem(
      "complete-item-1",
      userId,
      "https://example.com/complete1",
      "claimed",
      tokenId,
    );

    const response = await handleCompleteQueueItem(
      authedRequest(
        "POST",
        "/queue/complete-item-1/complete",
        "rcl_live_complete-1",
        {
          capture_id: "capture-1",
          captured_at: "2026-07-12T12:00:00.000Z",
        },
      ),
      env,
      testCtx,
      "complete-item-1",
    );
    expect(response.status).toBe(201);
    const body = await response.json();
    expect(body.id).toBe("capture-1");
    expect(body.r2_key_html).toBe(`pending/${userId}/capture-1/page.html`);

    const capture = await env.DB.prepare(
      "SELECT user_id, queue_item_id, url, r2_key_html, captured_at, fetched_by_backend FROM pending_captures WHERE id = ?",
    )
      .bind("capture-1")
      .first();
    expect(capture).toEqual({
      user_id: userId,
      queue_item_id: "complete-item-1",
      url: "https://example.com/complete1",
      r2_key_html: `pending/${userId}/capture-1/page.html`,
      captured_at: "2026-07-12T12:00:00.000Z",
      fetched_by_backend: 0,
    });

    const queueItem = await env.DB.prepare(
      "SELECT status FROM queue_items WHERE id = ?",
    )
      .bind("complete-item-1")
      .first();
    expect(queueItem.status).toBe("captured");
  });

  it("a retried complete with the same capture_id is idempotent", async () => {
    const { userId, tokenId } = await seedUserAndToken(
      "rcl_live_complete-retry",
    );
    await seedQueueItem(
      "complete-item-retry",
      userId,
      "https://example.com/retry",
      "claimed",
      tokenId,
    );
    const body = {
      capture_id: "capture-retry",
      captured_at: "2026-07-12T12:00:00.000Z",
    };

    const r1 = await handleCompleteQueueItem(
      authedRequest(
        "POST",
        "/queue/complete-item-retry/complete",
        "rcl_live_complete-retry",
        body,
      ),
      env,
      testCtx,
      "complete-item-retry",
    );
    // Second attempt: the queue item is now 'captured' (a terminal state),
    // which is exactly why the retry short-circuit has to happen before
    // the queue-item-status check -- otherwise this would incorrectly 410.
    const r2 = await handleCompleteQueueItem(
      authedRequest(
        "POST",
        "/queue/complete-item-retry/complete",
        "rcl_live_complete-retry",
        body,
      ),
      env,
      testCtx,
      "complete-item-retry",
    );
    expect(r1.status).toBe(201);
    expect(r2.status).toBe(204);

    const count = await env.DB.prepare(
      "SELECT count(*) as n FROM pending_captures WHERE id = ?",
    )
      .bind("capture-retry")
      .first();
    expect(count).toEqual({ n: 1 });
  });

  it("returns 409 when the item is not claimed by this device", async () => {
    const { userId } = await seedUserAndToken("rcl_live_complete-wrong-1");
    const otherTokenId = await seedToken(userId, "rcl_live_complete-wrong-2");
    await seedQueueItem(
      "complete-item-wrong",
      userId,
      "https://example.com/wrong",
      "claimed",
      otherTokenId,
    );

    const response = await handleCompleteQueueItem(
      authedRequest(
        "POST",
        "/queue/complete-item-wrong/complete",
        "rcl_live_complete-wrong-1",
        { capture_id: "capture-wrong", captured_at: "2026-07-12T12:00:00Z" },
      ),
      env,
      testCtx,
      "complete-item-wrong",
    );
    expect(response.status).toBe(409);
  });

  it("returns 409 when the item is still pending (never claimed)", async () => {
    const { userId } = await seedUserAndToken("rcl_live_complete-pending");
    await seedQueueItem(
      "complete-item-pending",
      userId,
      "https://example.com/pending",
      "pending",
      null,
    );

    const response = await handleCompleteQueueItem(
      authedRequest(
        "POST",
        "/queue/complete-item-pending/complete",
        "rcl_live_complete-pending",
        {
          capture_id: "capture-pending",
          captured_at: "2026-07-12T12:00:00Z",
        },
      ),
      env,
      testCtx,
      "complete-item-pending",
    );
    expect(response.status).toBe(409);
  });

  it("returns 410 for an item already captured under a different capture_id", async () => {
    const { userId, tokenId } = await seedUserAndToken(
      "rcl_live_complete-terminal",
    );
    await seedQueueItem(
      "complete-item-terminal",
      userId,
      "https://example.com/terminal",
      "captured",
      tokenId,
    );

    const response = await handleCompleteQueueItem(
      authedRequest(
        "POST",
        "/queue/complete-item-terminal/complete",
        "rcl_live_complete-terminal",
        {
          capture_id: "brand-new-capture-id",
          captured_at: "2026-07-12T12:00:00Z",
        },
      ),
      env,
      testCtx,
      "complete-item-terminal",
    );
    expect(response.status).toBe(410);
  });

  it("returns 404 for a nonexistent item id", async () => {
    await seedUserAndToken("rcl_live_complete-404");
    const response = await handleCompleteQueueItem(
      authedRequest(
        "POST",
        "/queue/does-not-exist/complete",
        "rcl_live_complete-404",
        { capture_id: "x", captured_at: "2026-07-12T12:00:00Z" },
      ),
      env,
      testCtx,
      "does-not-exist",
    );
    expect(response.status).toBe(404);
  });

  it("rejects an unknown bearer token", async () => {
    const response = await handleCompleteQueueItem(
      authedRequest("POST", "/queue/x/complete", "not-a-real-token", {
        capture_id: "x",
        captured_at: "2026-07-12T12:00:00Z",
      }),
      env,
      testCtx,
      "x",
    );
    expect(response.status).toBe(401);
  });

  it.each([
    ["missing capture_id", { captured_at: "2026-07-12T12:00:00Z" }],
    ["missing captured_at", { capture_id: "x" }],
  ])("rejects: %s", async (name, body) => {
    const rawToken = `rcl_live_complete-invalid-${name.replace(/\s+/g, "-")}`;
    await seedUserAndToken(rawToken);
    const response = await handleCompleteQueueItem(
      authedRequest("POST", "/queue/some-item/complete", rawToken, body),
      env,
      testCtx,
      "some-item",
    );
    expect(response.status).toBe(400);
  });
});

describe("handleFailQueueItem", () => {
  it("marks a claimed item failed", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_fail-1");
    await seedQueueItem(
      "fail-item-1",
      userId,
      "https://example.com/fail1",
      "claimed",
      tokenId,
    );

    const response = await handleFailQueueItem(
      authedRequest("POST", "/queue/fail-item-1/fail", "rcl_live_fail-1"),
      env,
      testCtx,
      "fail-item-1",
    );
    expect(response.status).toBe(204);

    const row = await env.DB.prepare(
      "SELECT status FROM queue_items WHERE id = ?",
    )
      .bind("fail-item-1")
      .first();
    expect(row.status).toBe("failed");
  });

  it("returns 409 when not claimed by this device", async () => {
    const { userId } = await seedUserAndToken("rcl_live_fail-wrong-1");
    const otherTokenId = await seedToken(userId, "rcl_live_fail-wrong-2");
    await seedQueueItem(
      "fail-item-wrong",
      userId,
      "https://example.com/fail-wrong",
      "claimed",
      otherTokenId,
    );

    const response = await handleFailQueueItem(
      authedRequest(
        "POST",
        "/queue/fail-item-wrong/fail",
        "rcl_live_fail-wrong-1",
      ),
      env,
      testCtx,
      "fail-item-wrong",
    );
    expect(response.status).toBe(409);
  });

  it("returns 410 for an already-terminal item", async () => {
    const { userId, tokenId } = await seedUserAndToken(
      "rcl_live_fail-terminal",
    );
    await seedQueueItem(
      "fail-item-terminal",
      userId,
      "https://example.com/fail-terminal",
      "failed",
      tokenId,
    );

    const response = await handleFailQueueItem(
      authedRequest(
        "POST",
        "/queue/fail-item-terminal/fail",
        "rcl_live_fail-terminal",
      ),
      env,
      testCtx,
      "fail-item-terminal",
    );
    expect(response.status).toBe(410);
  });

  it("returns 404 for a nonexistent item id", async () => {
    await seedUserAndToken("rcl_live_fail-404");
    const response = await handleFailQueueItem(
      authedRequest("POST", "/queue/does-not-exist/fail", "rcl_live_fail-404"),
      env,
      testCtx,
      "does-not-exist",
    );
    expect(response.status).toBe(404);
  });

  it("rejects an unknown bearer token", async () => {
    const response = await handleFailQueueItem(
      authedRequest("POST", "/queue/x/fail", "not-a-real-token"),
      env,
      testCtx,
      "x",
    );
    expect(response.status).toBe(401);
  });
});
