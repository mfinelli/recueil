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

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/svelte";
import Footer from "./Footer.svelte";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Footer", () => {
  it("always renders the brand, copyright, license, and links, regardless of /info", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("{}", { status: 500 })),
    );
    render(Footer);

    expect(screen.getByText("recueil")).toBeTruthy();
    expect(screen.getByText("© 2026 Mario Finelli")).toBeTruthy();
    expect(screen.getByText("AGPL-3.0")).toBeTruthy();
    expect(screen.getByRole("link", { name: "GitHub" })).toHaveProperty(
      "href",
      "https://github.com/mfinelli/recueil",
    );
    expect(screen.getByRole("link", { name: "recueil.app" })).toHaveProperty(
      "href",
      "https://recueil.app/",
    );
  });

  it("fetches /info directly, not through apiJSON's /api-prefixed client", () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          version: "1.0.0",
          commit: "acff9fd",
          date: "2026-07-26T11:05:48+00:00",
        }),
        { status: 200 },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    render(Footer);

    expect(fetchMock).toHaveBeenCalledWith("/info");
  });

  it("shows the version/commit badge once /info resolves", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            version: "1.0.0",
            commit: "acff9fd",
            date: "2026-07-26T11:05:48+00:00",
          }),
          { status: 200 },
        ),
      ),
    );
    render(Footer);

    expect(await screen.findByText("v1.0.0 · acff9fd")).toBeTruthy();
  });

  it("omits the version badge when /info fails, without throwing", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("", { status: 500 })),
    );
    render(Footer);

    // Give the failed fetch's .then/.catch a tick to settle.
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(screen.queryByText(/^v\d/)).toBeNull();
  });

  it("omits the version badge when fetch itself rejects (offline, etc.)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new Error("network error")),
    );
    render(Footer);

    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(screen.queryByText(/^v\d/)).toBeNull();
  });
});
