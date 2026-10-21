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

import { describe, it, expect, vi, afterEach } from "vitest";
import {
  render,
  screen,
  fireEvent,
  cleanup,
  within,
} from "@testing-library/svelte";

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, apiJSON: vi.fn() };
});

import { apiJSON, ApiError } from "../lib/api";
import type { RelatedItem } from "../lib/types";
import RelatedItems from "./RelatedItems.svelte";

const apiJSONMock = vi.mocked(apiJSON);

afterEach(() => {
  vi.useRealTimers();
  cleanup();
  apiJSONMock.mockReset();
});

const labels = {
  heading: "Related things",
  empty: "Nothing related yet.",
  edit: "Edit related things",
  done: "Done editing related things",
  searchPlaceholder: "Search things…",
  noResults: "No matching things.",
  remove: (name: string) => `Remove relation to ${name}`,
  relateError: "failed to relate",
  removeError: "failed to unrelate",
};

const item = (
  id: number,
  title: string | null,
  url = `example.com/${id}`,
): RelatedItem => ({
  id,
  title,
  normalized_url: url,
  favicon_path: null,
});

type Props = {
  items?: RelatedItem[];
  kind?: "page" | "link";
  ownerPath?: string;
  // null for "no id to exclude": a default parameter would turn undefined into 42.
  excludeId?: number | null;
};

function show({
  items = [],
  kind = "page",
  ownerPath = "/pages/42",
  excludeId = 42,
}: Props = {}) {
  const onchange = vi.fn();
  const onerror = vi.fn();
  const view = render(RelatedItems, {
    items,
    kind,
    ownerPath,
    excludeId: excludeId === null ? undefined : excludeId,
    labels,
    onchange,
    onerror,
  });
  return { ...view, onchange, onerror };
}

const pencil = () =>
  screen.getByRole("button", { name: "Edit related things" });
const search = () => screen.getByPlaceholderText("Search things…");

async function typeAndWait(value: string) {
  await fireEvent.input(search(), { target: { value } });
  await vi.advanceTimersByTimeAsync(300);
}

describe("RelatedItems", () => {
  describe("the list", () => {
    it("says so when there is nothing", () => {
      show();
      expect(
        screen.getByRole("heading", { name: "Related things" }),
      ).toBeTruthy();
      expect(screen.getByText("Nothing related yet.")).toBeTruthy();
    });

    it("shows each item as a row that opens it, with its title and URL, or the URL alone when untitled", () => {
      show({ items: [item(7, "Seven"), item(8, null, "example.com/eight")] });

      expect(
        screen.getByRole("link", { name: /Seven/ }).getAttribute("href"),
      ).toBe("#/pages/7");
      expect(screen.getByText("example.com/7")).toBeTruthy();
      expect(
        screen.getAllByText("example.com/eight").length,
      ).toBeGreaterThanOrEqual(1);
      expect(screen.queryByText("Nothing related yet.")).toBeNull();
    });

    it("opens items of its own kind, and finds their favicons under that kind", () => {
      const withIcon: RelatedItem = {
        ...item(5, "Five"),
        favicon_path: "ab/cd/favicon.png",
      };
      const { container } = show({
        kind: "link",
        ownerPath: "/links/1",
        items: [withIcon],
      });

      expect(
        screen.getByRole("link", { name: /Five/ }).getAttribute("href"),
      ).toBe("#/links/5");
      expect(container.querySelector("img")?.getAttribute("src")).toBe(
        "/api/links/5/favicon",
      );
    });
  });

  describe("editing", () => {
    it("keeps the remove buttons and the search box behind the pencil", async () => {
      show({ items: [item(7, "Seven")] });
      expect(
        screen.queryByRole("button", { name: "Remove relation to Seven" }),
      ).toBeNull();
      expect(screen.queryByPlaceholderText("Search things…")).toBeNull();

      await fireEvent.click(pencil());

      expect(
        screen.getByRole("button", { name: "Remove relation to Seven" }),
      ).toBeTruthy();
      expect(search()).toBeTruthy();
      expect(
        screen.getByRole("button", { name: "Done editing related things" }),
      ).toBeTruthy();
    });

    it("starts clean for another owner: editing closed and the search emptied", async () => {
      const { rerender } = show({ items: [item(7, "Seven")] });
      await fireEvent.click(pencil());
      await fireEvent.input(search(), { target: { value: "half typed" } });

      await rerender({ ownerPath: "/pages/43" });

      expect(screen.queryByPlaceholderText("Search things…")).toBeNull();
      await fireEvent.click(pencil());
      expect((search() as HTMLInputElement).value).toBe("");
    });
  });

  describe("removing", () => {
    it("deletes through the owner's own route and hands back the list without it", async () => {
      const { onchange, onerror } = show({
        items: [item(7, "Seven"), item(8, "Eight")],
      });
      await fireEvent.click(pencil());

      apiJSONMock.mockResolvedValueOnce(undefined);
      await fireEvent.click(
        screen.getByRole("button", { name: "Remove relation to Seven" }),
      );

      expect(apiJSONMock).toHaveBeenCalledWith("/pages/42/related-pages/7", {
        method: "DELETE",
      });
      await vi.waitFor(() =>
        expect(onchange).toHaveBeenCalledWith([item(8, "Eight")]),
      );
      expect(onerror).toHaveBeenCalledWith(null);
    });

    it("reports the server's message, or its own when it isn't an API error, and changes nothing", async () => {
      const { onchange, onerror } = show({ items: [item(7, "Seven")] });
      await fireEvent.click(pencil());

      apiJSONMock.mockRejectedValueOnce(new ApiError(404, "page not found"));
      await fireEvent.click(
        screen.getByRole("button", { name: "Remove relation to Seven" }),
      );
      await vi.waitFor(() =>
        expect(onerror).toHaveBeenLastCalledWith("page not found"),
      );

      apiJSONMock.mockRejectedValueOnce(new Error("network down"));
      await fireEvent.click(
        screen.getByRole("button", { name: "Remove relation to Seven" }),
      );
      await vi.waitFor(() =>
        expect(onerror).toHaveBeenLastCalledWith("failed to unrelate"),
      );

      expect(onchange).not.toHaveBeenCalled();
    });
  });

  describe("searching", () => {
    it("waits for a pause in typing, then asks the picker of its kind, excluding the owner", async () => {
      vi.useFakeTimers();
      show();
      await fireEvent.click(pencil());

      apiJSONMock.mockResolvedValueOnce({ pages: [item(9, "Nine")] });
      await fireEvent.input(search(), { target: { value: "ni" } });
      expect(apiJSONMock).not.toHaveBeenCalled();

      await vi.advanceTimersByTimeAsync(300);

      expect(apiJSONMock).toHaveBeenCalledWith(
        "/pages/relation-candidates?q=ni&exclude=42",
      );
      expect(
        await screen.findByRole("button", { name: "Nine example.com/9" }),
      ).toBeTruthy();
    });

    it("reads the answer under its own kind's key", async () => {
      vi.useFakeTimers();
      show({ kind: "link", ownerPath: "/links/1", excludeId: 1 });
      await fireEvent.click(pencil());

      apiJSONMock.mockResolvedValueOnce({ links: [item(3, "Three")] });
      await typeAndWait("th");

      expect(apiJSONMock).toHaveBeenCalledWith(
        "/links/relation-candidates?q=th&exclude=1",
      );
      expect(
        await screen.findByRole("button", { name: "Three example.com/3" }),
      ).toBeTruthy();
    });

    it("leaves out the exclusion when it has no owner id of its own kind to exclude", async () => {
      vi.useFakeTimers();
      // A list of the other kind (a page's related links): ids from two tables aren't
      // comparable, so nothing may be excluded.
      show({ kind: "link", ownerPath: "/pages/42", excludeId: null });
      await fireEvent.click(pencil());

      apiJSONMock.mockResolvedValueOnce({ links: [] });
      await typeAndWait("x");

      expect(apiJSONMock).toHaveBeenCalledWith(
        "/links/relation-candidates?q=x",
      );
    });

    it("leaves out anything already related, though the server returns it too", async () => {
      vi.useFakeTimers();
      show({ items: [item(7, "Seven")] });
      await fireEvent.click(pencil());

      apiJSONMock.mockResolvedValueOnce({
        pages: [item(7, "Seven"), item(9, "Nine")],
      });
      await typeAndWait("n");

      const dropdown = document.querySelector(
        ".related-results",
      ) as HTMLElement;
      expect(
        within(await vi.waitFor(() => dropdown)).queryByText("Seven"),
      ).toBeNull();
      expect(
        await screen.findByRole("button", { name: "Nine example.com/9" }),
      ).toBeTruthy();
    });

    it("says when nothing matches, and shows nothing for a blank query or a failed search", async () => {
      vi.useFakeTimers();
      show();
      await fireEvent.click(pencil());

      apiJSONMock.mockResolvedValueOnce({ pages: [] });
      await typeAndWait("zzz");
      expect(await screen.findByText("No matching things.")).toBeTruthy();

      apiJSONMock.mockClear();
      await typeAndWait("   ");
      expect(apiJSONMock).not.toHaveBeenCalled();
      expect(screen.queryByText("No matching things.")).toBeNull();

      apiJSONMock.mockRejectedValueOnce(new Error("network down"));
      await typeAndWait("boom");
      expect(screen.queryByRole("button", { name: /example\.com/ })).toBeNull();
    });

    it("ignores a slow answer for a query that has since changed", async () => {
      vi.useFakeTimers();
      show();
      await fireEvent.click(pencil());

      let resolveSlow: (v: unknown) => void = () => {};
      apiJSONMock.mockReturnValueOnce(new Promise((r) => (resolveSlow = r)));
      await typeAndWait("ni");
      apiJSONMock.mockResolvedValueOnce({ pages: [item(10, "Ten")] });
      await typeAndWait("ten");
      expect(
        await screen.findByRole("button", { name: "Ten example.com/10" }),
      ).toBeTruthy();

      resolveSlow({ pages: [item(9, "Nine")] });
      await vi.advanceTimersByTimeAsync(0);

      expect(
        screen.queryByRole("button", { name: "Nine example.com/9" }),
      ).toBeNull();
      expect(
        screen.getByRole("button", { name: "Ten example.com/10" }),
      ).toBeTruthy();
    });
  });

  describe("adding", () => {
    it("posts to the owner's route with the target named by its kind, then hands back the list in title order and clears the search", async () => {
      vi.useFakeTimers();
      const { onchange } = show({ items: [item(3, "Zed")] });
      await fireEvent.click(pencil());
      apiJSONMock.mockResolvedValueOnce({ pages: [item(9, "Abe")] });
      await typeAndWait("abe");

      apiJSONMock.mockResolvedValueOnce(item(9, "Abe"));
      await fireEvent.click(
        await screen.findByRole("button", { name: "Abe example.com/9" }),
      );

      expect(apiJSONMock).toHaveBeenLastCalledWith("/pages/42/related-pages", {
        method: "POST",
        body: { page_id: 9 },
      });
      await vi.waitFor(() =>
        expect(onchange).toHaveBeenCalledWith([item(9, "Abe"), item(3, "Zed")]),
      );
      expect((search() as HTMLInputElement).value).toBe("");
    });

    it("names a link target link_id, under the link owner's route", async () => {
      vi.useFakeTimers();
      show({ kind: "link", ownerPath: "/links/1", excludeId: 1 });
      await fireEvent.click(pencil());
      apiJSONMock.mockResolvedValueOnce({ links: [item(3, "Three")] });
      await typeAndWait("th");

      apiJSONMock.mockResolvedValueOnce(item(3, "Three"));
      await fireEvent.click(
        await screen.findByRole("button", { name: "Three example.com/3" }),
      );

      expect(apiJSONMock).toHaveBeenLastCalledWith("/links/1/related-links", {
        method: "POST",
        body: { link_id: 3 },
      });
    });

    it("can relate to the other kind: a page's related links post to the page's own route", async () => {
      vi.useFakeTimers();
      show({ kind: "link", ownerPath: "/pages/42", excludeId: null });
      await fireEvent.click(pencil());
      apiJSONMock.mockResolvedValueOnce({ links: [item(3, "Three")] });
      await typeAndWait("th");

      apiJSONMock.mockResolvedValueOnce(item(3, "Three"));
      await fireEvent.click(
        await screen.findByRole("button", { name: "Three example.com/3" }),
      );

      expect(apiJSONMock).toHaveBeenLastCalledWith("/pages/42/related-links", {
        method: "POST",
        body: { link_id: 3 },
      });
    });

    it("reports the server's message, or its own, and changes nothing", async () => {
      vi.useFakeTimers();
      const { onchange, onerror } = show();
      await fireEvent.click(pencil());
      apiJSONMock.mockResolvedValueOnce({ pages: [item(9, "Nine")] });
      await typeAndWait("ni");
      const result = await screen.findByRole("button", {
        name: "Nine example.com/9",
      });

      apiJSONMock.mockRejectedValueOnce(new ApiError(404, "page not found"));
      await fireEvent.click(result);
      await vi.waitFor(() =>
        expect(onerror).toHaveBeenLastCalledWith("page not found"),
      );

      apiJSONMock.mockRejectedValueOnce(new Error("network down"));
      await fireEvent.click(result);
      await vi.waitFor(() =>
        expect(onerror).toHaveBeenLastCalledWith("failed to relate"),
      );

      expect(onchange).not.toHaveBeenCalled();
    });
  });
});
