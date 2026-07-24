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

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Same in-memory storage.local fake used elsewhere, plus mocks for the two
// action.* badge calls and the alarms API queue.js's registerQueueRefreshAlarm
// touches.
let store;
const setBadgeTextMock = vi.fn();
const setBadgeBackgroundColorMock = vi.fn();
const alarmsCreateMock = vi.fn();
const alarmsOnAlarmListeners = [];
const tabsCreateMock = vi.fn();

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
    action: {
      setBadgeText: (...args) => setBadgeTextMock(...args),
      setBadgeBackgroundColor: (...args) =>
        setBadgeBackgroundColorMock(...args),
    },
    alarms: {
      create: (...args) => alarmsCreateMock(...args),
      onAlarm: {
        addListener: (fn) => alarmsOnAlarmListeners.push(fn),
      },
    },
    tabs: {
      create: (...args) => tabsCreateMock(...args),
    },
    // Real English strings, not a stub -- queue.js's describeClaimFailure
    // is what this file's own 409/410/404 tests exercise, so this needs to
    // resolve to the actual copy, not just something t() to prove it was
    // called. i18n.test.js covers t()'s own lookup/substitution/missing-key
    // contract in isolation; this only needs enough to not throw.
    i18n: {
      getMessage(key) {
        return (
          {
            queueClaimConflict:
              "recueil: this item is already being worked on from another device",
            queueClaimGone:
              "recueil: this item has already been captured (or permanently failed)",
            queueClaimNotFound: "recueil: this item no longer exists",
          }[key] ?? ""
        );
      },
    },
  },
}));

const { refreshQueueList, registerQueueRefreshAlarm, claimQueueItem } =
  await import("../src/background/queue.js");
const { setConfig, getQueueCache, getClaimedTabs } =
  await import("../src/common/storage.js");

const config = {
  workerBaseURL: "https://recueil.example.com",
  token: "rcl_live_test-token",
  deviceId: 1,
  deviceName: "test device",
  deviceType: "extension",
};

let fetchMock;
beforeEach(() => {
  store = {};
  setBadgeTextMock.mockReset();
  setBadgeBackgroundColorMock.mockReset();
  alarmsCreateMock.mockReset();
  alarmsOnAlarmListeners.length = 0;
  tabsCreateMock.mockReset();
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

function fakeQueueResponse(items) {
  return {
    ok: true,
    status: 200,
    headers: {
      get: (name) =>
        name.toLowerCase() === "content-type" ? "application/json" : null,
    },
    json: async () => ({ items }),
    text: async () => "",
  };
}

describe("refreshQueueList", () => {
  it("returns an empty cache and clears the badge when not paired, without calling the Worker", async () => {
    const cache = await refreshQueueList();

    expect(cache.items).toEqual([]);
    expect(fetchMock).not.toHaveBeenCalled();
    expect(setBadgeTextMock).toHaveBeenCalledWith({ text: "" });
  });

  it("fetches GET /queue, caches the result, and sets the badge to the item count", async () => {
    await setConfig(config);
    const items = [
      { id: "a", url: "https://example.com/a", status: "pending" },
      { id: "b", url: "https://example.com/b", status: "pending" },
    ];
    fetchMock.mockResolvedValue(fakeQueueResponse(items));

    const cache = await refreshQueueList();

    expect(fetchMock).toHaveBeenCalledWith(
      "https://recueil.example.com/queue",
      expect.objectContaining({ method: "GET" }),
    );
    expect(cache.items).toEqual(items);
    expect(cache.fetchedAt).toEqual(expect.any(String));
    expect(setBadgeTextMock).toHaveBeenCalledWith({ text: "2" });
    expect(setBadgeBackgroundColorMock).toHaveBeenCalledWith(
      expect.objectContaining({ color: expect.any(String) }),
    );

    // Actually persisted, not just returned.
    expect(await getQueueCache()).toEqual(cache);
  });

  it("clears the badge (empty text) when the queue is empty", async () => {
    await setConfig(config);
    fetchMock.mockResolvedValue(fakeQueueResponse([]));

    await refreshQueueList();

    expect(setBadgeTextMock).toHaveBeenCalledWith({ text: "" });
    expect(setBadgeBackgroundColorMock).not.toHaveBeenCalled();
  });
});

describe("registerQueueRefreshAlarm", () => {
  it("creates the recurring alarm and refreshes only when that alarm fires", async () => {
    registerQueueRefreshAlarm();

    expect(alarmsCreateMock).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ periodInMinutes: expect.any(Number) }),
    );
    expect(alarmsOnAlarmListeners).toHaveLength(1);

    const [createdName] = alarmsCreateMock.mock.calls[0];

    // An unrelated alarm firing must not trigger a refresh.
    alarmsOnAlarmListeners[0]({ name: "some-other-alarm" });
    await vi.waitFor(() => {
      expect(fetchMock).not.toHaveBeenCalled();
    });

    // The real alarm firing does.
    await setConfig(config);
    fetchMock.mockResolvedValue(fakeQueueResponse([]));
    alarmsOnAlarmListeners[0]({ name: createdName });
    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalled();
    });
  });
});

function fakeClaimResponse(body, { status = 200 } = {}) {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: {
      get: (name) =>
        name.toLowerCase() === "content-type" ? "application/json" : null,
    },
    json: async () => body,
    text: async () => JSON.stringify(body),
  };
}

describe("claimQueueItem", () => {
  it("claims via POST /queue/:id/claim, opens a focused tab, and tracks it", async () => {
    await setConfig(config);
    fetchMock.mockResolvedValue(
      fakeClaimResponse({ id: "item-1", url: "https://example.com/x" }),
    );
    tabsCreateMock.mockResolvedValue({ id: 77 });

    const claimed = await claimQueueItem("item-1");

    expect(fetchMock).toHaveBeenCalledWith(
      "https://recueil.example.com/queue/item-1/claim",
      expect.objectContaining({ method: "POST" }),
    );
    expect(tabsCreateMock).toHaveBeenCalledWith({
      url: "https://example.com/x",
      active: true,
    });
    expect(claimed).toEqual({ id: "item-1", url: "https://example.com/x" });
    expect(await getClaimedTabs()).toEqual({ 77: "item-1" });
  });

  it("does not track anything if the created tab has no id", async () => {
    await setConfig(config);
    fetchMock.mockResolvedValue(
      fakeClaimResponse({ id: "item-1", url: "https://example.com/x" }),
    );
    tabsCreateMock.mockResolvedValue({});

    await claimQueueItem("item-1");

    expect(await getClaimedTabs()).toEqual({});
  });

  it.each([
    [409, /already being worked on/],
    [410, /already been captured/],
    [404, /no longer exists/],
  ])(
    "translates a %i response into a human-readable message",
    async (status, expectedMessage) => {
      await setConfig(config);
      fetchMock.mockResolvedValue(fakeClaimResponse({}, { status }));

      await expect(claimQueueItem("item-1")).rejects.toThrow(expectedMessage);
      expect(tabsCreateMock).not.toHaveBeenCalled();
    },
  );

  it("throws NotPairedError when not paired, without calling the Worker", async () => {
    await expect(claimQueueItem("item-1")).rejects.toThrow(/not paired/);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
