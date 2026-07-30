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

// List/grid rendering, the view toggle, and favicon fallback are
// PageList's own concerns (see PageList.test.ts) -- this file only tests
// what TagDetail itself does: reading the :slug route param, requesting
// the right endpoint, showing the tag's name as a heading, and surfacing
// load errors, same division of labor as Library.test.ts/PageList.test.ts.
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/svelte";

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
import type { Page, Tag } from "../lib/types";
import TagDetail from "./TagDetail.svelte";

const apiJSONMock = vi.mocked(apiJSON);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
});

const recipesTag: Tag = { id: 1, name: "recipes", slug: "recipes" };

const examplePage: Page = {
  id: 5,
  normalized_url: "example.com/lasagna",
  title: "Lasagna recipe",
  latest_capture_at: "2026-05-01T12:00:00Z",
  excluded_from_mirror: false,
  favicon_path: null,
  notes: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

function mockLoad(tag: Tag, pages: Page[] = []) {
  apiJSONMock.mockImplementation((path: string) => {
    if (path === `/tags/${tag.slug}/pages`)
      return Promise.resolve({ tag, pages });
    throw new Error(`unexpected apiJSON call: ${path}`);
  });
}

describe("TagDetail", () => {
  it("requests the right endpoint for the routed tag slug", async () => {
    mockLoad(recipesTag, [examplePage]);
    render(TagDetail, { params: { slug: "recipes" } });

    expect(await screen.findByText("Lasagna recipe")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith("/tags/recipes/pages");
  });

  it("shows a loading state, then the tag's name as a heading and its pages", async () => {
    mockLoad(recipesTag, [examplePage]);
    render(TagDetail, { params: { slug: "recipes" } });

    expect(screen.getByText("Loading…")).toBeTruthy();

    expect(
      await screen.findByRole("heading", { name: "recipes" }),
    ).toBeTruthy();
    expect(screen.getByText("Lasagna recipe")).toBeTruthy();
  });

  it("shows the PageList empty state when the tag has no pages", async () => {
    mockLoad(recipesTag, []);
    render(TagDetail, { params: { slug: "recipes" } });

    expect(await screen.findByText("No pages have this tag yet.")).toBeTruthy();
  });

  it("shows the API's own error message when loading fails with ApiError", async () => {
    apiJSONMock.mockImplementation(() =>
      Promise.reject(new ApiError(404, "tag not found")),
    );
    render(TagDetail, { params: { slug: "nonexistent" } });

    expect(await screen.findByText("tag not found")).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError failure", async () => {
    apiJSONMock.mockImplementation(() =>
      Promise.reject(new Error("network error")),
    );
    render(TagDetail, { params: { slug: "recipes" } });

    expect(
      await screen.findByText("failed to load pages for this tag"),
    ).toBeTruthy();
  });

  it("links back to the tags list", async () => {
    mockLoad(recipesTag, []);
    render(TagDetail, { params: { slug: "recipes" } });

    const back = (await screen.findByRole("link", {
      name: /All tags/,
    })) as HTMLAnchorElement;
    expect(back.getAttribute("href")).toBe("#/tags");
  });
});
