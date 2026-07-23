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

// Everything this extension needs to remember lives under one
// storage.local key, read/written as a single object rather than several
// independent keys -- the base URL and the bearer token are only ever
// meaningful together (a token from one recueil instance is meaningless
// against another's workerBaseURL), so there's no scenario where writing
// them independently would be correct; a single key just makes "both or
// neither" the only representable state.
//
// storage.local not storage.sync: sync would replicate a live bearer
// credential through the browser's account-sync mechanism (Firefox Sync /
// Chrome Sync) to every other device signed into that browser account -- a
// real, meaningful difference from "just picking a storage API," not a style
// choice.

import browser from "webextension-polyfill";

const STORAGE_KEY = "recueil:config";

/**
 * @typedef {Object} RecueilConfig
 * @property {string} workerBaseURL - no trailing slash, e.g. "https://recueil.example.com"
 * @property {string} token - device bearer token returned by POST /pair
 * @property {number} deviceId
 * @property {string} deviceName
 * @property {string} deviceType
 */

/** @returns {Promise<RecueilConfig|null>} */
export async function getConfig() {
  const stored = await browser.storage.local.get(STORAGE_KEY);
  return /** @type {RecueilConfig|null} */ (stored[STORAGE_KEY] ?? null);
}

/** @param {RecueilConfig} config */
export async function setConfig(config) {
  await browser.storage.local.set({ [STORAGE_KEY]: config });
}

export async function clearConfig() {
  await browser.storage.local.remove(STORAGE_KEY);
}

// A separate key from STORAGE_KEY above, this is in-progress form state
// (the popup's whole DOM/JS state is torn down the instant it loses focus,
// in every browser, which is what actually prompted this: switching windows
// mid-pairing to go copy a token loses whatever you'd typed). Kept as its own
// key rather than folded into RecueilConfig because it's a fundamentally
// different kind of data -- disposable UI draft state, cleared the moment
// pairing actually succeeds (see auth.js's pair() call site in popup.js), not
// a credential meant to persist. An interim fix: moving the pairing form to
// its own real extension tab instead of the transient popup is the more
// correct fix, planned for whenever the UI gets a real styling pass.
const PAIRING_DRAFT_KEY = "recueil:pairing-draft";

/**
 * @typedef {Object} PairingDraft
 * @property {string} [workerBaseURL]
 * @property {string} [pairingToken]
 * @property {string} [deviceName]
 */

/** @returns {Promise<PairingDraft>} */
export async function getPairingDraft() {
  const stored = await browser.storage.local.get(PAIRING_DRAFT_KEY);
  return /** @type {PairingDraft} */ (stored[PAIRING_DRAFT_KEY] ?? {});
}

/** @param {PairingDraft} draft */
export async function setPairingDraft(draft) {
  await browser.storage.local.set({ [PAIRING_DRAFT_KEY]: draft });
}

export async function clearPairingDraft() {
  await browser.storage.local.remove(PAIRING_DRAFT_KEY);
}

// A cache of the last successful GET /queue response -- never authoritative
// on its own (see queue.js's doc comment: the real lock check happens at
// claim time, live, not against whatever's cached here), just what the
// popup displays without needing a network round-trip every time it opens.
const QUEUE_CACHE_KEY = "recueil:queue-cache";

/**
 * @typedef {Object} QueueCacheItem
 * @property {string} id
 * @property {string} url
 * @property {string} status
 * @property {number|null} claimed_by_token_id
 * @property {string|null} claimed_at
 * @property {string} created_at
 */

/**
 * @typedef {Object} QueueCache
 * @property {QueueCacheItem[]} items
 * @property {string|null} fetchedAt - ISO timestamp of the last successful
 *   refresh, or null if one has never happened yet
 */

/** @returns {Promise<QueueCache>} */
export async function getQueueCache() {
  const stored = await browser.storage.local.get(QUEUE_CACHE_KEY);
  return /** @type {QueueCache} */ (
    stored[QUEUE_CACHE_KEY] ?? { items: [], fetchedAt: null }
  );
}

/** @param {QueueCache} cache */
export async function setQueueCache(cache) {
  await browser.storage.local.set({ [QUEUE_CACHE_KEY]: cache });
}

// Tracks which open tab is fulfilling which claimed queue item -- see
// queue.js's claimQueueItem() (which sets this right after opening the
// tab) and capture.js's captureActiveTab() (which checks this to decide
// whether to complete via POST /queue/:id/complete instead of the default
// POST /captures/complete). Keyed by tabId, stringified -- object keys are
// always strings regardless of what you index with, made explicit here
// rather than relying on implicit coercion to be obvious at every call
// site.
//
// Never explicitly cleaned up if a tab is just closed without capturing since it's
// not needed: the Worker's own claim already goes stale and
// becomes reclaimable after 15 minutes (see terraform/worker/index.js's
// handleClaimQueueItem), so an orphaned entry here is harmless clutter,
// not a correctness problem. Tidied up on tab close anyway
// (background/index.js's tabs.onRemoved listener) purely so this doesn't
// grow without bound over a long browsing session.
const CLAIMED_TABS_KEY = "recueil:claimed-tabs";

/** @returns {Promise<Record<string, string>>} stringified tabId -> queue item id */
export async function getClaimedTabs() {
  const stored = await browser.storage.local.get(CLAIMED_TABS_KEY);
  return /** @type {Record<string, string>} */ (stored[CLAIMED_TABS_KEY] ?? {});
}

/**
 * @param {number} tabId
 * @param {string} itemId
 */
export async function setClaimedTab(tabId, itemId) {
  const tabs = await getClaimedTabs();
  tabs[String(tabId)] = itemId;
  await browser.storage.local.set({ [CLAIMED_TABS_KEY]: tabs });
}

/** @param {number} tabId */
export async function clearClaimedTab(tabId) {
  const tabs = await getClaimedTabs();
  delete tabs[String(tabId)];
  await browser.storage.local.set({ [CLAIMED_TABS_KEY]: tabs });
}

// Bookmark sync state -- see background/bookmarks.js for the actual
// reconciliation logic these back. Three separate keys rather than one
// combined object for the same reason config/pairing-draft/queue-cache/
// claimed-tabs already are: each is read and written independently, by
// different call sites, and there's no scenario where combining them
// would save anything.

const BOOKMARK_SYNC_ENABLED_KEY = "recueil:bookmark-sync-enabled";

/** @returns {Promise<boolean>} */
export async function isBookmarkSyncEnabled() {
  const stored = await browser.storage.local.get(BOOKMARK_SYNC_ENABLED_KEY);
  return stored[BOOKMARK_SYNC_ENABLED_KEY] === true;
}

/** @param {boolean} enabled */
export async function setBookmarkSyncEnabled(enabled) {
  await browser.storage.local.set({ [BOOKMARK_SYNC_ENABLED_KEY]: enabled });
}

// The browser-assigned id of the dedicated "recueil" bookmarks folder --
// per-browser-installation, like every bookmark/folder id, so this always
// needs a liveness check (does bookmarks.get(id) still resolve?) before
// being trusted, never assumed still valid just because it's stored. This
// is the only bookmark-related state recueil persists at all -- see
// background/bookmarks.js's own doc comment for why no per-page
// page_id -> bookmark id map is needed on top of it: reconciliation is
// done by diffing against the folder's actual live contents each sync,
// not against a separately-tracked copy of them.
const BOOKMARKS_FOLDER_ID_KEY = "recueil:bookmarks-folder-id";

/** @returns {Promise<string|null>} */
export async function getBookmarksFolderId() {
  const stored = await browser.storage.local.get(BOOKMARKS_FOLDER_ID_KEY);
  return /** @type {string|null} */ (stored[BOOKMARKS_FOLDER_ID_KEY] ?? null);
}

/** @param {string|null} folderId */
export async function setBookmarksFolderId(folderId) {
  await browser.storage.local.set({ [BOOKMARKS_FOLDER_ID_KEY]: folderId });
}
