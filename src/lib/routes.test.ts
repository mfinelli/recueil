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

// Mocks svelte-spa-router's push -- these tests care whether a guard
// redirects (and to where), not about actually driving real navigation
// through a mounted Router. vi.mock calls are hoisted above imports by
// Vitest, so this runs before routes.ts (which imports the real push)
// ever gets a chance to import the unmocked version.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("svelte-spa-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("svelte-spa-router")>();
  return { ...actual, push: vi.fn() };
});

// session.svelte.ts's own module-level bootstrap() fires real fetch calls
// at import time (see session.svelte.test.ts's own note on this) --
// routes.ts imports it transitively, so fetch needs stubbing before this
// file's own top-level imports run, not just inside a test. The guard
// tests below don't care about bootstrap's outcome at all: they set
// session.user/needsSetup directly per test rather than going through a
// real login/bootstrap flow, since it's the guard *logic* under test
// here, not session state transitions (already covered in
// session.svelte.test.ts).
vi.stubGlobal(
  "fetch",
  vi.fn().mockResolvedValue(new Response("{}", { status: 200 })),
);

import { push } from "svelte-spa-router";
import { session } from "./session.svelte";
import { requireSetup, requireGuest, requireAuth } from "./routes";

const pushMock = vi.mocked(push);

// The guards never actually read `detail` (see routes.ts's own comment:
// they only ever consult session.*), but RoutePrecondition's real type
// still requires an argument -- an empty stand-in satisfies the type
// without needing to construct a real RouteDetail.
const fakeDetail = {} as Parameters<typeof requireSetup>[0];

beforeEach(() => {
  pushMock.mockClear();
  session.user = null;
  session.needsSetup = false;
});

describe("requireSetup", () => {
  it("allows the route when setup is genuinely needed", () => {
    session.needsSetup = true;
    expect(requireSetup(fakeDetail)).toBe(true);
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("redirects to /login when setup is already done and nobody's logged in", () => {
    session.needsSetup = false;
    session.user = null;
    expect(requireSetup(fakeDetail)).toBe(false);
    expect(pushMock).toHaveBeenCalledWith("/login");
  });

  it("redirects to / when setup is already done and someone's logged in", () => {
    session.needsSetup = false;
    session.user = { id: 1, username: "alice", role: "admin" };
    expect(requireSetup(fakeDetail)).toBe(false);
    expect(pushMock).toHaveBeenCalledWith("/");
  });
});

describe("requireGuest", () => {
  it("allows the route for a logged-out user once setup is done", () => {
    session.needsSetup = false;
    session.user = null;
    expect(requireGuest(fakeDetail)).toBe(true);
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("redirects to /setup when setup is still needed, even before checking auth", () => {
    session.needsSetup = true;
    session.user = null;
    expect(requireGuest(fakeDetail)).toBe(false);
    expect(pushMock).toHaveBeenCalledWith("/setup");
  });

  it("redirects an already-logged-in user away from /login", () => {
    session.needsSetup = false;
    session.user = { id: 1, username: "alice", role: "admin" };
    expect(requireGuest(fakeDetail)).toBe(false);
    expect(pushMock).toHaveBeenCalledWith("/");
  });
});

describe("requireAuth", () => {
  it("allows the route for a logged-in user", () => {
    session.needsSetup = false;
    session.user = { id: 1, username: "alice", role: "admin" };
    expect(requireAuth(fakeDetail)).toBe(true);
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("redirects to /setup when setup is still needed, even before checking auth", () => {
    session.needsSetup = true;
    session.user = null;
    expect(requireAuth(fakeDetail)).toBe(false);
    expect(pushMock).toHaveBeenCalledWith("/setup");
  });

  it("redirects a logged-out user to /login", () => {
    session.needsSetup = false;
    session.user = null;
    expect(requireAuth(fakeDetail)).toBe(false);
    expect(pushMock).toHaveBeenCalledWith("/login");
  });
});
