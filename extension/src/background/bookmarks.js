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

// Syncs GET /archived-pages into the browser's own native bookmarks,
// inside one dedicated folder recueil creates and manages entirely on its
// own -- opted into via the `bookmarks` permission (optional_permissions
// in the manifest, requested by the popup's own toggle), not
// requested for everyone unless they want it.
//
// Reconciled by URL, not by tracking bookmark ids -- so no stored page_id ->
// bookmark id map at all. This depends on one fact worth stating explicitly,
// since it's not obvious from the field name: the `raw_url`
// GET /archived-pages returns is NOT the literal URL string from whichever
// capture happened to run last -- it's sourced from pages.normalized_url
// (see internal/mirror/sync.go's `RawURL: p.NormalizedUrl`), the exact
// column pages' own `UNIQUE (user_id, normalized_url)` constraint is built
// on. So it's already a stable, unique identity key per user, and diffing the
// archived-pages list against browser.bookmarks.getChildren(folderId) by
// URL is both simpler than and at least as correct as maintaining a
// separate tracked-id map -- the browser's own bookmark tree already *is*
// the persisted state to compare against, keeping a redundant local copy
// of it would only be a second thing that could drift from the truth.
// This also means the cross-device-sync case (a bookmark recueil created
// on another device, already propagated here by Firefox Sync or similar,
// before this device's own next sync tick runs) needs no special
// handling at all: it just looks like "a URL that's already there,"
// exactly like one created locally.
//
// The one rule everything here is still built around: recueil only ever
// touches bookmarks that are children of its own dedicated folder. It
// never searches the user's whole bookmark tree and never touches
// anything outside that one folder.
//
// To be explicit about what that means in practice: any bookmark
// management inside the recueil folder -- adding your own bookmarks
// there, renaming or moving the ones recueil created, anything -- is
// unsupported. It isn't preserved and isn't specially detected; it gets
// overwritten or removed the next time sync runs (see syncBookmarks'
// removal step below), on the same ordinary schedule as everything else,
// not just when disabling sync or unpairing. The folder is recueil's own
// managed space, not a general-purpose one recueil happens to also use.
//
// "Local edits get overwritten, deleting one just gets it recreated" is
// the explicit policy (not tracked/reconciled more cleverly) -- the
// correct way to stop syncing one specific page is the excluded_from_mirror
// toggle on the backend/dashboard, which removes it from
// GET /archived-pages entirely, not editing or deleting the bookmark
// locally and hoping recueil respects that.
//
// Saved links get a second, entirely separate sync of the same shape:
// their own opt-in, their own folder named "recueil links" -- a sibling of
// the existing "recueil" folder. Reconciled by URL against GET
// /archived-links. Everything above applies to it equally, with the link's
// raw_url (the URL as saved, which is a unique key per user all the same) in
// place of the page's normalized one. The two share this module's code, told
// apart only by a SyncKind descriptor, and share the one `bookmarks` permission,
// which is relinquished only when neither sync is left enabled.

import browser from "webextension-polyfill";
import {
  getConfig,
  isBookmarkSyncEnabled,
  setBookmarkSyncEnabled,
  getBookmarksFolderId,
  setBookmarksFolderId,
  isLinkBookmarkSyncEnabled,
  setLinkBookmarkSyncEnabled,
  getLinkBookmarksFolderId,
  setLinkBookmarksFolderId,
} from "../common/storage.js";
import { apiRequest, ApiError } from "../common/api-client.js";

// An explicitly unusual, unlikely-to-collide title -- see ensureFolder's
// doc comment for what this is actually for (discovering the real default
// bookmarks container's id, not a real bookmark meant to persist).
const PROBE_TITLE = "__recueil_probe__";
const SYNC_ALARM_NAME = "recueil-bookmarks-sync";
const SYNC_PERIOD_MINUTES = 360; // 6 hours, matching queue.js's own cadence

/**
 * What differs between the two syncs: everything else in this module is
 * shared. Both endpoints return a list whose items have a raw_url and a title.
 *
 * @typedef {Object} SyncKind
 * @property {string} folderTitle - the dedicated folder's name
 * @property {string} endpoint - the Worker route returning what to mirror
 * @property {"pages"|"links"} listKey - the property of the response holding it
 * @property {boolean} tolerateMissingRoute - whether a 404 from the endpoint
 *   means "nothing to sync from" rather than a failure (links only: the
 *   instance has them off, or a Worker that predates them)
 * @property {() => Promise<boolean>} isEnabled
 * @property {(enabled: boolean) => Promise<void>} setEnabled
 * @property {() => Promise<string|null>} getFolderId
 * @property {(folderId: string|null) => Promise<void>} setFolderId
 */

/** @type {SyncKind} */
const PAGES = {
  folderTitle: "recueil",
  endpoint: "/archived-pages",
  listKey: "pages",
  tolerateMissingRoute: false,
  isEnabled: isBookmarkSyncEnabled,
  setEnabled: setBookmarkSyncEnabled,
  getFolderId: getBookmarksFolderId,
  setFolderId: setBookmarksFolderId,
};

/** @type {SyncKind} */
const LINKS = {
  folderTitle: "recueil links",
  endpoint: "/archived-links",
  listKey: "links",
  tolerateMissingRoute: true,
  isEnabled: isLinkBookmarkSyncEnabled,
  setEnabled: setLinkBookmarkSyncEnabled,
  getFolderId: getLinkBookmarksFolderId,
  setFolderId: setLinkBookmarksFolderId,
};

/**
 * Ensures the dedicated recueil bookmarks folder exists -- reusing the
 * tracked id if it still resolves, otherwise searching for (and adopting)
 * one that already exists before creating a fresh one. That search
 * matters for the same reason individual bookmarks are reconciled by URL
 * rather than tracked id (see file doc comment): Firefox Sync (or
 * similar) could have already propagated a "recueil" folder from another
 * device to this one before this device's own extension has ever synced
 * -- without checking first, this would create a second, duplicate
 * folder rather than recognizing the one that's already there. The
 * standard this enforces: recueil will either create or adopt (outside
 * of native browser sync propagating one, which is expected and fine, not
 * something to guard against) exactly one bookmarks folder of each kind's
 * name -- never two.
 *
 * Finding the right place to look is the tricky part: Chrome and Firefox
 * use different, non-portable ids for "Other Bookmarks"/"Unfiled
 * Bookmarks" (and the title itself can be locale-translated), so neither
 * a hardcoded id nor a title match on the container itself is reliable.
 * Instead, a throwaway probe bookmark (created the same way the real
 * folder is, parentId omitted) discovers the actual default container's
 * id empirically -- whatever the browser just used for the probe is
 * exactly where bookmarks.create() would also put the real folder, no
 * guessing needed.
 *
 * @param {SyncKind} kind
 * @returns {Promise<string>}
 */
async function ensureFolder(kind) {
  const folderId = await kind.getFolderId();
  if (folderId) {
    try {
      await browser.bookmarks.get(folderId);
      return folderId;
    } catch {
      // Falls through to search-or-create below.
    }
  }

  const probe = await browser.bookmarks.create({ title: PROBE_TITLE });
  await browser.bookmarks.remove(probe.id).catch(() => {});
  if (probe.parentId === undefined) {
    // Shouldn't happen in practice -- a bookmark the browser itself just
    // created always has a real parent -- but the type is honestly
    // optional (BookmarkTreeNode.parentId is only absent for the true
    // root), so this is a real, if unreachable-in-practice, guard rather
    // than an unchecked assertion.
    throw new Error(
      "recueil: could not determine the default bookmarks folder",
    );
  }
  const defaultParentId = probe.parentId;

  const siblings = await browser.bookmarks.getChildren(defaultParentId);
  const existingFolder = siblings.find(
    (node) => node.title === kind.folderTitle && node.url === undefined,
  );
  if (existingFolder) {
    await kind.setFolderId(existingFolder.id);
    return existingFolder.id;
  }

  const folder = await browser.bookmarks.create({
    parentId: defaultParentId,
    title: kind.folderTitle,
  });
  await kind.setFolderId(folder.id);
  return folder.id;
}

/**
 * The real reconciliation: pulls the whole current list from the kind's
 * endpoint (see terraform/worker/index.js's handleListArchivedPages for why
 * this is a full list every time, not incremental) and diffs it by URL against
 * whatever's actually in the dedicated folder right now -- see file doc
 * comment for why no separate tracked-id map is needed for this. A no-op
 * entirely if this sync isn't enabled or the device isn't paired -- safe to
 * call from an alarm that fires regardless of either being true yet.
 *
 * @param {SyncKind} kind
 */
async function syncKind(kind) {
  if (!(await kind.isEnabled())) {
    return;
  }
  const config = await getConfig();
  if (!config) {
    return;
  }

  const folderId = await ensureFolder(kind);

  /** @type {any} */
  let response;
  try {
    response = await apiRequest(config, kind.endpoint);
  } catch (error) {
    // Links only: with the feature off on the instance (or a Worker that
    // predates it) the route doesn't exist. That's no list to reconcile
    // against, not an empty one, so the folder is left exactly as it is
    // rather than swept clean; it reconciles again once links are back.
    if (
      kind.tolerateMissingRoute &&
      error instanceof ApiError &&
      error.status === 404
    ) {
      return;
    }
    throw error;
  }
  /** @type {Array<{raw_url: string, title: string|null}>} */
  const items = response?.[kind.listKey] ?? [];

  const children = await browser.bookmarks.getChildren(folderId);
  /** @type {Map<string, {id: string, title: string, url?: string}>} */
  const childByUrl = new Map(
    children
      .filter((child) => child.url !== undefined)
      .map((child) => [/** @type {string} */ (child.url), child]),
  );
  const seenUrls = new Set();

  for (const item of items) {
    seenUrls.add(item.raw_url);
    const title = item.title || item.raw_url;
    const existing = childByUrl.get(item.raw_url);

    if (!existing) {
      await browser.bookmarks.create({
        parentId: folderId,
        title,
        url: item.raw_url,
      });
    } else if (existing.title !== title) {
      await browser.bookmarks.update(existing.id, { title });
    }
  }

  // Anything still in the folder that's no longer in the fetched list --
  // excluded_from_mirror flipped, or the page or link itself was deleted --
  // gets removed here, not left behind to accumulate.
  for (const child of children) {
    if (child.url !== undefined && !seenUrls.has(child.url)) {
      await browser.bookmarks.remove(child.id).catch(() => {});
    }
  }
}

/**
 * Teardown of one kind's folder, used both by the popup's "disable" toggles
 * and by unpair() -- deletes the entire folder (whatever's actually in it) and
 * forgets its tracked id.
 *
 * Using a full removeTree rather than removing entries one at a time
 * isn't introducing a new risk here -- any bookmark manually placed
 * inside recueil's own folder is already unsupported and already gets
 * swept away on the very next regular sync (syncKind above removes
 * anything whose URL isn't in the current list, with no special case for
 * "but a human put this here on purpose"), not just at teardown. This is
 * the same policy applied more thoroughly, not a separate tradeoff
 * specific to disabling/unpairing.
 *
 * @param {SyncKind} kind
 */
async function deleteFolderAndState(kind) {
  const folderId = await kind.getFolderId();
  if (folderId) {
    await browser.bookmarks.removeTree(folderId).catch(() => {});
  }
  await kind.setFolderId(null);
}

/**
 * Turns one sync on and runs an immediate sync -- called by the popup's
 * toggle only *after* it has already requested and confirmed the
 * `bookmarks` permission itself (see popup.js: that request has to happen
 * synchronously in the toggle's own change handler, the same
 * user-gesture reasoning as pairing's own <all_urls> request, not in
 * here). If the immediate sync itself fails (offline, instance
 * temporarily down), that error is allowed to propagate to the caller --
 * unlike the alarm's own best-effort handling, the popup can usefully
 * show this one -- but sync is left enabled regardless; the next alarm
 * tick will just try again.
 *
 * @param {SyncKind} kind
 */
async function enableSync(kind) {
  await kind.setEnabled(true);
  await syncKind(kind);
}

/**
 * The `bookmarks` permission is shared by both syncs, so turning one off only
 * relinquishes it if the other isn't still using it. No reason to keep
 * holding a permission that isn't being used; re-enabling later just requests
 * it again. Intentionally swallows its own failures: unpairing in particular
 * must never be blocked by a bookmarks-API hiccup.
 */
async function relinquishPermission() {
  await browser.permissions
    .remove({ permissions: ["bookmarks"] })
    .catch(() => {});
}

/**
 * Turns one sync off: tears down its folder and tracked state, clears its
 * enabled flag, and relinquishes the permission if the other sync isn't using
 * it either.
 *
 * @param {SyncKind} kind
 * @param {SyncKind} other
 */
async function disableSync(kind, other) {
  await deleteFolderAndState(kind);
  await kind.setEnabled(false);
  if (!(await other.isEnabled())) {
    await relinquishPermission();
  }
}

/** See syncKind: the archived-pages sync. */
export async function syncBookmarks() {
  await syncKind(PAGES);
}

/** See deleteFolderAndState: the archived-pages folder. */
export async function deleteBookmarksFolderAndState() {
  await deleteFolderAndState(PAGES);
}

/** See enableSync: the archived-pages sync. */
export async function enableBookmarkSync() {
  await enableSync(PAGES);
}

/**
 * Turns the archived-pages sync off (see disableSync). Used by the popup's
 * toggle; unpair() uses disableAllBookmarkSyncs instead, which has to take
 * both down.
 */
export async function disableBookmarkSync() {
  await disableSync(PAGES, LINKS);
}

// -- Saved links ("recueil links" folder) --

/** See syncKind: the saved-links sync. */
export async function syncLinkBookmarks() {
  await syncKind(LINKS);
}

/** See deleteFolderAndState: the saved-links folder. */
export async function deleteLinkBookmarksFolderAndState() {
  await deleteFolderAndState(LINKS);
}

/** See enableSync: the saved-links sync. */
export async function enableLinkBookmarkSync() {
  await enableSync(LINKS);
}

/** Turns the saved-links sync off (see disableSync). */
export async function disableLinkBookmarkSync() {
  await disableSync(LINKS, PAGES);
}

// -- Both --

/**
 * Runs both syncs, one after the other (not together: each may need the
 * default-container probe, which shouldn't race itself). They're independent,
 * so one failing never stops the other; the first error is rethrown once both
 * have run. Each is a no-op on its own if it isn't enabled, so this is safe to
 * call unconditionally from the alarm, startup and pairing.
 */
export async function syncAllBookmarks() {
  /** @type {unknown} */
  let firstError;
  let failed = false;
  for (const sync of [syncBookmarks, syncLinkBookmarks]) {
    try {
      await sync();
    } catch (error) {
      if (!failed) {
        failed = true;
        firstError = error;
      }
    }
  }
  if (failed) {
    throw firstError;
  }
}

/**
 * Tears down both syncs and relinquishes the permission: what unpairing needs
 * (see background/index.js for why this has to run before unpair's own
 * storage.local.clear(), not after).
 */
export async function disableAllBookmarkSyncs() {
  await deleteFolderAndState(PAGES);
  await deleteFolderAndState(LINKS);
  await setBookmarkSyncEnabled(false);
  await setLinkBookmarkSyncEnabled(false);
  await relinquishPermission();
}

/**
 * Sets up the recurring sync alarm and its listener. Idempotent to call on
 * every background startup, matching every other register*Alarm function.
 */
export function registerBookmarkSyncAlarm() {
  browser.alarms.create(SYNC_ALARM_NAME, {
    periodInMinutes: SYNC_PERIOD_MINUTES,
  });
  browser.alarms.onAlarm.addListener((alarm) => {
    if (alarm.name === SYNC_ALARM_NAME) {
      // Best-effort, same reasoning as queue.js's own alarm handler: a
      // failed sync (offline, instance temporarily down) isn't worth
      // surfacing anywhere, the next alarm tick just tries again.
      syncAllBookmarks().catch(() => {});
    }
  });
}
