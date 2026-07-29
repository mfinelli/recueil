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

// Same shape as Setup.test.ts (itself same shape as Login.test.ts) --
// Register has the same client-side password-confirmation check as Setup,
// minus the bootstrap-token field neither Login nor Register need.
// reloadIntoLibrary is mocked (the real one calls window.location.reload(),
// which jsdom doesn't implement) -- see Login.test.ts's comment.
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";

vi.stubGlobal(
  "fetch",
  vi.fn().mockResolvedValue(new Response("{}", { status: 200 })),
);

vi.mock("../lib/session.svelte", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/session.svelte")>();
  return { ...actual, reloadIntoLibrary: vi.fn() };
});

import { session, reloadIntoLibrary } from "../lib/session.svelte";
import { ApiError } from "../lib/api";
import Register from "./Register.svelte";

const reloadIntoLibraryMock = vi.mocked(reloadIntoLibrary);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  reloadIntoLibraryMock.mockClear();
});

// All three fields are `required` -- jsdom's own constraint validation
// blocks the submit event entirely if any are left empty (see
// Login.test.ts's own note on this), so every path below fills in real
// values first.
async function fillForm({
  username = "member",
  password = "correct-password",
  confirmPassword = password,
}: {
  username?: string;
  password?: string;
  confirmPassword?: string;
} = {}) {
  await fireEvent.input(screen.getByLabelText("Username"), {
    target: { value: username },
  });
  await fireEvent.input(screen.getByLabelText("Password"), {
    target: { value: password },
  });
  await fireEvent.input(screen.getByLabelText("Confirm password"), {
    target: { value: confirmPassword },
  });
}

describe("Register", () => {
  it("renders all three fields and a create-account button", () => {
    render(Register);

    expect(screen.getByLabelText("Username")).toBeTruthy();
    expect(screen.getByLabelText("Password")).toBeTruthy();
    expect(screen.getByLabelText("Confirm password")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Create account" })).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("renders a link back to the login page", () => {
    render(Register);

    const link = screen.getByRole("link", { name: "Sign in" });
    expect(link).toBeTruthy();
    expect(link).toHaveProperty("hash", "#/login");
  });

  it("shows a mismatch error and never calls register when the passwords differ", async () => {
    const registerSpy = vi
      .spyOn(session, "register")
      .mockResolvedValue(undefined);
    render(Register);

    await fillForm({ password: "correct-password", confirmPassword: "oops" });
    await fireEvent.click(
      screen.getByRole("button", { name: "Create account" }),
    );

    expect(await screen.findByText("passwords do not match")).toBeTruthy();
    expect(registerSpy).not.toHaveBeenCalled();
    expect(reloadIntoLibraryMock).not.toHaveBeenCalled();
  });

  it("submits the username/password and reloads into the library on success", async () => {
    const registerSpy = vi
      .spyOn(session, "register")
      .mockResolvedValue(undefined);
    render(Register);

    await fillForm({ username: "member", password: "correct-password" });
    await fireEvent.click(
      screen.getByRole("button", { name: "Create account" }),
    );

    expect(registerSpy).toHaveBeenCalledWith("member", "correct-password");
    expect(reloadIntoLibraryMock).toHaveBeenCalled();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows the API's own error message when register fails with ApiError", async () => {
    vi.spyOn(session, "register").mockRejectedValue(
      new ApiError(409, "username already taken"),
    );
    render(Register);

    await fillForm();
    await fireEvent.click(
      screen.getByRole("button", { name: "Create account" }),
    );

    expect(await screen.findByText("username already taken")).toBeTruthy();
    expect(reloadIntoLibraryMock).not.toHaveBeenCalled();
  });

  it("falls back to a generic error message for a non-ApiError failure", async () => {
    vi.spyOn(session, "register").mockRejectedValue(new Error("network error"));
    render(Register);

    await fillForm();
    await fireEvent.click(
      screen.getByRole("button", { name: "Create account" }),
    );

    expect(await screen.findByText("registration failed")).toBeTruthy();
    expect(reloadIntoLibraryMock).not.toHaveBeenCalled();
  });

  it("disables the fields and shows a pending state while the request is in flight", async () => {
    let resolveRegister: () => void = () => {};
    vi.spyOn(session, "register").mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveRegister = resolve;
        }),
    );
    render(Register);

    await fillForm();
    await fireEvent.click(
      screen.getByRole("button", { name: "Create account" }),
    );

    const pendingButton = screen.getByRole("button", { name: "Creating…" });
    expect(pendingButton).toBeTruthy();
    expect(pendingButton).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Username")).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Password")).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Confirm password")).toHaveProperty(
      "disabled",
      true,
    );

    resolveRegister();
    expect(
      await screen.findByRole("button", { name: "Create account" }),
    ).toBeTruthy();
  });
});
