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

import browser from "webextension-polyfill";
import {
  getConfig,
  isBookmarkSyncEnabled,
  setBookmarkSyncEnabled,
  getBookmarksFolderId,
  setBookmarksFolderId,
} from "../common/storage.js";
import { apiRequest } from "../common/api-client.js";

const FOLDER_TITLE = "recueil";
// An explicitly unusual, unlikely-to-collide title -- see ensureFolder's
// doc comment for what this is actually for (discovering the real default
// bookmarks container's id, not a real bookmark meant to persist).
const PROBE_TITLE = "__recueil_probe__";
const SYNC_ALARM_NAME = "recueil-bookmarks-sync";
const SYNC_PERIOD_MINUTES = 360; // 6 hours, matching queue.js's own cadence

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
 * something to guard against) exactly one bookmarks folder named
 * "recueil" -- never two.
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
 * @returns {Promise<string>}
 */
async function ensureFolder() {
  const folderId = await getBookmarksFolderId();
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
    (node) => node.title === FOLDER_TITLE && node.url === undefined,
  );
  if (existingFolder) {
    await setBookmarksFolderId(existingFolder.id);
    return existingFolder.id;
  }

  const folder = await browser.bookmarks.create({
    parentId: defaultParentId,
    title: FOLDER_TITLE,
  });
  await setBookmarksFolderId(folder.id);
  return folder.id;
}

/**
 * The real reconciliation: pulls the whole current archived-pages list
 * (see terraform/index.js's handleListArchivedPages for why this is a
 * full list every time, not incremental) and diffs it by URL against
 * whatever's actually in the dedicated folder right now -- see file doc
 * comment for why no separate tracked-id map is needed for this. A no-op
 * entirely if bookmark sync isn't enabled or the device isn't paired --
 * safe to call from an alarm that fires regardless of either being true
 * yet.
 */
export async function syncBookmarks() {
  if (!(await isBookmarkSyncEnabled())) {
    return;
  }
  const config = await getConfig();
  if (!config) {
    return;
  }

  const folderId = await ensureFolder();
  const response =
    /** @type {{pages: Array<{page_id: number, raw_url: string, title: string|null}>}} */ (
      await apiRequest(config, "/archived-pages")
    );
  const pages = response?.pages ?? [];

  const children = await browser.bookmarks.getChildren(folderId);
  /** @type {Map<string, {id: string, title: string, url?: string}>} */
  const childByUrl = new Map(
    children
      .filter((child) => child.url !== undefined)
      .map((child) => [/** @type {string} */ (child.url), child]),
  );
  const seenUrls = new Set();

  for (const page of pages) {
    seenUrls.add(page.raw_url);
    const title = page.title || page.raw_url;
    const existing = childByUrl.get(page.raw_url);

    if (!existing) {
      await browser.bookmarks.create({
        parentId: folderId,
        title,
        url: page.raw_url,
      });
    } else if (existing.title !== title) {
      await browser.bookmarks.update(existing.id, { title });
    }
  }

  // Anything still in the folder that's no longer in the fetched list --
  // excluded_from_mirror flipped, or the page itself was deleted -- gets
  // removed here, not left behind to accumulate.
  for (const child of children) {
    if (child.url !== undefined && !seenUrls.has(child.url)) {
      await browser.bookmarks.remove(child.id).catch(() => {});
    }
  }
}

/**
 * Shared teardown, used both by the popup's "disable" toggle and by unpair()
 * -- deletes the entire recueil folder (whatever's actually in it) and
 * forgets its tracked id.
 *
 * Using a full removeTree rather than removing entries one at a time
 * isn't introducing a new risk here -- any bookmark manually placed
 * inside recueil's own folder is already unsupported and already gets
 * swept away on the very next regular sync (syncBookmarks above removes
 * anything whose URL isn't in the current archived-pages list, with no
 * special case for "but a human put this here on purpose"), not just at
 * teardown. This is the same policy applied more thoroughly, not a
 * separate tradeoff specific to disabling/unpairing.
 */
export async function deleteBookmarksFolderAndState() {
  const folderId = await getBookmarksFolderId();
  if (folderId) {
    await browser.bookmarks.removeTree(folderId).catch(() => {});
  }
  await setBookmarksFolderId(null);
}

/**
 * Turns bookmark sync on and runs an immediate sync -- called by the
 * popup's toggle only *after* it has already requested and confirmed the
 * `bookmarks` permission itself (see popup.js: that request has to happen
 * synchronously in the toggle's own change handler, the same
 * user-gesture reasoning as pairing's own <all_urls> request, not in
 * here). If the immediate sync itself fails (offline, instance
 * temporarily down), that error is allowed to propagate to the caller --
 * unlike the alarm's own best-effort handling, the popup can usefully
 * show this one -- but sync is left enabled regardless; the next alarm
 * tick will just try again.
 */
export async function enableBookmarkSync() {
  await setBookmarkSyncEnabled(true);
  await syncBookmarks();
}

/**
 * Turns bookmark sync off: tears down the folder and its tracked state
 * (deleteBookmarksFolderAndState above), clears the enabled flag, and
 * relinquishes the `bookmarks` permission itself -- no reason to keep
 * holding a permission that isn't being used; re-enabling later just
 * requests it again. Used both by the popup's toggle and by unpair() --
 * see background/index.js for why the ordering there matters (this has
 * to run before unpair's own storage.local.clear(), not after).
 * Intentionally swallows its own failures throughout: unpairing in
 * particular must never be blocked by a bookmarks-API hiccup.
 */
export async function disableBookmarkSync() {
  await deleteBookmarksFolderAndState();
  await setBookmarkSyncEnabled(false);
  await browser.permissions
    .remove({ permissions: ["bookmarks"] })
    .catch(() => {});
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
      syncBookmarks().catch(() => {});
    }
  });
}
