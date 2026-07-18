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
  },
}));

const { refreshQueueList, registerQueueRefreshAlarm } =
  await import("../src/background/queue.js");
const { setConfig, getQueueCache } = await import("../src/common/storage.js");

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
    await Promise.resolve();
    expect(fetchMock).not.toHaveBeenCalled();

    // The real alarm firing does.
    await setConfig(config);
    fetchMock.mockResolvedValue(fakeQueueResponse([]));
    alarmsOnAlarmListeners[0]({ name: createdName });
    await Promise.resolve();
    await Promise.resolve();
    expect(fetchMock).toHaveBeenCalled();
  });
});
