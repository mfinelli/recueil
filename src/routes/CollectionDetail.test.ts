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

// Path resolution here is entirely client-side against the flat
// GET /collections list (see CollectionDetail.svelte's own top-of-file
// comment for why) -- these tests exercise that walk directly through
// the component's props/route param, not through a mocked resolver.
// List/grid rendering, the view toggle, and favicon fallback are
// PageList's own concerns (see PageList.test.ts).
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
import type { Collection, Page } from "../lib/types";
import CollectionDetail from "./CollectionDetail.svelte";

const apiJSONMock = vi.mocked(apiJSON);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
});

const cooking: Collection = {
  id: 1,
  parent_id: null,
  name: "Cooking",
  slug: "cooking",
  description: "Recipes and kitchen notes.",
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};
const recipes: Collection = {
  id: 2,
  parent_id: 1,
  name: "Recipes",
  slug: "recipes",
  description: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};
const baking: Collection = {
  id: 3,
  parent_id: 1,
  name: "Baking",
  slug: "baking",
  description: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

const examplePage: Page = {
  id: 9,
  normalized_url: "example.com/lasagna",
  title: "Lasagna recipe",
  latest_capture_at: "2026-05-01T12:00:00Z",
  excluded_from_mirror: false,
  favicon_path: null,
  notes: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

function mockLoad(
  collections: Collection[],
  pagesByCollectionId: Record<number, Page[]> = {},
) {
  apiJSONMock.mockImplementation((path: string) => {
    if (path === "/collections") return Promise.resolve({ collections });
    const match = /^\/collections\/(\d+)\/pages$/.exec(path);
    if (match) {
      const id = Number(match[1]);
      return Promise.resolve({ pages: pagesByCollectionId[id] ?? [] });
    }
    throw new Error(`unexpected apiJSON call: ${path}`);
  });
}

describe("CollectionDetail", () => {
  it("resolves a single-segment path and shows the collection's name and description", async () => {
    mockLoad([cooking]);
    render(CollectionDetail, { params: { wild: "cooking" } });

    expect(
      await screen.findByRole("heading", { name: "Cooking" }),
    ).toBeTruthy();
    expect(screen.getByText("Recipes and kitchen notes.")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith("/collections/1/pages");
  });

  it("resolves a nested path by walking segments against slug and parent_id", async () => {
    mockLoad([cooking, recipes]);
    render(CollectionDetail, { params: { wild: "cooking/recipes" } });

    expect(
      await screen.findByRole("heading", { name: "Recipes" }),
    ).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith("/collections/2/pages");
  });

  it("shows a breadcrumb with links to each ancestor for a nested collection", async () => {
    mockLoad([cooking, recipes]);
    render(CollectionDetail, { params: { wild: "cooking/recipes" } });

    await screen.findByRole("heading", { name: "Recipes" });
    const ancestorLink = screen.getByRole("link", {
      name: "Cooking",
    }) as HTMLAnchorElement;
    expect(ancestorLink.getAttribute("href")).toBe("#/collections/cooking");
  });

  it("doesn't show a breadcrumb for a top-level collection", async () => {
    mockLoad([cooking]);
    render(CollectionDetail, { params: { wild: "cooking" } });

    await screen.findByRole("heading", { name: "Cooking" });
    expect(screen.queryByRole("navigation", { name: "Breadcrumb" })).toBeNull();
  });

  it("lists sub-collections as their own links, sorted alphabetically", async () => {
    mockLoad([cooking, recipes, baking]);
    render(CollectionDetail, { params: { wild: "cooking" } });

    await screen.findByRole("heading", { name: "Cooking" });
    const bakingLink = screen.getByRole("link", {
      name: "Baking",
    }) as HTMLAnchorElement;
    expect(bakingLink.getAttribute("href")).toBe(
      "#/collections/cooking/baking",
    );
    const recipesLink = screen.getByRole("link", {
      name: "Recipes",
    }) as HTMLAnchorElement;
    expect(recipesLink.getAttribute("href")).toBe(
      "#/collections/cooking/recipes",
    );

    // Alphabetical: Baking before Recipes.
    const links = screen
      .getAllByRole("link")
      .map((el) => el.textContent?.trim());
    expect(links.indexOf("Baking")).toBeLessThan(links.indexOf("Recipes"));
  });

  it("shows the collection's own direct pages, not its subtree", async () => {
    mockLoad([cooking, recipes], { 1: [examplePage] });
    render(CollectionDetail, { params: { wild: "cooking" } });

    expect(await screen.findByText("Lasagna recipe")).toBeTruthy();
  });

  it("shows a not-found message for a path that doesn't resolve", async () => {
    mockLoad([cooking]);
    render(CollectionDetail, { params: { wild: "nonexistent" } });

    expect(await screen.findByText("Collection not found.")).toBeTruthy();
  });

  it("shows a not-found message when a later segment doesn't match the resolved parent", async () => {
    mockLoad([cooking, recipes]);
    // "recipes" exists, but not as a top-level collection -- it's
    // cooking's child, so this path shouldn't resolve.
    render(CollectionDetail, { params: { wild: "recipes" } });

    expect(await screen.findByText("Collection not found.")).toBeTruthy();
  });

  it("shows the API's own error message when loading the collection list fails", async () => {
    apiJSONMock.mockImplementation(() =>
      Promise.reject(new ApiError(500, "collection store unavailable")),
    );
    render(CollectionDetail, { params: { wild: "cooking" } });

    expect(
      await screen.findByText("collection store unavailable"),
    ).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError pages failure", async () => {
    apiJSONMock.mockImplementation((path: string) => {
      if (path === "/collections")
        return Promise.resolve({ collections: [cooking] });
      return Promise.reject(new Error("network error"));
    });
    render(CollectionDetail, { params: { wild: "cooking" } });

    expect(
      await screen.findByText("failed to load pages for this collection"),
    ).toBeTruthy();
  });

  it("links back to the collections management screen", async () => {
    mockLoad([cooking]);
    render(CollectionDetail, { params: { wild: "cooking" } });

    const back = (await screen.findByRole("link", {
      name: /All collections/,
    })) as HTMLAnchorElement;
    expect(back.getAttribute("href")).toBe("#/collections");
  });
});
