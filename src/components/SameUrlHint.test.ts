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
import { render, screen, cleanup } from "@testing-library/svelte";
import SameUrlHint from "./SameUrlHint.svelte";

afterEach(() => {
  cleanup();
});

describe("SameUrlHint", () => {
  it("on a link's screen, says the URL is also archived as a page and links to that page", () => {
    render(SameUrlHint, { kind: "page", id: 7, title: "The archived page" });

    expect(
      screen.getByText(/This URL is also archived as a page:/),
    ).toBeTruthy();
    const anchor = screen.getByRole("link", { name: "The archived page" });
    expect(anchor.getAttribute("href")).toBe("#/pages/7");
  });

  it("on a page's screen, says the URL is also saved as a link and links to that link", () => {
    render(SameUrlHint, { kind: "link", id: 9, title: "The saved link" });

    expect(screen.getByText(/This URL is also saved as a link:/)).toBeTruthy();
    const anchor = screen.getByRole("link", { name: "The saved link" });
    expect(anchor.getAttribute("href")).toBe("#/links/9");
  });

  it("names the other entity inside the same line", () => {
    const { container } = render(SameUrlHint, {
      kind: "page",
      id: 1,
      title: "Named here",
    });
    expect(container.querySelector(".same-url span")?.textContent).toContain(
      "Named here",
    );
  });
});
