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

// session.svelte.ts's `sessionReady` fires its bootstrap() network calls as
// a module-level side effect at import time (deliberately -- it's what
// lets App.svelte await it without an explicit call). That means a plain
// static `import` at the top of this file would race real (unmocked)
// fetch calls against every test's own mock setup. Instead: stub fetch
// first, `vi.resetModules()`, then `await import(...)` fresh inside each
// test -- forces bootstrap() to actually run against *that* test's mock,
// and gives every test its own isolated module instance rather than
// sharing the one singleton across the whole file.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status });
}

async function freshSession() {
  vi.resetModules();
  return await import("./session.svelte");
}

describe("session bootstrap", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("populates user and needsSetup from GET /auth/me and /setup-status", async () => {
    const fetchMock = vi.fn((url: string) => {
      if (url.endsWith("/auth/me"))
        return Promise.resolve(
          jsonResponse({ id: 1, username: "alice", role: "admin" }),
        );
      if (url.endsWith("/setup-status"))
        return Promise.resolve(jsonResponse({ needs_setup: false }));
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { session, sessionReady } = await freshSession();
    await sessionReady;

    expect(session.user).toEqual({ id: 1, username: "alice", role: "admin" });
    expect(session.needsSetup).toBe(false);
  });

  it("needsSetup is true and user is null on a fresh instance with no users yet", async () => {
    const fetchMock = vi.fn((url: string) => {
      if (url.endsWith("/auth/me"))
        return Promise.resolve(jsonResponse({ error: "unauthorized" }, 401));
      if (url.endsWith("/setup-status"))
        return Promise.resolve(jsonResponse({ needs_setup: true }));
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { session, sessionReady } = await freshSession();
    await sessionReady;

    expect(session.user).toBeNull();
    expect(session.needsSetup).toBe(true);
  });

  it("one request failing doesn't strand the other's result -- and doesn't reject sessionReady", async () => {
    const fetchMock = vi.fn((url: string) => {
      if (url.endsWith("/auth/me"))
        return Promise.reject(new Error("network error"));
      if (url.endsWith("/setup-status"))
        return Promise.resolve(jsonResponse({ needs_setup: false }));
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { session, sessionReady } = await freshSession();
    await expect(sessionReady).resolves.toBeUndefined();

    // /auth/me failed outright, so no user -- but the independent
    // /setup-status result still came through, exactly the point of
    // Promise.allSettled over Promise.all here.
    expect(session.user).toBeNull();
    expect(session.needsSetup).toBe(false);
  });

  it("both requests failing still resolves sessionReady rather than hanging forever", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new Error("backend unreachable")),
    );

    const { session, sessionReady } = await freshSession();
    await expect(sessionReady).resolves.toBeUndefined();
    expect(session.user).toBeNull();
    expect(session.needsSetup).toBe(false);
  });
});

describe("session actions", () => {
  beforeEach(() => {
    // Every action test starts from a settled, logged-out bootstrap so
    // login/completeSetup/logout are exercised against a known starting
    // state, not whatever the previous test's mock happened to leave.
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse({ error: "unauthorized" }, 401)),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("login sets session.user and clears needsSetup on success", async () => {
    const { session, sessionReady } = await freshSession();
    await sessionReady;

    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(
          jsonResponse({ id: 2, username: "bob", role: "member" }),
        ),
    );
    await session.login("bob", "correct-password");

    expect(session.user).toEqual({ id: 2, username: "bob", role: "member" });
    expect(session.needsSetup).toBe(false);
  });

  it("login leaves session.user untouched and propagates ApiError on failure", async () => {
    const { session, sessionReady } = await freshSession();
    await sessionReady;

    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(
          jsonResponse({ error: "invalid username or password" }, 401),
        ),
    );

    await expect(session.login("bob", "wrong-password")).rejects.toMatchObject({
      message: "invalid username or password",
    });
    expect(session.user).toBeNull();
  });

  it("completeSetup sets session.user and clears needsSetup on success", async () => {
    const { session, sessionReady } = await freshSession();
    await sessionReady;

    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(
          jsonResponse({ id: 1, username: "admin", role: "admin" }),
        ),
    );
    await session.completeSetup("bootstrap-token", "admin", "correct-password");

    expect(session.user).toEqual({ id: 1, username: "admin", role: "admin" });
    expect(session.needsSetup).toBe(false);
  });

  it("logout clears session.user", async () => {
    const { session, sessionReady } = await freshSession();
    await sessionReady;

    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(
          jsonResponse({ id: 2, username: "bob", role: "member" }),
        ),
    );
    await session.login("bob", "correct-password");
    expect(session.user).not.toBeNull();

    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValueOnce(new Response(null, { status: 204 })),
    );
    await session.logout();

    expect(session.user).toBeNull();
  });
});
