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

// Same AppHeader/apiJSON mocking approach as the other route tests.
// List/grid rendering, the view toggle, and favicon/thumbnail fallback
// now live in PageList.test.ts, since that behavior moved to its own
// component -- this file keeps only what's actually Library's job:
// fetching, loading/error state, search debounce, and pagination. One
// thing still worth noting here: search is debounced via a real 300ms
// setTimeout, so the one test that exercises it uses fake timers
// (restored to real afterward, in the shared afterEach, in case a test
// fails mid-fake-timer-block).
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

import { apiJSON, ApiError } from "../lib/api";
import type { Page } from "../lib/types";
import Library from "./Library.svelte";

const apiJSONMock = vi.mocked(apiJSON);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  localStorage.clear();
  vi.useRealTimers();
});

const examplePage: Page = {
  id: 1,
  normalized_url: "example.com/article",
  title: "An example article",
  latest_capture_at: "2026-05-01T12:00:00Z",
  excluded_from_mirror: false,
  favicon_path: "/some/path.png",
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

function mockLoad(pages: Page[] = [], total = pages.length) {
  apiJSONMock.mockImplementation((path: string) => {
    if (path.startsWith("/pages?")) return Promise.resolve({ pages, total });
    throw new Error(`unexpected apiJSON call: ${path}`);
  });
}

describe("Library", () => {
  it("shows a loading state, then the page list", async () => {
    mockLoad([examplePage]);
    render(Library);

    expect(screen.getByText("Loading…")).toBeTruthy();

    expect(await screen.findByText("An example article")).toBeTruthy();
    expect(screen.getByText("example.com/article")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith("/pages?limit=50&offset=0");
  });

  it("shows a nothing-archived placeholder when there are no pages and no search", async () => {
    mockLoad([]);
    render(Library);

    expect(await screen.findByText("Nothing archived yet.")).toBeTruthy();
  });

  it("shows the API's own error message when loading fails with ApiError", async () => {
    apiJSONMock.mockImplementation(() =>
      Promise.reject(new ApiError(500, "search index unavailable")),
    );
    render(Library);

    expect(await screen.findByText("search index unavailable")).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError failure", async () => {
    apiJSONMock.mockImplementation(() =>
      Promise.reject(new Error("network error")),
    );
    render(Library);

    expect(await screen.findByText("failed to load pages")).toBeTruthy();
  });

  it("debounces search input, resets to the first page, and shows the search-specific empty state", async () => {
    vi.useFakeTimers();
    mockLoad([examplePage]);
    render(Library);
    await vi.waitFor(() => expect(apiJSONMock).toHaveBeenCalledTimes(1));

    mockLoad([]);
    await fireEvent.input(screen.getByLabelText("Search"), {
      target: { value: "nonexistent" },
    });
    // Not yet -- still within the 300ms debounce window.
    expect(apiJSONMock).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(300);

    expect(apiJSONMock).toHaveBeenLastCalledWith(
      "/pages?limit=50&offset=0&q=nonexistent",
    );
    expect(await screen.findByText("No pages match your search.")).toBeTruthy();
  });

  it("paginates forward and back, disabling Previous/Next at the edges", async () => {
    // 120 total with a page size of 50 -- enough for a real "next page"
    // to exist beyond the first 50.
    mockLoad([examplePage], 120);
    render(Library);
    await screen.findByText("1–50 of 120");

    expect(screen.getByRole("button", { name: "Previous" })).toHaveProperty(
      "disabled",
      true,
    );
    expect(screen.getByRole("button", { name: "Next" })).toHaveProperty(
      "disabled",
      false,
    );

    // Re-queried after every reload rather than held across it: the
    // loading-state flicker remounts the whole {#if loading}/{:else}
    // branch (including this pagination div), so a button reference
    // captured before a reload is a detached node by the time the next
    // click fires on it.
    await fireEvent.click(screen.getByRole("button", { name: "Next" }));

    expect(apiJSONMock).toHaveBeenLastCalledWith("/pages?limit=50&offset=50");
    expect(await screen.findByText("51–100 of 120")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Previous" })).toHaveProperty(
      "disabled",
      false,
    );

    await fireEvent.click(screen.getByRole("button", { name: "Previous" }));

    expect(apiJSONMock).toHaveBeenLastCalledWith("/pages?limit=50&offset=0");
    expect(await screen.findByText("1–50 of 120")).toBeTruthy();
  });
});
