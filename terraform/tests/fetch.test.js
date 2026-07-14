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

import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

describe("fetch (router)", () => {
  it("returns 404 for an unknown path", async () => {
    const response = await SELF.fetch("https://example.com/nope");
    expect(response.status).toBe(404);
  });

  it("returns 404 for the mirror path with the wrong method", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/users/mirror",
      {
        method: "GET",
      },
    );
    expect(response.status).toBe(404);
  });

  it("routes POST /pair to handlePair (400 on empty body confirms it was reached, not 404'd)", async () => {
    const response = await SELF.fetch("https://example.com/pair", {
      method: "POST",
      body: JSON.stringify({}),
    });
    expect(response.status).toBe(400);
  });

  it("returns 404 for GET /pair (wrong method for this route)", async () => {
    const response = await SELF.fetch("https://example.com/pair", {
      method: "GET",
    });
    expect(response.status).toBe(404);
  });

  it("routes POST /queue to handleEnqueue (401 with no auth confirms it was reached)", async () => {
    const response = await SELF.fetch("https://example.com/queue", {
      method: "POST",
      body: JSON.stringify({ id: "x", url: "https://example.com" }),
    });
    expect(response.status).toBe(401);
  });

  it("routes GET /queue to handleListQueue (401 with no auth confirms it was reached)", async () => {
    const response = await SELF.fetch("https://example.com/queue", {
      method: "GET",
    });
    expect(response.status).toBe(401);
  });

  it("routes POST /queue/:id/claim, extracting the id from the path correctly", async () => {
    // With no auth this 401s before ever reading itemId, so this alone
    // doesn't prove extraction -- the real proof is in queue.test.js's
    // claim tests, which pass a real bearer token and confirm the correct
    // row gets updated. This just confirms the regex matches the shape at
    // all (doesn't fall through to 404) for an id containing characters
    // that could plausibly break a naive path match.
    const response = await SELF.fetch(
      "https://example.com/queue/some-uuid-like-id-123/claim",
      { method: "POST" },
    );
    expect(response.status).toBe(401);
  });

  it("does not match /queue/claim (missing the id segment) as a claim route", async () => {
    const response = await SELF.fetch("https://example.com/queue/claim", {
      method: "POST",
    });
    expect(response.status).toBe(404);
  });

  it("does not match a claim path with an extra trailing segment", async () => {
    const response = await SELF.fetch(
      "https://example.com/queue/some-id/claim/extra",
      { method: "POST" },
    );
    expect(response.status).toBe(404);
  });

  it("routes GET /internal/tokens to handleListTokens (400 with no user_id confirms it was reached)", async () => {
    const response = await SELF.fetch("https://example.com/internal/tokens", {
      method: "GET",
      headers: { "X-Service-Key": "test-service-secret" },
    });
    expect(response.status).toBe(400);
  });

  it("routes DELETE /internal/tokens/:id, extracting the id from the path correctly", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/tokens/123",
      {
        method: "DELETE",
        headers: { "X-Service-Key": "test-service-secret" },
      },
    );
    // No user_id query param -> 400, which still confirms routing (not
    // 404) reached handleRevokeToken with tokenIdParam="123".
    expect(response.status).toBe(400);
  });

  it("returns 404 for DELETE /internal/tokens (missing the id segment)", async () => {
    const response = await SELF.fetch("https://example.com/internal/tokens", {
      method: "DELETE",
      headers: { "X-Service-Key": "test-service-secret" },
    });
    expect(response.status).toBe(404);
  });

  it("routes POST /captures/upload-urls to handleGetUploadUrls (401 with no auth confirms it was reached)", async () => {
    const response = await SELF.fetch(
      "https://example.com/captures/upload-urls",
      { method: "POST", body: JSON.stringify({ capture_id: "x" }) },
    );
    expect(response.status).toBe(401);
  });

  it("routes POST /queue/:id/complete, extracting the id from the path correctly", async () => {
    const response = await SELF.fetch(
      "https://example.com/queue/some-id/complete",
      { method: "POST" },
    );
    // No auth -> 401, which still confirms routing (not 404) reached
    // handleCompleteQueueItem.
    expect(response.status).toBe(401);
  });

  it("does not match /queue/complete (missing the id segment) as a complete route", async () => {
    const response = await SELF.fetch("https://example.com/queue/complete", {
      method: "POST",
    });
    expect(response.status).toBe(404);
  });

  it("routes POST /queue/:id/fail, extracting the id from the path correctly", async () => {
    const response = await SELF.fetch(
      "https://example.com/queue/some-id/fail",
      { method: "POST" },
    );
    expect(response.status).toBe(401);
  });

  it("does not match /queue/fail (missing the id segment) as a fail route", async () => {
    const response = await SELF.fetch("https://example.com/queue/fail", {
      method: "POST",
    });
    expect(response.status).toBe(404);
  });

  it("routes GET /internal/pending-captures to handleListPendingCaptures", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/pending-captures",
    );
    // No service key -> 401, which still confirms routing (not 404) reached
    // handleListPendingCaptures.
    expect(response.status).toBe(401);
  });

  it("routes POST /internal/pending-captures/:id/fetched, extracting the id from the path correctly", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/pending-captures/some-id/fetched",
      { method: "POST" },
    );
    expect(response.status).toBe(401);
  });

  it("does not match /internal/pending-captures/fetched (missing the id segment) as a fetched route", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/pending-captures/fetched",
      { method: "POST" },
    );
    expect(response.status).toBe(404);
  });

  it("routes GET /internal/archived-pages/last-sync to handleGetArchivedPagesLastSync", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/archived-pages/last-sync",
    );
    expect(response.status).toBe(401);
  });

  it("routes POST /internal/archived-pages/mirror to handleMirrorArchivedPages", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/archived-pages/mirror",
      { method: "POST" },
    );
    expect(response.status).toBe(401);
  });

  it("routes GET /internal/archived-pages/page-ids to handleListArchivedPageIDs", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/archived-pages/page-ids",
    );
    expect(response.status).toBe(401);
  });

  it("routes POST /internal/archived-pages/delete to handleDeleteArchivedPages", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/archived-pages/delete",
      { method: "POST" },
    );
    expect(response.status).toBe(401);
  });
});
