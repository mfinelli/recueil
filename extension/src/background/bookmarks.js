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
  getBookmarksFolderId,
  setBookmarksFolderId,
} from "../common/storage.js";
import { apiRequest } from "../common/api-client.js";

const FOLDER_TITLE = "recueil";
const SYNC_ALARM_NAME = "recueil-bookmarks-sync";
const SYNC_PERIOD_MINUTES = 360; // 6 hours, matching queue.js's own cadence

/**
 * Ensures the dedicated recueil bookmarks folder exists, creating a fresh
 * one if the tracked id is missing or no longer resolves (the user
 * deleted it, or this is a browser recueil has never created it on yet).
 * Omits `parentId` when creating: it defaults to "Other Bookmarks" in
 * Chrome and "Unfiled Bookmarks" in Firefox, in both cases the same
 * portable, sensible default without needing to manually walk
 * bookmarks.getTree() to find it.
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
      // Falls through to create a fresh one below.
    }
  }
  const folder = await browser.bookmarks.create({ title: FOLDER_TITLE });
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
 * -- deletes the entire recueil folder (whatever's actually in it, not just
 * what this sync last touched) and forgets its tracked id. A full removeTree
 * rather than removing entries one at a time is deliberate: simpler, and the
 * accepted tradeoff (something the user manually placed inside recueil's own
 * folder would be swept away too) is a narrow, well-labeled-folder edge case,
 * not a real risk to anything outside it.
 */
export async function deleteBookmarksFolderAndState() {
  const folderId = await getBookmarksFolderId();
  if (folderId) {
    await browser.bookmarks.removeTree(folderId).catch(() => {});
  }
  await setBookmarksFolderId(null);
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
