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

// Collection-tree helpers shared by the detail screens that edit a page's or a
// link's collections (PageDetail, LinkDetail): all of them work from the flat
// list GET /api/collections returns (each with its slug and parent_id), the
// same list the "add to collection" picker is built from.
import type { Collection } from "./types";

// Walks a collection's parent_id chain in `all`, collecting one segment per
// ancestor (outermost first) via `pick`. Shared by collectionPath (slugs) and
// collectionNamePath (names) -- same traversal, a different field per
// ancestor. Empty if the collection isn't in `all`.
function ancestorSegments(
  all: Collection[],
  collectionId: number,
  pick: (c: Collection) => string,
): string[] {
  const byId = new Map(all.map((c) => [c.id, c]));
  const segments: string[] = [];
  let current = byId.get(collectionId);
  while (current) {
    segments.unshift(pick(current));
    current =
      current.parent_id !== null ? byId.get(current.parent_id) : undefined;
  }
  return segments;
}

// A collection's slug path, e.g. "zebra/side-dishes" -- what routes.ts's
// /collections/* wildcard expects, since it takes the full nested path, not
// just one collection's own slug. A page's or link's own collection memberships
// don't carry a slug, but the full list does, which is enough to rebuild it.
// null if the collection can't be found in `all` (the list failed to load, or
// some other inconsistency), in which case a caller renders plain text instead
// of a broken link.
export function collectionPath(
  all: Collection[],
  collectionId: number,
): string | null {
  const segments = ancestorSegments(all, collectionId, (c) => c.slug);
  return segments.length > 0 ? segments.join("/") : null;
}

// Names joined with " / " for human display, e.g. "Zebra / Recipes": the
// picker's option labels, so two same-named collections under different parents
// read as two different things. Every id this is called with comes from `all`
// itself, so it always resolves at least the collection's own name. (A "/"
// inside a collection's own name reads oddly here, but that's an edge case not
// worth guarding against.)
export function collectionNamePath(
  all: Collection[],
  collectionId: number,
): string {
  return ancestorSegments(all, collectionId, (c) => c.name).join(" / ");
}

// The collections an item isn't already in -- what the picker should offer,
// rather than letting someone "add" a membership that already exists. Sorted
// by full name path (not the fetch's own parent_id-then-name order, which
// doesn't group a collection near its actual children), so same-named
// collections under different parents land near their real siblings.
export function availableCollections(
  all: Collection[],
  memberIds: Iterable<number>,
): Collection[] {
  const members = new Set(memberIds);
  return all
    .filter((c) => !members.has(c.id))
    .map((c) => ({ collection: c, path: collectionNamePath(all, c.id) }))
    .sort((a, b) => a.path.localeCompare(b.path))
    .map((entry) => entry.collection);
}
