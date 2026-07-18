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

import { beforeEach, describe, expect, it, vi } from "vitest";

// A minimal in-memory fake of browser.storage.local -- matches the real
// API's shape (get/set/remove, all async, get returns an object keyed by
// the requested key) closely enough for storage.js's own usage of it.
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

const {
  getConfig,
  setConfig,
  clearConfig,
  getPairingDraft,
  setPairingDraft,
  clearPairingDraft,
  getClaimedTabs,
  setClaimedTab,
  clearClaimedTab,
} = await import("../src/common/storage.js");

beforeEach(() => {
  store = {};
});

describe("RecueilConfig storage", () => {
  it("returns null when nothing has ever been stored", async () => {
    expect(await getConfig()).toBeNull();
  });

  it("round-trips a config through setConfig/getConfig", async () => {
    const config = {
      workerBaseURL: "https://recueil.example.com",
      token: "rcl_live_abc123",
      deviceId: 1,
      deviceName: "Test Device",
      deviceType: "extension",
    };
    await setConfig(config);
    expect(await getConfig()).toEqual(config);
  });

  it("returns null again after clearConfig", async () => {
    await setConfig({
      workerBaseURL: "https://recueil.example.com",
      token: "t",
      deviceId: 1,
      deviceName: "d",
      deviceType: "extension",
    });
    await clearConfig();
    expect(await getConfig()).toBeNull();
  });

  it("a later setConfig call fully replaces the earlier one, not merges", async () => {
    await setConfig({
      workerBaseURL: "https://a.example.com",
      token: "t1",
      deviceId: 1,
      deviceName: "d1",
      deviceType: "extension",
    });
    await setConfig({
      workerBaseURL: "https://b.example.com",
      token: "t2",
      deviceId: 2,
      deviceName: "d2",
      deviceType: "extension",
    });
    const config = await getConfig();
    expect(config.workerBaseURL).toBe("https://b.example.com");
    expect(config.token).toBe("t2");
  });
});

describe("pairing draft storage", () => {
  it("returns an empty object, not null, when nothing has been saved", async () => {
    // Different from getConfig()'s null-when-absent -- the draft is always
    // destructured/read from directly by popup.js, so an empty object is the
    // more convenient "nothing here yet" than null would be.
    expect(await getPairingDraft()).toEqual({});
  });

  it("round-trips a partial draft", async () => {
    await setPairingDraft({ workerBaseURL: "https://recueil.example.com" });
    expect(await getPairingDraft()).toEqual({
      workerBaseURL: "https://recueil.example.com",
    });
  });

  it("is independent of RecueilConfig storage -- setting one never touches the other", async () => {
    await setConfig({
      workerBaseURL: "https://real.example.com",
      token: "real-token",
      deviceId: 1,
      deviceName: "real device",
      deviceType: "extension",
    });
    await setPairingDraft({ workerBaseURL: "https://draft.example.com" });

    expect((await getConfig()).workerBaseURL).toBe("https://real.example.com");
    expect((await getPairingDraft()).workerBaseURL).toBe(
      "https://draft.example.com",
    );
  });

  it("returns an empty object again after clearPairingDraft", async () => {
    await setPairingDraft({ deviceName: "something" });
    await clearPairingDraft();
    expect(await getPairingDraft()).toEqual({});
  });
});

describe("claimed-tabs tracking", () => {
  it("returns an empty object when nothing has been tracked", async () => {
    expect(await getClaimedTabs()).toEqual({});
  });

  it("round-trips a tabId -> itemId association", async () => {
    await setClaimedTab(42, "item-abc");
    expect(await getClaimedTabs()).toEqual({ 42: "item-abc" });
  });

  it("tracks multiple tabs independently", async () => {
    await setClaimedTab(1, "item-a");
    await setClaimedTab(2, "item-b");
    expect(await getClaimedTabs()).toEqual({ 1: "item-a", 2: "item-b" });
  });

  it("removes only the specified tab on clearClaimedTab", async () => {
    await setClaimedTab(1, "item-a");
    await setClaimedTab(2, "item-b");
    await clearClaimedTab(1);
    expect(await getClaimedTabs()).toEqual({ 2: "item-b" });
  });

  it("clearClaimedTab on an untracked tab is a harmless no-op", async () => {
    await setClaimedTab(1, "item-a");
    await clearClaimedTab(999);
    expect(await getClaimedTabs()).toEqual({ 1: "item-a" });
  });
});
