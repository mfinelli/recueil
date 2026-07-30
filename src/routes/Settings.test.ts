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
//
// mockLoad below is a path-dispatching mockImplementation base (GET
// /settings and GET /stats resolve independently, matched by URL), not a
// mockResolvedValueOnce chain -- Settings.svelte's $effect now fires
// both loads in parallel at mount, so a call-order-dependent chain would
// be fragile the same way PageDetail's parallel loads already
// established this file should avoid. PATCH /settings (a save) is
// layered on top per-test via mockResolvedValueOnce/mockRejectedValueOnce,
// which still works correctly here: those "once" overrides are consumed
// in call order ahead of the persistent mockImplementation, and by the
// time a save happens (a later, user-triggered call), both of the
// initial mount-time GETs have already resolved through the base
// implementation.
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
import type { UserSettings, Stats } from "../lib/types";
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

const defaultStats: Stats = {
  page_count: 3,
  capture_count: 5,
  html_compressed_bytes: 1024,
  html_uncompressed_bytes: 4096,
  favicon_bytes: 128,
  screenshot_bytes: 2048,
};

// GET /settings and GET /stats resolve independently by URL; anything
// else (a PATCH, or a call this helper wasn't told to expect) throws,
// same "fail loudly on an unexpected call" approach mockLoad-style
// helpers use elsewhere rather than silently returning undefined.
function mockLoad(settings: UserSettings, stats: Stats = defaultStats) {
  apiJSONMock.mockImplementation((url: unknown) => {
    if (url === "/stats") return Promise.resolve(stats);
    if (url === "/settings") return Promise.resolve(settings);
    throw new Error(`mockLoad: unexpected apiJSON call: ${String(url)}`);
  });
}

describe("Settings", () => {
  it("shows a loading state, then both toggle groups and stats once everything loads", async () => {
    mockLoad({ language: null, theme: null });
    render(Settings);

    // Three sections, each with their own loading indicator.
    expect(screen.getAllByText("Loading…")).toHaveLength(3);

    await screen.findByRole("group", { name: "Language" });
    expect(isActive("Automatic (browser language)")).toBe(true);
    expect(isActive("English")).toBe(false);
    expect(isActive("Français")).toBe(false);

    expect(screen.getByRole("group", { name: "Theme" })).toBeTruthy();
    expect(isActive("Automatic (system setting)")).toBe(true);
    expect(isActive("Light")).toBe(false);
    expect(isActive("Dark")).toBe(false);

    expect(
      await screen.findByText("3 pages archived (5 captures)"),
    ).toBeTruthy();

    expect(apiJSONMock).toHaveBeenCalledWith("/settings");
    expect(apiJSONMock).toHaveBeenCalledWith("/stats");
  });

  it("loads a previously set language and theme, marking the right toggle active", async () => {
    mockLoad({ language: "fr", theme: "dark" });
    render(Settings);

    await screen.findByRole("group", { name: "Language" });
    expect(isActive("Français")).toBe(true);
    expect(isActive("Automatic (browser language)")).toBe(false);
    expect(isActive("Dark")).toBe(true);
    expect(isActive("Automatic (system setting)")).toBe(false);
  });

  it("shows the API's own error message when loading settings fails with ApiError", async () => {
    apiJSONMock.mockImplementation((url: unknown) => {
      if (url === "/stats") return Promise.resolve(defaultStats);
      if (url === "/settings") {
        return Promise.reject(new ApiError(500, "database unavailable"));
      }
      throw new Error(`unexpected apiJSON call: ${String(url)}`);
    });
    render(Settings);

    expect(await screen.findByText("database unavailable")).toBeTruthy();
    // The load error and the toggles aren't mutually exclusive in the
    // template -- only `loading` gates them, so both are still shown
    // (at their default values) alongside the error.
    expect(screen.getByRole("group", { name: "Language" })).toBeTruthy();
    expect(screen.getByRole("group", { name: "Theme" })).toBeTruthy();
    // Stats loaded fine independently -- one resource failing doesn't
    // take the other down with it.
    expect(
      await screen.findByText("3 pages archived (5 captures)"),
    ).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError load failure", async () => {
    apiJSONMock.mockImplementation((url: unknown) => {
      if (url === "/stats") return Promise.resolve(defaultStats);
      if (url === "/settings")
        return Promise.reject(new Error("network error"));
      throw new Error(`unexpected apiJSON call: ${String(url)}`);
    });
    render(Settings);

    expect(await screen.findByText("failed to load settings")).toBeTruthy();
  });

  it("shows the API's own error message when loading stats fails, independent of settings", async () => {
    apiJSONMock.mockImplementation((url: unknown) => {
      if (url === "/settings") {
        return Promise.resolve({ language: null, theme: null });
      }
      if (url === "/stats") {
        return Promise.reject(new ApiError(500, "database unavailable"));
      }
      throw new Error(`unexpected apiJSON call: ${String(url)}`);
    });
    render(Settings);

    await screen.findByRole("group", { name: "Language" });
    expect(await screen.findByText("database unavailable")).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError stats load failure", async () => {
    apiJSONMock.mockImplementation((url: unknown) => {
      if (url === "/settings") {
        return Promise.resolve({ language: null, theme: null });
      }
      if (url === "/stats") return Promise.reject(new Error("network error"));
      throw new Error(`unexpected apiJSON call: ${String(url)}`);
    });
    render(Settings);

    expect(await screen.findByText("failed to load stats")).toBeTruthy();
  });

  it("shows the disk-usage breakdown, with the total excluding the informational uncompressed figure", async () => {
    mockLoad(
      { language: null, theme: null },
      {
        page_count: 10,
        capture_count: 12,
        html_compressed_bytes: 1024,
        html_uncompressed_bytes: 5120,
        favicon_bytes: 2048,
        screenshot_bytes: 4096,
      },
    );
    render(Settings);

    // Total = compressed HTML + favicons + screenshots (1024 + 2048 +
    // 4096 = 7168 bytes = 7.0 KB) -- NOT including the uncompressed
    // figure, which is informational only.
    expect(await screen.findByText("Occupying 7.0 KB total disk")).toBeTruthy();
    expect(screen.getByText("1.0 KB (5.0 KB uncompressed)")).toBeTruthy();
    expect(screen.getByText("2.0 KB")).toBeTruthy();
    expect(screen.getByText("4.0 KB")).toBeTruthy();
  });

  it("saves the selected language via PATCH (alongside the current theme) and shows a saved confirmation", async () => {
    mockLoad({ language: null, theme: null });
    render(Settings);
    await screen.findByRole("group", { name: "Language" });

    apiJSONMock.mockResolvedValueOnce({ language: "fr", theme: null });
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
    mockLoad({ language: "fr", theme: null });
    render(Settings);
    await screen.findByRole("group", { name: "Language" });

    const before = apiJSONMock.mock.calls.length;
    await fireEvent.click(screen.getByRole("button", { name: "Français" }));

    expect(apiJSONMock.mock.calls.length).toBe(before);
  });

  it("shows the API's own error message when saving the language fails with ApiError", async () => {
    mockLoad({ language: null, theme: null });
    render(Settings);
    await screen.findByRole("group", { name: "Language" });

    apiJSONMock.mockRejectedValueOnce(
      new ApiError(400, "unsupported language"),
    );
    await fireEvent.click(screen.getByRole("button", { name: "Français" }));

    expect(await screen.findByText("unsupported language")).toBeTruthy();
    expect(applyLanguageOverrideMock).not.toHaveBeenCalled();
  });

  it("falls back to a generic error message for a non-ApiError language save failure", async () => {
    mockLoad({ language: null, theme: null });
    render(Settings);
    await screen.findByRole("group", { name: "Language" });

    apiJSONMock.mockRejectedValueOnce(new Error("network error"));
    await fireEvent.click(screen.getByRole("button", { name: "Français" }));

    expect(await screen.findByText("failed to save settings")).toBeTruthy();
  });

  it("saves the selected theme via PATCH (alongside the current language), applies it live, and doesn't reload", async () => {
    mockLoad({ language: null, theme: null });
    render(Settings);
    await screen.findByRole("group", { name: "Theme" });

    apiJSONMock.mockResolvedValueOnce({ language: null, theme: "dark" });
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
    mockLoad({ language: null, theme: "dark" });
    render(Settings);
    await screen.findByRole("group", { name: "Theme" });

    apiJSONMock.mockResolvedValueOnce({ language: null, theme: null });
    await fireEvent.click(
      screen.getByRole("button", { name: "Automatic (system setting)" }),
    );

    expect(applyThemeMock).toHaveBeenCalledWith(null);
  });

  it("doesn't re-save when clicking the theme that's already active", async () => {
    mockLoad({ language: null, theme: "dark" });
    render(Settings);
    await screen.findByRole("group", { name: "Theme" });

    const before = apiJSONMock.mock.calls.length;
    await fireEvent.click(screen.getByRole("button", { name: "Dark" }));

    expect(apiJSONMock.mock.calls.length).toBe(before);
  });

  it("shows the API's own error message when saving the theme fails with ApiError", async () => {
    mockLoad({ language: null, theme: null });
    render(Settings);
    await screen.findByRole("group", { name: "Theme" });

    apiJSONMock.mockRejectedValueOnce(new ApiError(400, "invalid theme"));
    await fireEvent.click(screen.getByRole("button", { name: "Dark" }));

    expect(await screen.findByText("invalid theme")).toBeTruthy();
    expect(applyThemeMock).not.toHaveBeenCalled();
  });

  it("falls back to a generic error message for a non-ApiError theme save failure", async () => {
    mockLoad({ language: null, theme: null });
    render(Settings);
    await screen.findByRole("group", { name: "Theme" });

    apiJSONMock.mockRejectedValueOnce(new Error("network error"));
    await fireEvent.click(screen.getByRole("button", { name: "Dark" }));

    expect(await screen.findByText("failed to save settings")).toBeTruthy();
  });
});
