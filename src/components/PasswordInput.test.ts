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

import { describe, it, expect, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";
import PasswordInput from "./PasswordInput.svelte";

afterEach(() => {
  cleanup();
});

describe("PasswordInput", () => {
  it("renders a password-type input by default, with a Show password toggle", () => {
    render(PasswordInput, { id: "pw", autocomplete: "current-password" });

    const input = document.getElementById("pw") as HTMLInputElement;
    expect(input.type).toBe("password");
    expect(screen.getByRole("button", { name: "Show password" })).toBeTruthy();
  });

  it("toggles input type and the toggle button's label when clicked", async () => {
    render(PasswordInput, { id: "pw", autocomplete: "current-password" });

    const toggle = screen.getByRole("button", { name: "Show password" });
    const input = document.getElementById("pw") as HTMLInputElement;
    expect(input.type).toBe("password");

    await fireEvent.click(toggle);
    expect(input.type).toBe("text");
    expect(screen.getByRole("button", { name: "Hide password" })).toBeTruthy();

    await fireEvent.click(
      screen.getByRole("button", { name: "Hide password" }),
    );
    expect(input.type).toBe("password");
    expect(screen.getByRole("button", { name: "Show password" })).toBeTruthy();
  });

  it("reflects typed input back through the bindable value", async () => {
    render(PasswordInput, { id: "pw", autocomplete: "new-password" });

    const input = document.getElementById("pw") as HTMLInputElement;
    await fireEvent.input(input, { target: { value: "hunter2" } });

    expect(input.value).toBe("hunter2");
  });

  it("disables both the input and the toggle button when disabled", () => {
    render(PasswordInput, {
      id: "pw",
      autocomplete: "current-password",
      disabled: true,
    });

    const input = document.getElementById("pw") as HTMLInputElement;
    expect(input.disabled).toBe(true);
    expect(
      screen.getByRole("button", { name: "Show password" }),
    ).toHaveProperty("disabled", true);
  });
});
