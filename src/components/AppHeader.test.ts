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

// Mirrors routes.test.ts's established pattern for the same two import-time
// hazards AppHeader shares with routes.ts: mock svelte-spa-router's push
// (real `link` action is left alone -- it's exercised for real, just doesn't
// get asserted on directly), and stub fetch before this file's own top-level
// imports run, since importing AppHeader pulls in session.svelte.ts
// transitively and that module fires its bootstrap() fetch calls as an
// import-time side effect (see session.svelte.test.ts's own note on this).
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";
import { tick } from "svelte";

vi.mock("svelte-spa-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("svelte-spa-router")>();
  return { ...actual, push: vi.fn() };
});

vi.stubGlobal(
  "fetch",
  vi.fn().mockResolvedValue(new Response("{}", { status: 200 })),
);

import { push } from "svelte-spa-router";
import { session } from "../lib/session.svelte";
import AppHeader from "./AppHeader.svelte";

const pushMock = vi.mocked(push);

// @testing-library/svelte's auto-cleanup only self-registers when it finds
// beforeEach/afterEach already sitting in global scope (see its own
// index.js) -- this project doesn't set vitest's `globals: true` anywhere
// else, so cleanup() needs calling explicitly rather than relying on that.
afterEach(() => {
  cleanup();
});

beforeEach(() => {
  pushMock.mockClear();
  session.user = null;
});

describe("AppHeader", () => {
  it("always renders the brand and main nav links", () => {
    render(AppHeader);

    expect(screen.getByRole("link", { name: "recueil" })).toBeTruthy();
    for (const label of [
      "Library",
      "Collections",
      "Tags",
      "Devices",
      "Queue",
      "Settings",
    ]) {
      expect(screen.getByRole("link", { name: label })).toBeTruthy();
    }
  });

  it("doesn't render the account section when logged out", () => {
    session.user = null;
    render(AppHeader);

    expect(screen.queryByText("alice")).toBeNull();
    expect(screen.queryByRole("button", { name: "Sign out" })).toBeNull();
  });

  it("shows the username and a sign-out button when logged in", () => {
    session.user = { id: 1, username: "alice", role: "admin" };
    render(AppHeader);

    expect(screen.getByText("alice")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sign out" })).toBeTruthy();
  });

  it("logs out and redirects to /login when sign-out is clicked", async () => {
    session.user = { id: 1, username: "alice", role: "admin" };
    const logoutSpy = vi.spyOn(session, "logout").mockResolvedValue(undefined);
    render(AppHeader);

    await fireEvent.click(screen.getByRole("button", { name: "Sign out" }));

    expect(logoutSpy).toHaveBeenCalled();
    expect(pushMock).toHaveBeenCalledWith("/login");
  });

  it("toggles the mobile nav disclosure open and closed", async () => {
    render(AppHeader);

    const toggle = screen.getByRole("button", { name: "Menu" });
    expect(toggle).toHaveProperty("ariaExpanded", "false");

    await fireEvent.click(toggle);
    expect(toggle).toHaveProperty("ariaExpanded", "true");

    await fireEvent.click(toggle);
    expect(toggle).toHaveProperty("ariaExpanded", "false");
  });

  it("closes the mobile nav disclosure when a nav link is clicked", async () => {
    render(AppHeader);

    const toggle = screen.getByRole("button", { name: "Menu" });
    await fireEvent.click(toggle);
    expect(toggle).toHaveProperty("ariaExpanded", "true");

    await fireEvent.click(screen.getByRole("link", { name: "Collections" }));
    expect(toggle).toHaveProperty("ariaExpanded", "false");
  });

  // svelte-spa-router/active reads the current location directly off
  // window.location's hash (see Router.svelte's own getLocation()), not
  // through the mocked push() above -- so these drive it the same way the
  // real router does: set the hash, then fire the hashchange event its
  // internal listener is waiting for.
  async function setHash(hash: string) {
    window.location.hash = hash;
    window.dispatchEvent(new Event("hashchange"));
    await tick();
  }

  afterEach(() => {
    window.location.hash = "";
  });

  function isActive(name: string): boolean {
    return screen.getByRole("link", { name }).classList.contains("active");
  }

  it("marks Library active by default (path '/')", () => {
    render(AppHeader);

    expect(isActive("Library")).toBe(true);
    expect(isActive("Collections")).toBe(false);
  });

  it("marks Collections active on both /collections and a nested collection path", async () => {
    render(AppHeader);

    await setHash("#/collections");
    expect(isActive("Collections")).toBe(true);

    await setHash("#/collections/cooking/recipes");
    expect(isActive("Collections")).toBe(true);
    expect(isActive("Library")).toBe(false);
  });

  it("marks Library active on a page detail route", async () => {
    render(AppHeader);

    await setHash("#/pages/42");
    expect(isActive("Library")).toBe(true);
  });

  it("marks Tags active on a tag detail route", async () => {
    render(AppHeader);

    await setHash("#/tags/woodworking");
    expect(isActive("Tags")).toBe(true);
  });
});
