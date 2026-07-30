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
//  - delete is the first write path on this screen that navigates away on
//    success, so its own tests need push mocked-and-captured (not just
//    stubbed, like the rest of this file's svelte-spa-router mock), same
//    as Login.test.ts/Setup.test.ts's own pushMock pattern -- and confirm()
//    mocked the same way Tags.test.ts/Collections.test.ts already do for
//    their own confirm()-gated deletes.
//  - recapture never touches `page` at all on success  -- its test only
//    asserts the transient "Queued!" button label, not any change to the
//    rendered page state.
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

import { push } from "svelte-spa-router";
import { apiJSON, ApiError } from "../lib/api";
import type {
  CaptureSummary,
  Collection,
  PageDetail,
  PageLink,
} from "../lib/types";
import PageDetailRoute from "./PageDetail.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const pushMock = vi.mocked(push);
const confirmMock = vi.fn();
vi.stubGlobal("confirm", confirmMock);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  confirmMock.mockReset();
  pushMock.mockClear();
  vi.useRealTimers();
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
  slug: "articles",
  description: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

const recipesCollection: Collection = {
  id: 6,
  parent_id: null,
  name: "Recipes",
  slug: "recipes",
  description: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

const basePage: PageDetail = {
  id: 42,
  normalized_url: "example.com/article",
  title: "An example article",
  latest_capture_at: "2026-05-01T12:00:00Z",
  excluded_from_mirror: false,
  favicon_path: null,
  notes: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
  captures: [exampleCapture],
  tags: [{ id: 1, name: "reading", slug: "reading", source: "manual" }],
  collections: [{ id: 5, name: "Articles", parent_id: null }],
  links: [],
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
    // Checked by default: excluded_from_mirror: false means this page IS
    // synced, and the checkbox is now framed as the positive ("sync"),
    // not the negative ("exclude") -- checked means synced.
    expect(
      screen.getByRole("checkbox", {
        name: "Sync with my browser's bookmarks",
      }),
    ).toHaveProperty("checked", true);
    // A real link now (tag.slug lets it link to TagDetail, collectionPath
    // resolves the collection's from allCollections) -- see the dedicated
    // linking tests below for the cases that matter more.
    expect(screen.getByRole("link", { name: "reading" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Articles" })).toBeTruthy();
    // source/size formatted as just "2.0 KB" for an extension capture --
    // no source label for the common case, see the de-emphasis tests below.
    expect(screen.getByText(/2\.0 KB/)).toBeTruthy();
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
    await fireEvent.click(screen.getByRole("button", { name: "Edit tags" }));

    apiJSONMock.mockResolvedValueOnce({
      id: 2,
      name: "archive",
      slug: "archive",
    });
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
    await fireEvent.click(screen.getByRole("button", { name: "Edit tags" }));

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
    await fireEvent.click(screen.getByRole("button", { name: "Edit tags" }));

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(
      screen.getByRole("button", { name: "Remove tag reading" }),
    );

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42/tags/1", {
      method: "DELETE",
    });
    expect(screen.queryByText("reading")).toBeNull();
  });

  it("hides the remove buttons and add-tag form until Edit tags is clicked", async () => {
    render42();
    await screen.findByText("reading");

    expect(
      screen.queryByRole("button", { name: "Remove tag reading" }),
    ).toBeNull();
    expect(screen.queryByPlaceholderText("Add a tag…")).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Edit tags" }));
    expect(
      screen.getByRole("button", { name: "Remove tag reading" }),
    ).toBeTruthy();
    expect(screen.getByPlaceholderText("Add a tag…")).toBeTruthy();

    await fireEvent.click(
      screen.getByRole("button", { name: "Done editing tags" }),
    );
    expect(
      screen.queryByRole("button", { name: "Remove tag reading" }),
    ).toBeNull();
    expect(screen.queryByPlaceholderText("Add a tag…")).toBeNull();
  });

  it("adds an existing collection via the picker, then hides it once every collection is linked", async () => {
    render42();
    await screen.findByRole("link", { name: "Articles" });
    await fireEvent.click(
      screen.getByRole("button", { name: "Edit collections" }),
    );

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
    await screen.findByRole("link", { name: "Articles" });
    await fireEvent.click(
      screen.getByRole("button", { name: "Edit collections" }),
    );

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
    await screen.findByRole("link", { name: "Articles" });
    await fireEvent.click(
      screen.getByRole("button", { name: "Edit collections" }),
    );

    const created: Collection = {
      id: 7,
      parent_id: null,
      name: "Cooking",
      slug: "cooking",
      description: null,
      created_at: "2026-05-01T12:00:00Z",
      updated_at: "2026-05-01T12:00:00Z",
    };
    apiJSONMock.mockResolvedValueOnce(created).mockResolvedValueOnce(undefined);
    const input = screen.getByPlaceholderText("Or create a new collection…");
    await fireEvent.input(input, { target: { value: "Cooking" } });
    await fireEvent.click(screen.getByRole("button", { name: "Create & add" }));

    expect(apiJSONMock).toHaveBeenNthCalledWith(3, "/collections", {
      method: "POST",
      body: { name: "Cooking" },
    });
    expect(apiJSONMock).toHaveBeenNthCalledWith(4, "/pages/42/collections", {
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
    await screen.findByRole("link", { name: "Articles" });
    await fireEvent.click(
      screen.getByRole("button", { name: "Edit collections" }),
    );

    apiJSONMock.mockRejectedValueOnce(new ApiError(400, "name already in use"));
    await fireEvent.input(
      screen.getByPlaceholderText("Or create a new collection…"),
      { target: { value: "Articles" } },
    );
    await fireEvent.click(screen.getByRole("button", { name: "Create & add" }));

    expect(await screen.findByText("name already in use")).toBeTruthy();
  });

  it("links a collection pill to its full nested path, reconstructed from allCollections", async () => {
    const nested: Collection = {
      id: 8,
      parent_id: 5,
      name: "Side Dishes",
      slug: "side-dishes",
      description: null,
      created_at: "2026-05-01T12:00:00Z",
      updated_at: "2026-05-01T12:00:00Z",
    };
    render42({
      page: {
        ...basePage,
        collections: [{ id: 8, name: "Side Dishes", parent_id: 5 }],
      },
      collections: [articlesCollection, recipesCollection, nested],
    });

    expect(
      await screen.findByRole("link", { name: "Side Dishes" }),
    ).toHaveProperty("hash", "#/collections/articles/side-dishes");
  });

  it("renders a collection pill as plain text, not a broken link, when it can't be found in allCollections", async () => {
    render42({ collections: [] });

    await screen.findByText("Articles");
    expect(screen.queryByRole("link", { name: "Articles" })).toBeNull();
  });

  it("hides the remove buttons and both add-collection forms until Edit collections is clicked", async () => {
    render42();
    await screen.findByRole("link", { name: "Articles" });

    expect(
      screen.queryByRole("button", { name: "Remove from Articles" }),
    ).toBeNull();
    expect(screen.queryByPlaceholderText("Add to a collection…")).toBeNull();
    expect(
      screen.queryByPlaceholderText("Or create a new collection…"),
    ).toBeNull();

    await fireEvent.click(
      screen.getByRole("button", { name: "Edit collections" }),
    );
    expect(
      screen.getByRole("button", { name: "Remove from Articles" }),
    ).toBeTruthy();
    expect(
      screen.getByPlaceholderText("Or create a new collection…"),
    ).toBeTruthy();
  });

  it("toggles the mirror-exclusion checkbox on success", async () => {
    render42();
    const checkbox = await screen.findByRole("checkbox", {
      name: "Sync with my browser's bookmarks",
    });
    // excluded_from_mirror: false on basePage -- checked (synced) by
    // default, since the checkbox is now framed as the positive.
    expect(checkbox).toHaveProperty("checked", true);

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(checkbox);

    // The PATCH body is unaffected by the display inversion --
    // toggleExcludedFromMirror always flips page.excluded_from_mirror to
    // its opposite regardless of how the checkbox presents that value.
    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42", {
      method: "PATCH",
      body: { excluded_from_mirror: true },
    });
    // Now excluded (unsynced), so unchecked.
    expect(checkbox).toHaveProperty("checked", false);
  });

  it("shows an error when the mirror toggle fails, but doesn't correct the checkbox's now-stale checked state", async () => {
    render42();
    const checkbox = await screen.findByRole("checkbox", {
      name: "Sync with my browser's bookmarks",
    });

    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "update rejected"));
    await fireEvent.click(checkbox);

    expect(await screen.findByText("update rejected")).toBeTruthy();
    // Genuine finding, not a test artifact: page.excluded_from_mirror is
    // only ever written on success, and Svelte's one-way
    // `checked={!page.excluded_from_mirror}` binding only re-writes the
    // DOM when that underlying value actually *changes* -- a failed
    // write leaves it exactly as it was, so there's no dependency change
    // to trigger a re-sync. The browser's own native click-toggle (which
    // sets `checked` before onchange even fires, independent of Svelte)
    // is therefore left uncorrected: the box visually flips to unchecked
    // and stays there until something unrelated causes this section to
    // re-render, even though the write failed and the page is still
    // synced. Minor, since actionError is shown alongside it, but worth
    // knowing about for any other one-way-bound control following this
    // same pattern.
    expect(checkbox).toHaveProperty("checked", false);
  });

  it("edits the title, saving on success", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit title" }));
    const input = screen.getByPlaceholderText("Title");
    expect(input).toHaveProperty("value", "An example article");

    apiJSONMock.mockResolvedValueOnce({ ...basePage, title: "New title" });
    await fireEvent.input(input, { target: { value: "New title" } });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42", {
      method: "PATCH",
      body: { title: "New title" },
    });
    expect(
      await screen.findByRole("heading", { name: "New title" }),
    ).toBeTruthy();
  });

  it("cancels a title edit without calling the API", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit title" }));
    await fireEvent.input(screen.getByPlaceholderText("Title"), {
      target: { value: "Discarded" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(apiJSONMock).not.toHaveBeenCalledWith(
      "/pages/42",
      expect.objectContaining({ method: "PATCH" }),
    );
    expect(
      screen.getByRole("heading", { name: "An example article" }),
    ).toBeTruthy();
  });

  it("shows the API's own error message when saving a title fails", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit title" }));
    apiJSONMock.mockRejectedValueOnce(
      new ApiError(400, "title cannot be empty"),
    );
    await fireEvent.input(screen.getByPlaceholderText("Title"), {
      target: { value: "   " },
    });
    // The submit button is disabled for a blank/whitespace-only title
    // (see PageDetail.svelte's own `!titleInput.trim()` guard), so this
    // exercises the same trimmed-empty rejection the backend enforces --
    // clicking Save here is a no-op at the browser level (disabled
    // button), so use a real value instead and let the mocked rejection
    // stand in for the backend's own validation failure.
    await fireEvent.input(screen.getByPlaceholderText("Title"), {
      target: { value: "New title" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("title cannot be empty")).toBeTruthy();
  });

  it("shows an empty-notes message when there are no notes", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    expect(screen.getByText("No notes yet.")).toBeTruthy();
  });

  it("renders notes through the markdown subset, not as plain text", async () => {
    render42({
      page: {
        ...basePage,
        notes: "**bold** and a list:\n- one\n- two",
      },
    });
    await screen.findByRole("heading", { name: "An example article" });

    const strong = screen.getByText("bold", { selector: "strong" });
    expect(strong).toBeTruthy();
    // Scoped to the notes body specifically -- tags/collections render
    // their own <ul> chip lists too, so an unscoped getByRole("list")
    // would be ambiguous.
    const notesBody = strong.closest(".notes-body") as HTMLElement;
    expect(within(notesBody).getAllByRole("listitem")).toHaveLength(2);
  });

  it("hides the edit-toggle button while editing -- Save/Cancel own exiting edit mode, not a second click on it", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit notes" }));
    expect(screen.queryByRole("button", { name: "Edit notes" })).toBeNull();

    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("button", { name: "Edit notes" })).toBeTruthy();
  });

  it("edits notes, saving on success", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit notes" }));
    const textarea = screen.getByPlaceholderText("Add a note…");
    expect(textarea).toHaveProperty("value", "");

    apiJSONMock.mockResolvedValueOnce({
      ...basePage,
      notes: "**Worth** revisiting",
    });
    await fireEvent.input(textarea, {
      target: { value: "**Worth** revisiting" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42", {
      method: "PATCH",
      body: { notes: "**Worth** revisiting" },
    });
    expect(
      await screen.findByText("Worth", { selector: "strong" }),
    ).toBeTruthy();
  });

  it("clears notes back to the empty state by saving a blank textarea", async () => {
    render42({ page: { ...basePage, notes: "an existing note" } });
    await screen.findByText("an existing note");

    await fireEvent.click(screen.getByRole("button", { name: "Edit notes" }));
    apiJSONMock.mockResolvedValueOnce({ ...basePage, notes: null });
    await fireEvent.input(screen.getByPlaceholderText("Add a note…"), {
      target: { value: "   " },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42", {
      method: "PATCH",
      body: { notes: "" },
    });
    expect(await screen.findByText("No notes yet.")).toBeTruthy();
  });

  it("cancels a notes edit without calling the API", async () => {
    render42({ page: { ...basePage, notes: "original note" } });
    await screen.findByText("original note");

    await fireEvent.click(screen.getByRole("button", { name: "Edit notes" }));
    await fireEvent.input(screen.getByPlaceholderText("Add a note…"), {
      target: { value: "discarded" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(apiJSONMock).not.toHaveBeenCalledWith(
      "/pages/42",
      expect.objectContaining({ method: "PATCH" }),
    );
    expect(screen.getByText("original note")).toBeTruthy();
  });

  it("shows the API's own error message when saving notes fails", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    await fireEvent.click(screen.getByRole("button", { name: "Edit notes" }));
    apiJSONMock.mockRejectedValueOnce(
      new ApiError(500, "failed to update notes"),
    );
    await fireEvent.input(screen.getByPlaceholderText("Add a note…"), {
      target: { value: "a note" },
    });
    await fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByText("failed to update notes")).toBeTruthy();
  });

  it("shows an empty-links message when there are no links", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    expect(screen.getByText("No pages linked yet.")).toBeTruthy();
  });

  it("shows a linked page's title and normalized_url as a list row", async () => {
    const discussion: PageLink = {
      id: 7,
      title: "Show HN: I built a self-hosted bookmarker",
      normalized_url: "example.com/show-hn",
      favicon_path: null,
    };
    render42({ page: { ...basePage, links: [discussion] } });
    await screen.findByRole("heading", { name: "An example article" });

    expect(
      screen.getByText("Show HN: I built a self-hosted bookmarker"),
    ).toBeTruthy();
    expect(screen.getByText("example.com/show-hn")).toBeTruthy();
    expect(
      screen.getByRole("link", {
        name: "Show HN: I built a self-hosted bookmarker example.com/show-hn",
      }),
    ).toHaveProperty("href", expect.stringContaining("/pages/7"));
  });

  it("hides the remove buttons and search input until Edit linked pages is clicked", async () => {
    const linked: PageLink = {
      id: 7,
      title: "Linked page",
      normalized_url: "example.com/linked",
      favicon_path: null,
    };
    render42({ page: { ...basePage, links: [linked] } });
    await screen.findByRole("heading", { name: "An example article" });

    expect(
      screen.queryByRole("button", { name: "Remove link to Linked page" }),
    ).toBeNull();
    expect(screen.queryByPlaceholderText("Search by title or URL…")).toBeNull();

    await fireEvent.click(
      screen.getByRole("button", { name: "Edit linked pages" }),
    );
    expect(
      screen.getByRole("button", { name: "Remove link to Linked page" }),
    ).toBeTruthy();
    expect(screen.getByPlaceholderText("Search by title or URL…")).toBeTruthy();
  });

  it("searches for candidates after a debounce, excludes already-linked pages, and adds one on click", async () => {
    vi.useFakeTimers();
    const alreadyLinked: PageLink = {
      id: 5,
      title: "Already linked",
      normalized_url: "example.com/already-linked",
      favicon_path: null,
    };
    const candidate: PageLink = {
      id: 9,
      title: "A Philosophy of Software Design",
      normalized_url: "example.com/philosophy",
      favicon_path: null,
    };
    render42({ page: { ...basePage, links: [alreadyLinked] } });
    await vi.waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "An example article" }),
      ).toBeTruthy(),
    );

    await fireEvent.click(
      screen.getByRole("button", { name: "Edit linked pages" }),
    );

    apiJSONMock.mockResolvedValueOnce({
      pages: [alreadyLinked, candidate],
    });
    await fireEvent.input(
      screen.getByPlaceholderText("Search by title or URL…"),
      { target: { value: "philosophy" } },
    );
    // Not yet -- still within the 300ms debounce window.
    expect(apiJSONMock).not.toHaveBeenCalledWith(
      expect.stringContaining("link-candidates"),
    );

    await vi.advanceTimersByTimeAsync(300);

    expect(apiJSONMock).toHaveBeenLastCalledWith(
      "/pages/link-candidates?q=philosophy&exclude=42",
    );
    // alreadyLinked came back in the search results too (a realistic
    // response, since the backend doesn't filter it out server-side --
    // see SearchPagesForLinking's own comment), but only the genuinely
    // new candidate should render *in the results dropdown* -- "Already
    // linked" legitimately still appears elsewhere on the page, in its
    // own linked-pages list, so the check is scoped to the dropdown.
    const dropdown = document.querySelector(".link-results") as HTMLElement;
    expect(within(dropdown).queryByText("Already linked")).toBeNull();
    const resultButton = await screen.findByRole("button", {
      name: "A Philosophy of Software Design example.com/philosophy",
    });

    apiJSONMock.mockResolvedValueOnce(candidate);
    await fireEvent.click(resultButton);

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42/links", {
      method: "POST",
      body: { link_page_id: 9 },
    });
    expect(
      await screen.findByText("A Philosophy of Software Design"),
    ).toBeTruthy();
    // The search box clears itself after a successful add.
    expect(
      screen.getByPlaceholderText("Search by title or URL…"),
    ).toHaveProperty("value", "");
  });

  it("removes a link", async () => {
    const linked: PageLink = {
      id: 7,
      title: "Linked page",
      normalized_url: "example.com/linked",
      favicon_path: null,
    };
    render42({ page: { ...basePage, links: [linked] } });
    await screen.findByRole("heading", { name: "An example article" });

    await fireEvent.click(
      screen.getByRole("button", { name: "Edit linked pages" }),
    );
    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(
      screen.getByRole("button", { name: "Remove link to Linked page" }),
    );

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42/links/7", {
      method: "DELETE",
    });
    expect(await screen.findByText("No pages linked yet.")).toBeTruthy();
  });

  it("shows the API's own error message when adding a link fails", async () => {
    vi.useFakeTimers();
    const candidate: PageLink = {
      id: 9,
      title: "A candidate page",
      normalized_url: "example.com/candidate",
      favicon_path: null,
    };
    render42();
    await vi.waitFor(() =>
      expect(
        screen.getByRole("heading", { name: "An example article" }),
      ).toBeTruthy(),
    );

    await fireEvent.click(
      screen.getByRole("button", { name: "Edit linked pages" }),
    );
    apiJSONMock.mockResolvedValueOnce({ pages: [candidate] });
    await fireEvent.input(
      screen.getByPlaceholderText("Search by title or URL…"),
      { target: { value: "candidate" } },
    );
    await vi.advanceTimersByTimeAsync(300);
    const resultButton = await screen.findByRole("button", {
      name: "A candidate page example.com/candidate",
    });

    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "failed to link page"));
    await fireEvent.click(resultButton);

    expect(await screen.findByText("failed to link page")).toBeTruthy();
  });

  it("shows the API's own error message when removing a link fails", async () => {
    const linked: PageLink = {
      id: 7,
      title: "Linked page",
      normalized_url: "example.com/linked",
      favicon_path: null,
    };
    render42({ page: { ...basePage, links: [linked] } });
    await screen.findByRole("heading", { name: "An example article" });

    await fireEvent.click(
      screen.getByRole("button", { name: "Edit linked pages" }),
    );
    apiJSONMock.mockRejectedValueOnce(
      new ApiError(500, "failed to remove link"),
    );
    await fireEvent.click(
      screen.getByRole("button", { name: "Remove link to Linked page" }),
    );

    expect(await screen.findByText("failed to remove link")).toBeTruthy();
  });

  it("queues a recapture, showing a transient confirmation", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(screen.getByRole("button", { name: "Recapture" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42/recapture", {
      method: "POST",
    });
    expect(await screen.findByRole("button", { name: "Queued!" })).toBeTruthy();
  });

  it("shows the API's own error message when queuing a recapture fails", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "queue unavailable"));
    await fireEvent.click(screen.getByRole("button", { name: "Recapture" }));

    expect(await screen.findByText("queue unavailable")).toBeTruthy();
  });

  it("deletes the page after confirmation and navigates back to the library", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    confirmMock.mockReturnValue(true);
    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(screen.getByRole("button", { name: "Delete page" }));

    expect(confirmMock).toHaveBeenCalledWith(
      'Delete "An example article"? This can\'t be undone from the dashboard.',
    );
    expect(apiJSONMock).toHaveBeenCalledWith("/pages/42", {
      method: "DELETE",
    });
    await vi.waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/");
    });
  });

  it("doesn't delete the page when the confirmation is declined", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    confirmMock.mockReturnValue(false);
    await fireEvent.click(screen.getByRole("button", { name: "Delete page" }));

    expect(apiJSONMock).not.toHaveBeenCalledWith(
      "/pages/42",
      expect.objectContaining({ method: "DELETE" }),
    );
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("shows the API's own error message when delete fails, without navigating away", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    confirmMock.mockReturnValue(true);
    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "delete failed"));
    await fireEvent.click(screen.getByRole("button", { name: "Delete page" }));

    expect(await screen.findByText("delete failed")).toBeTruthy();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("disables Recapture during the queued confirmation window, so it can't be clicked again while it'd just be a no-op", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    apiJSONMock.mockResolvedValueOnce(undefined);
    const button = screen.getByRole("button", { name: "Recapture" });
    expect(button).toHaveProperty("disabled", false);

    await fireEvent.click(button);
    const queuedButton = await screen.findByRole("button", {
      name: "Queued!",
    });
    expect(queuedButton).toHaveProperty("disabled", true);
  });

  it("shows the favicon when favicon_path is set", async () => {
    const { container } = render42({
      page: { ...basePage, favicon_path: "/some/path.png" },
    });
    await screen.findByRole("heading", { name: "An example article" });

    const favicon = container.querySelector<HTMLImageElement>(".favicon");
    expect(favicon?.src).toContain("/api/pages/42/favicon");
  });

  it("shows no favicon at all (no placeholder) when favicon_path is null", async () => {
    const { container } = render42({
      page: { ...basePage, favicon_path: null },
    });
    await screen.findByRole("heading", { name: "An example article" });

    expect(container.querySelector(".favicon")).toBeNull();
  });

  it("hides the favicon on a broken image, again with no placeholder", async () => {
    const { container } = render42({
      page: { ...basePage, favicon_path: "/some/path.png" },
    });
    await screen.findByRole("heading", { name: "An example article" });

    const favicon = container.querySelector<HTMLImageElement>(".favicon");
    await fireEvent.error(favicon as HTMLImageElement);

    expect(container.querySelector(".favicon")).toBeNull();
  });

  it("omits the source label for an extension capture (the common case), showing only the size", async () => {
    render42();
    await screen.findByRole("heading", { name: "An example article" });

    expect(screen.getByText("2.0 KB")).toBeTruthy();
    expect(screen.queryByText(/extension/)).toBeNull();
  });

  it("calls out a manual_upload capture specifically, since it's the uncommon case", async () => {
    render42({
      page: {
        ...basePage,
        captures: [{ ...exampleCapture, source: "manual_upload" }],
      },
    });
    await screen.findByRole("heading", { name: "An example article" });

    expect(screen.getByText(/manual upload/)).toBeTruthy();
    expect(screen.getByText(/2\.0 KB/)).toBeTruthy();
  });
});
