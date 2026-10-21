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

// The links counterpart of archived-pages.test.js, covering the same five
// handlers against archived_links: the backend-facing four (checkpoint, batch
// upsert, id list, batch delete) and the device-facing list, which is also
// gated by the links flag.
import { env } from "cloudflare:workers";
import { describe, expect, it } from "vitest";
import {
  handleDeleteArchivedLinks,
  handleGetArchivedLinksLastSync,
  handleListArchivedLinkIDs,
  handleListArchivedLinks,
  handleMirrorArchivedLinks,
} from "../index.js";
import { sha256Hex } from "./test-helpers.js";

// D1 state is shared across test cases within this file, so every key needs to
// be unique across the whole file, as in archived-pages.test.js.
let nextUserId = 1;
let nextLinkId = 1;

const newLinkId = () => nextLinkId++;

async function seedUser() {
  const userId = nextUserId++;
  await env.DB.prepare(
    "INSERT INTO users (id, pairing_token_hash) VALUES (?, NULL)",
  )
    .bind(userId)
    .run();
  return userId;
}

async function seedArchivedLink(
  linkId,
  userId,
  { rawUrl, title = null, updatedAt } = {},
) {
  await env.DB.prepare(
    `INSERT INTO archived_links (link_id, user_id, raw_url, title, updated_at)
     VALUES (?, ?, ?, ?, ?)`,
  )
    .bind(
      linkId,
      userId,
      rawUrl ?? `https://example.com/link-${linkId}`,
      title,
      updatedAt ?? "2026-07-12T12:00:00.000Z",
    )
    .run();
}

async function archivedLinkRow(linkId) {
  return env.DB.prepare("SELECT * FROM archived_links WHERE link_id = ?")
    .bind(linkId)
    .first();
}

function serviceRequest(method, path, body) {
  const init = { method, headers: { "X-Service-Key": "test-service-secret" } };
  if (body !== undefined) init.body = JSON.stringify(body);
  return new Request(`https://example.com${path}`, init);
}

async function seedToken(userId, rawToken) {
  await env.DB.prepare(
    `INSERT INTO tokens (token_hash, user_id, device_name, device_type)
     VALUES (?, ?, 'test-device', 'extension')`,
  )
    .bind(await sha256Hex(rawToken), userId)
    .run();
}

function authedRequest(rawToken) {
  return new Request("https://example.com/archived-links", {
    method: "GET",
    headers: { Authorization: `Bearer ${rawToken}` },
  });
}

const testCtx = { waitUntil: () => {} };
const linksOff = { ...env, LINKS_ENABLED: "false" };

describe("handleGetArchivedLinksLastSync", () => {
  it("requires the service key", async () => {
    const res = await handleGetArchivedLinksLastSync(
      new Request("https://example.com/internal/archived-links/last-sync"),
      env,
    );
    expect(res.status).toBe(401);
  });

  it("returns the max updated_at across all links, not scoped to one user", async () => {
    const a = await seedUser();
    const b = await seedUser();
    await seedArchivedLink(newLinkId(), a, {
      updatedAt: "2026-07-12T10:00:00.000Z",
    });
    await seedArchivedLink(newLinkId(), b, {
      updatedAt: "2026-08-01T20:00:00.000Z",
    });
    await seedArchivedLink(newLinkId(), a, {
      updatedAt: "2026-07-13T09:00:00.000Z",
    });

    const res = await handleGetArchivedLinksLastSync(
      serviceRequest("GET", "/internal/archived-links/last-sync"),
      env,
    );
    expect(res.status).toBe(200);
    // ISO timestamps sort as strings, and rows other tests in this file added
    // may be later, so "at least" is the property that holds.
    expect((await res.json()).last_sync >= "2026-08-01T20:00:00.000Z").toBe(
      true,
    );
  });

  it("still answers with links off: there is just nothing pushed", async () => {
    const res = await handleGetArchivedLinksLastSync(
      serviceRequest("GET", "/internal/archived-links/last-sync"),
      linksOff,
    );
    expect(res.status).toBe(200);
  });
});

describe("handleMirrorArchivedLinks", () => {
  const entry = (userId, overrides = {}) => ({
    link_id: newLinkId(),
    user_id: userId,
    raw_url: "https://example.com/mirrored#frag",
    title: "A link",
    updated_at: "2026-09-01T12:00:00.000Z",
    ...overrides,
  });

  const mirror = (links, e = env) =>
    handleMirrorArchivedLinks(
      serviceRequest("POST", "/internal/archived-links/mirror", { links }),
      e,
    );

  it("requires the service key", async () => {
    const res = await handleMirrorArchivedLinks(
      new Request("https://example.com/x", { method: "POST", body: "{}" }),
      env,
    );
    expect(res.status).toBe(401);
  });

  it("inserts new links, keeping the URL exactly as sent and updated_at verbatim", async () => {
    const userId = await seedUser();
    const link = entry(userId);

    const res = await mirror([link]);
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ upserted: 1 });

    expect(await archivedLinkRow(link.link_id)).toEqual({
      link_id: link.link_id,
      user_id: userId,
      raw_url: "https://example.com/mirrored#frag",
      title: "A link",
      updated_at: "2026-09-01T12:00:00.000Z",
    });
  });

  it("stores a missing or null title as NULL", async () => {
    const userId = await seedUser();
    const noTitle = entry(userId);
    delete noTitle.title;
    const nullTitle = entry(userId, { title: null });

    expect((await mirror([noTitle, nullTitle])).status).toBe(200);
    expect((await archivedLinkRow(noTitle.link_id)).title).toBeNull();
    expect((await archivedLinkRow(nullTitle.link_id)).title).toBeNull();
  });

  it("updates an existing link in place rather than adding a second row", async () => {
    const userId = await seedUser();
    const link = entry(userId, { title: "Before" });
    await mirror([link]);

    const res = await mirror([
      {
        ...link,
        title: "After",
        raw_url: "https://example.com/new",
        updated_at: "2026-09-02T00:00:00.000Z",
      },
    ]);
    expect(res.status).toBe(200);

    const { results } = await env.DB.prepare(
      "SELECT * FROM archived_links WHERE link_id = ?",
    )
      .bind(link.link_id)
      .all();
    expect(results).toHaveLength(1);
    expect(results[0]).toMatchObject({
      title: "After",
      raw_url: "https://example.com/new",
      updated_at: "2026-09-02T00:00:00.000Z",
    });
  });

  it("applies a whole batch, in order", async () => {
    const userId = await seedUser();
    const links = [entry(userId), entry(userId), entry(userId)];
    expect(await (await mirror(links)).json()).toEqual({ upserted: 3 });
    for (const link of links) {
      expect(await archivedLinkRow(link.link_id)).not.toBeNull();
    }
  });

  it("lands all of a batch or none of it: one bad entry writes nothing", async () => {
    const userId = await seedUser();
    const good = entry(userId);
    const res = await mirror([good, { ...entry(userId), updated_at: 5 }]);
    expect(res.status).toBe(400);
    expect(await archivedLinkRow(good.link_id)).toBeNull();
  });

  it("accepts an empty batch", async () => {
    const res = await mirror([]);
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ upserted: 0 });
  });

  it.each([
    ["a missing link_id", { link_id: undefined }],
    ["a non-numeric link_id", { link_id: "1" }],
    ["a missing user_id", { user_id: undefined }],
    ["a missing raw_url", { raw_url: undefined }],
    ["a non-string raw_url", { raw_url: 5 }],
    ["a missing updated_at", { updated_at: undefined }],
    ["a non-string title", { title: 5 }],
  ])("rejects %s with 400", async (_name, overrides) => {
    const userId = await seedUser();
    const res = await mirror([entry(userId, overrides)]);
    expect(res.status).toBe(400);
  });

  it("rejects a body that isn't JSON, lacks links, or has a non-array", async () => {
    const bad = (body) =>
      handleMirrorArchivedLinks(
        new Request("https://example.com/x", {
          method: "POST",
          headers: { "X-Service-Key": "test-service-secret" },
          body,
        }),
        env,
      );
    expect((await bad("{nope")).status).toBe(400);
    expect((await bad("{}")).status).toBe(400);
    expect((await bad('{"links":"x"}')).status).toBe(400);
  });

  it("still works with links off", async () => {
    const userId = await seedUser();
    expect((await mirror([entry(userId)], linksOff)).status).toBe(200);
  });
});

describe("handleListArchivedLinkIDs", () => {
  it("requires the service key", async () => {
    const res = await handleListArchivedLinkIDs(
      new Request("https://example.com/x"),
      env,
    );
    expect(res.status).toBe(401);
  });

  it("lists every link_id, across users", async () => {
    const a = await seedUser();
    const b = await seedUser();
    const ids = [newLinkId(), newLinkId()];
    await seedArchivedLink(ids[0], a);
    await seedArchivedLink(ids[1], b);

    const res = await handleListArchivedLinkIDs(
      serviceRequest("GET", "/internal/archived-links/link-ids"),
      env,
    );
    expect(res.status).toBe(200);
    expect((await res.json()).link_ids).toEqual(expect.arrayContaining(ids));
  });
});

describe("handleDeleteArchivedLinks", () => {
  const del = (link_ids) =>
    handleDeleteArchivedLinks(
      serviceRequest("POST", "/internal/archived-links/delete", { link_ids }),
      env,
    );

  it("requires the service key", async () => {
    const res = await handleDeleteArchivedLinks(
      new Request("https://example.com/x", { method: "POST", body: "{}" }),
      env,
    );
    expect(res.status).toBe(401);
  });

  it("deletes exactly the ids given", async () => {
    const userId = await seedUser();
    const [gone, kept] = [newLinkId(), newLinkId()];
    await seedArchivedLink(gone, userId);
    await seedArchivedLink(kept, userId);

    const res = await del([gone]);
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ deleted: 1 });
    expect(await archivedLinkRow(gone)).toBeNull();
    expect(await archivedLinkRow(kept)).not.toBeNull();
  });

  it("an id that isn't there is fine, and an empty list is a no-op", async () => {
    expect((await del([999999])).status).toBe(200);
    expect(await (await del([])).json()).toEqual({ deleted: 0 });
  });

  it.each([
    ["a missing link_ids", {}],
    ["a non-array link_ids", { link_ids: "1" }],
    ["a non-numeric id in link_ids", { link_ids: [1, "2"] }],
  ])("rejects %s with 400", async (_name, body) => {
    const res = await handleDeleteArchivedLinks(
      serviceRequest("POST", "/internal/archived-links/delete", body),
      env,
    );
    expect(res.status).toBe(400);
  });
});

describe("handleListArchivedLinks", () => {
  it("lists only the calling user's links, as link_id, raw_url, title and updated_at", async () => {
    const userId = await seedUser();
    const other = await seedUser();
    await seedToken(userId, "rcl_live_al_list");
    const mine = newLinkId();
    await seedArchivedLink(mine, userId, {
      rawUrl: "https://example.com/mine#frag",
      title: "Mine",
      updatedAt: "2026-09-05T00:00:00.000Z",
    });
    await seedArchivedLink(newLinkId(), other);

    const res = await handleListArchivedLinks(
      authedRequest("rcl_live_al_list"),
      env,
      testCtx,
    );
    expect(res.status).toBe(200);
    expect((await res.json()).links).toEqual([
      {
        link_id: mine,
        raw_url: "https://example.com/mine#frag",
        title: "Mine",
        updated_at: "2026-09-05T00:00:00.000Z",
      },
    ]);
  });

  it("lists in link_id order, and an empty list as []", async () => {
    const userId = await seedUser();
    await seedToken(userId, "rcl_live_al_order");
    const [first, second] = [newLinkId(), newLinkId()];
    await seedArchivedLink(second, userId);
    await seedArchivedLink(first, userId);

    const res = await handleListArchivedLinks(
      authedRequest("rcl_live_al_order"),
      env,
      testCtx,
    );
    expect((await res.json()).links.map((l) => l.link_id)).toEqual([
      first,
      second,
    ]);

    const empty = await seedUser();
    await seedToken(empty, "rcl_live_al_empty");
    const emptyRes = await handleListArchivedLinks(
      authedRequest("rcl_live_al_empty"),
      env,
      testCtx,
    );
    expect(await emptyRes.json()).toEqual({ links: [] });
  });

  it("requires a bearer token", async () => {
    const res = await handleListArchivedLinks(
      new Request("https://example.com/archived-links"),
      env,
      testCtx,
    );
    expect(res.status).toBe(401);
  });

  it("is a 404 while links are off", async () => {
    const userId = await seedUser();
    await seedToken(userId, "rcl_live_al_off");
    const res = await handleListArchivedLinks(
      authedRequest("rcl_live_al_off"),
      linksOff,
      testCtx,
    );
    expect(res.status).toBe(404);
  });
});
