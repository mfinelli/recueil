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

// Same AppHeader/apiJSON mocking approach as Library.test.ts, which this
// mirrors: this file is about what the Links page itself does -- fetching,
// loading/error state, search debounce (a real 300ms setTimeout, hence the
// fake timers in the one test that exercises it, restored to real in the
// shared afterEach in case a test fails mid-block) and pagination. Row
// rendering lives in LinkList.test.ts.
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";

// router is replaced by a plain object so a test can choose the page's query
// string ("tag=rust") before rendering; push is captured for clearing a filter.
//
// The object needs more than querystring: svelte-spa-router's entry file IS
// Router.svelte, and its `active` action (which AppHeader uses, so it runs in
// every test here) imports `router` from that same module and reads
// router.loc.location in an effect. Replacing `router` replaces that import too,
// so without a `loc` the effect throws an uncaught TypeError that fails the run
// even though every test passes.
vi.mock("svelte-spa-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("svelte-spa-router")>();
  return {
    ...actual,
    push: vi.fn(),
    router: {
      querystring: "",
      location: "/links",
      loc: { location: "/links", querystring: "" },
    },
  };
});

vi.stubGlobal(
  "fetch",
  vi.fn().mockResolvedValue(new Response("{}", { status: 200 })),
);

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, apiJSON: vi.fn() };
});

import { push, router } from "svelte-spa-router";
import { apiJSON, ApiError } from "../lib/api";
import type { Link, LinkListResponse } from "../lib/types";
import Links from "./Links.svelte";

const apiJSONMock = vi.mocked(apiJSON);

const pushMock = vi.mocked(push);
const mockedRouter = router as unknown as { querystring: string };

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  pushMock.mockClear();
  mockedRouter.querystring = "";
  vi.useRealTimers();
});

const exampleLink: Link = {
  id: 1,
  raw_url: "https://example.com/video",
  normalized_url: "https://example.com/video",
  title: "An example video",
  favicon_path: null,
  notes: null,
  source: "extension",
  excluded_from_mirror: false,
  matching_page_id: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

function mockLoad(
  links: Link[] = [],
  total = links.length,
  extra: Partial<LinkListResponse> = {},
) {
  apiJSONMock.mockImplementation((path: string) => {
    if (path.startsWith("/links?"))
      return Promise.resolve({ links, total, ...extra });
    throw new Error(`unexpected apiJSON call: ${path}`);
  });
}

describe("Links", () => {
  it("shows a loading state, then the list", async () => {
    mockLoad([exampleLink]);
    render(Links);

    expect(screen.getByText("Loading…")).toBeTruthy();

    expect(await screen.findByText("An example video")).toBeTruthy();
    expect(screen.getByText("https://example.com/video")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith("/links?limit=50&offset=0");
  });

  it("titles the screen and offers a search box", async () => {
    mockLoad([exampleLink]);
    render(Links);
    await screen.findByText("An example video");

    expect(screen.getAllByText("Links").some((el) => el.tagName === "P")).toBe(
      true,
    );
    expect(screen.getByLabelText("Search")).toBeTruthy();
    expect(screen.getByPlaceholderText("Search your links…")).toBeTruthy();
  });

  it("opens the Add link form from the toolbar, beside the search box", async () => {
    mockLoad([exampleLink]);
    render(Links);
    await screen.findByText("An example video");
    expect(screen.queryByRole("dialog")).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Add link" }));

    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Add a link" })).toBeTruthy();
  });

  it("has no view toggle: a link has no thumbnail, so there is only a list", async () => {
    mockLoad([exampleLink]);
    render(Links);
    await screen.findByText("An example video");

    expect(screen.queryByRole("button", { name: /grid/i })).toBeNull();
  });

  it("shows a nothing-saved placeholder, with the hint, when there are no links and no search", async () => {
    mockLoad([]);
    render(Links);

    expect(await screen.findByText("No links saved yet.")).toBeTruthy();
    expect(
      screen.getByText(
        'Add one here, or use "Save as link" in the browser extension.',
      ),
    ).toBeTruthy();
    expect(screen.queryByText(/of \d+/)).toBeNull();
  });

  it("shows an error, not the empty state, when loading fails", async () => {
    apiJSONMock.mockRejectedValue(new ApiError(500, "boom"));
    render(Links);

    expect(await screen.findByText("boom")).toBeTruthy();
    expect(screen.queryByText("No links saved yet.")).toBeNull();
  });

  it("falls back to a generic message for a non-API error", async () => {
    apiJSONMock.mockRejectedValue(new Error("network down"));
    render(Links);

    expect(await screen.findByText("failed to load links")).toBeTruthy();
  });

  it("searches after a debounce, resets to the first page, and says so when nothing matches", async () => {
    mockLoad([exampleLink]);
    render(Links);
    await screen.findByText("An example video");

    vi.useFakeTimers();
    const input = screen.getByLabelText("Search");
    await fireEvent.input(input, { target: { value: "nope" } });

    // Not yet: the debounce hasn't elapsed.
    expect(apiJSONMock).toHaveBeenCalledTimes(1);

    mockLoad([]);
    await vi.advanceTimersByTimeAsync(300);
    vi.useRealTimers();

    expect(await screen.findByText("No links match your search.")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenLastCalledWith(
      "/links?limit=50&offset=0&q=nope",
    );
    // A search result has no "add one" hint.
    expect(screen.queryByText(/Save as link/)).toBeNull();
  });

  it("collapses a burst of keystrokes into one request", async () => {
    mockLoad([exampleLink]);
    render(Links);
    await screen.findByText("An example video");

    vi.useFakeTimers();
    const input = screen.getByLabelText("Search");
    for (const value of ["a", "ab", "abc"]) {
      await fireEvent.input(input, { target: { value } });
      await vi.advanceTimersByTimeAsync(100);
    }
    await vi.advanceTimersByTimeAsync(300);
    vi.useRealTimers();

    await screen.findByText("An example video");
    const searches = apiJSONMock.mock.calls.filter(([p]) =>
      String(p).includes("q="),
    );
    expect(searches).toEqual([["/links?limit=50&offset=0&q=abc"]]);
  });

  describe("pagination", () => {
    function manyLinks(n: number): Link[] {
      return Array.from({ length: n }, (_, i) => ({
        ...exampleLink,
        id: i + 1,
        title: `Link ${i + 1}`,
        normalized_url: `https://example.com/${i + 1}`,
      }));
    }

    it("shows the range and total, with Previous disabled on the first page", async () => {
      mockLoad(manyLinks(50), 120);
      render(Links);
      await screen.findByText("Link 1");

      expect(screen.getByText("1–50 of 120")).toBeTruthy();
      expect(
        (screen.getByRole("button", { name: /Previous/ }) as HTMLButtonElement)
          .disabled,
      ).toBe(true);
      expect(
        (screen.getByRole("button", { name: /Next/ }) as HTMLButtonElement)
          .disabled,
      ).toBe(false);
    });

    it("loads the next page at the next offset", async () => {
      mockLoad(manyLinks(50), 120);
      render(Links);
      await screen.findByText("Link 1");

      await fireEvent.click(screen.getByRole("button", { name: /Next/ }));

      await screen.findByText("51–100 of 120");
      expect(apiJSONMock).toHaveBeenLastCalledWith("/links?limit=50&offset=50");
    });

    it("disables Next on the last page, and Previous steps back", async () => {
      mockLoad(manyLinks(20), 120);
      render(Links);
      await screen.findByText("Link 1");
      await fireEvent.click(screen.getByRole("button", { name: /Next/ }));
      await fireEvent.click(screen.getByRole("button", { name: /Next/ }));
      await screen.findByText("101–120 of 120");

      expect(
        (screen.getByRole("button", { name: /Next/ }) as HTMLButtonElement)
          .disabled,
      ).toBe(true);

      await fireEvent.click(screen.getByRole("button", { name: /Previous/ }));
      await screen.findByText("51–100 of 120");
    });

    it("shows no pagination when there's nothing to paginate", async () => {
      mockLoad([]);
      render(Links);
      await screen.findByText("No links saved yet.");
      expect(screen.queryByRole("button", { name: /Next/ })).toBeNull();
    });
  });
});

describe("Links: filtering by tag or collection", () => {
  it("asks for the tag in the URL, and says so with a pill naming it", async () => {
    mockedRouter.querystring = "tag=rust";
    mockLoad([exampleLink], 1, { tag: { id: 4, name: "Rust", slug: "rust" } });
    render(Links);

    expect(await screen.findByText("An example video")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith(
      "/links?limit=50&offset=0&tag=rust",
    );
    expect(screen.getByText("Filtered by tag")).toBeTruthy();
    expect(screen.getByText("Rust")).toBeTruthy();
  });

  it("asks for the collection in the URL, and names it", async () => {
    mockedRouter.querystring = "collection=12";
    mockLoad([exampleLink], 1, {
      collection: { id: 12, name: "Reading list", parent_id: null },
    });
    render(Links);

    expect(await screen.findByText("An example video")).toBeTruthy();
    expect(apiJSONMock).toHaveBeenCalledWith(
      "/links?limit=50&offset=0&collection=12",
    );
    expect(screen.getByText("Filtered by collection")).toBeTruthy();
    expect(screen.getByText("Reading list")).toBeTruthy();
  });

  it("has no pill without a filter", async () => {
    mockLoad([exampleLink]);
    render(Links);
    await screen.findByText("An example video");
    expect(screen.queryByText(/Filtered by/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Clear filter" })).toBeNull();
  });

  it("clears the filter by going back to the unfiltered list", async () => {
    mockedRouter.querystring = "tag=rust";
    mockLoad([exampleLink], 1, { tag: { id: 4, name: "Rust", slug: "rust" } });
    render(Links);
    await screen.findByText("Rust");

    await fireEvent.click(screen.getByRole("button", { name: "Clear filter" }));

    expect(pushMock).toHaveBeenCalledWith("/links");
  });

  it("keeps the filter when searching and paginating", async () => {
    mockedRouter.querystring = "tag=rust";
    mockLoad(
      Array.from({ length: 50 }, (_, i) => ({
        ...exampleLink,
        id: i + 1,
        title: `Link ${i + 1}`,
      })),
      120,
      { tag: { id: 4, name: "Rust", slug: "rust" } },
    );
    render(Links);
    await screen.findByText("Link 1");

    await fireEvent.click(screen.getByRole("button", { name: /Next/ }));
    await screen.findByText("51–100 of 120");
    expect(apiJSONMock).toHaveBeenLastCalledWith(
      "/links?limit=50&offset=50&tag=rust",
    );
  });

  it("shows a filter-specific empty message, without the add-one hint", async () => {
    mockedRouter.querystring = "tag=rust";
    mockLoad([], 0, { tag: { id: 4, name: "Rust", slug: "rust" } });
    render(Links);

    expect(await screen.findByText("No links here yet.")).toBeTruthy();
    expect(screen.queryByText(/Save as link/)).toBeNull();
    expect(screen.queryByText("No links saved yet.")).toBeNull();
    // The pill still says what is empty, and can be cleared.
    expect(screen.getByRole("button", { name: "Clear filter" })).toBeTruthy();
  });

  it("shows the server's error, and no pill, for a tag that doesn't exist", async () => {
    mockedRouter.querystring = "tag=gone";
    apiJSONMock.mockRejectedValue(new ApiError(404, "tag not found"));
    render(Links);

    expect(await screen.findByText("tag not found")).toBeTruthy();
    expect(screen.queryByText(/Filtered by/)).toBeNull();
  });
});
