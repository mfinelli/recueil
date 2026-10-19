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

// LinkList takes `links`, `emptyMessage` and `emptyHint` as plain props -- no
// fetching, no apiJSON mock needed, as with PageList.test.ts. How a list of
// links ends up in front of the component (search, pagination, loading/error
// state) is Links.test.ts's concern; this file is about what LinkList does
// with whatever array it's handed.
import { describe, it, expect, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";
import type { Link } from "../lib/types";
import LinkList from "./LinkList.svelte";

afterEach(() => {
  cleanup();
});

const exampleLink: Link = {
  id: 1,
  raw_url: "https://example.com/watch?v=abc#t=30",
  normalized_url: "https://example.com/watch?v=abc",
  title: "An example video",
  favicon_path: "ab/cd/id/favicon.png",
  notes: null,
  source: "extension",
  excluded_from_mirror: false,
  matching_page_id: null,
  created_at: "2026-05-01T12:00:00Z",
  updated_at: "2026-05-01T12:00:00Z",
};

const otherLink: Link = {
  ...exampleLink,
  id: 2,
  raw_url: "https://example.org/repo",
  normalized_url: "https://example.org/repo",
  title: "A repository",
};

function props(links: Link[]) {
  return { links, emptyMessage: "Nothing here." };
}

describe("LinkList", () => {
  it("renders a row with the favicon, title, normalized URL and saved date", () => {
    const { container } = render(LinkList, props([exampleLink]));

    expect(screen.getByText("An example video")).toBeTruthy();
    expect(screen.getByText("https://example.com/watch?v=abc")).toBeTruthy();
    const img = container.querySelector<HTMLImageElement>(".favicon");
    expect(img?.src).toContain("/api/links/1/favicon");
    expect(
      screen.getByText(
        new Date(exampleLink.created_at).toLocaleDateString(undefined, {
          year: "numeric",
          month: "short",
          day: "numeric",
        }),
      ),
    ).toBeTruthy();
  });

  it("renders every link, in the order given", () => {
    render(LinkList, props([exampleLink, otherLink]));
    const titles = screen
      .getAllByText(/An example video|A repository/)
      .map((el) => el.textContent);
    expect(titles).toEqual(["An example video", "A repository"]);
  });

  it("falls back to the normalized URL as the title when there's none", () => {
    const { container } = render(
      LinkList,
      props([{ ...exampleLink, title: null }]),
    );
    expect(container.querySelector(".title")?.textContent).toBe(
      "https://example.com/watch?v=abc",
    );
  });

  describe("the row's two destinations", () => {
    it("opens the link's own detail screen from the row", () => {
      render(LinkList, props([exampleLink]));
      const main = screen
        .getByText("An example video")
        .closest("a") as HTMLAnchorElement;
      // use:link rewrites the href for hash routing.
      expect(main.getAttribute("href")).toBe("#/links/1");
    });

    it("opens the saved raw_url, fragment and all, in a new tab from the trailing icon", () => {
      render(LinkList, props([exampleLink]));
      const open = screen.getByRole("link", {
        name: "Open An example video in a new tab",
      }) as HTMLAnchorElement;
      expect(open.getAttribute("href")).toBe(exampleLink.raw_url);
      expect(open.getAttribute("target")).toBe("_blank");
      expect(open.getAttribute("rel")).toBe("noreferrer");
    });

    it("names the trailing icon by the URL when there's no title", () => {
      render(LinkList, props([{ ...exampleLink, title: null }]));
      expect(
        screen.getByRole("link", {
          name: "Open https://example.com/watch?v=abc in a new tab",
        }),
      ).toBeTruthy();
    });

    it("makes the two siblings, not nested: the icon link is not inside the row link", () => {
      const { container } = render(LinkList, props([exampleLink]));
      const open = container.querySelector(".open") as HTMLElement;
      expect(open.closest(".row")).toBeNull();
      expect(container.querySelectorAll("li a")).toHaveLength(2);
    });
  });

  it("puts the favicon, title, URL and date inside the one row link, so the whole row is its target", () => {
    const { container } = render(LinkList, props([exampleLink]));
    const row = container.querySelector(".row") as HTMLElement;
    for (const selector of [".favicon", ".title", ".url", ".date"]) {
      expect(row.querySelector(selector), selector).toBeTruthy();
    }
  });

  describe("the favicon", () => {
    it("shows the Globe placeholder, without an image request, when there's no favicon", () => {
      const { container } = render(
        LinkList,
        props([{ ...exampleLink, favicon_path: null }]),
      );
      expect(container.querySelector(".favicon")).toBeNull();
      expect(container.querySelector(".favicon-placeholder")).toBeTruthy();
    });

    it("falls back to the placeholder when the favicon request fails", async () => {
      const { container } = render(LinkList, props([exampleLink]));
      const img = container.querySelector(".favicon") as HTMLImageElement;

      await fireEvent.error(img);

      expect(container.querySelector(".favicon")).toBeNull();
      expect(container.querySelector(".favicon-placeholder")).toBeTruthy();
    });

    it("a failed favicon doesn't hide another link's working one", async () => {
      const { container } = render(LinkList, props([exampleLink, otherLink]));
      const imgs = container.querySelectorAll<HTMLImageElement>(".favicon");
      expect(imgs).toHaveLength(2);

      await fireEvent.error(imgs[0]);

      const remaining =
        container.querySelectorAll<HTMLImageElement>(".favicon");
      expect(remaining).toHaveLength(1);
      expect(remaining[0].src).toContain("/api/links/2/favicon");
    });
  });

  describe("the Also archived marker", () => {
    it("appears, as an image role with a label, only when a page has the same URL", () => {
      render(
        LinkList,
        props([{ ...exampleLink, matching_page_id: 7 }, otherLink]),
      );
      expect(
        screen.getAllByRole("img", {
          name: "This URL is also archived as a page",
        }),
      ).toHaveLength(1);
    });

    it('reads "Also archived" on its own line inside the row, under the URL', () => {
      const { container } = render(
        LinkList,
        props([{ ...exampleLink, matching_page_id: 7 }]),
      );
      const meta = container.querySelector(".row .meta");
      expect(meta?.textContent?.trim()).toBe("Also archived");
    });

    it("is not a link: the row carries no link to the matching page", () => {
      const { container } = render(
        LinkList,
        props([{ ...exampleLink, matching_page_id: 7 }]),
      );
      expect(container.querySelector('a[href^="/pages/"]')).toBeNull();
    });

    it("is absent when no page has the same URL", () => {
      render(LinkList, props([exampleLink]));
      expect(
        screen.queryByRole("img", {
          name: "This URL is also archived as a page",
        }),
      ).toBeNull();
    });
  });

  it("shows no Wayback state in a row", () => {
    const { container } = render(LinkList, props([exampleLink]));
    expect(container.textContent).not.toMatch(/wayback|internet archive/i);
  });

  describe("the empty state", () => {
    it("shows the message, and the hint when given", () => {
      render(LinkList, {
        links: [],
        emptyMessage: "No links saved yet.",
        emptyHint: "Use the extension.",
      });
      expect(screen.getByText("No links saved yet.")).toBeTruthy();
      expect(screen.getByText("Use the extension.")).toBeTruthy();
    });

    it("shows just the message when there's no hint", () => {
      const { container } = render(LinkList, {
        links: [],
        emptyMessage: "No links match your search.",
      });
      expect(screen.getByText("No links match your search.")).toBeTruthy();
      expect(container.querySelector(".hint")).toBeNull();
    });

    it("renders no list", () => {
      const { container } = render(LinkList, props([]));
      expect(container.querySelector("ul")).toBeNull();
    });
  });
});
