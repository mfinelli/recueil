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

// Same AppHeader/apiJSON/confirm mocking approach as Collections.test.ts.
// No create-tag coverage here -- unlike collections, there's no
// standalone tag-creation endpoint at all (see Tags.svelte's own
// top-of-file comment), so this is rename/delete only.
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
import type { Tag } from "../lib/types";
import Tags from "./Tags.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const confirmMock = vi.fn();
vi.stubGlobal("confirm", confirmMock);

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  confirmMock.mockReset();
});

function mockLoad(tags: Tag[] = []) {
  apiJSONMock.mockImplementation((path: string) => {
    if (path === "/tags") return Promise.resolve({ tags });
    throw new Error(`unexpected apiJSON call: ${path}`);
  });
}

const recipes: Tag = { id: 1, name: "recipes", slug: "recipes" };

describe("Tags", () => {
  it("shows a loading state, then the tag list with each tag's slug", () => {
    mockLoad([recipes]);
    render(Tags);

    expect(screen.getByText("Loading…")).toBeTruthy();
  });

  it("shows a placeholder when there are no tags", async () => {
    mockLoad([]);
    render(Tags);

    expect(await screen.findByText("No tags yet.")).toBeTruthy();
  });

  it("shows the API's own error message when loading fails with ApiError", async () => {
    apiJSONMock.mockImplementation(() =>
      Promise.reject(new ApiError(500, "tag store unavailable")),
    );
    render(Tags);

    expect(await screen.findByText("tag store unavailable")).toBeTruthy();
  });

  it("lists each tag with a link to its detail page and its slug", async () => {
    mockLoad([recipes]);
    render(Tags);

    const link = (await screen.findByRole("link", {
      name: /recipes/,
    })) as HTMLAnchorElement;
    expect(link.getAttribute("href")).toBe("#/tags/recipes");
    expect(within(link).getByText("/tags/recipes")).toBeTruthy();
  });

  it("renames a tag, re-deriving the slug from the new name by default", async () => {
    mockLoad([recipes]);
    render(Tags);

    const row = (await screen.findByText("recipes")).closest(
      "li",
    ) as HTMLElement;
    await fireEvent.click(within(row).getByRole("button", { name: "Rename" }));

    const renameInput = within(row).getByDisplayValue("recipes");
    apiJSONMock.mockResolvedValueOnce({
      id: 1,
      name: "Cooking",
      slug: "cooking",
    });
    await fireEvent.input(renameInput, { target: { value: "Cooking" } });
    await fireEvent.click(within(row).getByRole("button", { name: "Save" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/tags/1", {
      method: "PATCH",
      body: { name: "Cooking" },
    });
    expect(await screen.findByText("Cooking")).toBeTruthy();
  });

  it("shows a live slug preview that follows the name, collapsed behind an edit button", async () => {
    mockLoad([recipes]);
    render(Tags);

    const row = (await screen.findByText("recipes")).closest(
      "li",
    ) as HTMLElement;
    await fireEvent.click(within(row).getByRole("button", { name: "Rename" }));

    const renameInput = within(row).getByDisplayValue("recipes");
    await fireEvent.input(renameInput, { target: { value: "My Recipes!" } });

    expect(
      within(row).getByRole("button", { name: "URL: /tags/my-recipes" }),
    ).toBeTruthy();
  });

  it("sends an explicit slug once the slug field is opened and edited", async () => {
    mockLoad([recipes]);
    render(Tags);

    const row = (await screen.findByText("recipes")).closest(
      "li",
    ) as HTMLElement;
    await fireEvent.click(within(row).getByRole("button", { name: "Rename" }));
    await fireEvent.click(
      within(row).getByRole("button", { name: "URL: /tags/recipes" }),
    );

    const slugInput = within(row).getByPlaceholderText("custom-slug");
    await fireEvent.input(slugInput, { target: { value: "food" } });
    apiJSONMock.mockResolvedValueOnce({
      id: 1,
      name: "recipes",
      slug: "food",
    });
    await fireEvent.click(within(row).getByRole("button", { name: "Save" }));

    expect(apiJSONMock).toHaveBeenCalledWith("/tags/1", {
      method: "PATCH",
      body: { name: "recipes", slug: "food" },
    });
  });

  it("shows a validation error and disables Save for an invalid explicit slug", async () => {
    mockLoad([recipes]);
    render(Tags);

    const row = (await screen.findByText("recipes")).closest(
      "li",
    ) as HTMLElement;
    await fireEvent.click(within(row).getByRole("button", { name: "Rename" }));
    await fireEvent.click(
      within(row).getByRole("button", { name: "URL: /tags/recipes" }),
    );

    const slugInput = within(row).getByPlaceholderText("custom-slug");
    await fireEvent.input(slugInput, { target: { value: "Not Valid!" } });

    expect(
      within(row).getByText(
        "Use lowercase letters, numbers, and hyphens only.",
      ),
    ).toBeTruthy();
    expect(within(row).getByRole("button", { name: "Save" })).toHaveProperty(
      "disabled",
      true,
    );
  });

  it("shows the API's own error message when renaming fails", async () => {
    mockLoad([recipes]);
    render(Tags);

    const row = (await screen.findByText("recipes")).closest(
      "li",
    ) as HTMLElement;
    await fireEvent.click(within(row).getByRole("button", { name: "Rename" }));
    apiJSONMock.mockRejectedValueOnce(
      new ApiError(409, "a tag with that name or slug already exists"),
    );
    await fireEvent.click(within(row).getByRole("button", { name: "Save" }));

    expect(
      await screen.findByText("a tag with that name or slug already exists"),
    ).toBeTruthy();
  });

  it("deletes a tag after a simple confirmation", async () => {
    mockLoad([recipes]);
    confirmMock.mockReturnValue(true);
    render(Tags);

    const row = (await screen.findByText("recipes")).closest(
      "li",
    ) as HTMLElement;
    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(within(row).getByRole("button", { name: "Delete" }));

    expect(confirmMock).toHaveBeenCalledWith(
      'Delete "recipes"? It will be removed from every page that has it.',
    );
    expect(apiJSONMock).toHaveBeenCalledWith("/tags/1", { method: "DELETE" });
    expect(await screen.findByText("No tags yet.")).toBeTruthy();
  });

  it("doesn't delete a tag when the confirmation is declined", async () => {
    mockLoad([recipes]);
    confirmMock.mockReturnValue(false);
    render(Tags);

    const row = (await screen.findByText("recipes")).closest(
      "li",
    ) as HTMLElement;
    const before = apiJSONMock.mock.calls.length;
    await fireEvent.click(within(row).getByRole("button", { name: "Delete" }));

    expect(apiJSONMock.mock.calls.length).toBe(before);
    expect(screen.getByText("recipes")).toBeTruthy();
  });

  it("shows the API's own error message when deleting fails", async () => {
    mockLoad([recipes]);
    confirmMock.mockReturnValue(true);
    render(Tags);

    const row = (await screen.findByText("recipes")).closest(
      "li",
    ) as HTMLElement;
    apiJSONMock.mockRejectedValueOnce(new ApiError(500, "delete failed"));
    await fireEvent.click(within(row).getByRole("button", { name: "Delete" }));

    expect(await screen.findByText("delete failed")).toBeTruthy();
    expect(screen.getByText("recipes")).toBeTruthy();
  });
});
