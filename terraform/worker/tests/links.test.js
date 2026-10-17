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
import {
  handleClaimPendingLinks,
  handleCleanupPendingLinks,
  handleCompleteQueueItemAsLink,
  handleGetLinkUploadUrls,
  handleMarkPendingLinkFetched,
  handleSaveLink,
  LINK_FAVICON_INLINE_MAX_BYTES,
} from "../index.js";
import { sha256Hex } from "./test-helpers.js";

// D1 state is shared across test cases within this file, so user, token and
// link ids are all unique across it.
let nextUserId = 1;

// Idempotent per raw token, so table-driven cases can share one device.
const seededDevices = new Map();

async function seedUserAndToken(rawToken) {
  const existing = seededDevices.get(rawToken);
  if (existing) return existing;
  const userId = nextUserId++;
  await env.DB.prepare(
    "INSERT INTO users (id, pairing_token_hash) VALUES (?, NULL)",
  )
    .bind(userId)
    .run();
  const inserted = await env.DB.prepare(
    `INSERT INTO tokens (token_hash, user_id, device_name, device_type)
     VALUES (?, ?, 'test-device', 'extension') RETURNING id`,
  )
    .bind(await sha256Hex(rawToken), userId)
    .first();
  const device = { userId, tokenId: inserted.id };
  seededDevices.set(rawToken, device);
  return device;
}

// A second device for a user who already has one.
async function seedExtraToken(userId, rawToken) {
  const inserted = await env.DB.prepare(
    `INSERT INTO tokens (token_hash, user_id, device_name, device_type)
     VALUES (?, ?, 'other-device', 'extension') RETURNING id`,
  )
    .bind(await sha256Hex(rawToken), userId)
    .first();
  return inserted.id;
}

async function seedQueueItem(id, userId, status, claimedByTokenId) {
  await env.DB.prepare(
    `INSERT INTO queue_items (id, user_id, url, status, claimed_by_token_id)
     VALUES (?, ?, ?, ?, ?)`,
  )
    .bind(
      id,
      userId,
      `https://example.com/${id}`,
      status,
      claimedByTokenId ?? null,
    )
    .run();
}

// Seeds a pending_links row directly. claimedAgo is a raw SQLite datetime
// modifier (e.g. "-90 minutes") applied by SQLite itself, for the same reason
// captures.test.js does it: the claim and retention windows compare against
// datetime('now', ...), so the offsets have to be computed there.
async function seedPendingLink(
  id,
  userId,
  {
    url,
    title = null,
    faviconExt = null,
    faviconData = null,
    r2KeyFavicon = null,
    savedAt = "2026-07-12T12:00:00.000Z",
    fetchedByBackend = 0,
    claimedAgo = null,
  } = {},
) {
  await env.DB.prepare(
    `INSERT INTO pending_links
       (id, user_id, url, title, favicon_ext, favicon_data, r2_key_favicon,
        saved_at, fetched_by_backend, claimed_at)
     VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9,
             CASE WHEN ?10 IS NULL THEN NULL ELSE datetime('now', ?10) END)`,
  )
    .bind(
      id,
      userId,
      url ?? `https://example.com/${id}`,
      title,
      faviconExt,
      faviconData,
      r2KeyFavicon,
      savedAt,
      fetchedByBackend,
      claimedAgo,
    )
    .run();
}

function serviceRequest(method, path) {
  return new Request(`https://example.com${path}`, {
    method,
    headers: { "X-Service-Key": "test-service-secret" },
  });
}

function authedRequest(method, path, rawToken, body) {
  const init = { method, headers: { Authorization: `Bearer ${rawToken}` } };
  if (body !== undefined) init.body = JSON.stringify(body);
  return new Request(`https://example.com${path}`, init);
}

const testCtx = { waitUntil: () => {} };
const linksOff = { ...env, LINKS_ENABLED: "false" };

// ASCII, so btoa is exact: n raw bytes become 4*ceil(n/3) characters of base64.
const faviconOfSize = (n) => btoa("a".repeat(n));

function saveBody(overrides = {}) {
  return {
    link_id: "unset",
    url: "https://example.com/a-page",
    title: "A page",
    saved_at: "2026-07-12T12:00:00.000Z",
    ...overrides,
  };
}

async function save(rawToken, body, e = env) {
  return handleSaveLink(
    authedRequest("POST", "/links", rawToken, body),
    e,
    testCtx,
  );
}

async function completeAsLink(rawToken, itemId, body, e = env) {
  return handleCompleteQueueItemAsLink(
    authedRequest("POST", `/queue/${itemId}/complete-link`, rawToken, body),
    e,
    testCtx,
    itemId,
  );
}

async function row(table, id) {
  return env.DB.prepare(`SELECT * FROM ${table} WHERE id = ?`).bind(id).first();
}

describe("handleSaveLink (POST /links, a direct save)", () => {
  it("saves a link with an inline favicon, parking the bytes in the row", async () => {
    const { userId } = await seedUserAndToken("rcl_live_link_inline");
    const data = faviconOfSize(2048);

    const res = await save(
      "rcl_live_link_inline",
      saveBody({
        link_id: "link-inline",
        favicon_ext: "png",
        favicon_data: data,
      }),
    );
    expect(res.status).toBe(201);
    expect(await res.json()).toEqual({
      id: "link-inline",
      r2_key_favicon: null,
    });

    const stored = await row("pending_links", "link-inline");
    expect(stored).toMatchObject({
      user_id: userId,
      queue_item_id: null,
      url: "https://example.com/a-page",
      title: "A page",
      favicon_ext: "png",
      favicon_data: data,
      r2_key_favicon: null,
      saved_at: "2026-07-12T12:00:00.000Z",
      fetched_by_backend: 0,
      claimed_at: null,
    });
  });

  it("saves a link with no favicon", async () => {
    await seedUserAndToken("rcl_live_link_bare");
    const res = await save(
      "rcl_live_link_bare",
      saveBody({ link_id: "link-bare" }),
    );
    expect(res.status).toBe(201);
    expect(await res.json()).toEqual({ id: "link-bare", r2_key_favicon: null });

    const stored = await row("pending_links", "link-bare");
    expect(stored.favicon_ext).toBeNull();
    expect(stored.favicon_data).toBeNull();
    expect(stored.r2_key_favicon).toBeNull();
  });

  it("records a favicon already uploaded to R2 by a recomputed key, never a client's", async () => {
    const { userId } = await seedUserAndToken("rcl_live_link_r2");
    const res = await save(
      "rcl_live_link_r2",
      saveBody({
        link_id: "link-r2",
        favicon_ext: "ico",
        // A client trying to point the row at someone else's object.
        r2_key_favicon: "pending/999/elsewhere/page.html",
      }),
    );
    expect(res.status).toBe(201);
    const key = `pending-links/${userId}/link-r2/favicon.ico`;
    expect(await res.json()).toEqual({ id: "link-r2", r2_key_favicon: key });

    const stored = await row("pending_links", "link-r2");
    expect(stored.favicon_ext).toBe("ico");
    expect(stored.favicon_data).toBeNull();
    expect(stored.r2_key_favicon).toBe(key);
  });

  it("accepts an inline favicon of exactly the cap, and refuses one byte more with 413", async () => {
    await seedUserAndToken("rcl_live_link_cap");

    const atCap = await save(
      "rcl_live_link_cap",
      saveBody({
        link_id: "link-at-cap",
        favicon_ext: "png",
        favicon_data: faviconOfSize(LINK_FAVICON_INLINE_MAX_BYTES),
      }),
    );
    expect(atCap.status).toBe(201);

    const overCap = await save(
      "rcl_live_link_cap",
      saveBody({
        link_id: "link-over-cap",
        favicon_ext: "png",
        favicon_data: faviconOfSize(LINK_FAVICON_INLINE_MAX_BYTES + 1),
      }),
    );
    expect(overCap.status).toBe(413);
    expect(await overCap.text()).toContain("/links/upload-urls");
    expect(await row("pending_links", "link-over-cap")).toBeNull();
  });

  it("refuses a request whose declared body is far larger than any inline favicon", async () => {
    await seedUserAndToken("rcl_live_link_huge");
    const res = await handleSaveLink(
      new Request("https://example.com/links", {
        method: "POST",
        headers: {
          Authorization: "Bearer rcl_live_link_huge",
          "Content-Length": String(5 * 1024 * 1024),
        },
        body: JSON.stringify(saveBody({ link_id: "link-huge" })),
      }),
      env,
      testCtx,
    );
    expect(res.status).toBe(413);
    expect(await row("pending_links", "link-huge")).toBeNull();
  });

  it.each([
    ["no link_id", { link_id: undefined }],
    ["an empty link_id", { link_id: "" }],
    ["no url", { url: undefined }],
    ["a url that isn't a url", { url: "not a url" }],
    ["a non-string url", { url: 42 }],
    ["no saved_at", { saved_at: undefined }],
    ["a saved_at that isn't a timestamp", { saved_at: "yesterday" }],
    ["a bare-number saved_at", { saved_at: "1" }],
    [
      "a saved_at that looks like a timestamp but isn't",
      { saved_at: "2026-13-45T99:99:99Z" },
    ],
    ["a non-string title", { title: 42 }],
    [
      "an unknown favicon_ext",
      { favicon_ext: "gif", favicon_data: faviconOfSize(8) },
    ],
    ["favicon_data without favicon_ext", { favicon_data: faviconOfSize(8) }],
    [
      "favicon_data that isn't base64",
      { favicon_ext: "png", favicon_data: "***not base64***" },
    ],
    [
      "favicon_data with broken padding",
      { favicon_ext: "png", favicon_data: "abc" },
    ],
    ["a non-string favicon_data", { favicon_ext: "png", favicon_data: 1234 }],
  ])("rejects %s with 400, writing nothing", async (name, overrides) => {
    await seedUserAndToken("rcl_live_link_invalid");
    const id = `link-invalid-${name.replace(/\W+/g, "-")}`;
    const res = await save(
      "rcl_live_link_invalid",
      saveBody({ link_id: id, ...overrides }),
    );
    expect(res.status).toBe(400);
    expect(await row("pending_links", id)).toBeNull();
  });

  it("rejects a body that isn't JSON, and one that isn't an object", async () => {
    await seedUserAndToken("rcl_live_link_badjson");
    for (const body of ["{nope", "null"]) {
      const res = await handleSaveLink(
        new Request("https://example.com/links", {
          method: "POST",
          headers: { Authorization: "Bearer rcl_live_link_badjson" },
          body,
        }),
        env,
        testCtx,
      );
      expect(res.status).toBe(400);
    }
  });

  it("accepts any parseable URL, as enqueue does: the scheme and length are not the Worker's call", async () => {
    await seedUserAndToken("rcl_live_link_schemes");
    const longUrl = `https://example.com/${"a".repeat(9000)}`;
    for (const [id, url] of [
      ["link-ftp", "ftp://example.com/file"],
      ["link-chrome", "chrome://settings"],
      ["link-long-url", longUrl],
    ]) {
      const res = await save(
        "rcl_live_link_schemes",
        saveBody({ link_id: id, url }),
      );
      expect(res.status).toBe(201);
      expect((await row("pending_links", id)).url).toBe(url);
    }
  });

  it("normalizes saved_at to UTC ISO-8601, so ordering by it is ordering by time", async () => {
    await seedUserAndToken("rcl_live_link_tz");
    const res = await save(
      "rcl_live_link_tz",
      saveBody({ link_id: "link-tz", saved_at: "2026-07-12T14:00:00+02:00" }),
    );
    expect(res.status).toBe(201);
    expect((await row("pending_links", "link-tz")).saved_at).toBe(
      "2026-07-12T12:00:00.000Z",
    );
  });

  it("stores a title as given, including an empty one: turning that into NULL is ingestion's job", async () => {
    await seedUserAndToken("rcl_live_link_title");
    for (const [id, title, expected] of [
      ["link-title-plain", "Hello, world", "Hello, world"],
      ["link-title-empty", "", ""],
      ["link-title-spaces", "   ", "   "],
      ["link-title-null", null, null],
    ]) {
      await save("rcl_live_link_title", saveBody({ link_id: id, title }));
      expect((await row("pending_links", id)).title).toBe(expected);
    }
  });

  it("cuts a title past the defensive cap rather than refusing the save", async () => {
    await seedUserAndToken("rcl_live_link_title_cap");
    await save(
      "rcl_live_link_title_cap",
      saveBody({ link_id: "link-title-at-cap", title: "t".repeat(4096) }),
    );
    expect(
      (await row("pending_links", "link-title-at-cap")).title,
    ).toHaveLength(4096);

    const res = await save(
      "rcl_live_link_title_cap",
      saveBody({ link_id: "link-title-over-cap", title: "t".repeat(6000) }),
    );
    expect(res.status).toBe(201);
    expect(
      (await row("pending_links", "link-title-over-cap")).title,
    ).toHaveLength(4096);
  });

  it("doesn't leave half an emoji at the end of a cut title", async () => {
    await seedUserAndToken("rcl_live_link_title_emoji");
    // "😀" is a surrogate pair (2 UTF-16 units); this puts its first half at
    // the very last position the cap would keep.
    const title = `${"t".repeat(4095)}😀`;
    await save(
      "rcl_live_link_title_emoji",
      saveBody({ link_id: "link-title-emoji", title }),
    );
    const stored = (await row("pending_links", "link-title-emoji")).title;
    expect(stored).toBe("t".repeat(4095));
  });

  it("is idempotent: a retry with the same link_id is a 204 and writes no second row", async () => {
    await seedUserAndToken("rcl_live_link_retry");
    const body = saveBody({ link_id: "link-retry" });
    expect((await save("rcl_live_link_retry", body)).status).toBe(201);
    expect((await save("rcl_live_link_retry", body)).status).toBe(204);

    const { results } = await env.DB.prepare(
      "SELECT id FROM pending_links WHERE id = 'link-retry'",
    ).all();
    expect(results).toHaveLength(1);
  });

  it("requires a bearer token", async () => {
    const res = await handleSaveLink(
      new Request("https://example.com/links", {
        method: "POST",
        body: JSON.stringify(saveBody({ link_id: "link-noauth" })),
      }),
      env,
      testCtx,
    );
    expect(res.status).toBe(401);
  });

  it("is a 404 while links are off, before it even asks who is calling", async () => {
    await seedUserAndToken("rcl_live_link_off");
    const authed = await save(
      "rcl_live_link_off",
      saveBody({ link_id: "link-off" }),
      linksOff,
    );
    expect(authed.status).toBe(404);

    const anonymous = await handleSaveLink(
      new Request("https://example.com/links", { method: "POST", body: "{}" }),
      linksOff,
      testCtx,
    );
    expect(anonymous.status).toBe(404);
    expect(await row("pending_links", "link-off")).toBeNull();
  });
});

describe("handleCompleteQueueItemAsLink (POST /queue/:id/complete-link)", () => {
  // The body is a direct save's minus url: that comes from the queue item.
  function linkBody(overrides = {}) {
    const body = saveBody(overrides);
    delete body.url;
    return body;
  }

  it("records the link and marks the claimed item captured, in the same batch", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_cl_ok");
    await seedQueueItem("q-cl-ok", userId, "claimed", tokenId);

    const res = await completeAsLink(
      "rcl_live_cl_ok",
      "q-cl-ok",
      linkBody({
        link_id: "link-cl-ok",
        favicon_ext: "png",
        favicon_data: faviconOfSize(512),
      }),
    );
    expect(res.status).toBe(201);
    expect(await res.json()).toEqual({
      id: "link-cl-ok",
      r2_key_favicon: null,
    });

    expect(await row("pending_links", "link-cl-ok")).toMatchObject({
      user_id: userId,
      queue_item_id: "q-cl-ok",
      title: "A page",
      favicon_ext: "png",
    });
    expect((await row("queue_items", "q-cl-ok")).status).toBe("captured");
  });

  it("takes the URL from the queue item, never the body: a page and a link saved from one item share it even if it redirects", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_cl_url");
    await seedQueueItem("q-cl-url", userId, "claimed", tokenId);

    const res = await completeAsLink(
      "rcl_live_cl_url",
      "q-cl-url",
      // Even a body that tries to supply its own.
      {
        ...linkBody({ link_id: "link-cl-url" }),
        url: "https://example.com/after-the-redirect",
      },
    );
    expect(res.status).toBe(201);
    expect((await row("pending_links", "link-cl-url")).url).toBe(
      "https://example.com/q-cl-url",
    );
  });

  it("records an R2 favicon by a recomputed key", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_cl_r2");
    await seedQueueItem("q-cl-r2", userId, "claimed", tokenId);
    const res = await completeAsLink(
      "rcl_live_cl_r2",
      "q-cl-r2",
      linkBody({ link_id: "link-cl-r2", favicon_ext: "svg" }),
    );
    expect(res.status).toBe(201);
    expect((await row("pending_links", "link-cl-r2")).r2_key_favicon).toBe(
      `pending-links/${userId}/link-cl-r2/favicon.svg`,
    );
  });

  it("a retry after the item flipped to captured is still a 204, not a 410", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_cl_retry");
    await seedQueueItem("q-cl-retry", userId, "claimed", tokenId);
    const body = linkBody({ link_id: "link-cl-retry" });
    expect(
      (await completeAsLink("rcl_live_cl_retry", "q-cl-retry", body)).status,
    ).toBe(201);
    expect(
      (await completeAsLink("rcl_live_cl_retry", "q-cl-retry", body)).status,
    ).toBe(204);
  });

  it("404s an item that doesn't exist, or belongs to another user", async () => {
    const owner = await seedUserAndToken("rcl_live_cl_404_owner");
    await seedQueueItem("q-cl-theirs", owner.userId, "claimed", owner.tokenId);
    await seedUserAndToken("rcl_live_cl_404");

    for (const [id, itemId] of [
      ["link-cl-missing", "q-does-not-exist"],
      ["link-cl-theirs", "q-cl-theirs"],
    ]) {
      const res = await completeAsLink(
        "rcl_live_cl_404",
        itemId,
        linkBody({ link_id: id }),
      );
      expect(res.status).toBe(404);
      expect(await row("pending_links", id)).toBeNull();
    }
  });

  it("410s an item already captured or failed", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_cl_410");
    await seedQueueItem("q-cl-done", userId, "captured", tokenId);
    await seedQueueItem("q-cl-failed", userId, "failed", tokenId);
    for (const itemId of ["q-cl-done", "q-cl-failed"]) {
      const id = `link-${itemId}`;
      const res = await completeAsLink(
        "rcl_live_cl_410",
        itemId,
        linkBody({ link_id: id }),
      );
      expect(res.status).toBe(410);
      expect(await row("pending_links", id)).toBeNull();
    }
  });

  it("409s an item that is unclaimed, or claimed by a different device", async () => {
    const { userId } = await seedUserAndToken("rcl_live_cl_409");
    const otherDevice = await seedExtraToken(userId, "rcl_live_cl_409_b");
    await seedQueueItem("q-cl-pending", userId, "pending", null);
    await seedQueueItem("q-cl-other-device", userId, "claimed", otherDevice);
    for (const itemId of ["q-cl-pending", "q-cl-other-device"]) {
      const id = `link-${itemId}`;
      const res = await completeAsLink(
        "rcl_live_cl_409",
        itemId,
        linkBody({ link_id: id }),
      );
      expect(res.status).toBe(409);
      expect(await row("pending_links", id)).toBeNull();
      expect((await row("queue_items", itemId)).status).not.toBe("captured");
    }
  });

  it("validates the shared fields the way a direct save does, leaving the item claimed", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_cl_invalid");
    await seedQueueItem("q-cl-invalid", userId, "claimed", tokenId);
    for (const overrides of [
      { saved_at: "yesterday" },
      { link_id: "" },
      { favicon_ext: "gif" },
      {
        favicon_ext: "png",
        favicon_data: faviconOfSize(LINK_FAVICON_INLINE_MAX_BYTES + 1),
      },
    ]) {
      const res = await completeAsLink(
        "rcl_live_cl_invalid",
        "q-cl-invalid",
        linkBody({ link_id: "link-cl-invalid", ...overrides }),
      );
      expect([400, 413]).toContain(res.status);
    }
    expect(await row("pending_links", "link-cl-invalid")).toBeNull();
    expect((await row("queue_items", "q-cl-invalid")).status).toBe("claimed");
  });

  it("requires a bearer token", async () => {
    const res = await handleCompleteQueueItemAsLink(
      new Request("https://example.com/queue/q/complete-link", {
        method: "POST",
        body: JSON.stringify(linkBody({ link_id: "link-cl-noauth" })),
      }),
      env,
      testCtx,
      "q",
    );
    expect(res.status).toBe(401);
  });

  it("is a 404 while links are off, leaving the queue item alone", async () => {
    const { userId, tokenId } = await seedUserAndToken("rcl_live_cl_off");
    await seedQueueItem("q-cl-off", userId, "claimed", tokenId);
    const res = await completeAsLink(
      "rcl_live_cl_off",
      "q-cl-off",
      linkBody({ link_id: "link-cl-off" }),
      linksOff,
    );
    expect(res.status).toBe(404);
    expect((await row("queue_items", "q-cl-off")).status).toBe("claimed");
  });
});

describe("handleGetLinkUploadUrls", () => {
  const FAKE_SHA256 = "c".repeat(64);

  // Mirrors index.js's private hexToBase64 (see captures.test.js).
  function hexToBase64(hexDigest) {
    const bytes = new Uint8Array(hexDigest.length / 2);
    for (let i = 0; i < bytes.length; i++) {
      bytes[i] = parseInt(hexDigest.slice(i * 2, i * 2 + 2), 16);
    }
    let binary = "";
    for (const byte of bytes) binary += String.fromCharCode(byte);
    return btoa(binary);
  }

  const upload = (rawToken, body, e = env) =>
    handleGetLinkUploadUrls(
      authedRequest("POST", "/links/upload-urls", rawToken, body),
      e,
      testCtx,
    );

  it("issues a presigned PUT for the favicon under a recomputed, links-only key", async () => {
    const { userId } = await seedUserAndToken("rcl_live_link_up_ok");
    const res = await upload("rcl_live_link_up_ok", {
      link_id: "link-up",
      favicon_ext: "svg",
      content_sha256_favicon: FAKE_SHA256,
    });
    expect(res.status).toBe(200);
    const body = await res.json();

    const key = `pending-links/${userId}/link-up/favicon.svg`;
    expect(body.link_id).toBe("link-up");
    expect(body.r2_key_favicon).toBe(key);
    expect(new URL(body.upload_url_favicon).pathname).toBe(
      `/test-bucket/${key}`,
    );
    expect(body.required_headers_favicon).toEqual({
      "x-amz-checksum-sha256": hexToBase64(FAKE_SHA256),
    });
    expect(body.expires_in_seconds).toBeGreaterThan(0);
  });

  it.each([
    ["no link_id", { favicon_ext: "png", content_sha256_favicon: FAKE_SHA256 }],
    ["no favicon_ext", { link_id: "x", content_sha256_favicon: FAKE_SHA256 }],
    [
      "an unknown favicon_ext",
      { link_id: "x", favicon_ext: "gif", content_sha256_favicon: FAKE_SHA256 },
    ],
    ["no checksum", { link_id: "x", favicon_ext: "png" }],
    [
      "a malformed checksum",
      { link_id: "x", favicon_ext: "png", content_sha256_favicon: "nothex" },
    ],
  ])("rejects %s with 400", async (_name, body) => {
    await seedUserAndToken("rcl_live_link_up_bad");
    const res = await upload("rcl_live_link_up_bad", body);
    expect(res.status).toBe(400);
  });

  it("requires a bearer token", async () => {
    const res = await handleGetLinkUploadUrls(
      new Request("https://example.com/links/upload-urls", {
        method: "POST",
        body: JSON.stringify({}),
      }),
      env,
      testCtx,
    );
    expect(res.status).toBe(401);
  });

  it("is a 404 while links are off", async () => {
    await seedUserAndToken("rcl_live_link_up_off");
    const res = await upload(
      "rcl_live_link_up_off",
      { link_id: "x", favicon_ext: "png", content_sha256_favicon: FAKE_SHA256 },
      linksOff,
    );
    expect(res.status).toBe(404);
  });

  it("fails loudly when R2 credentials aren't configured", async () => {
    await seedUserAndToken("rcl_live_link_up_nocreds");
    const res = await upload(
      "rcl_live_link_up_nocreds",
      { link_id: "x", favicon_ext: "png", content_sha256_favicon: FAKE_SHA256 },
      { ...env, R2_ACCESS_KEY_SECRET: "" },
    );
    expect(res.status).toBe(500);
  });
});

describe("handleClaimPendingLinks", () => {
  // Rows from other tests in this file may still be unclaimed; each test here
  // therefore looks only for its own ids rather than asserting the whole batch.
  async function claim(path = "/internal/pending-links", e = env) {
    return handleClaimPendingLinks(serviceRequest("POST", path), e);
  }
  const ids = (body) => body.pending_links.map((r) => r.id);

  it("requires the service key", async () => {
    const res = await handleClaimPendingLinks(
      new Request("https://example.com/internal/pending-links", {
        method: "POST",
      }),
      env,
    );
    expect(res.status).toBe(401);
  });

  it("claims unfetched rows, oldest SAVED first, and returns the favicon payload", async () => {
    const { userId } = await seedUserAndToken("rcl_live_claim_order");
    // Inserted newest-saved first, so insertion order and saved order disagree.
    await seedPendingLink("claim-order-b", userId, {
      savedAt: "2026-07-12T12:05:00.000Z",
      faviconExt: "png",
      faviconData: "AAAA",
    });
    await seedPendingLink("claim-order-a", userId, {
      savedAt: "2026-07-12T12:00:00.000Z",
      title: "First",
    });

    const body = await (
      await claim("/internal/pending-links?limit=100")
    ).json();
    const mine = body.pending_links.filter((r) =>
      r.id.startsWith("claim-order-"),
    );
    expect(mine.map((r) => r.id)).toEqual(["claim-order-a", "claim-order-b"]);

    const b = mine[1];
    expect(b).toMatchObject({
      user_id: userId,
      queue_item_id: null,
      favicon_ext: "png",
      favicon_data: "AAAA",
      r2_key_favicon: null,
      saved_at: "2026-07-12T12:05:00.000Z",
    });
    expect(b.claimed_at).not.toBeNull();
    expect(mine[0].title).toBe("First");
  });

  it("claims a row only once: a second claim doesn't return it", async () => {
    const { userId } = await seedUserAndToken("rcl_live_claim_once");
    await seedPendingLink("claim-once", userId);

    const first = await (
      await claim("/internal/pending-links?limit=100")
    ).json();
    expect(ids(first)).toContain("claim-once");
    const second = await (
      await claim("/internal/pending-links?limit=100")
    ).json();
    expect(ids(second)).not.toContain("claim-once");
  });

  it("takes over a claim older than the timeout, but not a fresh one", async () => {
    const { userId } = await seedUserAndToken("rcl_live_claim_timeout");
    await seedPendingLink("claim-stale", userId, { claimedAgo: "-90 minutes" });
    await seedPendingLink("claim-fresh", userId, { claimedAgo: "-10 minutes" });

    const body = await (
      await claim("/internal/pending-links?limit=100")
    ).json();
    expect(ids(body)).toContain("claim-stale");
    expect(ids(body)).not.toContain("claim-fresh");
  });

  it("never returns a row the backend already fetched", async () => {
    const { userId } = await seedUserAndToken("rcl_live_claim_fetched");
    await seedPendingLink("claim-fetched", userId, { fetchedByBackend: 1 });
    const body = await (
      await claim("/internal/pending-links?limit=100")
    ).json();
    expect(ids(body)).not.toContain("claim-fetched");
  });

  it("honors limit, and rejects one that is out of range or not a number", async () => {
    const { userId } = await seedUserAndToken("rcl_live_claim_limit");
    await seedPendingLink("claim-limit-1", userId);
    await seedPendingLink("claim-limit-2", userId);

    const limited = await (
      await claim("/internal/pending-links?limit=1")
    ).json();
    expect(limited.pending_links).toHaveLength(1);

    for (const bad of ["0", "101", "-1", "abc", "1.5"]) {
      const res = await claim(`/internal/pending-links?limit=${bad}`);
      expect(res.status).toBe(400);
    }
  });

  it("still works while links are off: there is just nothing to claim", async () => {
    const res = await claim("/internal/pending-links", linksOff);
    expect(res.status).toBe(200);
    expect(await res.json()).toHaveProperty("pending_links");
  });
});

describe("handleMarkPendingLinkFetched", () => {
  const mark = (id) =>
    handleMarkPendingLinkFetched(
      serviceRequest("POST", `/internal/pending-links/${id}/fetched`),
      env,
      id,
    );

  it("requires the service key", async () => {
    const res = await handleMarkPendingLinkFetched(
      new Request("https://example.com/x", { method: "POST" }),
      env,
      "whatever",
    );
    expect(res.status).toBe(401);
  });

  it("flags the row fetched and drops an inline favicon's bytes", async () => {
    const { userId } = await seedUserAndToken("rcl_live_fetched_inline");
    await seedPendingLink("fetched-inline", userId, {
      faviconExt: "png",
      faviconData: faviconOfSize(1024),
    });

    expect((await mark("fetched-inline")).status).toBe(204);
    const stored = await row("pending_links", "fetched-inline");
    expect(stored.fetched_by_backend).toBe(1);
    expect(stored.favicon_ext).toBeNull();
    expect(stored.favicon_data).toBeNull();
    expect(stored.r2_key_favicon).toBeNull();
  });

  it("drops an R2 favicon's key too", async () => {
    const { userId } = await seedUserAndToken("rcl_live_fetched_r2");
    await seedPendingLink("fetched-r2", userId, {
      faviconExt: "svg",
      r2KeyFavicon: `pending-links/${userId}/fetched-r2/favicon.svg`,
    });
    expect((await mark("fetched-r2")).status).toBe(204);
    expect(
      (await row("pending_links", "fetched-r2")).r2_key_favicon,
    ).toBeNull();
  });

  it("works on a row with no favicon, and again on a retry", async () => {
    const { userId } = await seedUserAndToken("rcl_live_fetched_bare");
    await seedPendingLink("fetched-bare", userId);
    expect((await mark("fetched-bare")).status).toBe(204);
    expect((await mark("fetched-bare")).status).toBe(204);
  });

  it("404s an unknown id", async () => {
    expect((await mark("no-such-link")).status).toBe(404);
  });
});

describe("handleCleanupPendingLinks", () => {
  const cleanup = () =>
    handleCleanupPendingLinks(
      serviceRequest("POST", "/internal/pending-links/cleanup"),
      env,
    );

  it("requires the service key", async () => {
    const res = await handleCleanupPendingLinks(
      new Request("https://example.com/x", { method: "POST" }),
      env,
    );
    expect(res.status).toBe(401);
  });

  it("deletes only fetched rows past the retention window", async () => {
    const { userId } = await seedUserAndToken("rcl_live_cleanup");
    await seedPendingLink("cleanup-old-fetched", userId, {
      fetchedByBackend: 1,
      claimedAgo: "-100 hours",
    });
    await seedPendingLink("cleanup-recent-fetched", userId, {
      fetchedByBackend: 1,
      claimedAgo: "-1 hours",
    });
    await seedPendingLink("cleanup-old-unfetched", userId, {
      fetchedByBackend: 0,
      claimedAgo: "-100 hours",
    });

    const res = await cleanup();
    expect(res.status).toBe(200);
    expect((await res.json()).deleted).toBeGreaterThanOrEqual(1);

    expect(await row("pending_links", "cleanup-old-fetched")).toBeNull();
    expect(await row("pending_links", "cleanup-recent-fetched")).not.toBeNull();
    expect(await row("pending_links", "cleanup-old-unfetched")).not.toBeNull();
  });
});

describe("pending_links favicon shape", () => {
  it("refuses an inline-and-R2 favicon, or one with no extension, at the schema", async () => {
    const { userId } = await seedUserAndToken("rcl_live_shape");
    await expect(
      seedPendingLink("shape-both", userId, {
        faviconExt: "png",
        faviconData: "AAAA",
        r2KeyFavicon: "pending-links/1/x/favicon.png",
      }),
    ).rejects.toThrow();
    await expect(
      seedPendingLink("shape-no-ext", userId, { faviconData: "AAAA" }),
    ).rejects.toThrow();
    await expect(
      seedPendingLink("shape-ext-only", userId, { faviconExt: "png" }),
    ).rejects.toThrow();
    await expect(
      seedPendingLink("shape-bad-ext", userId, {
        faviconExt: "gif",
        faviconData: "AAAA",
      }),
    ).rejects.toThrow();
  });
});
