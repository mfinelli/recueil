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
import { session } from "../lib/session.svelte";
import LinkCountLine from "./LinkCountLine.svelte";

afterEach(() => {
  cleanup();
  session.linksEnabled = false;
});

function show(props: {
  count: number;
  kind: "tag" | "collection";
  href?: string;
}) {
  session.linksEnabled = true;
  return render(LinkCountLine, { href: "/links?tag=x", ...props });
}

describe("LinkCountLine", () => {
  it.each([
    [1, "tag", "1 link has this tag."],
    [3, "tag", "3 links have this tag."],
    [1, "collection", "1 link in this collection."],
    [12, "collection", "12 links in this collection."],
  ] as const)("reads %i for a %s as %s", (count, kind, text) => {
    show({ count, kind });
    expect(screen.getByText(text)).toBeTruthy();
  });

  it("links to the given filtered Links page", () => {
    show({ count: 2, kind: "collection", href: "/links?collection=7" });
    expect(
      screen.getByRole("link", { name: "View links" }).getAttribute("href"),
    ).toBe("#/links?collection=7");
  });

  it("renders nothing for a count of 0", () => {
    const { container } = show({ count: 0, kind: "tag" });
    expect(container.textContent?.trim()).toBe("");
  });

  it("renders nothing with links off", () => {
    session.linksEnabled = false;
    const { container } = render(LinkCountLine, {
      count: 5,
      kind: "tag",
      href: "/links?tag=x",
    });
    expect(container.textContent?.trim()).toBe("");
  });
});
