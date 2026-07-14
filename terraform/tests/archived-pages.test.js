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
  handleDeleteArchivedPages,
  handleGetArchivedPagesLastSync,
  handleListArchivedPageIDs,
  handleMirrorArchivedPages,
} from "../index.js";

// D1 state is shared across test cases within this file (no automatic
// reset between them, unlike Go's dbtest.Reset), so every row needs a
// genuinely unique key across the whole file, not just within one test --
// the same reasoning nextUserId already applies to users.
let nextUserId = 1;
let nextPageId = 1;

function newPageId() {
  return nextPageId++;
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

async function seedArchivedPage(
  pageId,
  userId,
  {
    rawUrl,
    title = null,
    latestCaptureAt = "2026-07-12T12:00:00.000Z",
    updatedAt,
  } = {},
) {
  await env.DB.prepare(
    `INSERT INTO archived_pages
       (page_id, user_id, raw_url, title, latest_capture_at, updated_at)
     VALUES (?, ?, ?, ?, ?, ?)`,
  )
    .bind(
      pageId,
      userId,
      rawUrl ?? `https://example.com/page-${pageId}`,
      title,
      latestCaptureAt,
      updatedAt ?? "2026-07-12T12:00:00.000Z",
    )
    .run();
}

function serviceRequest(method, path, body) {
  const init = {
    method,
    headers: { "X-Service-Key": "test-service-secret" },
  };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  return new Request(`https://example.com${path}`, init);
}

const testCtx = { waitUntil: () => {} };

describe("handleGetArchivedPagesLastSync", () => {
  // No "empty" case tested here deliberately: D1 state is shared across
  // this whole file (see the note above nextUserId), so "archived_pages
  // has zero rows" isn't a state any test after the first insert can
  // actually observe -- that's a property of test isolation, not of the
  // handler, and asserting it would just be fragile against test
  // reordering rather than actually verifying anything.

  it("returns the max updated_at across all pages, not scoped to one user", async () => {
    const userA = await seedUser();
    const userB = await seedUser();
    await seedArchivedPage(newPageId(), userA, {
      updatedAt: "2026-07-12T10:00:00.000Z",
    });
    await seedArchivedPage(newPageId(), userB, {
      updatedAt: "2026-07-12T20:00:00.000Z",
    });
    await seedArchivedPage(newPageId(), userA, {
      updatedAt: "2026-07-12T12:00:00.000Z",
    });

    const response = await handleGetArchivedPagesLastSync(
      serviceRequest("GET", "/internal/archived-pages/last-sync"),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.last_sync).toBe("2026-07-12T20:00:00.000Z");
  });

  it("requires the service key", async () => {
    const response = await handleGetArchivedPagesLastSync(
      new Request("https://example.com/internal/archived-pages/last-sync"),
      env,
    );
    expect(response.status).toBe(401);
  });
});

describe("handleMirrorArchivedPages", () => {
  it("inserts new rows", async () => {
    const userId = await seedUser();
    const pageId = newPageId();
    const response = await handleMirrorArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/mirror", {
        pages: [
          {
            page_id: pageId,
            user_id: userId,
            raw_url: "https://example.com/page",
            title: "Example",
            latest_capture_at: "2026-07-12T12:00:00.000Z",
            updated_at: "2026-07-12T12:00:00.000Z",
          },
        ],
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.upserted).toBe(1);

    const row = await env.DB.prepare(
      "SELECT * FROM archived_pages WHERE page_id = ?",
    )
      .bind(pageId)
      .first();
    expect(row).toMatchObject({
      page_id: pageId,
      user_id: userId,
      raw_url: "https://example.com/page",
      title: "Example",
      latest_capture_at: "2026-07-12T12:00:00.000Z",
      updated_at: "2026-07-12T12:00:00.000Z",
    });
  });

  it("updates an existing row on conflict, using updated_at from the request body verbatim (never D1's own clock)", async () => {
    const userId = await seedUser();
    const pageId = newPageId();
    await seedArchivedPage(pageId, userId, {
      rawUrl: "https://example.com/old-url",
      title: "Old Title",
      updatedAt: "2026-07-12T09:00:00.000Z",
    });

    await handleMirrorArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/mirror", {
        pages: [
          {
            page_id: pageId,
            user_id: userId,
            raw_url: "https://example.com/new-url",
            title: "New Title",
            latest_capture_at: "2026-07-12T13:00:00.000Z",
            updated_at: "2026-07-12T13:00:00.000Z",
          },
        ],
      }),
      env,
      testCtx,
    );

    const row = await env.DB.prepare(
      "SELECT raw_url, title, updated_at FROM archived_pages WHERE page_id = ?",
    )
      .bind(pageId)
      .first();
    expect(row).toEqual({
      raw_url: "https://example.com/new-url",
      title: "New Title",
      updated_at: "2026-07-12T13:00:00.000Z",
    });
  });

  it("handles a null title", async () => {
    const userId = await seedUser();
    const pageId = newPageId();
    const response = await handleMirrorArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/mirror", {
        pages: [
          {
            page_id: pageId,
            user_id: userId,
            raw_url: "https://example.com/page",
            latest_capture_at: "2026-07-12T12:00:00.000Z",
            updated_at: "2026-07-12T12:00:00.000Z",
          },
        ],
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
    const row = await env.DB.prepare(
      "SELECT title FROM archived_pages WHERE page_id = ?",
    )
      .bind(pageId)
      .first();
    expect(row.title).toBeNull();
  });

  it("handles multiple pages in one batch atomically", async () => {
    const userId = await seedUser();
    const pageIdA = newPageId();
    const pageIdB = newPageId();
    const response = await handleMirrorArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/mirror", {
        pages: [
          {
            page_id: pageIdA,
            user_id: userId,
            raw_url: "https://example.com/a",
            latest_capture_at: "2026-07-12T12:00:00.000Z",
            updated_at: "2026-07-12T12:00:00.000Z",
          },
          {
            page_id: pageIdB,
            user_id: userId,
            raw_url: "https://example.com/b",
            latest_capture_at: "2026-07-12T12:00:00.000Z",
            updated_at: "2026-07-12T12:00:01.000Z",
          },
        ],
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.upserted).toBe(2);

    const rows = await env.DB.prepare(
      "SELECT page_id FROM archived_pages WHERE page_id IN (?, ?)",
    )
      .bind(pageIdA, pageIdB)
      .all();
    expect(rows.results).toHaveLength(2);
  });

  it("handles an empty pages array without error", async () => {
    const response = await handleMirrorArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/mirror", {
        pages: [],
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.upserted).toBe(0);
  });

  it.each([
    ["missing pages key entirely", {}],
    ["pages is not an array", { pages: "not-an-array" }],
    [
      "a page entry missing page_id",
      {
        pages: [
          {
            user_id: 1,
            raw_url: "x",
            latest_capture_at: "x",
            updated_at: "x",
          },
        ],
      },
    ],
    [
      "a page entry with wrong field types",
      {
        pages: [
          {
            page_id: "not-a-number",
            user_id: 1,
            raw_url: "x",
            latest_capture_at: "x",
            updated_at: "x",
          },
        ],
      },
    ],
  ])("rejects: %s", async (name, body) => {
    const response = await handleMirrorArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/mirror", body),
      env,
      testCtx,
    );
    expect(response.status).toBe(400);
  });

  it("requires the service key", async () => {
    const response = await handleMirrorArchivedPages(
      new Request("https://example.com/internal/archived-pages/mirror", {
        method: "POST",
        body: JSON.stringify({ pages: [] }),
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(401);
  });
});

describe("handleListArchivedPageIDs", () => {
  it("includes freshly-seeded page_ids across multiple users", async () => {
    const userA = await seedUser();
    const userB = await seedUser();
    const pageIdA1 = newPageId();
    const pageIdB = newPageId();
    const pageIdA2 = newPageId();
    await seedArchivedPage(pageIdA1, userA);
    await seedArchivedPage(pageIdB, userB);
    await seedArchivedPage(pageIdA2, userA);

    const response = await handleListArchivedPageIDs(
      serviceRequest("GET", "/internal/archived-pages/page-ids"),
      env,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.page_ids).toEqual(
      expect.arrayContaining([pageIdA1, pageIdB, pageIdA2]),
    );
  });

  it("requires the service key", async () => {
    const response = await handleListArchivedPageIDs(
      new Request("https://example.com/internal/archived-pages/page-ids"),
      env,
    );
    expect(response.status).toBe(401);
  });
});

describe("handleDeleteArchivedPages", () => {
  it("deletes exactly the given page_ids and leaves the rest untouched", async () => {
    const userId = await seedUser();
    const pageIdKeep1 = newPageId();
    const pageIdDelete = newPageId();
    const pageIdKeep2 = newPageId();
    await seedArchivedPage(pageIdKeep1, userId);
    await seedArchivedPage(pageIdDelete, userId);
    await seedArchivedPage(pageIdKeep2, userId);

    const response = await handleDeleteArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/delete", {
        page_ids: [pageIdDelete],
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.deleted).toBe(1);

    const remaining = await env.DB.prepare(
      "SELECT page_id FROM archived_pages WHERE page_id IN (?, ?, ?)",
    )
      .bind(pageIdKeep1, pageIdDelete, pageIdKeep2)
      .all();
    expect(remaining.results.map((r) => r.page_id).sort()).toEqual(
      [pageIdKeep1, pageIdKeep2].sort(),
    );
  });

  it("handles an empty page_ids array without error", async () => {
    const response = await handleDeleteArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/delete", {
        page_ids: [],
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
    const body = await response.json();
    expect(body.deleted).toBe(0);
  });

  it("deleting a page_id that doesn't exist is a harmless no-op", async () => {
    const response = await handleDeleteArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/delete", {
        page_ids: [999999],
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(200);
  });

  it.each([
    ["missing page_ids key entirely", {}],
    ["page_ids is not an array", { page_ids: "not-an-array" }],
    ["page_ids contains a non-number", { page_ids: [1, "two", 3] }],
  ])("rejects: %s", async (name, body) => {
    const response = await handleDeleteArchivedPages(
      serviceRequest("POST", "/internal/archived-pages/delete", body),
      env,
      testCtx,
    );
    expect(response.status).toBe(400);
  });

  it("requires the service key", async () => {
    const response = await handleDeleteArchivedPages(
      new Request("https://example.com/internal/archived-pages/delete", {
        method: "POST",
        body: JSON.stringify({ page_ids: [] }),
      }),
      env,
      testCtx,
    );
    expect(response.status).toBe(401);
  });
});
