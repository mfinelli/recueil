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
  syncLinkBookmarks,
  enableLinkBookmarkSync,
  disableLinkBookmarkSync,
  deleteLinkBookmarksFolderAndState,
  syncAllBookmarks,
  disableAllBookmarkSyncs,
} = await import("../src/background/bookmarks.js");
const {
  setConfig,
  setBookmarkSyncEnabled,
  isBookmarkSyncEnabled,
  getBookmarksFolderId,
  setLinkBookmarkSyncEnabled,
  isLinkBookmarkSyncEnabled,
  getLinkBookmarksFolderId,
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

function fakeJSON(body, { ok = true, status = 200 } = {}) {
  return {
    ok,
    status,
    statusText: ok ? "OK" : "Error",
    headers: {
      get: (name) =>
        name.toLowerCase() === "content-type" ? "application/json" : null,
    },
    json: async () => body,
    text: async () => JSON.stringify(body),
  };
}

const notFound = () => fakeJSON({}, { ok: false, status: 404 });

// Answers each route from a table, so a test can serve pages and links at once.
function serve(routes) {
  fetchMock.mockImplementation(async (url) => {
    const path = new URL(url).pathname;
    if (!(path in routes)) throw new Error(`unexpected fetch: ${path}`);
    const value = routes[path];
    return typeof value === "function" ? value() : value;
  });
}

const childrenOf = (folderId) =>
  [...store.values()].filter((n) => n.parentId === folderId);

const folderNamed = (title) =>
  [...store.values()].find((n) => n.title === title && n.url === undefined);

describe("syncLinkBookmarks -- gating", () => {
  it("does nothing when the link sync isn't enabled, even if the page sync is", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    await syncLinkBookmarks();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(store.size).toBe(0);
  });

  it("does nothing when not paired", async () => {
    await setLinkBookmarkSyncEnabled(true);
    await syncLinkBookmarks();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("syncLinkBookmarks -- its own folder", () => {
  it("creates a top-level folder named 'recueil links' beside the pages folder, not inside it", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-pages": fakeJSON({ pages: [] }),
      "/archived-links": fakeJSON({ links: [] }),
    });

    await syncAllBookmarks();

    const pagesFolder = folderNamed("recueil");
    const linksFolder = folderNamed("recueil links");
    expect(pagesFolder).toBeTruthy();
    expect(linksFolder).toBeTruthy();
    expect(linksFolder.id).not.toBe(pagesFolder.id);
    // Siblings in the same default container.
    expect(linksFolder.parentId).toBe(pagesFolder.parentId);
    expect(await getLinkBookmarksFolderId()).toBe(linksFolder.id);
    expect(await getBookmarksFolderId()).toBe(pagesFolder.id);
  });

  it("does not adopt the pages folder as its own, nor the other way round", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    // A "recueil" folder (the pages one) is already in the default container.
    const existing = await (
      await import("webextension-polyfill")
    ).default.bookmarks.create({
      title: "recueil",
    });
    serve({ "/archived-links": fakeJSON({ links: [] }) });

    await syncLinkBookmarks();

    const linksFolder = folderNamed("recueil links");
    expect(linksFolder).toBeTruthy();
    expect(linksFolder.id).not.toBe(existing.id);
  });

  it("adopts an existing 'recueil links' folder rather than creating a duplicate", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    const existing = await (
      await import("webextension-polyfill")
    ).default.bookmarks.create({
      title: "recueil links",
    });
    serve({ "/archived-links": fakeJSON({ links: [] }) });

    await syncLinkBookmarks();

    expect(await getLinkBookmarksFolderId()).toBe(existing.id);
    expect(
      [...store.values()].filter((n) => n.title === "recueil links"),
    ).toHaveLength(1);
  });
});

describe("syncLinkBookmarks -- reconciliation by URL", () => {
  it("creates a bookmark for each link, keeping the URL exactly as saved, fragment and all", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-links": fakeJSON({
        links: [
          {
            link_id: 1,
            raw_url: "https://app.example.com/#/board/3",
            title: "Board",
            updated_at: "x",
          },
        ],
      }),
    });

    await syncLinkBookmarks();

    const children = childrenOf(await getLinkBookmarksFolderId());
    expect(children).toHaveLength(1);
    expect(children[0]).toMatchObject({
      title: "Board",
      url: "https://app.example.com/#/board/3",
    });
  });

  it("uses the URL as the title for a link with none", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-links": fakeJSON({
        links: [{ link_id: 1, raw_url: "https://example.com/x", title: null }],
      }),
    });

    await syncLinkBookmarks();

    expect(childrenOf(await getLinkBookmarksFolderId())[0].title).toBe(
      "https://example.com/x",
    );
  });

  it("updates a changed title in place, and creates nothing new for an unchanged link", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-links": fakeJSON({
        links: [
          { link_id: 1, raw_url: "https://example.com/x", title: "Before" },
        ],
      }),
    });
    await syncLinkBookmarks();
    const id = childrenOf(await getLinkBookmarksFolderId())[0].id;

    await syncLinkBookmarks();
    expect(bookmarksUpdateMock).not.toHaveBeenCalled();

    serve({
      "/archived-links": fakeJSON({
        links: [
          { link_id: 1, raw_url: "https://example.com/x", title: "After" },
        ],
      }),
    });
    await syncLinkBookmarks();

    const children = childrenOf(await getLinkBookmarksFolderId());
    expect(children).toHaveLength(1);
    expect(children[0].id).toBe(id);
    expect(children[0].title).toBe("After");
  });

  it("removes a bookmark whose link is no longer listed (deleted, or excluded from the sync)", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-links": fakeJSON({
        links: [
          { link_id: 1, raw_url: "https://example.com/keep", title: "Keep" },
          { link_id: 2, raw_url: "https://example.com/drop", title: "Drop" },
        ],
      }),
    });
    await syncLinkBookmarks();

    serve({
      "/archived-links": fakeJSON({
        links: [
          { link_id: 1, raw_url: "https://example.com/keep", title: "Keep" },
        ],
      }),
    });
    await syncLinkBookmarks();

    expect(
      childrenOf(await getLinkBookmarksFolderId()).map((n) => n.url),
    ).toEqual(["https://example.com/keep"]);
  });

  it("never touches the pages folder", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-pages": fakeJSON({
        pages: [
          { page_id: 1, raw_url: "https://example.com/page", title: "Page" },
        ],
      }),
      "/archived-links": fakeJSON({
        links: [
          { link_id: 1, raw_url: "https://example.com/link", title: "Link" },
        ],
      }),
    });

    await syncAllBookmarks();
    // The links list empties out; the page's bookmark must stay.
    serve({
      "/archived-pages": fakeJSON({
        pages: [
          { page_id: 1, raw_url: "https://example.com/page", title: "Page" },
        ],
      }),
      "/archived-links": fakeJSON({ links: [] }),
    });
    await syncAllBookmarks();

    expect(childrenOf(await getBookmarksFolderId()).map((n) => n.url)).toEqual([
      "https://example.com/page",
    ]);
    expect(childrenOf(await getLinkBookmarksFolderId())).toHaveLength(0);
  });
});

describe("syncLinkBookmarks -- an instance without links", () => {
  it("leaves the folder exactly as it is on a 404, instead of sweeping it clean", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-links": fakeJSON({
        links: [{ link_id: 1, raw_url: "https://example.com/x", title: "X" }],
      }),
    });
    await syncLinkBookmarks();

    serve({ "/archived-links": notFound() });
    await expect(syncLinkBookmarks()).resolves.toBeUndefined();

    expect(
      childrenOf(await getLinkBookmarksFolderId()).map((n) => n.url),
    ).toEqual(["https://example.com/x"]);
  });

  it("still lets any other failure through", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({ "/archived-links": fakeJSON({}, { ok: false, status: 500 }) });
    await expect(syncLinkBookmarks()).rejects.toThrow(/500/);
  });

  it("does not tolerate a 404 for the archived-pages sync, which has always existed", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    serve({ "/archived-pages": notFound() });
    await expect(syncBookmarks()).rejects.toThrow(/404/);
  });
});

describe("enableLinkBookmarkSync / disableLinkBookmarkSync", () => {
  it("enabling sets its own flag, not the pages one, and syncs immediately", async () => {
    await setConfig(config);
    serve({ "/archived-links": fakeJSON({ links: [] }) });

    await enableLinkBookmarkSync();

    expect(await isLinkBookmarkSyncEnabled()).toBe(true);
    expect(await isBookmarkSyncEnabled()).toBe(false);
    expect(await getLinkBookmarksFolderId()).not.toBeNull();
  });

  it("leaves it enabled even if the immediate sync fails", async () => {
    await setConfig(config);
    fetchMock.mockRejectedValue(new TypeError("network error"));
    await expect(enableLinkBookmarkSync()).rejects.toThrow();
    expect(await isLinkBookmarkSyncEnabled()).toBe(true);
  });

  it("disabling removes only the links folder and keeps the permission while the pages sync still needs it", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-pages": fakeJSON({ pages: [] }),
      "/archived-links": fakeJSON({ links: [] }),
    });
    await syncAllBookmarks();

    await disableLinkBookmarkSync();

    expect(folderNamed("recueil links")).toBeUndefined();
    expect(folderNamed("recueil")).toBeTruthy();
    expect(await isLinkBookmarkSyncEnabled()).toBe(false);
    expect(await isBookmarkSyncEnabled()).toBe(true);
    expect(permissionsRemoveMock).not.toHaveBeenCalled();
  });

  it("disabling relinquishes the permission when nothing else is using it", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({ "/archived-links": fakeJSON({ links: [] }) });
    await syncLinkBookmarks();

    await disableLinkBookmarkSync();

    expect(permissionsRemoveMock).toHaveBeenCalledWith({
      permissions: ["bookmarks"],
    });
  });

  it("turning the pages sync off keeps the permission while the links sync still needs it", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-pages": fakeJSON({ pages: [] }),
      "/archived-links": fakeJSON({ links: [] }),
    });
    await syncAllBookmarks();

    await disableBookmarkSync();

    expect(folderNamed("recueil")).toBeUndefined();
    expect(folderNamed("recueil links")).toBeTruthy();
    expect(permissionsRemoveMock).not.toHaveBeenCalled();
  });

  it("deleteLinkBookmarksFolderAndState removes just that folder and forgets its id", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-links": fakeJSON({
        links: [{ link_id: 1, raw_url: "https://example.com/x", title: "X" }],
      }),
    });
    await syncLinkBookmarks();

    await deleteLinkBookmarksFolderAndState();

    expect(store.size).toBe(0);
    expect(await getLinkBookmarksFolderId()).toBeNull();
  });
});

describe("syncAllBookmarks", () => {
  it("does nothing when neither sync is enabled", async () => {
    await setConfig(config);
    await syncAllBookmarks();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("runs only the sync that's enabled", async () => {
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({ "/archived-links": fakeJSON({ links: [] }) });
    await syncAllBookmarks();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(new URL(fetchMock.mock.calls[0][0]).pathname).toBe(
      "/archived-links",
    );
  });

  it("runs the links sync even when the pages sync fails, then reports the failure", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-pages": fakeJSON({}, { ok: false, status: 500 }),
      "/archived-links": fakeJSON({
        links: [{ link_id: 1, raw_url: "https://example.com/x", title: "X" }],
      }),
    });

    await expect(syncAllBookmarks()).rejects.toThrow(/500/);

    expect(childrenOf(await getLinkBookmarksFolderId())).toHaveLength(1);
  });

  it("runs the pages sync even when the links sync fails", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-pages": fakeJSON({
        pages: [{ page_id: 1, raw_url: "https://example.com/p", title: "P" }],
      }),
      "/archived-links": fakeJSON({}, { ok: false, status: 500 }),
    });

    await expect(syncAllBookmarks()).rejects.toThrow(/500/);

    expect(childrenOf(await getBookmarksFolderId())).toHaveLength(1);
  });

  it("is what the alarm runs, so the links folder is kept current too", async () => {
    registerBookmarkSyncAlarm();
    const [createdName] = alarmsCreateMock.mock.calls[0];
    await setConfig(config);
    await setLinkBookmarkSyncEnabled(true);
    serve({ "/archived-links": fakeJSON({ links: [] }) });

    alarmsOnAlarmListeners[0]({ name: createdName });

    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalled();
    });
    expect(new URL(fetchMock.mock.calls[0][0]).pathname).toBe(
      "/archived-links",
    );
  });
});

describe("disableAllBookmarkSyncs (what unpairing runs)", () => {
  it("takes both folders and both flags down and relinquishes the permission", async () => {
    await setConfig(config);
    await setBookmarkSyncEnabled(true);
    await setLinkBookmarkSyncEnabled(true);
    serve({
      "/archived-pages": fakeJSON({
        pages: [{ page_id: 1, raw_url: "https://example.com/p", title: "P" }],
      }),
      "/archived-links": fakeJSON({
        links: [{ link_id: 1, raw_url: "https://example.com/l", title: "L" }],
      }),
    });
    await syncAllBookmarks();
    expect(store.size).toBeGreaterThan(0);

    await disableAllBookmarkSyncs();

    expect(store.size).toBe(0);
    expect(await getBookmarksFolderId()).toBeNull();
    expect(await getLinkBookmarksFolderId()).toBeNull();
    expect(await isBookmarkSyncEnabled()).toBe(false);
    expect(await isLinkBookmarkSyncEnabled()).toBe(false);
    expect(permissionsRemoveMock).toHaveBeenCalledTimes(1);
    expect(permissionsRemoveMock).toHaveBeenCalledWith({
      permissions: ["bookmarks"],
    });
  });

  it("doesn't throw if the permission can't be relinquished", async () => {
    permissionsRemoveMock.mockRejectedValue(new Error("boom"));
    await expect(disableAllBookmarkSyncs()).resolves.toBeUndefined();
  });
});
