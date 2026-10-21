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

import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";

let store;
vi.mock("webextension-polyfill", () => ({
  default: {
    storage: {
      local: {
        async get(key) {
          return key in store ? { [key]: store[key] } : {};
        },
        async set(obj) {
          Object.assign(store, obj);
        },
        async remove(key) {
          delete store[key];
        },
      },
    },
  },
}));

const { getLinkFeatures, LINK_FEATURES_TTL_MS } =
  await import("../src/background/link-config.js");
const { setLinkFeatureCache, getLinkFeatureCache } =
  await import("../src/common/storage.js");

const config = {
  workerBaseURL: "https://recueil.example.com",
  token: "tok",
  deviceId: 1,
  deviceName: "d",
  deviceType: "extension",
};

const NOW = Date.parse("2026-10-17T12:00:00.000Z");
const iso = (ms) => new Date(ms).toISOString();

let fetchMock;
function worker(status, body) {
  fetchMock.mockResolvedValueOnce(
    new Response(body === undefined ? "" : JSON.stringify(body), {
      status,
      headers: { "content-type": "application/json" },
    }),
  );
}

beforeEach(() => {
  store = {};
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the freshness window", () => {
  it("is a day: this is a setting turned on once and then left alone", () => {
    expect(LINK_FEATURES_TTL_MS).toBe(24 * 60 * 60 * 1000);
  });
});

describe("getLinkFeatures", () => {
  it("asks the Worker with the device token, and remembers the answer", async () => {
    worker(200, { links_enabled: true, favicon_inline_max_bytes: 131072 });

    const features = await getLinkFeatures(config, { now: NOW });

    expect(features).toEqual({
      linksEnabled: true,
      faviconInlineMaxBytes: 131072,
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("https://recueil.example.com/config");
    expect(init.headers.Authorization).toBe("Bearer tok");
    expect(await getLinkFeatureCache()).toEqual({
      workerBaseURL: "https://recueil.example.com",
      linksEnabled: true,
      faviconInlineMaxBytes: 131072,
      fetchedAt: iso(NOW),
    });
  });

  it("answers from the cache while it's fresh, without calling the Worker", async () => {
    await setLinkFeatureCache({
      workerBaseURL: config.workerBaseURL,
      linksEnabled: true,
      faviconInlineMaxBytes: 4096,
      fetchedAt: iso(NOW - LINK_FEATURES_TTL_MS + 1000),
    });

    const features = await getLinkFeatures(config, { now: NOW });

    expect(features).toEqual({
      linksEnabled: true,
      faviconInlineMaxBytes: 4096,
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("refreshes once the cache is older than the window", async () => {
    await setLinkFeatureCache({
      workerBaseURL: config.workerBaseURL,
      linksEnabled: true,
      faviconInlineMaxBytes: 4096,
      fetchedAt: iso(NOW - LINK_FEATURES_TTL_MS - 1),
    });
    worker(200, { links_enabled: false, favicon_inline_max_bytes: 8192 });

    const features = await getLinkFeatures(config, { now: NOW });

    expect(features).toEqual({
      linksEnabled: false,
      faviconInlineMaxBytes: 8192,
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("force skips the freshness check", async () => {
    await setLinkFeatureCache({
      workerBaseURL: config.workerBaseURL,
      linksEnabled: true,
      faviconInlineMaxBytes: 4096,
      fetchedAt: iso(NOW),
    });
    worker(200, { links_enabled: true, favicon_inline_max_bytes: 1024 });

    const features = await getLinkFeatures(config, { force: true, now: NOW });

    expect(features.faviconInlineMaxBytes).toBe(1024);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("ignores a cache from a different instance", async () => {
    await setLinkFeatureCache({
      workerBaseURL: "https://other.example.com",
      linksEnabled: true,
      faviconInlineMaxBytes: 4096,
      fetchedAt: iso(NOW),
    });
    worker(200, { links_enabled: false, favicon_inline_max_bytes: 100 });

    const features = await getLinkFeatures(config, { now: NOW });

    expect(features.linksEnabled).toBe(false);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  describe("when the Worker can't be reached", () => {
    it("uses a stale cached answer rather than failing", async () => {
      await setLinkFeatureCache({
        workerBaseURL: config.workerBaseURL,
        linksEnabled: true,
        faviconInlineMaxBytes: 4096,
        fetchedAt: iso(NOW - 10 * LINK_FEATURES_TTL_MS),
      });
      fetchMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));

      const features = await getLinkFeatures(config, { now: NOW });

      expect(features).toEqual({
        linksEnabled: true,
        faviconInlineMaxBytes: 4096,
      });
    });

    it("also falls back on a server error", async () => {
      await setLinkFeatureCache({
        workerBaseURL: config.workerBaseURL,
        linksEnabled: true,
        faviconInlineMaxBytes: 4096,
        fetchedAt: iso(NOW - 10 * LINK_FEATURES_TTL_MS),
      });
      worker(500, { error: "boom" });

      const features = await getLinkFeatures(config, { now: NOW });
      expect(features.linksEnabled).toBe(true);
    });

    it("throws, with no default to fall back on, when nothing is cached", async () => {
      fetchMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));
      await expect(getLinkFeatures(config, { now: NOW })).rejects.toThrow(
        /network error/,
      );
    });

    it("does not use a stale cache from another instance", async () => {
      await setLinkFeatureCache({
        workerBaseURL: "https://other.example.com",
        linksEnabled: true,
        faviconInlineMaxBytes: 4096,
        fetchedAt: iso(NOW - 10 * LINK_FEATURES_TTL_MS),
      });
      fetchMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));
      await expect(getLinkFeatures(config, { now: NOW })).rejects.toThrow();
    });
  });

  it("never papers over a rejected token with a cached answer", async () => {
    await setLinkFeatureCache({
      workerBaseURL: config.workerBaseURL,
      linksEnabled: true,
      faviconInlineMaxBytes: 4096,
      fetchedAt: iso(NOW - 10 * LINK_FEATURES_TTL_MS),
    });
    worker(401);
    await expect(getLinkFeatures(config, { now: NOW })).rejects.toThrow(
      /re-pairing required/,
    );
  });

  it("treats a Worker with no /config as links not available, and caches that", async () => {
    worker(404);

    const features = await getLinkFeatures(config, { now: NOW });

    expect(features).toEqual({ linksEnabled: false, faviconInlineMaxBytes: 0 });
    // Not asked again on the next call.
    await getLinkFeatures(config, { now: NOW + 1000 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("reads anything but an exact true as links off, and an unusable cap as 'send it all through R2'", async () => {
    worker(200, { links_enabled: "true", favicon_inline_max_bytes: "lots" });
    const features = await getLinkFeatures(config, { now: NOW });
    expect(features).toEqual({ linksEnabled: false, faviconInlineMaxBytes: 0 });
  });
});
