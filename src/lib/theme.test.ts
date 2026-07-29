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

import { describe, it, expect, afterEach } from "vitest";
import { applyTheme } from "./theme";

const STORAGE_KEY = "recueil-theme";

afterEach(() => {
  delete document.documentElement.dataset.theme;
  localStorage.clear();
});

describe("applyTheme", () => {
  it("sets data-theme and caches the value for light", () => {
    applyTheme("light");

    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem(STORAGE_KEY)).toBe("light");
  });

  it("sets data-theme and caches the value for dark", () => {
    applyTheme("dark");

    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem(STORAGE_KEY)).toBe("dark");
  });

  it("removes data-theme entirely (not 'undefined') and clears the cache for null", () => {
    applyTheme("dark");
    applyTheme(null);

    expect("theme" in document.documentElement.dataset).toBe(false);
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("treats any value other than exactly light/dark as automatic", () => {
    applyTheme("dark");
    applyTheme("solarized");

    expect("theme" in document.documentElement.dataset).toBe(false);
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("empty string is treated as automatic, same as null", () => {
    applyTheme("dark");
    applyTheme("");

    expect("theme" in document.documentElement.dataset).toBe(false);
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull();
  });
});
