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

import { afterEach, describe, expect, it, vi } from "vitest";
import { selectFavicon } from "../src/capture-inject/favicon.js";

/**
 * Builds a fake fetchFn (matching relay-fetch.js's FetchLike contract)
 * from a map of absolute-URL -> descriptor. A missing entry, or one with
 * status >= 400, behaves like a failed/absent favicon -- exactly what
 * fetchFavicon() in the real module treats as "nothing here."
 */
function fakeFetch(responses) {
  const calls = [];
  const fn = vi.fn(async (url) => {
    calls.push(url);
    const entry = responses[url];
    if (!entry) {
      return {
        status: 404,
        statusText: "Not Found",
        url,
        headers: fakeHeaders({}),
        arrayBuffer: async () => new ArrayBuffer(0),
      };
    }
    if (entry.throws) {
      throw new Error("network error");
    }
    return {
      status: entry.status ?? 200,
      statusText: entry.statusText ?? "OK",
      url: entry.finalURL ?? url,
      headers: fakeHeaders(entry.headers ?? {}),
      arrayBuffer: async () =>
        entry.body ?? new TextEncoder().encode("fake-bytes").buffer,
    };
  });
  fn.calls = calls;
  return fn;
}

function fakeHeaders(map) {
  const lower = Object.fromEntries(
    Object.entries(map).map(([k, v]) => [k.toLowerCase(), v]),
  );
  return { get: (name) => lower[name.toLowerCase()] ?? null };
}

function baseURL(path) {
  return new URL(path, document.baseURI).href;
}

describe("selectFavicon", () => {
  afterEach(() => {
    document.head.innerHTML = "";
  });

  describe("no <link> tags -- conventional path fallback", () => {
    it("returns the first fallback path that succeeds (favicon.svg)", async () => {
      const svgURL = baseURL("/favicon.svg");
      const fetchFn = fakeFetch({
        [svgURL]: { headers: { "content-type": "image/svg+xml" } },
      });

      const result = await selectFavicon(fetchFn);

      expect(result).not.toBeNull();
      expect(result.url).toBe(svgURL);
      expect(result.ext).toBe("svg");
      expect(fetchFn.calls).toEqual([svgURL]);
    });

    it("falls through in order (svg, png, ico), stopping at the first success", async () => {
      const icoURL = baseURL("/favicon.ico");
      const fetchFn = fakeFetch({
        [icoURL]: { headers: { "content-type": "image/x-icon" } },
      });

      const result = await selectFavicon(fetchFn);

      expect(result.url).toBe(icoURL);
      expect(result.ext).toBe("ico");
      expect(fetchFn.calls).toEqual([
        baseURL("/favicon.svg"),
        baseURL("/favicon.png"),
        icoURL,
      ]);
    });

    it("returns null when every conventional path fails", async () => {
      const fetchFn = fakeFetch({});
      const result = await selectFavicon(fetchFn);
      expect(result).toBeNull();
      expect(fetchFn.calls).toHaveLength(3);
    });

    it("treats a zero-byte response the same as a failure", async () => {
      const svgURL = baseURL("/favicon.svg");
      const pngURL = baseURL("/favicon.png");
      const fetchFn = fakeFetch({
        [svgURL]: { body: new ArrayBuffer(0) },
        [pngURL]: { headers: { "content-type": "image/png" } },
      });

      const result = await selectFavicon(fetchFn);
      expect(result.url).toBe(pngURL);
    });
  });

  describe("<link> tag present -- takes priority over conventional paths", () => {
    it("uses the declared link and never probes conventional paths", async () => {
      document.head.innerHTML = `<link rel="icon" href="/my-icon.png">`;
      const iconURL = baseURL("/my-icon.png");
      const fetchFn = fakeFetch({
        [iconURL]: { headers: { "content-type": "image/png" } },
      });

      const result = await selectFavicon(fetchFn);

      expect(result.url).toBe(iconURL);
      expect(fetchFn.calls).toEqual([iconURL]);
    });

    it("prefers svg outright over a larger declared raster candidate", async () => {
      document.head.innerHTML = `
        <link rel="icon" href="/icon-512.png" sizes="512x512">
        <link rel="icon" type="image/svg+xml" href="/icon.svg">
      `;
      const svgURL = baseURL("/icon.svg");
      const fetchFn = fakeFetch({
        [svgURL]: { headers: { "content-type": "image/svg+xml" } },
      });

      const result = await selectFavicon(fetchFn);
      expect(result.url).toBe(svgURL);
    });

    it("detects svg by file extension even without an explicit type attribute", async () => {
      document.head.innerHTML = `
        <link rel="icon" href="/icon-512.png" sizes="512x512">
        <link rel="icon" href="/icon.svg">
      `;
      const svgURL = baseURL("/icon.svg");
      const fetchFn = fakeFetch({ [svgURL]: {} });

      const result = await selectFavicon(fetchFn);
      expect(result.url).toBe(svgURL);
    });

    it("picks the raster candidate with the largest declared sizes", async () => {
      document.head.innerHTML = `
        <link rel="icon" href="/icon-32.png" sizes="32x32">
        <link rel="icon" href="/icon-192.png" sizes="192x192">
        <link rel="icon" href="/icon-16.png" sizes="16x16">
      `;
      const bigURL = baseURL("/icon-192.png");
      const fetchFn = fakeFetch({ [bigURL]: {} });

      const result = await selectFavicon(fetchFn);
      expect(result.url).toBe(bigURL);
    });

    it('treats sizes="any" as larger than any explicit raster size', async () => {
      document.head.innerHTML = `
        <link rel="icon" href="/icon-512.png" sizes="512x512">
        <link rel="icon" href="/icon-any.png" sizes="any">
      `;
      const anyURL = baseURL("/icon-any.png");
      const fetchFn = fakeFetch({ [anyURL]: {} });

      const result = await selectFavicon(fetchFn);
      expect(result.url).toBe(anyURL);
    });

    it("recognizes shortcut icon and apple-touch-icon rel values, case-insensitively", async () => {
      document.head.innerHTML = `<link rel="Shortcut Icon" href="/icon.ico">`;
      const iconURL = baseURL("/icon.ico");
      const fetchFn = fakeFetch({ [iconURL]: {} });

      const result = await selectFavicon(fetchFn);
      expect(result.url).toBe(iconURL);
    });

    it("gives up entirely when the declared link fails, rather than falling back", async () => {
      document.head.innerHTML = `<link rel="icon" href="/broken.png">`;
      // Every conventional fallback path would succeed if it were ever
      // tried -- the point of this test is confirming it never is.
      const fetchFn = fakeFetch({
        [baseURL("/favicon.svg")]: {},
        [baseURL("/favicon.png")]: {},
        [baseURL("/favicon.ico")]: {},
      });

      const result = await selectFavicon(fetchFn);

      expect(result).toBeNull();
      expect(fetchFn.calls).toEqual([baseURL("/broken.png")]);
    });

    it("falls back to conventional paths when the only link tag has no href", async () => {
      // A rel="icon" tag with no href isn't really a declaration at all --
      // this is real, if slightly surprising, behavior worth pinning down
      // rather than leaving implicit.
      document.head.innerHTML = `<link rel="icon">`;
      const svgURL = baseURL("/favicon.svg");
      const fetchFn = fakeFetch({ [svgURL]: {} });

      const result = await selectFavicon(fetchFn);
      expect(result.url).toBe(svgURL);
    });
  });

  describe("extension inference", () => {
    it.each([
      ["image/svg+xml", "svg"],
      ["image/png", "png"],
      ["image/x-icon", "ico"],
      ["image/vnd.microsoft.icon", "ico"],
      ["image/svg+xml; charset=utf-8", "svg"],
    ])(
      "infers %s as %s from content-type",
      async (contentType, expectedExt) => {
        const url = baseURL("/favicon.svg");
        const fetchFn = fakeFetch({
          [url]: { headers: { "content-type": contentType } },
        });

        const result = await selectFavicon(fetchFn);
        expect(result.ext).toBe(expectedExt);
      },
    );

    it("falls back to the URL's own extension when content-type is missing", async () => {
      const url = baseURL("/favicon.png");
      const fetchFn = fakeFetch({ [url]: {} });

      const result = await selectFavicon(fetchFn);
      expect(result.ext).toBe("png");
    });

    it("defaults to ico when neither content-type nor URL extension is recognizable", async () => {
      document.head.innerHTML = `<link rel="icon" href="/icon-endpoint">`;
      const url = baseURL("/icon-endpoint");
      const fetchFn = fakeFetch({ [url]: {} });

      const result = await selectFavicon(fetchFn);
      expect(result.ext).toBe("ico");
    });
  });
});
