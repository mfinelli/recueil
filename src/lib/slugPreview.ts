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

// Client-side best-effort mirror of the backend's internal/slug.Generate,
// used only to show a live "URL will be /tags/..." preview while someone
// types a name -- NOT the source of truth. The server always computes
// and validates the real slug on save (see resolveSlug in
// internal/httpapi), so this never needs to be byte-for-byte identical;
// it only needs to be close enough that the preview doesn't visibly
// mismatch what gets saved a moment later. Known, accepted gaps: no
// MaxLength truncation (backend caps at 63 chars; a live preview for a
// name that long has bigger UX problems than a mismatched preview), and
// `\p{M}` strips Unicode combining marks the same way NFKD decomposition
// does in Go, but the two runtimes' Unicode tables could in principle
// drift a version apart from each other.
export function previewSlug(name: string): string {
  const decomposed = name.normalize("NFKD").replace(/\p{M}/gu, "");
  return decomposed
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}
