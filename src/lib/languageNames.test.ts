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
import { languageDisplayName } from "./languageNames";

describe("languageDisplayName", () => {
  it("translates a known config name into the target locale", () => {
    expect(languageDisplayName("french", "en")).toBe("French");
    expect(languageDisplayName("french", "fr")).toBe("français");
    expect(languageDisplayName("german", "fr")).toBe("allemand");
  });

  it("is case-insensitive to nothing -- config names are always lowercase, but the display name itself is properly cased per locale", () => {
    expect(languageDisplayName("english", "en")).toBe("English");
    expect(languageDisplayName("english", "fr")).toBe("anglais");
  });

  it("falls back to a capitalized raw name for a config with no BCP-47 mapping", () => {
    // "simple" itself is never actually passed to this function by
    // PageDetail.svelte (it's special-cased before the call), but the
    // fallback path this exercises is the same one a genuinely unmapped
    // config -- e.g. one a newer Postgres ships that CONFIG_TO_BCP47
    // hasn't been updated for -- would hit too.
    expect(languageDisplayName("simple", "en")).toBe("Simple");
    expect(languageDisplayName("klingon", "en")).toBe("Klingon");
  });
});
