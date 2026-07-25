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
import { previewSlug } from "./slugPreview";

describe("previewSlug", () => {
  it.each([
    ["recipes", "recipes"],
    ["My Recipes", "my-recipes"],
    ["C++", "c"],
    ["rock & roll", "rock-roll"],
    ["café", "cafe"],
    ["naïve résumé", "naive-resume"],
    ["  --Go!--  ", "go"],
    ["42", "42"],
    ["js-notes", "js-notes"],
    ["a   b---c", "a-b-c"],
    ["🎉🎉🎉", ""],
    ["日本語", ""],
    ["日本語 notes", "notes"],
    ["", ""],
  ])("previewSlug(%j) === %j", (input, expected) => {
    expect(previewSlug(input)).toBe(expected);
  });
});
