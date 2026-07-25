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

// PageList takes `pages` and `emptyMessage` as plain props -- no fetching,
// no apiJSON mock needed here, unlike the route-level tests. Anything
// about *how* a list of pages ends up in front of this component (search,
// pagination, loading/error state) is Library's own concern and stays
// tested in Library.test.ts; this file is only about what PageList does
// with whatever `pages` array it's handed: list/grid rendering, the view
// toggle and its localStorage persistence, and the favicon/thumbnail
// fallback.
import { describe, it, expect, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";
import type { Page } from "../lib/types";
import PageList from "./PageList.svelte";

// Mirrors PageList.svelte's own private VIEW_MODE_KEY constant -- not
// exported, so duplicated here, same as Library.test.ts used to do.
const VIEW_MODE_KEY = "recueil:library-view-mode";

afterEach(() => {
  cleanup();
  localStorage.clear();
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

const otherPage: Page = {
  ...examplePage,
  id: 2,
  normalized_url: "example.com/other-article",
  title: "Another article",
};

describe("PageList", () => {
  it("renders pages in list view by default, with favicon, title, and url", () => {
    const { container } = render(PageList, {
      pages: [examplePage],
      emptyMessage: "Nothing here.",
    });

    expect(screen.getByText("An example article")).toBeTruthy();
    expect(screen.getByText("example.com/article")).toBeTruthy();
    const img = container.querySelector<HTMLImageElement>(".favicon");
    expect(img?.src).toContain("/api/pages/1/favicon");
  });

  it("falls back to the normalized URL as the title when there's none", () => {
    render(PageList, {
      pages: [{ ...examplePage, title: null }],
      emptyMessage: "Nothing here.",
    });

    expect(
      screen.getByRole("link", { name: /example\.com\/article/ }),
    ).toBeTruthy();
  });

  it("shows the empty message when there are no pages", () => {
    render(PageList, { pages: [], emptyMessage: "Nothing here." });

    expect(screen.getByText("Nothing here.")).toBeTruthy();
  });

  it("switches to grid view and persists the choice to localStorage", async () => {
    const { container } = render(PageList, {
      pages: [examplePage],
      emptyMessage: "Nothing here.",
    });

    await fireEvent.click(screen.getByRole("button", { name: "Grid" }));

    expect(localStorage.getItem(VIEW_MODE_KEY)).toBe("grid");
    // Grid view's own thumbnail markup replaces the list view's favicon
    // markup for the same page. alt="" is intentional on both (they're
    // decorative, with the title as a text sibling), which also means
    // neither gets an accessible "img" role -- queried by class through
    // the container instead of screen.getByRole for that reason.
    expect(screen.getByText("An example article")).toBeTruthy();
    const img = container.querySelector<HTMLImageElement>(".thumbnail");
    expect(img?.src).toContain("/thumbnail");
  });

  it("starts in grid view when that was the last-persisted choice", () => {
    localStorage.setItem(VIEW_MODE_KEY, "grid");
    const { container } = render(PageList, {
      pages: [examplePage],
      emptyMessage: "Nothing here.",
    });

    const img = container.querySelector<HTMLImageElement>(".thumbnail");
    expect(img?.src).toContain("/thumbnail");
  });

  it("shows a placeholder instead of a broken favicon image", async () => {
    const { container } = render(PageList, {
      pages: [examplePage],
      emptyMessage: "Nothing here.",
    });

    const img = container.querySelector<HTMLImageElement>(".favicon");
    expect(img).toBeTruthy();
    await fireEvent.error(img as HTMLImageElement);

    expect(container.querySelector(".favicon")).toBeNull();
    expect(container.querySelector(".favicon-placeholder")).toBeTruthy();
  });

  it("shows a placeholder with the title's first letter instead of a broken thumbnail image", async () => {
    localStorage.setItem(VIEW_MODE_KEY, "grid");
    const { container } = render(PageList, {
      pages: [examplePage],
      emptyMessage: "Nothing here.",
    });

    const img = container.querySelector<HTMLImageElement>(".thumbnail");
    expect(img).toBeTruthy();
    await fireEvent.error(img as HTMLImageElement);

    expect(container.querySelector(".thumbnail")).toBeNull();
    const placeholder = container.querySelector(".thumbnail-placeholder");
    expect(placeholder?.textContent).toBe("A");
  });

  it("clears previously-failed image state when the pages prop changes", async () => {
    const { container, rerender } = render(PageList, {
      pages: [examplePage],
      emptyMessage: "Nothing here.",
    });

    const img = container.querySelector<HTMLImageElement>(".favicon");
    await fireEvent.error(img as HTMLImageElement);
    expect(container.querySelector(".favicon-placeholder")).toBeTruthy();

    // A fresh pages array (as a caller would pass after, say, navigating
    // to a different tag) shouldn't carry over the previous array's
    // failed-image bookkeeping, even for a page id that happens to
    // reappear.
    await rerender({ pages: [otherPage], emptyMessage: "Nothing here." });
    expect(container.querySelector(".favicon-placeholder")).toBeNull();
    expect(container.querySelector(".favicon")).toBeTruthy();

    await rerender({ pages: [examplePage], emptyMessage: "Nothing here." });
    expect(container.querySelector(".favicon-placeholder")).toBeNull();
    expect(container.querySelector(".favicon")).toBeTruthy();
  });
});
