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

// Same AppHeader/apiJSON mocking approach as Devices.test.ts, including
// window.confirm for deletion. apiJSON is mocked with a base
// mockImplementation that only answers the mount-time GET /collections
// (there's just the one load call here, no parallel-load race like
// Devices/Queue) -- every write action (create/rename/delete) layers its
// own mockResolvedValueOnce/mockRejectedValueOnce on top for the specific
// call it makes, since those hit different paths/methods than the load.
//
// The tree-building/sorting logic (buildTree, parent/child nesting,
// alphabetical sort at every level) is exercised indirectly through
// rendering rather than as a unit -- it's private to this component, and
// asserting on the rendered DOM order is the more honest test of what a
// user actually sees.
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
import type { Collection } from "../lib/types";
import Collections from "./Collections.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const confirmMock = vi.fn();
vi.stubGlobal("confirm", confirmMock);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  confirmMock.mockReset();
});

function mockLoad(collections: Collection[] = []) {
  apiJSONMock.mockImplementation((path: string) => {
    if (path === "/collections") return Promise.resolve({ collections });
    throw new Error(`unexpected apiJSON call: ${path}`);
  });
}

const zebra: Collection = {
  id: 1,
  parent_id: null,
  name: "Zebra",
  slug: "zebra",
  description: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};
const apple: Collection = {
  id: 2,
  parent_id: null,
  name: "Apple",
  slug: "apple",
  description: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};
const zebraSub: Collection = {
  id: 3,
  parent_id: 1,
  name: "Sub",
  slug: "sub",
  description: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

describe("Collections", () => {
  it("shows a loading state, then the collection tree", () => {
    mockLoad([zebra]);
    render(Collections);

    expect(screen.getByText("Loading…")).toBeTruthy();
  });

  it("shows a placeholder when there are no collections", async () => {
    mockLoad([]);
    render(Collections);

    expect(await screen.findByText("No collections yet.")).toBeTruthy();
  });

  it("shows the API's own error message when loading fails with ApiError", async () => {
    apiJSONMock.mockImplementation(() =>
      Promise.reject(new ApiError(500, "collection store unavailable")),
    );
    render(Collections);

    expect(
      await screen.findByText("collection store unavailable"),
    ).toBeTruthy();
  });

  it("sorts top-level collections alphabetically and nests children under their parent", async () => {
    mockLoad([zebra, apple, zebraSub]);
    render(Collections);

    await screen.findByText("Zebra");
    const names = screen
      .getAllByText(/^(Zebra|Apple|Sub)$/)
      .map((el) => el.textContent);
    // Apple sorts before Zebra among top-level roots; Sub is Zebra's
    // child, so it comes after Zebra despite starting with a letter
    // between "Apple" and "Zebra" alphabetically -- nesting wins over a
    // flat alphabetical pass.
    expect(names).toEqual(["Apple", "Zebra", "Sub"]);
  });

  it("links each collection name to its slug-path detail URL", async () => {
    mockLoad([zebra, zebraSub]);
    render(Collections);

    const zebraLink = (await screen.findByRole("link", {
      name: "Zebra",
    })) as HTMLAnchorElement;
    expect(zebraLink.getAttribute("href")).toBe("#/collections/zebra");

    const subLink = (await screen.findByRole("link", {
      name: "Sub",
    })) as HTMLAnchorElement;
    expect(subLink.getAttribute("href")).toBe("#/collections/zebra/sub");
  });

  it("creates a top-level collection and clears the input on success", async () => {
    mockLoad([]);
    render(Collections);
    await screen.findByText("No collections yet.");

    apiJSONMock.mockResolvedValueOnce({
      id: 9,
      parent_id: null,
      name: "New Collection",
      created_at: "2026-05-01T12:00:00Z",
    });
    const input = screen.getByPlaceholderText("New top-level collection…");
    await fireEvent.input(input, { target: { value: "New Collection" } });
    await fireEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/collections", {
      method: "POST",
      body: { name: "New Collection" },
    });
    expect(await screen.findByText("New Collection")).toBeTruthy();
    expect(input).toHaveProperty("value", "");
  });

  it("shows the API's own error message when creating a collection fails", async () => {
    mockLoad([]);
    render(Collections);
    await screen.findByText("No collections yet.");

    apiJSONMock.mockRejectedValueOnce(new ApiError(400, "name already in use"));
    await fireEvent.input(
      screen.getByPlaceholderText("New top-level collection…"),
      { target: { value: "Duplicate" } },
    );
    await fireEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("name already in use")).toBeTruthy();
  });

  it("adds a sub-collection to an existing node", async () => {
    mockLoad([zebra]);
    render(Collections);

    const row = (await screen.findByText("Zebra")).closest("li") as HTMLElement;
    await fireEvent.click(
      within(row).getByRole("button", { name: "+ Sub-collection" }),
    );

    const childInput = within(row).getByPlaceholderText("Sub-collection name…");
    apiJSONMock.mockResolvedValueOnce({
      id: 3,
      parent_id: 1,
      name: "Child",
      created_at: "2026-05-01T12:00:00Z",
    });
    await fireEvent.input(childInput, { target: { value: "Child" } });
    await fireEvent.click(within(row).getByRole("button", { name: "Create" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/collections", {
      method: "POST",
      body: { name: "Child", parent_id: 1 },
    });
    expect(await screen.findByText("Child")).toBeTruthy();
  });

  it("renames a collection", async () => {
    mockLoad([zebra]);
    render(Collections);

    const row = (await screen.findByText("Zebra")).closest("li") as HTMLElement;
    await fireEvent.click(within(row).getByRole("button", { name: "Rename" }));

    const renameInput = within(row).getByDisplayValue("Zebra");
    apiJSONMock.mockResolvedValueOnce({ ...zebra, name: "Zorse" });
    await fireEvent.input(renameInput, { target: { value: "Zorse" } });
    await fireEvent.click(within(row).getByRole("button", { name: "Save" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/collections/1", {
      method: "PATCH",
      body: { name: "Zorse" },
    });
    expect(await screen.findByText("Zorse")).toBeTruthy();
    expect(screen.queryByText("Zebra")).toBeNull();
  });

  it("deletes a leaf collection after a simple confirmation", async () => {
    mockLoad([zebra]);
    confirmMock.mockReturnValue(true);
    render(Collections);

    const row = (await screen.findByText("Zebra")).closest("li") as HTMLElement;
    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(within(row).getByRole("button", { name: "Delete" }));

    expect(confirmMock).toHaveBeenCalledWith(
      'Delete "Zebra"? Pages stay archived, but they\'ll no longer be in this collection.',
    );
    expect(apiJSONMock).toHaveBeenCalledWith("/collections/1", {
      method: "DELETE",
    });
    expect(await screen.findByText("No collections yet.")).toBeTruthy();
  });

  it("warns about descendants and removes them too when deleting a parent", async () => {
    mockLoad([zebra, zebraSub]);
    confirmMock.mockReturnValue(true);
    render(Collections);

    const row = (await screen.findByText("Zebra")).closest("li") as HTMLElement;
    apiJSONMock.mockResolvedValueOnce(undefined);
    // Scope to the button that's a direct child of Zebra's own row, not
    // Sub's nested row underneath it (also has its own "Delete" button).
    const zebraRowActions = row.querySelector(".row-actions") as HTMLElement;
    await fireEvent.click(
      within(zebraRowActions).getByRole("button", { name: "Delete" }),
    );

    expect(confirmMock).toHaveBeenCalledWith(
      'Delete "Zebra" and its 1 sub-collection? Pages stay archived, but they\'ll no longer be in any of these.',
    );
    expect(screen.queryByText("Zebra")).toBeNull();
    expect(screen.queryByText("Sub")).toBeNull();
    expect(await screen.findByText("No collections yet.")).toBeTruthy();
  });

  it("doesn't delete a collection when the confirmation is declined", async () => {
    mockLoad([zebra]);
    confirmMock.mockReturnValue(false);
    render(Collections);

    const row = (await screen.findByText("Zebra")).closest("li") as HTMLElement;
    const before = apiJSONMock.mock.calls.length;
    await fireEvent.click(within(row).getByRole("button", { name: "Delete" }));

    expect(apiJSONMock.mock.calls.length).toBe(before);
    expect(screen.getByText("Zebra")).toBeTruthy();
  });

  it("shows the API's own error message when deleting fails", async () => {
    mockLoad([zebra]);
    confirmMock.mockReturnValue(true);
    render(Collections);

    const row = (await screen.findByText("Zebra")).closest("li") as HTMLElement;
    apiJSONMock.mockRejectedValueOnce(new ApiError(409, "still in use"));
    await fireEvent.click(within(row).getByRole("button", { name: "Delete" }));

    expect(await screen.findByText("still in use")).toBeTruthy();
    expect(screen.getByText("Zebra")).toBeTruthy();
  });
});
