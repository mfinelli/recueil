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

import { describe, it, expect } from "vitest";
import {
  availableCollections,
  collectionNamePath,
  collectionPath,
} from "./collections";
import type { Collection } from "./types";

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

const cookbook = collection(20, "Cookbook", "cookbook");
const zebra = collection(21, "Zebra", "zebra");
const cookbookRecipes = collection(22, "Recipes", "recipes", 20);
const zebraRecipes = collection(23, "Recipes", "recipes", 21);
const sideDishes = collection(24, "Side dishes", "side-dishes", 23);
const all = [cookbook, zebra, cookbookRecipes, zebraRecipes, sideDishes];

describe("collectionPath", () => {
  it("is the slug alone for a top-level collection", () => {
    expect(collectionPath(all, 21)).toBe("zebra");
  });

  it("joins the ancestors' slugs, outermost first", () => {
    expect(collectionPath(all, 24)).toBe("zebra/recipes/side-dishes");
  });

  it("distinguishes same-slugged collections by their parents", () => {
    expect(collectionPath(all, 22)).toBe("cookbook/recipes");
    expect(collectionPath(all, 23)).toBe("zebra/recipes");
  });

  it("is null for a collection that isn't in the list", () => {
    expect(collectionPath(all, 999)).toBeNull();
    expect(collectionPath([], 20)).toBeNull();
  });

  it("stops at an unknown parent rather than looping or throwing", () => {
    const orphan = collection(30, "Orphan", "orphan", 999);
    expect(collectionPath([orphan], 30)).toBe("orphan");
  });
});

describe("collectionNamePath", () => {
  it("joins the ancestors' names with a slash, so same-named collections read apart", () => {
    expect(collectionNamePath(all, 22)).toBe("Cookbook / Recipes");
    expect(collectionNamePath(all, 23)).toBe("Zebra / Recipes");
    expect(collectionNamePath(all, 24)).toBe("Zebra / Recipes / Side dishes");
  });

  it("is just the name for a top-level collection", () => {
    expect(collectionNamePath(all, 20)).toBe("Cookbook");
  });
});

describe("availableCollections", () => {
  it("leaves out the collections the item is already in", () => {
    const available = availableCollections(all, [20, 24]).map((c) => c.id);
    expect(available).not.toContain(20);
    expect(available).not.toContain(24);
    expect(available).toHaveLength(3);
  });

  it("offers everything when the item is in none", () => {
    expect(availableCollections(all, [])).toHaveLength(all.length);
  });

  it("sorts by full name path, so a collection sits next to its siblings", () => {
    expect(availableCollections(all, []).map((c) => c.id)).toEqual([
      20, // Cookbook
      22, // Cookbook / Recipes
      21, // Zebra
      23, // Zebra / Recipes
      24, // Zebra / Recipes / Side dishes
    ]);
  });

  it("accepts any iterable of ids", () => {
    expect(
      availableCollections(all, new Set([20, 21])).map((c) => c.id),
    ).not.toContain(21);
  });
});
