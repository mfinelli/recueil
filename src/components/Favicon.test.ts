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
import { render, fireEvent, cleanup } from "@testing-library/svelte";
import Favicon from "./Favicon.svelte";

afterEach(() => {
  cleanup();
});

describe("Favicon", () => {
  it("renders the image, lazily and with no alt text, at the size asked for", () => {
    const { container } = render(Favicon, {
      src: "/api/pages/1/favicon",
      size: 20,
    });

    const img = container.querySelector<HTMLImageElement>("img.favicon");
    expect(img?.getAttribute("src")).toBe("/api/pages/1/favicon");
    expect(img?.getAttribute("alt")).toBe("");
    expect(img?.getAttribute("loading")).toBe("lazy");
    expect(img?.style.width).toBe("20px");
    expect(img?.style.height).toBe("20px");
    expect(container.querySelector(".favicon-placeholder")).toBeNull();
  });

  it("skips the request entirely, going straight to the placeholder, when there's no src", () => {
    const { container } = render(Favicon, { src: null, size: 20 });

    expect(container.querySelector("img")).toBeNull();
    const placeholder = container.querySelector<HTMLElement>(
      ".favicon-placeholder",
    );
    expect(placeholder).toBeTruthy();
    expect(placeholder?.getAttribute("aria-hidden")).toBe("true");
    expect(placeholder?.style.width).toBe("20px");
    expect(container.querySelector("svg")).toBeTruthy();
  });

  it("falls back to the placeholder when the image fails to load", async () => {
    const { container } = render(Favicon, {
      src: "/api/pages/1/favicon",
      size: 14,
    });

    await fireEvent.error(container.querySelector("img") as HTMLImageElement);

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".favicon-placeholder")).toBeTruthy();
  });

  it("scales the placeholder's Globe with the size", () => {
    const sizeOf = (size: number) => {
      const { container, unmount } = render(Favicon, { src: null, size });
      const svg = container.querySelector("svg");
      const width = Number(svg?.getAttribute("width"));
      unmount();
      return width;
    };
    expect(sizeOf(20)).toBeGreaterThan(sizeOf(14));
    expect(sizeOf(14)).toBeGreaterThan(0);
  });

  it("shows nothing at all, when told not to use a placeholder, if there's no src or it fails", async () => {
    const none = render(Favicon, { src: null, size: 16, placeholder: false });
    expect(none.container.querySelector("img")).toBeNull();
    expect(none.container.querySelector(".favicon-placeholder")).toBeNull();
    none.unmount();

    const failing = render(Favicon, {
      src: "/api/links/9/favicon",
      size: 16,
      placeholder: false,
    });
    await fireEvent.error(
      failing.container.querySelector("img") as HTMLImageElement,
    );
    expect(failing.container.querySelector("img")).toBeNull();
    expect(failing.container.querySelector(".favicon-placeholder")).toBeNull();
  });

  it("remembers which src failed, so a different src gets a fresh try", async () => {
    const { container, rerender } = render(Favicon, {
      src: "/api/pages/1/favicon",
      size: 20,
    });
    await fireEvent.error(container.querySelector("img") as HTMLImageElement);
    expect(container.querySelector("img")).toBeNull();

    await rerender({ src: "/api/pages/2/favicon", size: 20 });

    expect(container.querySelector("img")?.getAttribute("src")).toBe(
      "/api/pages/2/favicon",
    );
  });

  it("stays failed for the same src", async () => {
    const { container, rerender } = render(Favicon, {
      src: "/api/pages/1/favicon",
      size: 20,
    });
    await fireEvent.error(container.querySelector("img") as HTMLImageElement);

    await rerender({ src: "/api/pages/1/favicon", size: 20 });

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector(".favicon-placeholder")).toBeTruthy();
  });
});
