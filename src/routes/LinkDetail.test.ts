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

// Mirrors PageDetail.test.ts's mocking shape: AppHeader/svelte-spa-router/fetch
// stubbed up front, a path+method-dispatching apiJSON mock for the mount-time
// loads (the link and the collections, fetched in parallel, the second
// best-effort), and mockResolvedValueOnce/mockRejectedValueOnce layered on top
// per write. confirm() is mocked and push captured for the delete path, the one
// write that leaves the screen.
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
import { session } from "../lib/session.svelte";
import type { Collection, LinkDetail } from "../lib/types";
import LinkDetailRoute from "./LinkDetail.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const pushMock = vi.mocked(push);
const confirmMock = vi.fn();
vi.stubGlobal("confirm", confirmMock);

afterEach(() => {
  session.waybackEnabled = false;
  cleanup();
  apiJSONMock.mockReset();
  confirmMock.mockReset();
  pushMock.mockClear();
});

function collection(
  id: number,
  name: string,
  slug: string,
  parent_id: number | null = null,
): Collection {
  return {
    id,
    parent_id,
    name,
    slug,
    description: null,
    created_at: "2026-05-01T12:00:00Z",
    updated_at: "2026-05-01T12:00:00Z",
  };
}

const watchLater = collection(5, "Watch later", "watch-later");
const talks = collection(6, "Talks", "talks", 5);
const reference = collection(7, "Reference", "reference");

const baseLink: LinkDetail = {
  id: 42,
  raw_url: "https://example.com/watch?v=abc#t=30",
  normalized_url: "https://example.com/watch?v=abc",
  title: "An example video",
  favicon_path: null,
  notes: null,
  source: "extension",
  excluded_from_mirror: false,
  matching_page_id: null,
  matching_page_title: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
  tags: [{ id: 1, name: "video", slug: "video" }],
  collections: [{ id: 5, name: "Watch later", parent_id: null }],
  wayback: null,
  cleaned_url: null,
};

type LoadOptions = {
  id?: string;
  link?: LinkDetail;
  linkError?: unknown;
  collections?: Collection[];
  collectionsError?: unknown;
};

function mockLoad({
  id = "42",
  link = baseLink,
  linkError,
  collections = [watchLater, talks, reference],
  collectionsError,
}: LoadOptions = {}) {
  apiJSONMock.mockImplementation(
    (path: string, options?: { method?: string }) => {
      const method = options?.method ?? "GET";
      if (path === `/links/${id}` && method === "GET") {
        return linkError ? Promise.reject(linkError) : Promise.resolve(link);
      }
      if (path === "/collections" && method === "GET") {
        return collectionsError
          ? Promise.reject(collectionsError)
          : Promise.resolve({ collections });
      }
      throw new Error(`unexpected apiJSON call: ${method} ${path}`);
    },
  );
}

function render42(overrides: LoadOptions = {}) {
  mockLoad(overrides);
  return render(LinkDetailRoute, { params: { id: "42" } });
}

describe("LinkDetail", () => {
  describe("loading and the header", () => {
    it("shows a loading state, then the title, source URL, tags and collections", async () => {
      render42();
      expect(screen.getByText("Loading…")).toBeTruthy();

      expect(
        await screen.findByRole("heading", { name: "An example video" }),
      ).toBeTruthy();
      expect(screen.getByRole("link", { name: "video" })).toBeTruthy();
      expect(screen.getByRole("link", { name: "Watch later" })).toBeTruthy();
      expect(apiJSONMock).toHaveBeenCalledWith("/links/42");
      expect(apiJSONMock).toHaveBeenCalledWith("/collections");
    });

    it("links back to the Links list", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      const back = screen.getByRole("link", { name: "Links" });
      expect(back.getAttribute("href")).toBe("#/links");
    });

    it("shows and opens the saved raw_url, fragment and all, in a new tab", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });

      const source = screen.getByRole("link", {
        name: "https://example.com/watch?v=abc#t=30",
      });
      expect(source.getAttribute("href")).toBe(
        "https://example.com/watch?v=abc#t=30",
      );
      expect(source.getAttribute("target")).toBe("_blank");
      expect(source.getAttribute("rel")).toBe("noreferrer");
    });

    it("falls back to the normalized URL as the title when there's none", async () => {
      render42({ link: { ...baseLink, title: null } });
      expect(
        await screen.findByRole("heading", {
          name: "https://example.com/watch?v=abc",
        }),
      ).toBeTruthy();
    });

    it("shows the favicon when there is one, and nothing when there isn't", async () => {
      const { container, unmount } = render42({
        link: { ...baseLink, favicon_path: "ab/cd/id/favicon.png" },
      });
      await screen.findByRole("heading", { name: "An example video" });
      expect(
        container.querySelector<HTMLImageElement>(".favicon")?.src,
      ).toContain("/api/links/42/favicon");
      unmount();
      cleanup();

      const second = render42();
      await screen.findByRole("heading", { name: "An example video" });
      expect(second.container.querySelector(".favicon")).toBeNull();
      // The source row shows nothing, not a placeholder (unlike the lists).
      expect(second.container.querySelector(".favicon-placeholder")).toBeNull();
    });

    it("drops a favicon that fails to load, leaving nothing in its place", async () => {
      const { container } = render42({
        link: { ...baseLink, favicon_path: "ab/cd/id/favicon.png" },
      });
      await screen.findByRole("heading", { name: "An example video" });
      await fireEvent.error(container.querySelector(".favicon") as Element);
      expect(container.querySelector(".favicon")).toBeNull();
      expect(container.querySelector(".favicon-placeholder")).toBeNull();
    });

    it("says a manually added link was added manually, and a browser-saved one says nothing", async () => {
      const { unmount } = render42({ link: { ...baseLink, source: "manual" } });
      await screen.findByRole("heading", { name: "An example video" });
      expect(screen.getByText("added manually")).toBeTruthy();
      unmount();
      cleanup();

      render42();
      await screen.findByRole("heading", { name: "An example video" });
      expect(screen.queryByText("added manually")).toBeNull();
    });

    it("has none of a page's captures, recapture, AI or linked-pages sections", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });

      for (const text of [
        /Recapture/,
        /Captures/,
        /Linked pages/,
        /AI-suggested/,
      ]) {
        expect(screen.queryByText(text), String(text)).toBeNull();
      }
    });

    it("shows the API's error, not the page, when the link can't be loaded", async () => {
      render42({ linkError: new ApiError(404, "link not found") });
      expect(await screen.findByText("link not found")).toBeTruthy();
      expect(screen.queryByRole("heading")).toBeNull();
    });

    it("falls back to a generic message for a non-API error", async () => {
      render42({ linkError: new Error("network down") });
      expect(await screen.findByText("failed to load link")).toBeTruthy();
    });

    it("still renders the link when the collections fail to load, linking none of them", async () => {
      render42({ collectionsError: new Error("boom") });
      await screen.findByRole("heading", { name: "An example video" });
      // No path to build the chip's URL from, so it's plain text, not a link.
      expect(screen.getByText("Watch later")).toBeTruthy();
      expect(screen.queryByRole("link", { name: "Watch later" })).toBeNull();
    });
  });

  describe("the same-URL hint", () => {
    it("names the page with the same URL and links to it", async () => {
      render42({
        link: {
          ...baseLink,
          matching_page_id: 7,
          matching_page_title: "The archived page",
        },
      });
      await screen.findByRole("heading", { name: "An example video" });

      expect(
        screen.getByText(/This URL is also archived as a page:/),
      ).toBeTruthy();
      expect(
        screen
          .getByRole("link", { name: "The archived page" })
          .getAttribute("href"),
      ).toBe("#/pages/7");
    });

    it("falls back to the URL when the page has no title", async () => {
      render42({
        link: { ...baseLink, matching_page_id: 7, matching_page_title: null },
      });
      await screen.findByRole("heading", { name: "An example video" });
      expect(
        screen
          .getByText(/This URL is also archived as a page:/)
          .querySelector("a")
          ?.textContent?.trim(),
      ).toBe("https://example.com/watch?v=abc");
    });

    it("is absent when no page has the same URL", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      expect(screen.queryByText(/also archived as a page/)).toBeNull();
    });
  });

  describe("editing the title", () => {
    async function startEditing() {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      await fireEvent.click(screen.getByRole("button", { name: "Edit title" }));
      return screen.getByPlaceholderText("Title") as HTMLInputElement;
    }

    it("opens an input with the current title and the replace-on-resave hint", async () => {
      const input = await startEditing();
      expect(input.value).toBe("An example video");
      expect(
        screen.getByText(
          "Saving this link again from your browser replaces the title.",
        ),
      ).toBeTruthy();
    });

    it("saves the trimmed title and shows it", async () => {
      const input = await startEditing();
      apiJSONMock.mockResolvedValueOnce({
        ...baseLink,
        title: "A better name",
      });
      await fireEvent.input(input, { target: { value: "  A better name  " } });
      await fireEvent.click(screen.getByRole("button", { name: "Save" }));

      expect(apiJSONMock).toHaveBeenCalledWith("/links/42", {
        method: "PATCH",
        body: { title: "A better name" },
      });
      expect(
        await screen.findByRole("heading", { name: "A better name" }),
      ).toBeTruthy();
      expect(screen.queryByPlaceholderText("Title")).toBeNull();
    });

    it("can't save an empty title", async () => {
      const input = await startEditing();
      await fireEvent.input(input, { target: { value: "   " } });
      expect(
        (screen.getByRole("button", { name: "Save" }) as HTMLButtonElement)
          .disabled,
      ).toBe(true);
    });

    it("cancels without writing", async () => {
      const input = await startEditing();
      await fireEvent.input(input, { target: { value: "Changed my mind" } });
      await fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

      expect(
        screen.getByRole("heading", { name: "An example video" }),
      ).toBeTruthy();
      expect(apiJSONMock).not.toHaveBeenCalledWith(
        "/links/42",
        expect.objectContaining({ method: "PATCH" }),
      );
    });

    it("shows the error and keeps the editor open when saving fails", async () => {
      const input = await startEditing();
      apiJSONMock.mockRejectedValueOnce(new ApiError(500, "disk on fire"));
      await fireEvent.input(input, { target: { value: "New" } });
      await fireEvent.click(screen.getByRole("button", { name: "Save" }));

      expect(await screen.findByText("disk on fire")).toBeTruthy();
      expect(screen.getByPlaceholderText("Title")).toBeTruthy();
    });
  });

  describe("editing the notes", () => {
    it("shows an empty-state line when there are no notes", async () => {
      render42();
      expect(await screen.findByText("No notes yet.")).toBeTruthy();
    });

    it("renders existing notes as markdown", async () => {
      render42({ link: { ...baseLink, notes: "why I **saved** this" } });
      await screen.findByRole("heading", { name: "An example video" });
      expect(screen.getByText("saved").tagName).toBe("STRONG");
    });

    it("saves trimmed notes", async () => {
      render42();
      await screen.findByText("No notes yet.");
      await fireEvent.click(screen.getByRole("button", { name: "Edit notes" }));

      apiJSONMock.mockResolvedValueOnce({ ...baseLink, notes: "a note" });
      await fireEvent.input(screen.getByPlaceholderText("Add a note…"), {
        target: { value: "  a note \n" },
      });
      await fireEvent.click(screen.getByRole("button", { name: "Save" }));

      expect(apiJSONMock).toHaveBeenCalledWith("/links/42", {
        method: "PATCH",
        body: { notes: "a note" },
      });
      expect(await screen.findByText("a note")).toBeTruthy();
    });

    it("clears the notes when saved empty", async () => {
      render42({ link: { ...baseLink, notes: "old note" } });
      await screen.findByText("old note");
      await fireEvent.click(screen.getByRole("button", { name: "Edit notes" }));

      apiJSONMock.mockResolvedValueOnce({ ...baseLink, notes: null });
      await fireEvent.input(screen.getByPlaceholderText("Add a note…"), {
        target: { value: "   " },
      });
      await fireEvent.click(screen.getByRole("button", { name: "Save" }));

      expect(apiJSONMock).toHaveBeenCalledWith("/links/42", {
        method: "PATCH",
        body: { notes: "" },
      });
      expect(await screen.findByText("No notes yet.")).toBeTruthy();
    });

    it("shows the error when saving notes fails", async () => {
      render42();
      await screen.findByText("No notes yet.");
      await fireEvent.click(screen.getByRole("button", { name: "Edit notes" }));
      apiJSONMock.mockRejectedValueOnce(new ApiError(500, "no notes for you"));
      await fireEvent.click(screen.getByRole("button", { name: "Save" }));
      expect(await screen.findByText("no notes for you")).toBeTruthy();
    });
  });

  describe("tags", () => {
    it("links each tag to its tag screen", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      expect(
        screen.getByRole("link", { name: "video" }).getAttribute("href"),
      ).toBe("#/tags/video");
    });

    it("hides the remove buttons and the add form until editing", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      expect(screen.queryByPlaceholderText("Add a tag…")).toBeNull();
      expect(
        screen.queryByRole("button", { name: "Remove tag video" }),
      ).toBeNull();

      await fireEvent.click(screen.getByRole("button", { name: "Edit tags" }));
      expect(screen.getByPlaceholderText("Add a tag…")).toBeTruthy();
      expect(
        screen.getByRole("button", { name: "Remove tag video" }),
      ).toBeTruthy();
    });

    it("adds a tag, keeping the list sorted, and clears the input", async () => {
      render42();
      await screen.findByText("video");
      await fireEvent.click(screen.getByRole("button", { name: "Edit tags" }));

      apiJSONMock.mockResolvedValueOnce({
        id: 2,
        name: "archive",
        slug: "archive",
      });
      const input = screen.getByPlaceholderText("Add a tag…");
      await fireEvent.input(input, { target: { value: "archive" } });
      const form = input.closest("form") as HTMLElement;
      await fireEvent.click(within(form).getByRole("button", { name: "Add" }));

      expect(apiJSONMock).toHaveBeenCalledWith("/links/42/tags", {
        method: "POST",
        body: { name: "archive" },
      });
      expect(await screen.findByText("archive")).toBeTruthy();
      expect(input).toHaveProperty("value", "");
      const names = screen
        .getAllByRole("listitem")
        .map((li) => li.textContent?.trim())
        .filter((t) => t === "archive" || t === "video");
      expect(names).toEqual(["archive", "video"]);
    });

    it("shows the API's own error when adding a tag fails", async () => {
      render42();
      await screen.findByText("video");
      await fireEvent.click(screen.getByRole("button", { name: "Edit tags" }));
      apiJSONMock.mockRejectedValueOnce(new ApiError(400, "invalid tag name"));
      const input = screen.getByPlaceholderText("Add a tag…");
      await fireEvent.input(input, { target: { value: "???" } });
      await fireEvent.click(
        within(input.closest("form") as HTMLElement).getByRole("button", {
          name: "Add",
        }),
      );
      expect(await screen.findByText("invalid tag name")).toBeTruthy();
    });

    it("removes a tag", async () => {
      render42();
      await screen.findByText("video");
      await fireEvent.click(screen.getByRole("button", { name: "Edit tags" }));

      apiJSONMock.mockResolvedValueOnce(undefined);
      await fireEvent.click(
        screen.getByRole("button", { name: "Remove tag video" }),
      );

      expect(apiJSONMock).toHaveBeenCalledWith("/links/42/tags/1", {
        method: "DELETE",
      });
      await vi.waitFor(() => expect(screen.queryByText("video")).toBeNull());
    });

    it("keeps the tag and shows an error when removing fails", async () => {
      render42();
      await screen.findByText("video");
      await fireEvent.click(screen.getByRole("button", { name: "Edit tags" }));
      apiJSONMock.mockRejectedValueOnce(new ApiError(500, "cannot remove"));
      await fireEvent.click(
        screen.getByRole("button", { name: "Remove tag video" }),
      );
      expect(await screen.findByText("cannot remove")).toBeTruthy();
      expect(screen.getByText("video")).toBeTruthy();
    });
  });

  describe("collections", () => {
    it("links a collection chip to its nested path, built from the full list", async () => {
      render42({
        link: {
          ...baseLink,
          collections: [{ id: 6, name: "Talks", parent_id: 5 }],
        },
      });
      await screen.findByRole("heading", { name: "An example video" });
      expect(
        screen.getByRole("link", { name: "Talks" }).getAttribute("href"),
      ).toBe("#/collections/watch-later/talks");
    });

    it("offers only the collections the link isn't already in, labelled by their full path", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      await fireEvent.click(
        screen.getByRole("button", { name: "Edit collections" }),
      );

      const options = Array.from(
        (screen.getByRole("combobox") as HTMLSelectElement).options,
      ).map((o) => o.textContent?.trim());
      expect(options).toEqual([
        "Add to a collection…",
        "Reference",
        "Watch later / Talks",
      ]);
    });

    it("adds the link to an existing collection", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      await fireEvent.click(
        screen.getByRole("button", { name: "Edit collections" }),
      );

      apiJSONMock.mockResolvedValueOnce(undefined);
      const select = screen.getByRole("combobox");
      await fireEvent.change(select, { target: { value: "7" } });
      await fireEvent.click(
        within(select.closest("form") as HTMLElement).getByRole("button", {
          name: "Add",
        }),
      );

      expect(apiJSONMock).toHaveBeenCalledWith("/links/42/collections", {
        method: "POST",
        body: { collection_id: 7 },
      });
      await vi.waitFor(() => {
        const items = screen
          .getAllByRole("listitem")
          .map((li) => li.textContent?.trim());
        expect(items).toContain("Reference");
      });
    });

    it("creates a collection and adds the link to it, in two calls", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      await fireEvent.click(
        screen.getByRole("button", { name: "Edit collections" }),
      );

      apiJSONMock.mockResolvedValueOnce(
        collection(8, "Brand new", "brand-new"),
      );
      apiJSONMock.mockResolvedValueOnce(undefined);
      const input = screen.getByPlaceholderText("Or create a new collection…");
      await fireEvent.input(input, { target: { value: "Brand new" } });
      await fireEvent.click(
        screen.getByRole("button", { name: "Create & add" }),
      );

      expect(apiJSONMock).toHaveBeenCalledWith("/collections", {
        method: "POST",
        body: { name: "Brand new" },
      });
      expect(apiJSONMock).toHaveBeenCalledWith("/links/42/collections", {
        method: "POST",
        body: { collection_id: 8 },
      });
      expect(await screen.findByText("Brand new")).toBeTruthy();
      // The chip appears when the link call resolves; the input is cleared
      // just after.
      await vi.waitFor(() => expect(input).toHaveProperty("value", ""));
    });

    it("shows an error and adds nothing when creating the collection fails", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      await fireEvent.click(
        screen.getByRole("button", { name: "Edit collections" }),
      );
      apiJSONMock.mockRejectedValueOnce(new ApiError(409, "already exists"));
      await fireEvent.input(
        screen.getByPlaceholderText("Or create a new collection…"),
        { target: { value: "Dup" } },
      );
      await fireEvent.click(
        screen.getByRole("button", { name: "Create & add" }),
      );
      expect(await screen.findByText("already exists")).toBeTruthy();
      expect(apiJSONMock).not.toHaveBeenCalledWith(
        "/links/42/collections",
        expect.anything(),
      );
    });

    it("removes the link from a collection", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      await fireEvent.click(
        screen.getByRole("button", { name: "Edit collections" }),
      );

      apiJSONMock.mockResolvedValueOnce(undefined);
      await fireEvent.click(
        screen.getByRole("button", { name: "Remove from Watch later" }),
      );

      expect(apiJSONMock).toHaveBeenCalledWith("/links/42/collections/5", {
        method: "DELETE",
      });
      await vi.waitFor(() =>
        expect(
          screen.queryByRole("button", { name: "Remove from Watch later" }),
        ).toBeNull(),
      );
    });

    it("keeps the membership and shows an error when removing fails", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      await fireEvent.click(
        screen.getByRole("button", { name: "Edit collections" }),
      );
      apiJSONMock.mockRejectedValueOnce(new ApiError(500, "cannot remove"));
      await fireEvent.click(
        screen.getByRole("button", { name: "Remove from Watch later" }),
      );
      expect(await screen.findByText("cannot remove")).toBeTruthy();
      expect(
        screen.getByRole("button", { name: "Remove from Watch later" }),
      ).toBeTruthy();
    });
  });

  describe("cleaning the URL", () => {
    const dirty = {
      ...baseLink,
      raw_url: "https://example.com/watch?v=abc&utm_source=news#t=30",
      cleaned_url: "https://example.com/watch?v=abc#t=30",
    };

    it("offers the cleaner URL, with a hint saying what it does", async () => {
      render42({ link: dirty });
      await screen.findByRole("heading", { name: "An example video" });

      expect(screen.getByText("Cleaner URL")).toBeTruthy();
      expect(
        screen.getByText("https://example.com/watch?v=abc#t=30"),
      ).toBeTruthy();
      expect(screen.getByRole("button", { name: "Use this URL" })).toBeTruthy();
      expect(
        screen.getByText(
          "Removes tracking and other extras from the URL this link opens.",
        ),
      ).toBeTruthy();
    });

    it("offers nothing when the URL is already clean", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      expect(screen.queryByText("Cleaner URL")).toBeNull();
      expect(screen.queryByRole("button", { name: "Use this URL" })).toBeNull();
    });

    it("asks the server to clean it, then shows the new URL and stops offering", async () => {
      render42({ link: dirty });
      await screen.findByRole("heading", { name: "An example video" });
      apiJSONMock.mockResolvedValueOnce({
        ...baseLink,
        raw_url: "https://example.com/watch?v=abc#t=30",
      });

      await fireEvent.click(
        screen.getByRole("button", { name: "Use this URL" }),
      );

      // Nothing is sent but the request: the server works out the URL.
      expect(apiJSONMock).toHaveBeenCalledWith("/links/42/clean-url", {
        method: "POST",
      });
      await vi.waitFor(() =>
        expect(
          screen.queryByRole("button", { name: "Use this URL" }),
        ).toBeNull(),
      );
      const source = screen.getByRole("link", {
        name: "https://example.com/watch?v=abc#t=30",
      });
      expect(source.getAttribute("href")).toBe(
        "https://example.com/watch?v=abc#t=30",
      );
    });

    it("keeps the offer, and shows the error, when it fails", async () => {
      render42({ link: dirty });
      await screen.findByRole("heading", { name: "An example video" });
      apiJSONMock.mockRejectedValueOnce(new ApiError(500, "update rejected"));

      await fireEvent.click(
        screen.getByRole("button", { name: "Use this URL" }),
      );

      expect(await screen.findByText("update rejected")).toBeTruthy();
      expect(screen.getByRole("button", { name: "Use this URL" })).toBeTruthy();
    });

    it("falls back to a generic message for a non-API failure", async () => {
      render42({ link: dirty });
      await screen.findByRole("heading", { name: "An example video" });
      apiJSONMock.mockRejectedValueOnce(new Error("network down"));
      await fireEvent.click(
        screen.getByRole("button", { name: "Use this URL" }),
      );
      expect(await screen.findByText("failed to update the URL")).toBeTruthy();
    });
  });

  describe("the bookmark-sync setting", () => {
    it("is checked for a link that's synced, as the default is", async () => {
      render42();
      const checkbox = await screen.findByRole("checkbox", {
        name: "Sync with my browser's bookmarks",
      });
      expect(checkbox).toHaveProperty("checked", true);
    });

    it("is unchecked for a link that's excluded", async () => {
      render42({ link: { ...baseLink, excluded_from_mirror: true } });
      const checkbox = await screen.findByRole("checkbox", {
        name: "Sync with my browser's bookmarks",
      });
      expect(checkbox).toHaveProperty("checked", false);
    });

    it("excludes the link when unchecked, and syncs it again when checked", async () => {
      render42();
      const checkbox = await screen.findByRole("checkbox", {
        name: "Sync with my browser's bookmarks",
      });

      apiJSONMock.mockResolvedValueOnce(undefined);
      await fireEvent.click(checkbox);
      // The PATCH body is the inverse of what the checkbox shows: unchecking
      // it means excluded.
      expect(apiJSONMock).toHaveBeenCalledWith("/links/42", {
        method: "PATCH",
        body: { excluded_from_mirror: true },
      });
      expect(checkbox).toHaveProperty("checked", false);

      apiJSONMock.mockResolvedValueOnce(undefined);
      await fireEvent.click(checkbox);
      await vi.waitFor(() =>
        expect(apiJSONMock).toHaveBeenLastCalledWith("/links/42", {
          method: "PATCH",
          body: { excluded_from_mirror: false },
        }),
      );
      await vi.waitFor(() => expect(checkbox).toHaveProperty("checked", true));
    });

    it("shows the error when the update fails", async () => {
      render42();
      const checkbox = await screen.findByRole("checkbox", {
        name: "Sync with my browser's bookmarks",
      });
      apiJSONMock.mockRejectedValueOnce(new ApiError(500, "update rejected"));
      await fireEvent.click(checkbox);
      expect(await screen.findByText("update rejected")).toBeTruthy();
    });

    it("falls back to a generic message for a non-API failure", async () => {
      render42();
      const checkbox = await screen.findByRole("checkbox", {
        name: "Sync with my browser's bookmarks",
      });
      apiJSONMock.mockRejectedValueOnce(new Error("network down"));
      await fireEvent.click(checkbox);
      expect(
        await screen.findByText("failed to update mirror setting"),
      ).toBeTruthy();
    });
  });

  describe("the Internet Archive block", () => {
    it("sits directly above the actions row, with the calendar link for the link's normalized URL", async () => {
      session.waybackEnabled = true;
      render42();
      const heading = await screen.findByRole("heading", {
        name: "Internet Archive",
      });

      const check = screen.getByRole("link", {
        name: "Check for existing snapshots",
      });
      expect(check.getAttribute("href")).toBe(
        "https://web.archive.org/web/*/https://example.com/watch?v=abc",
      );
      const actions = document.querySelector(".actions-row") as HTMLElement;
      expect(
        heading.compareDocumentPosition(actions) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
      expect(heading.closest("section")?.nextElementSibling).toBe(actions);
    });

    it("submits to this link's own route and shows the pending state without leaving the screen", async () => {
      session.waybackEnabled = true;
      render42();
      await screen.findByRole("heading", { name: "Internet Archive" });

      apiJSONMock.mockResolvedValueOnce({
        status: "pending",
        submitted_at: "2026-10-12T15:41:00Z",
        last_submitted_at: null,
        error: null,
      });
      await fireEvent.click(
        screen.getByRole("button", { name: /Submit to Wayback Machine/ }),
      );

      expect(apiJSONMock).toHaveBeenCalledWith("/links/42/wayback", {
        method: "POST",
      });
      expect(
        await screen.findByText("Submitting to the Wayback Machine…"),
      ).toBeTruthy();
      expect(
        (
          screen.getByRole("button", {
            name: /Submitting…/,
          }) as HTMLButtonElement
        ).disabled,
      ).toBe(true);
    });

    it("shows what the server recorded, when the link has been submitted before", async () => {
      session.waybackEnabled = true;
      render42({
        link: {
          ...baseLink,
          wayback: {
            status: "succeeded",
            submitted_at: "2026-10-12T15:41:00Z",
            last_submitted_at: "2026-10-12T15:41:00Z",
            error: null,
          },
        },
      });
      expect(
        await screen.findByText("Submitted to the Wayback Machine"),
      ).toBeTruthy();
      expect(screen.getByRole("link", { name: "View snapshots" })).toBeTruthy();
    });

    it("offers no button when submission isn't configured, only the link", async () => {
      session.waybackEnabled = false;
      render42();
      await screen.findByRole("heading", { name: "Internet Archive" });
      expect(
        screen.queryByRole("button", { name: /Submit to Wayback Machine/ }),
      ).toBeNull();
      expect(
        screen.getByRole("link", { name: "Check for existing snapshots" }),
      ).toBeTruthy();
    });
  });

  describe("deleting", () => {
    it("does nothing when the confirmation is declined", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      confirmMock.mockReturnValue(false);

      await fireEvent.click(
        screen.getByRole("button", { name: "Delete link" }),
      );

      expect(confirmMock).toHaveBeenCalledWith(
        'Delete "An example video"? This can\'t be undone from the dashboard.',
      );
      expect(apiJSONMock).not.toHaveBeenCalledWith(
        "/links/42",
        expect.objectContaining({ method: "DELETE" }),
      );
      expect(pushMock).not.toHaveBeenCalled();
    });

    it("deletes the link and returns to the Links list once confirmed", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      confirmMock.mockReturnValue(true);
      apiJSONMock.mockResolvedValueOnce(undefined);

      await fireEvent.click(
        screen.getByRole("button", { name: "Delete link" }),
      );

      expect(apiJSONMock).toHaveBeenCalledWith("/links/42", {
        method: "DELETE",
      });
      await vi.waitFor(() => expect(pushMock).toHaveBeenCalledWith("/links"));
    });

    it("names the URL in the confirmation when the link has no title", async () => {
      render42({ link: { ...baseLink, title: null } });
      await screen.findByRole("heading", {
        name: "https://example.com/watch?v=abc",
      });
      confirmMock.mockReturnValue(false);
      await fireEvent.click(
        screen.getByRole("button", { name: "Delete link" }),
      );
      expect(confirmMock).toHaveBeenCalledWith(
        expect.stringContaining("https://example.com/watch?v=abc"),
      );
    });

    it("stays on the screen, shows the error and re-enables delete when it fails", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      confirmMock.mockReturnValue(true);
      apiJSONMock.mockRejectedValueOnce(new ApiError(500, "cannot delete"));

      await fireEvent.click(
        screen.getByRole("button", { name: "Delete link" }),
      );

      expect(await screen.findByText("cannot delete")).toBeTruthy();
      expect(pushMock).not.toHaveBeenCalled();
      expect(
        (
          screen.getByRole("button", {
            name: "Delete link",
          }) as HTMLButtonElement
        ).disabled,
      ).toBe(false);
    });

    it("offers delete as the only action", async () => {
      render42();
      await screen.findByRole("heading", { name: "An example video" });
      const row = screen
        .getByRole("button", { name: "Delete link" })
        .closest(".actions-row") as HTMLElement;
      expect(within(row).getAllByRole("button")).toHaveLength(1);
    });
  });
});
