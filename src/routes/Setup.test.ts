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

// Same shape as Login.test.ts (same import-time hazards, same mocking
// setup) -- Setup adds one thing Login doesn't have: a client-side
// password-confirmation check that short-circuits before
// session.completeSetup is ever called.
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
import Setup from "./Setup.svelte";

const pushMock = vi.mocked(push);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  pushMock.mockClear();
});

// All four fields are `required` -- jsdom's own constraint validation
// blocks the submit event entirely if any are left empty (see
// Login.test.ts's own note on this), so every path below fills in real
// values first.
async function fillForm({
  token = "the-bootstrap-token",
  username = "admin",
  password = "correct-password",
  confirmPassword = password,
}: {
  token?: string;
  username?: string;
  password?: string;
  confirmPassword?: string;
} = {}) {
  await fireEvent.input(screen.getByLabelText("Bootstrap token"), {
    target: { value: token },
  });
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

describe("Setup", () => {
  it("renders all four fields and a create-account button", () => {
    render(Setup);

    expect(screen.getByLabelText("Bootstrap token")).toBeTruthy();
    expect(screen.getByLabelText("Username")).toBeTruthy();
    expect(screen.getByLabelText("Password")).toBeTruthy();
    expect(screen.getByLabelText("Confirm password")).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "Create admin account" }),
    ).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows a mismatch error and never calls completeSetup when the passwords differ", async () => {
    const completeSetupSpy = vi
      .spyOn(session, "completeSetup")
      .mockResolvedValue(undefined);
    render(Setup);

    await fillForm({ password: "correct-password", confirmPassword: "oops" });
    await fireEvent.click(
      screen.getByRole("button", { name: "Create admin account" }),
    );

    expect(await screen.findByText("passwords do not match")).toBeTruthy();
    expect(completeSetupSpy).not.toHaveBeenCalled();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("submits the token/username/password and redirects to / on success", async () => {
    const completeSetupSpy = vi
      .spyOn(session, "completeSetup")
      .mockResolvedValue(undefined);
    render(Setup);

    await fillForm({
      token: "the-bootstrap-token",
      username: "admin",
      password: "correct-password",
    });
    await fireEvent.click(
      screen.getByRole("button", { name: "Create admin account" }),
    );

    expect(completeSetupSpy).toHaveBeenCalledWith(
      "the-bootstrap-token",
      "admin",
      "correct-password",
    );
    expect(pushMock).toHaveBeenCalledWith("/");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows the API's own error message when completeSetup fails with ApiError", async () => {
    vi.spyOn(session, "completeSetup").mockRejectedValue(
      new ApiError(400, "invalid bootstrap token"),
    );
    render(Setup);

    await fillForm();
    await fireEvent.click(
      screen.getByRole("button", { name: "Create admin account" }),
    );

    expect(await screen.findByText("invalid bootstrap token")).toBeTruthy();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("falls back to a generic error message for a non-ApiError failure", async () => {
    vi.spyOn(session, "completeSetup").mockRejectedValue(
      new Error("network error"),
    );
    render(Setup);

    await fillForm();
    await fireEvent.click(
      screen.getByRole("button", { name: "Create admin account" }),
    );

    expect(await screen.findByText("setup failed")).toBeTruthy();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("disables the fields and shows a pending state while the request is in flight", async () => {
    let resolveCompleteSetup: () => void = () => {};
    vi.spyOn(session, "completeSetup").mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveCompleteSetup = resolve;
        }),
    );
    render(Setup);

    await fillForm();
    await fireEvent.click(
      screen.getByRole("button", { name: "Create admin account" }),
    );

    const pendingButton = screen.getByRole("button", { name: "Creating…" });
    expect(pendingButton).toBeTruthy();
    expect(pendingButton).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Bootstrap token")).toHaveProperty(
      "disabled",
      true,
    );
    expect(screen.getByLabelText("Username")).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Password")).toHaveProperty("disabled", true);
    expect(screen.getByLabelText("Confirm password")).toHaveProperty(
      "disabled",
      true,
    );

    resolveCompleteSetup();
    expect(
      await screen.findByRole("button", { name: "Create admin account" }),
    ).toBeTruthy();
  });
});
