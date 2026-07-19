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

// A minimal in-memory fake of browser.bookmarks -- just enough of the real
// contract (create/get/update/remove/removeTree/getChildren, get()
// rejecting for an unknown id) for syncBookmarks' own reconciliation logic
// to exercise meaningfully, the same spirit as storage.test.js's in-memory
// storage.local fake.
// Simulates the real browser assigning an actual container id when
// bookmarks.create() omits parentId -- a real Chrome/Firefox never
// returns undefined for this on something it just created, only recueil's
// own earlier, unfixed code assumed it might stay unset.
const DEFAULT_ROOT_ID = "default-root";

let store;
let nextBookmarkId;
let storageStore;
let bookmarksUpdateMock;

let alarmsCreateMock;
let alarmsOnAlarmListeners;
let permissionsRemoveMock;

vi.mock("webextension-polyfill", () => ({
  default: {
    storage: {
      local: {
        async get(key) {
          return key in storageStore ? { [key]: storageStore[key] } : {};
        },
        async set(obj) {
          Object.assign(storageStore, obj);
        },
        async remove(key) {
          delete storageStore[key];
        },
      },
    },
    bookmarks: {
      async create({ parentId, title, url }) {
        const id = String(nextBookmarkId++);
        const node = { id, parentId: parentId ?? DEFAULT_ROOT_ID, title, url };
        store.set(id, node);
        return node;
      },
      async get(id) {
        if (!store.has(id)) {
          throw new Error(`No bookmark found for id ${id}`);
        }
        return [store.get(id)];
      },
      update: (...args) => bookmarksUpdateMock(...args),
      async remove(id) {
        store.delete(id);
      },
      async removeTree(id) {
        store.delete(id);
        for (const [childId, node] of store) {
          if (node.parentId === id) {
            store.delete(childId);
          }
        }
      },
      async getChildren(id) {
        return [...store.values()].filter((node) => node.parentId === id);
      },
    },
    alarms: {
      create: (...args) => alarmsCreateMock(...args),
      onAlarm: {
        addListener: (fn) => alarmsOnAlarmListeners.push(fn),
      },
    },
    permissions: {
      remove: (...args) => permissionsRemoveMock(...args),
    },
  },
}));

const {
  syncBookmarks,
  deleteBookmarksFolderAndState,
  registerBookmarkSyncAlarm,
  enableBookmarkSync,
  disableBookmarkSync,
} = await import("../src/background/bookmarks.js");
const {
  setConfig,
  setBookmarkSyncEnabled,
  isBookmarkSyncEnabled,
  getBookmarksFolderId,
} = await import("../src/common/storage.js");

const config = {
  workerBaseURL: "https://recueil.example.com",
  token: "rcl_live_test-token",
  deviceId: 1,
  deviceName: "test device",
  deviceType: "extension",
};

let fetchMock;
beforeEach(() => {
  store = new Map();
  storageStore = {};
  nextBookmarkId = 1;
  bookmarksUpdateMock = vi.fn(async (id, changes) => {
    const node = store.get(id);
    if (!node) {
      throw new Error(`No bookmark found for id ${id}`);
    }
    Object.assign(node, changes);
    return node;
  });
  alarmsCreateMock = vi.fn();
  alarmsOnAlarmListeners = [];
  permissionsRemoveMock = vi.fn().mockResolvedValue(undefined);
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

function fakePagesResponse(pages) {
  return {
    ok: true,
    status: 200,
    headers: {
      get: (name) =>
        name.toLowerCase() === "content-type" ? "application/json" : null,
    },
    json: async () => ({ pages }),
    text: async () => "",
  };
}

describe("syncBookmarks -- gating", () => {
  it("does nothing when bookmark sync isn't enabled", async () => {
    await setConfig(config);
    await syncBookmarks();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(store.size).toBe(0);
  });

  it("does nothing when not paired, even if sync is enabled", async () => {
    await setBookmarkSyncEnabled(true);
    await syncBookmarks();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(store.size).toBe(0);
  });
});

describe("syncBookmarks -- folder handling", () => {
  it("creates the dedicated folder in the discovered default location on first sync", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(fakePagesResponse([]));

    await syncBookmarks();

    const folderId = await getBookmarksFolderId();
    expect(folderId).not.toBeNull();
    const folder = store.get(folderId);
    expect(folder.title).toBe("recueil");
    expect(folder.parentId).toBe(DEFAULT_ROOT_ID);
    // The probe bookmark used to discover that location is cleaned up,
    // not left behind.
    expect(
      [...store.values()].some((n) => n.title === "__recueil_probe__"),
    ).toBe(false);
  });

  it("reuses the tracked folder id on a later sync, without creating a new one", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(fakePagesResponse([]));

    await syncBookmarks();
    const folderId = await getBookmarksFolderId();
    const sizeAfterFirst = store.size;

    await syncBookmarks();
    expect(await getBookmarksFolderId()).toBe(folderId);
    expect(store.size).toBe(sizeAfterFirst);
  });

  it("creates a fresh folder if the tracked id no longer resolves", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(fakePagesResponse([]));

    await syncBookmarks();
    const oldFolderId = await getBookmarksFolderId();
    store.delete(oldFolderId); // simulate the user deleting it manually

    await syncBookmarks();
    const newFolderId = await getBookmarksFolderId();
    expect(newFolderId).not.toBe(oldFolderId);
    expect(store.has(newFolderId)).toBe(true);
  });

  it("adopts an existing recueil folder in the default location instead of creating a duplicate", async () => {
    // Simulates a folder that arrived via Firefox Sync (or similar) from
    // another device, before this device's own recueil extension has
    // ever synced -- no tracked folder id locally at all yet.
    store.set("synced-folder-1", {
      id: "synced-folder-1",
      parentId: DEFAULT_ROOT_ID,
      title: "recueil",
      url: undefined,
    });

    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(fakePagesResponse([]));

    await syncBookmarks();

    expect(await getBookmarksFolderId()).toBe("synced-folder-1");
    const recueilFolders = [...store.values()].filter(
      (n) => n.title === "recueil" && n.url === undefined,
    );
    expect(recueilFolders).toHaveLength(1);
  });
});

describe("syncBookmarks -- reconciliation by URL", () => {
  it("creates a bookmark for a newly-archived page", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(
      fakePagesResponse([
        { page_id: 1, raw_url: "https://example.com/a", title: "Page A" },
      ]),
    );

    await syncBookmarks();

    const folderId = await getBookmarksFolderId();
    const children = [...store.values()].filter((n) => n.parentId === folderId);
    expect(children).toHaveLength(1);
    expect(children[0]).toMatchObject({
      title: "Page A",
      url: "https://example.com/a",
    });
  });

  it("falls back to the URL as the title when the page has none", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(
      fakePagesResponse([
        { page_id: 1, raw_url: "https://example.com/a", title: null },
      ]),
    );

    await syncBookmarks();

    const folderId = await getBookmarksFolderId();
    const [bookmark] = [...store.values()].filter(
      (n) => n.parentId === folderId,
    );
    expect(bookmark.title).toBe("https://example.com/a");
  });

  it("updates a bookmark's title, matched by URL, when it changes on the backend", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(
      fakePagesResponse([
        { page_id: 1, raw_url: "https://example.com/a", title: "Old Title" },
      ]),
    );
    await syncBookmarks();
    const folderId = await getBookmarksFolderId();
    const [bookmark] = [...store.values()].filter(
      (n) => n.parentId === folderId,
    );

    fetchMock.mockResolvedValue(
      fakePagesResponse([
        { page_id: 1, raw_url: "https://example.com/a", title: "New Title" },
      ]),
    );
    await syncBookmarks();

    // Same bookmark id, title updated in place -- not a new bookmark.
    expect(store.get(bookmark.id).title).toBe("New Title");
    expect(
      [...store.values()].filter((n) => n.parentId === folderId),
    ).toHaveLength(1);
  });

  it("does not call update when nothing has actually changed", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(
      fakePagesResponse([
        { page_id: 1, raw_url: "https://example.com/a", title: "Page A" },
      ]),
    );
    await syncBookmarks();
    bookmarksUpdateMock.mockClear();

    await syncBookmarks();

    expect(bookmarksUpdateMock).not.toHaveBeenCalled();
  });

  it("recognizes a bookmark already sitting in the folder, matched by URL, without creating a duplicate", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    // First sync just creates the folder and learns its id.
    fetchMock.mockResolvedValue(fakePagesResponse([]));
    await syncBookmarks();
    const folderId = await getBookmarksFolderId();

    // Simulate a bookmark that's already sitting in the folder --
    // whether because it arrived via native browser sync from another
    // device, or was somehow otherwise already there -- with no tracked
    // id anywhere on this side at all (there's no tracked-id concept to
    // have missed).
    store.set("pre-existing-99", {
      id: "pre-existing-99",
      parentId: folderId,
      title: "Page A",
      url: "https://example.com/a",
    });

    fetchMock.mockResolvedValue(
      fakePagesResponse([
        { page_id: 1, raw_url: "https://example.com/a", title: "Page A" },
      ]),
    );
    await syncBookmarks();

    const children = [...store.values()].filter((n) => n.parentId === folderId);
    expect(children).toHaveLength(1);
    expect(children[0].id).toBe("pre-existing-99");
  });

  it("removes a bookmark whose page is no longer in the fetched list", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(
      fakePagesResponse([
        { page_id: 1, raw_url: "https://example.com/a", title: "Page A" },
      ]),
    );
    await syncBookmarks();
    const folderId = await getBookmarksFolderId();
    const [bookmark] = [...store.values()].filter(
      (n) => n.parentId === folderId,
    );
    expect(store.has(bookmark.id)).toBe(true);

    // The page got excluded_from_mirror, or deleted -- no longer returned.
    fetchMock.mockResolvedValue(fakePagesResponse([]));
    await syncBookmarks();

    expect(store.has(bookmark.id)).toBe(false);
  });
});

describe("deleteBookmarksFolderAndState", () => {
  it("removes the entire folder tree and clears the tracked folder id", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(
      fakePagesResponse([
        { page_id: 1, raw_url: "https://example.com/a", title: "Page A" },
      ]),
    );
    await syncBookmarks();
    const folderId = await getBookmarksFolderId();
    expect(store.has(folderId)).toBe(true);

    await deleteBookmarksFolderAndState();

    expect(store.size).toBe(0);
    expect(await getBookmarksFolderId()).toBeNull();
  });

  it("is a harmless no-op when no folder was ever created", async () => {
    await expect(deleteBookmarksFolderAndState()).resolves.toBeUndefined();
  });
});

describe("enableBookmarkSync", () => {
  it("sets the enabled flag and runs an immediate sync", async () => {
    await setConfig(config);
    fetchMock.mockResolvedValue(fakePagesResponse([]));

    await enableBookmarkSync();

    expect(await isBookmarkSyncEnabled()).toBe(true);
    // The immediate sync actually ran, not just the flag flipped -- it
    // created the dedicated folder as a side effect.
    expect(await getBookmarksFolderId()).not.toBeNull();
  });

  it("leaves sync enabled even if the immediate sync itself fails", async () => {
    await setConfig(config);
    fetchMock.mockRejectedValue(new TypeError("network error"));

    await expect(enableBookmarkSync()).rejects.toThrow();

    // Unlike the alarm's own best-effort handling, this error is allowed
    // to propagate to the caller (the popup can usefully show it) -- but
    // the flag itself isn't rolled back; the next alarm tick just tries
    // again.
    expect(await isBookmarkSyncEnabled()).toBe(true);
  });
});

describe("disableBookmarkSync", () => {
  it("tears down the folder, clears the flag, and relinquishes the permission", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(
      fakePagesResponse([
        { page_id: 1, raw_url: "https://example.com/a", title: "Page A" },
      ]),
    );
    await syncBookmarks();
    expect(store.size).toBeGreaterThan(0);

    await disableBookmarkSync();

    expect(store.size).toBe(0);
    expect(await getBookmarksFolderId()).toBeNull();
    expect(await isBookmarkSyncEnabled()).toBe(false);
    expect(permissionsRemoveMock).toHaveBeenCalledWith({
      permissions: ["bookmarks"],
    });
  });

  it("does not throw if relinquishing the permission itself fails", async () => {
    permissionsRemoveMock.mockRejectedValue(new Error("boom"));
    await expect(disableBookmarkSync()).resolves.toBeUndefined();
    expect(await isBookmarkSyncEnabled()).toBe(false);
  });
});

describe("registerBookmarkSyncAlarm", () => {
  it("creates the recurring alarm and syncs only when that alarm fires", async () => {
    registerBookmarkSyncAlarm();

    expect(alarmsCreateMock).toHaveBeenCalledWith(
      expect.any(String),
      expect.objectContaining({ periodInMinutes: expect.any(Number) }),
    );
    expect(alarmsOnAlarmListeners).toHaveLength(1);
    const [createdName] = alarmsCreateMock.mock.calls[0];

    alarmsOnAlarmListeners[0]({ name: "some-other-alarm" });
    await vi.waitFor(() => {
      expect(fetchMock).not.toHaveBeenCalled();
    });

    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    fetchMock.mockResolvedValue(fakePagesResponse([]));
    alarmsOnAlarmListeners[0]({ name: createdName });
    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalled();
    });
  });
});
