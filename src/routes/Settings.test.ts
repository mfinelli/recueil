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

// Settings nests AppHeader (same svelte-spa-router/fetch import-time
// hazards as AppHeader.test.ts), loads via a mount-time $effect rather
// than an onMount, and calls apiJSON directly rather than going through
// session.svelte.ts -- so apiJSON itself is mocked here (ApiError kept
// real via importOriginal) rather than stubbing fetch responses for it.
// applyLanguageOverride is also mocked: the real one calls
// window.location.reload(), which jsdom doesn't implement and would just
// log noise for something this file doesn't need to exercise (that's
// locale.ts's own concern, not Settings.svelte's).
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

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, apiJSON: vi.fn() };
});

vi.mock("../lib/locale", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/locale")>();
  return { ...actual, applyLanguageOverride: vi.fn() };
});

import { apiJSON, ApiError } from "../lib/api";
import { applyLanguageOverride } from "../lib/locale";
import Settings from "./Settings.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const applyLanguageOverrideMock = vi.mocked(applyLanguageOverride);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  applyLanguageOverrideMock.mockClear();
});

describe("Settings", () => {
  it("shows a loading state, then the language select once settings load", async () => {
    apiJSONMock.mockResolvedValueOnce({ language: null });
    render(Settings);

    expect(screen.getByText("Loading…")).toBeTruthy();

    const select = await screen.findByRole("combobox");
    expect(select).toHaveProperty("value", "");
    expect(apiJSONMock).toHaveBeenCalledWith("/settings");
  });

  it("shows the API's own error message when loading settings fails with ApiError", async () => {
    apiJSONMock.mockRejectedValueOnce(
      new ApiError(500, "database unavailable"),
    );
    render(Settings);

    expect(await screen.findByText("database unavailable")).toBeTruthy();
    // The load error and the select aren't mutually exclusive in the
    // template -- only `loading` gates the select, so it's still shown
    // (at its default value) alongside the error.
    expect(screen.getByRole("combobox")).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError load failure", async () => {
    apiJSONMock.mockRejectedValueOnce(new Error("network error"));
    render(Settings);

    expect(await screen.findByText("failed to load settings")).toBeTruthy();
  });

  it("saves the selected language via PATCH and shows a saved confirmation", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null })
      .mockResolvedValueOnce({ language: "fr" });
    render(Settings);

    const select = await screen.findByRole("combobox");
    await fireEvent.change(select, { target: { value: "fr" } });

    expect(apiJSONMock).toHaveBeenLastCalledWith("/settings", {
      method: "PATCH",
      body: { language: "fr" },
    });
    expect(await screen.findByText("Saved")).toBeTruthy();
    expect(applyLanguageOverrideMock).toHaveBeenCalledWith("fr");
  });

  it("shows the API's own error message when saving fails with ApiError", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null })
      .mockRejectedValueOnce(new ApiError(400, "unsupported language"));
    render(Settings);

    const select = await screen.findByRole("combobox");
    await fireEvent.change(select, { target: { value: "fr" } });

    expect(await screen.findByText("unsupported language")).toBeTruthy();
    expect(applyLanguageOverrideMock).not.toHaveBeenCalled();
  });

  it("falls back to a generic error message for a non-ApiError save failure", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null })
      .mockRejectedValueOnce(new Error("network error"));
    render(Settings);

    const select = await screen.findByRole("combobox");
    await fireEvent.change(select, { target: { value: "fr" } });

    expect(await screen.findByText("failed to save settings")).toBeTruthy();
  });
});
