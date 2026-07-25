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

// Biggest screen so far, both in lines and in write actions -- same
// mocking shape as Collections.test.ts/Devices.test.ts throughout
// (AppHeader/svelte-spa-router/fetch, path+method-dispatching apiJSON
// mock for the mount-time loads, mockResolvedValueOnce/
// mockRejectedValueOnce layered on top per action). Three things specific
// to this screen:
//  - the mount-time $effect fires three parallel calls via
//    Promise.allSettled (page, collections, language options) --
//    collections/languages are best-effort (see this file's own top
//    comment), so mockLoad() below lets each fail independently without
//    the others being affected.
//  - createAndAddCollection makes two sequential apiJSON calls (create,
//    then link) -- its own tests queue two mockResolvedValueOnce/
//    mockRejectedValueOnce in call order rather than one.
//  - two write paths (mirror toggle, capture language) turned up a real,
//    if minor, finding while writing their failure-path tests: their
//    controls' one-way bindings (`checked={page.excluded_from_mirror}`,
//    `value={capture.language}`) only re-write the DOM when the
//    underlying $state value actually changes, so a failed write -- which
//    leaves that value untouched -- never corrects the browser's own
//    native pre-onchange DOM mutation. The control visually stays at
//    whatever the user picked, alongside the error message, rather than
//    reverting. See each test's own comment for specifics.
import { describe, it, expect, vi, afterEach } from "vitest";
import {
  render,
  screen,
  fireEvent,
  cleanup,
  within,
} from "@testing-library/svelte";

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
import type { CaptureSummary, Collection, PageDetail } from "../lib/types";
import PageDetailRoute from "./PageDetail.svelte";

const apiJSONMock = vi.mocked(apiJSON);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
});

const exampleCapture: CaptureSummary = {
  id: 100,
  source: "extension",
  raw_url: "https://example.com/article",
  title: "An example article",
  thumbnail_path: null,
  language: "en",
  html_compressed_size_bytes: 1024,
  html_uncompressed_size_bytes: 2048,
  captured_at: "2026-05-01T12:00:00Z",
};

const articlesCollection: Collection = {
  id: 5,
  parent_id: null,
  name: "Articles",
  created_at: "2026-05-01T12:00:00Z",
};

const recipesCollection: Collection = {
  id: 6,
  parent_id: null,
  name: "Recipes",
  created_at: "2026-05-01T12:00:00Z",
};

const basePage: PageDetail = {
  id: 42,
  normalized_url: "example.com/article",
  title: "An example article",
  latest_capture_at: "2026-05-01T12:00:00Z",
  excluded_from_mirror: false,
  favicon_path: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
  captures: [exampleCapture],
  tags: [{ id: 1, name: "reading", source: "manual" }],
  collections: [{ id: 5, name: "Articles", parent_id: null }],
};

type LoadOptions = {
  id?: string;
  page?: PageDetail;
  pageError?: unknown;
  collections?: Collection[];
  collectionsError?: unknown;
  languages?: string[];
  languagesError?: unknown;
};

function mockLoad({
  id = "42",
  page = basePage,
  pageError,
  collections = [articlesCollection, recipesCollection],
  collectionsError,
  languages = [],
  languagesError,
}: LoadOptions = {}) {
  apiJSONMock.mockImplementation(
    (path: string, options?: { method?: string }) => {
      const method = options?.method ?? "GET";
      if (path === `/pages/${id}` && method === "GET") {
        return pageError ? Promise.reject(pageError) : Promise.resolve(page);
      }
      if (path === "/collections" && method === "GET") {
        return collectionsError
          ? Promise.reject(collectionsError)
          : Promise.resolve({ collections });
      }
      if (path === "/text-search-configs" && method === "GET") {
        return languagesError
          ? Promise.reject(languagesError)
          : Promise.resolve({ languages });
      }
      throw new Error(`unexpected apiJSON call: ${method} ${path}`);
    },
  );
}

function render42(overrides: LoadOptions = {}) {
  mockLoad(overrides);
  return render(PageDetailRoute, { params: { id: "42" } });
}

describe("PageDetail", () => {
  it("shows a loading state, then the page, tags, collections, and captures", async () => {
    render42();

    expect(screen.getByText("Loading…")).toBeTruthy();

    expect(
      await screen.findByRole("heading", { name: "An example article" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "example.com/article" }),
    ).toBeTruthy();
    expect(
      screen.getByRole("checkbox", {
        name: "Exclude from bookmark-list mirror",
      }),
    ).toHaveProperty("checked", false);
    expect(screen.getByText("reading")).toBeTruthy();
    expect(screen.getByText("Articles")).toBeTruthy();
    // source/size formatted as "extension · 2.0 KB" for a 2048-byte
    // capture.
    expect(screen.getByText(/extension · 2\.0 KB/)).toBeTruthy();
  });

  it("shows the API's own error message when loading the page fails with ApiError", async () => {
    render42({ pageError: new ApiError(404, "page not found") });

    expect(await screen.findByText("page not found")).toBeTruthy();
  });

  it("falls back to a generic error message for a non-ApiError load failure", async () => {
    render42({ pageError: new Error("network error") });

    expect(await screen.findByText("failed to load page")).toBeTruthy();
  });

  it("adds a tag, keeping the list sorted, and clears the input", async () => {
    render42();
    await screen.findByText("reading");

    apiJSONMock.mockResolvedValueOnce({ id: 2, name: "archive" });
    const input = screen.getByPlaceholderText("Add a tag…");
    await fireEvent.input(input, { target: { value: "archive" } });
    const tagForm = input.closest("form") as HTMLElement;
    await fireEvent.click(within(tagForm).getByRole("button", { name: "Add" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42/tags", {
      method: "POST",
      body: { name: "archive" },
    });
    expect(await screen.findByText("archive")).toBeTruthy();
    expect(input).toHaveProperty("value", "");
  });

  it("shows the API's own error message when adding a tag fails", async () => {
    render42();
    await screen.findByText("reading");

    apiJSONMock.mockRejectedValueOnce(new ApiError(400, "invalid tag name"));
    const input = screen.getByPlaceholderText("Add a tag…");
    await fireEvent.input(input, { target: { value: "???" } });
    const tagForm = input.closest("form") as HTMLElement;
    await fireEvent.click(within(tagForm).getByRole("button", { name: "Add" }));

    expect(await screen.findByText("invalid tag name")).toBeTruthy();
  });

  it("removes a tag", async () => {
    render42();
    await screen.findByText("reading");

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(
      screen.getByRole("button", { name: "Remove tag reading" }),
    );

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42/tags/1", {
      method: "DELETE",
    });
    expect(screen.queryByText("reading")).toBeNull();
  });

  it("adds an existing collection via the picker, then hides it once every collection is linked", async () => {
    render42();
    await screen.findByText("Articles");

    apiJSONMock.mockResolvedValueOnce(undefined);
    const select = screen.getByRole("combobox", {
      name: "",
    }) as HTMLSelectElement;
    const pickerForm = select.closest("form") as HTMLElement;
    await fireEvent.change(select, { target: { value: "6" } });
    await fireEvent.click(
      within(pickerForm).getByRole("button", { name: "Add" }),
    );

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42/collections", {
      method: "POST",
      body: { collection_id: 6 },
    });
    expect(await screen.findByText("Recipes")).toBeTruthy();
    // Both of this page's only two candidate collections are now linked,
    // so the "add existing" picker has nothing left to offer and
    // disappears entirely.
    expect(screen.queryByPlaceholderText("Add to a collection…")).toBeNull();
  });

  it("removes a collection", async () => {
    render42();
    await screen.findByText("Articles");

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(
      screen.getByRole("button", { name: "Remove from Articles" }),
    );

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42/collections/5", {
      method: "DELETE",
    });
    // Not queryByText("Articles") -- once unlinked, "Articles" legitimately
    // reappears as an option in the "add existing" picker, so the chip's
    // own remove button (gone once the chip itself is gone) is the
    // unambiguous signal here.
    expect(
      screen.queryByRole("button", { name: "Remove from Articles" }),
    ).toBeNull();
  });

  it("creates a new collection and links it in one action", async () => {
    render42();
    await screen.findByText("Articles");

    const created: Collection = {
      id: 7,
      parent_id: null,
      name: "Cooking",
      created_at: "2026-05-01T12:00:00Z",
    };
    apiJSONMock.mockResolvedValueOnce(created).mockResolvedValueOnce(undefined);
    const input = screen.getByPlaceholderText("Or create a new collection…");
    await fireEvent.input(input, { target: { value: "Cooking" } });
    await fireEvent.click(screen.getByRole("button", { name: "Create & add" }));

    expect(apiJSONMock).toHaveBeenNthCalledWith(4, "/collections", {
      method: "POST",
      body: { name: "Cooking" },
    });
    expect(apiJSONMock).toHaveBeenNthCalledWith(5, "/pages/42/collections", {
      method: "POST",
      body: { collection_id: 7 },
    });
    // waitFor rather than a single findByRole: the chip appearing and
    // newCollectionName clearing are two separate statements after the
    // same await, a microtask apart -- polling for both together avoids
    // asserting on the gap between them.
    await vi.waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Remove from Cooking" }),
      ).toBeTruthy();
      expect(
        screen.getByPlaceholderText("Or create a new collection…"),
      ).toHaveProperty("value", "");
    });
  });

  it("shows the API's own error message when create-and-add fails on the create step", async () => {
    render42();
    await screen.findByText("Articles");

    apiJSONMock.mockRejectedValueOnce(new ApiError(400, "name already in use"));
    await fireEvent.input(
      screen.getByPlaceholderText("Or create a new collection…"),
      { target: { value: "Articles" } },
    );
    await fireEvent.click(screen.getByRole("button", { name: "Create & add" }));

    expect(await screen.findByText("name already in use")).toBeTruthy();
  });

  it("toggles the mirror-exclusion checkbox on success", async () => {
    render42();
    const checkbox = await screen.findByRole("checkbox", {
      name: "Exclude from bookmark-list mirror",
    });

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(checkbox);

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42", {
      method: "PATCH",
      body: { excluded_from_mirror: true },
    });
    expect(checkbox).toHaveProperty("checked", true);
  });

  it("shows an error when the mirror toggle fails, but doesn't correct the checkbox's now-stale checked state", async () => {
    render42();
    const checkbox = await screen.findByRole("checkbox", {
      name: "Exclude from bookmark-list mirror",
    });

    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "update rejected"));
    await fireEvent.click(checkbox);

    expect(await screen.findByText("update rejected")).toBeTruthy();
    // Genuine finding, not a test artifact: page.excluded_from_mirror is
    // only ever written on success, and Svelte's one-way
    // `checked={page.excluded_from_mirror}` binding only re-writes the
    // DOM when that underlying value actually *changes* -- a failed
    // write leaves it exactly as it was, so there's no dependency change
    // to trigger a re-sync. The browser's own native click-toggle (which
    // sets `checked` before onchange even fires, independent of Svelte)
    // is therefore left uncorrected: the box visually stays checked
    // until something unrelated causes this section to re-render. Minor,
    // since actionError is shown alongside it, but worth knowing about
    // for any other one-way-bound control following this same pattern.
    expect(checkbox).toHaveProperty("checked", true);
  });

  it("updates a capture's language on success", async () => {
    render42({ languages: ["en", "fr", "de"] });
    const select = await screen.findByRole("combobox", { name: "Language" });

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.change(select, { target: { value: "fr" } });

    expect(apiJSONMock).toHaveBeenCalledWith("/captures/100/language", {
      method: "PATCH",
      body: { language: "fr" },
    });
    expect(select).toHaveProperty("value", "fr");
  });

  it("shows an error when the language update fails, but doesn't correct the select's now-stale value", async () => {
    render42({ languages: ["en", "fr", "de"] });
    const select = await screen.findByRole("combobox", { name: "Language" });

    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "update rejected"));
    await fireEvent.change(select, { target: { value: "fr" } });

    expect(await screen.findByText("update rejected")).toBeTruthy();
    // Same finding as the mirror-toggle test above: capture.language is
    // only written on success, so the one-way `value={capture.language}`
    // binding never re-fires on a failed update (the value it's tracking
    // never changed), leaving the <select>'s own DOM value at whatever
    // the change event already set it to ("fr") rather than reverting to
    // the still-actual "en".
    expect(select).toHaveProperty("value", "fr");
  });
});
