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
const tabsQueryMock = vi.fn();
const tabsRemoveMock = vi.fn();
const executeScriptMock = vi.fn();

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
    tabs: {
      query: (...args) => tabsQueryMock(...args),
      remove: (...args) => tabsRemoveMock(...args),
    },
    scripting: {
      executeScript: (...args) => executeScriptMock(...args),
    },
    i18n: {
      getMessage(key) {
        return (
          {
            linksDisabledError:
              "recueil: saving links is turned off on this instance",
          }[key] ?? ""
        );
      },
    },
  },
}));

const {
  saveActiveTabAsLink,
  saveTabAsLink,
  getLinksEnabled,
  LinksDisabledError,
  refreshLinkFeatures,
} = await import("../src/background/link.js");
const { NotPairedError } = await import("../src/background/capture.js");
const { setLinkFeatureCache, setClaimedTab, getClaimedTabs } =
  await import("../src/common/storage.js");

const WORKER = "https://recueil.example.com";
const config = {
  workerBaseURL: WORKER,
  token: "tok",
  deviceId: 1,
  deviceName: "d",
  deviceType: "extension",
};

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

// A fresh feature cache, so no test here triggers a GET /config unless it means
// to.
async function cacheFeatures({ linksEnabled = true, max = 1024 } = {}) {
  await setLinkFeatureCache({
    workerBaseURL: WORKER,
    linksEnabled,
    faviconInlineMaxBytes: max,
    fetchedAt: new Date().toISOString(),
  });
}

// Every fetch the code under test makes, in order, as plain data.
let calls;
let routes;
function route(method, urlSuffix, respond) {
  routes.push({ method, urlSuffix, respond });
}

function stubFetch() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url, init = {}) => {
      const method = init.method ?? "GET";
      calls.push({
        method,
        url,
        headers: init.headers,
        body: init.body,
        json:
          typeof init.body === "string" && init.headers?.["Content-Type"]
            ? JSON.parse(init.body)
            : undefined,
      });
      const hit = routes.find(
        (r) => r.method === method && url.endsWith(r.urlSuffix),
      );
      if (!hit) throw new Error(`unexpected fetch: ${method} ${url}`);
      return hit.respond();
    }),
  );
}

const json = (status, body) => () =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });

// What link-inject.js hands back, as the second executeScript call returns it.
function pageSays({ title = "An example page", favicon = null } = {}) {
  executeScriptMock.mockImplementation(async (opts) => {
    if (opts.files) return [];
    return [
      {
        result: {
          title,
          favicon: favicon && {
            bytes: Array.from(favicon.bytes),
            ext: favicon.ext,
          },
        },
      },
    ];
  });
}

const icon = (n, ext = "png") => ({
  bytes: new Uint8Array(n).fill(7),
  ext,
});

const callsTo = (suffix) => calls.filter((c) => c.url.endsWith(suffix));

beforeEach(async () => {
  store = { "recueil:config": config };
  calls = [];
  routes = [];
  tabsQueryMock.mockReset();
  tabsRemoveMock.mockReset().mockResolvedValue(undefined);
  executeScriptMock.mockReset();
  tabsQueryMock.mockResolvedValue([
    { id: 7, url: "https://example.com/page#frag", title: "Browser's title" },
  ]);
  stubFetch();
  route("POST", "/links", json(201, { id: "ok" }));
  await cacheFeatures();
  pageSays();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("saving the active tab as a link", () => {
  it("sends the URL, the title and a save time, with the device token, to POST /links", async () => {
    const result = await saveActiveTabAsLink();

    expect(callsTo("/links")).toHaveLength(1);
    const call = callsTo("/links")[0];
    expect(call.url).toBe(`${WORKER}/links`);
    expect(call.method).toBe("POST");
    expect(call.headers.Authorization).toBe("Bearer tok");
    expect(call.json.link_id).toMatch(UUID);
    expect(call.json.url).toBe("https://example.com/page#frag");
    expect(call.json.title).toBe("An example page");
    expect(new Date(call.json.saved_at).toISOString()).toBe(call.json.saved_at);
    expect(call.json).not.toHaveProperty("favicon_ext");
    expect(call.json).not.toHaveProperty("favicon_data");

    expect(result.linkId).toBe(call.json.link_id);
    expect(result.title).toBe("An example page");
  });

  it("injects only the small link bundle, into the top frame, never the capture bundle", async () => {
    await saveActiveTabAsLink();

    const loads = executeScriptMock.mock.calls
      .map(([opts]) => opts)
      .filter((opts) => opts.files);
    expect(loads).toEqual([
      { target: { tabId: 7 }, files: ["link-inject.js"] },
    ]);
  });

  it("uses a fresh id for each save", async () => {
    await saveActiveTabAsLink();
    await saveActiveTabAsLink();
    const [a, b] = callsTo("/links").map((c) => c.json.link_id);
    expect(a).not.toBe(b);
  });

  it("leaves the title out when the page has none, trimming one it has", async () => {
    pageSays({ title: "   " });
    await saveActiveTabAsLink();
    expect(callsTo("/links")[0].json).not.toHaveProperty("title");

    calls.length = 0;
    pageSays({ title: "  Spaced out  " });
    await saveActiveTabAsLink();
    expect(callsTo("/links")[0].json.title).toBe("Spaced out");
  });

  it("does not close a tab it didn't open", async () => {
    await saveActiveTabAsLink();
    expect(tabsRemoveMock).not.toHaveBeenCalled();
  });

  describe("the favicon", () => {
    it("rides inline, as base64 with its format, when it fits under the cap", async () => {
      pageSays({ favicon: icon(512) });

      await saveActiveTabAsLink();

      const body = callsTo("/links")[0].json;
      expect(body.favicon_ext).toBe("png");
      expect(atob(body.favicon_data)).toHaveLength(512);
      expect(callsTo("/links/upload-urls")).toHaveLength(0);
    });

    it("rides inline at exactly the cap", async () => {
      pageSays({ favicon: icon(1024) });
      await saveActiveTabAsLink();
      expect(callsTo("/links")[0].json).toHaveProperty("favicon_data");
      expect(callsTo("/links/upload-urls")).toHaveLength(0);
    });

    it("goes through R2 when it's over the cap: presign, PUT, then a save carrying only the format", async () => {
      pageSays({ favicon: icon(1025, "ico") });
      route(
        "POST",
        "/links/upload-urls",
        json(200, {
          link_id: "x",
          upload_url_favicon: "https://r2.example.com/signed-put",
          r2_key_favicon: "pending-links/1/x/favicon.ico",
          required_headers_favicon: { "x-amz-checksum-sha256": "abc=" },
        }),
      );
      vi.stubGlobal(
        "fetch",
        vi.fn(async (url, init = {}) => {
          const method = init.method ?? "GET";
          calls.push({
            method,
            url,
            headers: init.headers,
            body: init.body,
            json:
              typeof init.body === "string" && init.headers?.["Content-Type"]
                ? JSON.parse(init.body)
                : undefined,
          });
          if (url === "https://r2.example.com/signed-put") {
            return new Response(null, { status: 200 });
          }
          const hit = routes.find(
            (r) => r.method === method && url.endsWith(r.urlSuffix),
          );
          if (!hit) throw new Error(`unexpected fetch: ${method} ${url}`);
          return hit.respond();
        }),
      );

      await saveActiveTabAsLink();

      const [presign] = callsTo("/links/upload-urls");
      expect(presign.json.favicon_ext).toBe("ico");
      expect(presign.json.content_sha256_favicon).toMatch(/^[0-9a-f]{64}$/);

      const put = calls.find(
        (c) => c.url === "https://r2.example.com/signed-put",
      );
      expect(put.method).toBe("PUT");
      expect(put.headers).toEqual({ "x-amz-checksum-sha256": "abc=" });
      expect(put.body.byteLength).toBe(1025);
      // The R2 request carries no Authorization: it's the presigned URL's own
      // signature that authorizes it, not the device token.
      expect(put.headers).not.toHaveProperty("Authorization");

      const save = callsTo("/links").at(-1).json;
      expect(save.favicon_ext).toBe("ico");
      expect(save).not.toHaveProperty("favicon_data");
      expect(save.link_id).toBe(presign.json.link_id);
    });

    it("takes the R2 route when the Worker refuses an inline favicon as too large anyway, and refreshes its cached cap", async () => {
      pageSays({ favicon: icon(512) });
      // The first save is refused (the cap is lower than this device thought).
      routes.length = 0;
      let saves = 0;
      route("POST", "/links", () =>
        ++saves === 1
          ? new Response("Favicon too large to send inline", { status: 413 })
          : new Response(JSON.stringify({ id: "ok" }), {
              status: 201,
              headers: { "content-type": "application/json" },
            }),
      );
      route(
        "GET",
        "/config",
        json(200, { links_enabled: true, favicon_inline_max_bytes: 100 }),
      );
      route(
        "POST",
        "/links/upload-urls",
        json(200, {
          upload_url_favicon: "https://r2.example.com/put2",
          required_headers_favicon: {},
        }),
      );
      route(
        "PUT",
        "https://r2.example.com/put2",
        () => new Response(null, { status: 200 }),
      );

      await saveActiveTabAsLink();

      expect(callsTo("/config")).toHaveLength(1);
      expect(callsTo("/links/upload-urls")).toHaveLength(1);
      const saved = callsTo("/links");
      expect(saved).toHaveLength(2);
      expect(saved[0].json).toHaveProperty("favicon_data");
      expect(saved[1].json).not.toHaveProperty("favicon_data");
      expect(saved[1].json.favicon_ext).toBe("png");
      expect(saved[1].json.link_id).toBe(saved[0].json.link_id);
    });

    it("lets any other failure of the inline save through unchanged", async () => {
      pageSays({ favicon: icon(512) });
      routes.length = 0;
      route("POST", "/links", () => new Response("nope", { status: 500 }));

      await expect(saveActiveTabAsLink()).rejects.toThrow(/500/);
      expect(callsTo("/links/upload-urls")).toHaveLength(0);
    });
  });

  describe("when the page can't be read", () => {
    it("still saves the link, with the title the browser already has and no favicon", async () => {
      executeScriptMock.mockRejectedValue(
        new Error("Cannot access contents of url chrome://extensions"),
      );

      await saveActiveTabAsLink();

      const body = callsTo("/links")[0].json;
      expect(body.title).toBe("Browser's title");
      expect(body.url).toBe("https://example.com/page#frag");
      expect(body).not.toHaveProperty("favicon_ext");
    });
  });

  describe("completing a claimed queue item", () => {
    beforeEach(async () => {
      await setClaimedTab(7, "item-42");
      route("POST", "/queue/item-42/complete-link", json(201, { id: "ok" }));
    });

    it("goes to the queue's own route without a URL, which the Worker takes from the item", async () => {
      await saveActiveTabAsLink();

      expect(callsTo("/links")).toHaveLength(0);
      const call = callsTo("/queue/item-42/complete-link")[0];
      expect(call.json.link_id).toMatch(UUID);
      expect(call.json).not.toHaveProperty("url");
      expect(call.json.title).toBe("An example page");
    });

    it("sends an inline favicon the same way", async () => {
      pageSays({ favicon: icon(300) });
      await saveActiveTabAsLink();
      expect(callsTo("/queue/item-42/complete-link")[0].json).toHaveProperty(
        "favicon_data",
      );
    });

    it("forgets the claim and closes the tab it opened, once it succeeds", async () => {
      await saveActiveTabAsLink();

      expect(await getClaimedTabs()).toEqual({});
      expect(tabsRemoveMock).toHaveBeenCalledWith(7);
    });

    it("keeps the claim and the tab when the save fails, so it can be tried again", async () => {
      routes.length = 0;
      route(
        "POST",
        "/queue/item-42/complete-link",
        () => new Response("gone", { status: 410 }),
      );

      await expect(saveActiveTabAsLink()).rejects.toThrow(/410/);

      expect(await getClaimedTabs()).toEqual({ 7: "item-42" });
      expect(tabsRemoveMock).not.toHaveBeenCalled();
    });

    it("doesn't fail the save because the tab was already closed", async () => {
      tabsRemoveMock.mockRejectedValueOnce(new Error("No tab with id"));
      await expect(saveActiveTabAsLink()).resolves.toBeTruthy();
    });
  });

  describe("when it can't be done", () => {
    it("refuses with a clear error when links are off, sending nothing", async () => {
      await cacheFeatures({ linksEnabled: false });
      await expect(saveActiveTabAsLink()).rejects.toBeInstanceOf(
        LinksDisabledError,
      );
      expect(calls).toHaveLength(0);
      expect(executeScriptMock).not.toHaveBeenCalled();
    });

    it("refuses when the device isn't paired", async () => {
      store = {};
      await expect(saveActiveTabAsLink()).rejects.toBeInstanceOf(
        NotPairedError,
      );
    });

    it("refuses when there's no active tab", async () => {
      tabsQueryMock.mockResolvedValue([]);
      await expect(saveActiveTabAsLink()).rejects.toThrow(/no active tab/);
    });

    it("surfaces the Worker's own refusal", async () => {
      routes.length = 0;
      route(
        "POST",
        "/links",
        () => new Response("Missing or invalid fields", { status: 400 }),
      );
      await expect(saveActiveTabAsLink()).rejects.toThrow(
        /400 Missing or invalid fields/,
      );
    });
  });
});

describe("saveTabAsLink", () => {
  it("takes the tab's id, URL and title directly", async () => {
    await saveTabAsLink(9, "https://example.org/x", "Fallback title");
    expect(executeScriptMock.mock.calls[0][0].target.tabId).toBe(9);
    expect(callsTo("/links")[0].json.url).toBe("https://example.org/x");
  });
});

describe("getLinksEnabled", () => {
  it("is true when paired and the Worker has links on", async () => {
    expect(await getLinksEnabled()).toBe(true);
  });

  it("is false when links are off", async () => {
    await cacheFeatures({ linksEnabled: false });
    expect(await getLinksEnabled()).toBe(false);
  });

  it("is false when unpaired, without asking the Worker", async () => {
    store = {};
    expect(await getLinksEnabled()).toBe(false);
    expect(calls).toHaveLength(0);
  });

  it("is false, quietly, when it can't find out", async () => {
    delete store["recueil:link-features"];
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    expect(await getLinksEnabled()).toBe(false);
  });

  it("answers from the cache without a request while it's fresh", async () => {
    await getLinksEnabled();
    await getLinksEnabled();
    expect(callsTo("/config")).toHaveLength(0);
  });
});

describe("when the feature has been turned off since the config was cached", () => {
  // The cache says links are on, but the Worker has since been switched off: it
  // 404s every link route.
  function workerNowSaysOff() {
    route(
      "GET",
      "/config",
      json(200, { links_enabled: false, favicon_inline_max_bytes: 1024 }),
    );
  }

  it("reports that links are off, instead of a raw 404, and updates the cache", async () => {
    routes.length = 0;
    route("POST", "/links", () => new Response("Not Found", { status: 404 }));
    workerNowSaysOff();

    await expect(saveActiveTabAsLink()).rejects.toBeInstanceOf(
      LinksDisabledError,
    );

    expect(callsTo("/config")).toHaveLength(1);
    // The popup stops offering the link from now on.
    expect(await getLinksEnabled()).toBe(false);
  });

  it("does the same for a queued save", async () => {
    await setClaimedTab(7, "item-42");
    routes.length = 0;
    route(
      "POST",
      "/queue/item-42/complete-link",
      () => new Response("Not Found", { status: 404 }),
    );
    workerNowSaysOff();

    await expect(saveActiveTabAsLink()).rejects.toBeInstanceOf(
      LinksDisabledError,
    );
    // Still claimed: nothing was saved.
    expect(await getClaimedTabs()).toEqual({ 7: "item-42" });
    expect(tabsRemoveMock).not.toHaveBeenCalled();
  });

  it("does the same when the favicon's presign is what the Worker refuses", async () => {
    pageSays({ favicon: icon(5000) });
    routes.length = 0;
    route(
      "POST",
      "/links/upload-urls",
      () => new Response("Not Found", { status: 404 }),
    );
    workerNowSaysOff();

    await expect(saveActiveTabAsLink()).rejects.toBeInstanceOf(
      LinksDisabledError,
    );
  });

  it("rethrows a 404 that isn't about the feature, such as a queue item that no longer exists", async () => {
    await setClaimedTab(7, "item-42");
    routes.length = 0;
    route(
      "POST",
      "/queue/item-42/complete-link",
      () => new Response("Not Found", { status: 404 }),
    );
    route(
      "GET",
      "/config",
      json(200, { links_enabled: true, favicon_inline_max_bytes: 1024 }),
    );

    await expect(saveActiveTabAsLink()).rejects.toThrow(/404 Not Found/);
  });

  it("rethrows the 404 if the refresh itself can't be made", async () => {
    routes.length = 0;
    route("POST", "/links", () => new Response("Not Found", { status: 404 }));
    route("GET", "/config", () => {
      throw new TypeError("Failed to fetch");
    });

    await expect(saveActiveTabAsLink()).rejects.toThrow(/404 Not Found/);
  });
});

describe("refreshLinkFeatures", () => {
  it("asks the Worker even though the cache is fresh, and stores the answer", async () => {
    route(
      "GET",
      "/config",
      json(200, { links_enabled: false, favicon_inline_max_bytes: 64 }),
    );

    await refreshLinkFeatures();

    expect(callsTo("/config")).toHaveLength(1);
    expect(await getLinksEnabled()).toBe(false);
  });

  it("does nothing when unpaired", async () => {
    store = {};
    await refreshLinkFeatures();
    expect(calls).toHaveLength(0);
  });

  it("swallows a failure, leaving the cache as it was", async () => {
    route("GET", "/config", () => {
      throw new TypeError("Failed to fetch");
    });
    await expect(refreshLinkFeatures()).resolves.toBeUndefined();
    expect(await getLinksEnabled()).toBe(true);
  });
});
