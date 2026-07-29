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
// locale.ts's own concern, not Settings.svelte's). applyTheme is mocked
// too, for a much simpler reason -- it's a real DOM mutation
// (document.documentElement.dataset.theme), and these tests only care
// that Settings.svelte calls it with the right value, not that jsdom's
// dataset actually changes.
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

vi.mock("../lib/theme", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/theme")>();
  return { ...actual, applyTheme: vi.fn() };
});

import { apiJSON, ApiError } from "../lib/api";
import { applyLanguageOverride } from "../lib/locale";
import { applyTheme } from "../lib/theme";
import Settings from "./Settings.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const applyLanguageOverrideMock = vi.mocked(applyLanguageOverride);
const applyThemeMock = vi.mocked(applyTheme);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  applyLanguageOverrideMock.mockClear();
  applyThemeMock.mockClear();
});

function isActive(name: string): boolean {
  return screen.getByRole("button", { name }).classList.contains("active");
}

describe("Settings", () => {
  it("shows a loading state, then both toggle groups once settings load", async () => {
    apiJSONMock.mockResolvedValueOnce({ language: null, theme: null });
    render(Settings);

    // Two sections, each with their own loading indicator now.
    expect(screen.getAllByText("Loading…")).toHaveLength(2);

    await screen.findByRole("group", { name: "Language" });
    expect(isActive("Automatic (browser language)")).toBe(true);
    expect(isActive("English")).toBe(false);
    expect(isActive("Français")).toBe(false);

    expect(screen.getByRole("group", { name: "Theme" })).toBeTruthy();
    expect(isActive("Automatic (system setting)")).toBe(true);
    expect(isActive("Light")).toBe(false);
    expect(isActive("Dark")).toBe(false);

    expect(apiJSONMock).toHaveBeenCalledWith("/settings");
  });

  it("loads a previously set language and theme, marking the right toggle active", async () => {
    apiJSONMock.mockResolvedValueOnce({ language: "fr", theme: "dark" });
    render(Settings);

    await screen.findByRole("group", { name: "Language" });
    expect(isActive("Français")).toBe(true);
    expect(isActive("Automatic (browser language)")).toBe(false);
    expect(isActive("Dark")).toBe(true);
    expect(isActive("Automatic (system setting)")).toBe(false);
  });

  it("shows the API's own error message when loading settings fails with ApiError", async () => {
    apiJSONMock.mockRejectedValueOnce(
      new ApiError(500, "database unavailable"),
    );
    render(Settings);

    expect(await screen.findByText("database unavailable")).toBeTruthy();
    // The load error and the toggles aren't mutually exclusive in the
    // template -- only `loading` gates them, so both are still shown
    // (at their default values) alongside the error.
    expect(screen.getByRole("group", { name: "Language" })).toBeTruthy();
    expect(screen.getByRole("group", { name: "Theme" })).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError load failure", async () => {
    apiJSONMock.mockRejectedValueOnce(new Error("network error"));
    render(Settings);

    expect(await screen.findByText("failed to load settings")).toBeTruthy();
  });

  it("saves the selected language via PATCH (alongside the current theme) and shows a saved confirmation", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null, theme: null })
      .mockResolvedValueOnce({ language: "fr", theme: null });
    render(Settings);

    await screen.findByRole("group", { name: "Language" });
    await fireEvent.click(screen.getByRole("button", { name: "Français" }));

    expect(apiJSONMock).toHaveBeenLastCalledWith("/settings", {
      method: "PATCH",
      body: { language: "fr", theme: "" },
    });
    expect(await screen.findByText("Saved")).toBeTruthy();
    expect(applyLanguageOverrideMock).toHaveBeenCalledWith("fr");
    // Changing language reloads the page (via applyLanguageOverride) --
    // theme itself is untouched by this change, so nothing should have
    // called applyTheme.
    expect(applyThemeMock).not.toHaveBeenCalled();
  });

  it("doesn't re-save when clicking the language that's already active", async () => {
    apiJSONMock.mockResolvedValueOnce({ language: "fr", theme: null });
    render(Settings);

    await screen.findByRole("group", { name: "Language" });
    const before = apiJSONMock.mock.calls.length;
    await fireEvent.click(screen.getByRole("button", { name: "Français" }));

    expect(apiJSONMock.mock.calls.length).toBe(before);
  });

  it("shows the API's own error message when saving the language fails with ApiError", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null, theme: null })
      .mockRejectedValueOnce(new ApiError(400, "unsupported language"));
    render(Settings);

    await screen.findByRole("group", { name: "Language" });
    await fireEvent.click(screen.getByRole("button", { name: "Français" }));

    expect(await screen.findByText("unsupported language")).toBeTruthy();
    expect(applyLanguageOverrideMock).not.toHaveBeenCalled();
  });

  it("falls back to a generic error message for a non-ApiError language save failure", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null, theme: null })
      .mockRejectedValueOnce(new Error("network error"));
    render(Settings);

    await screen.findByRole("group", { name: "Language" });
    await fireEvent.click(screen.getByRole("button", { name: "Français" }));

    expect(await screen.findByText("failed to save settings")).toBeTruthy();
  });

  it("saves the selected theme via PATCH (alongside the current language), applies it live, and doesn't reload", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null, theme: null })
      .mockResolvedValueOnce({ language: null, theme: "dark" });
    render(Settings);

    await screen.findByRole("group", { name: "Theme" });
    await fireEvent.click(screen.getByRole("button", { name: "Dark" }));

    expect(apiJSONMock).toHaveBeenLastCalledWith("/settings", {
      method: "PATCH",
      body: { language: "", theme: "dark" },
    });
    expect(await screen.findByText("Saved")).toBeTruthy();
    expect(applyThemeMock).toHaveBeenCalledWith("dark");
    // Unlike language, a theme change must NOT trigger a reload.
    expect(applyLanguageOverrideMock).not.toHaveBeenCalled();
  });

  it("applies null (not the empty string) when the theme is cleared back to automatic", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null, theme: "dark" })
      .mockResolvedValueOnce({ language: null, theme: null });
    render(Settings);

    await screen.findByRole("group", { name: "Theme" });
    await fireEvent.click(
      screen.getByRole("button", { name: "Automatic (system setting)" }),
    );

    expect(applyThemeMock).toHaveBeenCalledWith(null);
  });

  it("doesn't re-save when clicking the theme that's already active", async () => {
    apiJSONMock.mockResolvedValueOnce({ language: null, theme: "dark" });
    render(Settings);

    await screen.findByRole("group", { name: "Theme" });
    const before = apiJSONMock.mock.calls.length;
    await fireEvent.click(screen.getByRole("button", { name: "Dark" }));

    expect(apiJSONMock.mock.calls.length).toBe(before);
  });

  it("shows the API's own error message when saving the theme fails with ApiError", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null, theme: null })
      .mockRejectedValueOnce(new ApiError(400, "invalid theme"));
    render(Settings);

    await screen.findByRole("group", { name: "Theme" });
    await fireEvent.click(screen.getByRole("button", { name: "Dark" }));

    expect(await screen.findByText("invalid theme")).toBeTruthy();
    expect(applyThemeMock).not.toHaveBeenCalled();
  });

  it("falls back to a generic error message for a non-ApiError theme save failure", async () => {
    apiJSONMock
      .mockResolvedValueOnce({ language: null, theme: null })
      .mockRejectedValueOnce(new Error("network error"));
    render(Settings);

    await screen.findByRole("group", { name: "Theme" });
    await fireEvent.click(screen.getByRole("button", { name: "Dark" }));

    expect(await screen.findByText("failed to save settings")).toBeTruthy();
  });
});
