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

import { SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";

describe("fetch (router)", () => {
  it("returns 404 for an unknown path", async () => {
    const response = await SELF.fetch("https://example.com/nope");
    expect(response.status).toBe(404);
  });

  it("returns 404 for the mirror path with the wrong method", async () => {
    const response = await SELF.fetch(
      "https://example.com/internal/users/mirror",
      {
        method: "GET",
      },
    );
    expect(response.status).toBe(404);
  });
});
