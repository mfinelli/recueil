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

// Same two import-time hazards as AppHeader.test.ts (Login imports
// session.svelte.ts transitively too): mock svelte-spa-router's push, and
// stub fetch before this file's own top-level imports run so bootstrap()
// doesn't fire an unmocked network call.
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";

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
import { ApiError } from "../lib/api";
import Login from "./Login.svelte";

const pushMock = vi.mocked(push);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  pushMock.mockClear();
});

describe("Login", () => {
  afterEach(() => {
    // Direct mutation, not a fresh session.svelte import -- Login reads
    // session.openRegistration reactively, so resetting it here (rather
    // than the heavier vi.resetModules()/re-import dance
    // session.svelte.test.ts uses for bootstrap coverage) is enough to
    // keep this one field from leaking between tests in this file.
    session.openRegistration = false;
  });

  it("renders the username and password fields and a sign-in button", () => {
    render(Login);

    expect(screen.getByLabelText("Username")).toBeTruthy();
    expect(screen.getByLabelText("Password")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("doesn't render a register link when open registration is disabled", () => {
    session.openRegistration = false;
    render(Login);

    expect(screen.queryByRole("link", { name: "Register" })).toBeNull();
  });

  it("renders a register link when open registration is enabled", () => {
    session.openRegistration = true;
    render(Login);

    const link = screen.getByRole("link", { name: "Register" });
    expect(link).toBeTruthy();
    expect(link).toHaveProperty("hash", "#/register");
  });

  it("submits the entered username/password and redirects to / on success", async () => {
    const loginSpy = vi.spyOn(session, "login").mockResolvedValue(undefined);
    render(Login);

    await fireEvent.input(screen.getByLabelText("Username"), {
      target: { value: "alice" },
    });
    await fireEvent.input(screen.getByLabelText("Password"), {
      target: { value: "correct-password" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(loginSpy).toHaveBeenCalledWith("alice", "correct-password");
    expect(pushMock).toHaveBeenCalledWith("/");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows the API's own error message when login fails with ApiError", async () => {
    vi.spyOn(session, "login").mockRejectedValue(
      new ApiError(401, "invalid username or password"),
    );
    render(Login);

    // Both fields are `required` -- jsdom's own constraint validation
    // blocks the submit event entirely if they're left empty, so the
    // error path needs real values just like the success-path test does.
    await fireEvent.input(screen.getByLabelText("Username"), {
      target: { value: "alice" },
    });
    await fireEvent.input(screen.getByLabelText("Password"), {
      target: { value: "wrong-password" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(
      await screen.findByText("invalid username or password"),
    ).toBeTruthy();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("falls back to a generic error message for a non-ApiError failure", async () => {
    vi.spyOn(session, "login").mockRejectedValue(new Error("network error"));
    render(Login);

    await fireEvent.input(screen.getByLabelText("Username"), {
      target: { value: "alice" },
    });
    await fireEvent.input(screen.getByLabelText("Password"), {
      target: { value: "wrong-password" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("login failed")).toBeTruthy();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("disables the fields and shows a pending state while the request is in flight", async () => {
    let resolveLogin: () => void = () => {};
    vi.spyOn(session, "login").mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveLogin = resolve;
        }),
    );
    render(Login);

    await fireEvent.input(screen.getByLabelText("Username"), {
      target: { value: "alice" },
    });
    await fireEvent.input(screen.getByLabelText("Password"), {
      target: { value: "correct-password" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

    const pendingButton = screen.getByRole("button", { name: "Signing in…" });
    expect(pendingButton).toBeTruthy();
    expect(pendingButton).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Username")).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Password")).toHaveProperty("disabled", true);

    resolveLogin();
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeTruthy();
  });
});
