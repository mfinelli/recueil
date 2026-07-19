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

// Background entry point. Wires together the pieces that actually do
// things (fetch-relay.js, frame-tree-relay.js, auth.js, capture.js,
// queue.js, bookmarks.js) behind a single runtime.onMessage listener --
// popup.js is the real caller of all of this via that listener; the
// __recueil* globals at the bottom exist alongside it, not instead of it.
//
// MV3 service workers are non-persistent: nothing in module scope here
// survives an idle-timeout unload. registerFetchRelay() and
// runtime.onMessage.addListener() re-registering on every wake is the
// correct behavior, not a leak -- there's no state to lose because none is
// kept; getConfig() (see storage.js) re-reads from storage.local fresh
// every time it's needed instead.

import browser from "webextension-polyfill";
import { registerFetchRelay } from "./fetch-relay.js";
import { registerFrameTreeRelay } from "./frame-tree-relay.js";
import { pair, getAuthState, unpair } from "./auth.js";
import { captureActiveTab, runCaptureInject } from "./capture.js";
import {
  refreshQueueList,
  registerQueueRefreshAlarm,
  claimQueueItem,
} from "./queue.js";
import {
  syncBookmarks,
  registerBookmarkSyncAlarm,
  enableBookmarkSync,
  disableBookmarkSync,
} from "./bookmarks.js";
import {
  getQueueCache,
  clearClaimedTab,
  isBookmarkSyncEnabled,
} from "../common/storage.js";
import {
  PAIR_DEVICE,
  GET_AUTH_STATE,
  CAPTURE_ACTIVE_TAB,
  UNPAIR_DEVICE,
  GET_QUEUE_LIST,
  REFRESH_QUEUE_LIST,
  CLAIM_QUEUE_ITEM,
  GET_BOOKMARK_SYNC_STATE,
  ENABLE_BOOKMARK_SYNC,
  DISABLE_BOOKMARK_SYNC,
} from "../common/messages.js";

registerFetchRelay();
// Routes single-file-core's cross-frame collection messages to the top
// frame so embedded iframes get captured -- required on Firefox, a no-op
// on Chrome (which coordinates frames in-page). See frame-tree-relay.js.
registerFrameTreeRelay();

registerQueueRefreshAlarm();
registerBookmarkSyncAlarm();
// Once per real browser start/extension install-or-update, not per
// service-worker wake (see queue.js's own doc comment for why that
// distinction matters) -- gets the badge (and, for bookmark sync, the
// folder's contents) roughly right without waiting for the first 6-hour
// alarm tick after e.g. a fresh install. syncBookmarks() is always safe to
// call unconditionally here -- it no-ops on its own if sync isn't enabled
// or the device isn't paired yet, the same as the alarm-driven call does.
browser.runtime.onStartup.addListener(() => {
  refreshQueueList().catch(() => {});
  syncBookmarks().catch(() => {});
});
browser.runtime.onInstalled.addListener(() => {
  refreshQueueList().catch(() => {});
  syncBookmarks().catch(() => {});
});

// Tidies up storage.js's claimed-tabs tracking when a tab closes --
// purely storage hygiene, not a correctness requirement: an orphaned
// entry left behind by, say, the browser crashing is harmless (the
// Worker's own claim goes stale and becomes reclaimable after 15 minutes
// regardless, see terraform/index.js's handleClaimQueueItem), just clutter
// that would otherwise accumulate over a long browsing session.
browser.tabs.onRemoved.addListener((tabId) => {
  clearClaimedTab(tabId).catch(() => {});
});

// message is genuinely untyped at this boundary -- see fetch-relay.js's
// own listener for the same reasoning.
browser.runtime.onMessage.addListener((/** @type {any} */ message) => {
  if (!message || typeof message.type !== "string") {
    return undefined;
  }
  switch (message.type) {
    case PAIR_DEVICE:
      return pair(message.payload).then((config) => {
        // Otherwise the popup shows "Nothing in the queue" until the next
        // alarm tick (up to 6 hours away) or a manual refresh, even if the
        // instance already has real pending items -- worth doing eagerly
        // right when pairing succeeds, since that's exactly when the user
        // is about to look at the popup again anyway. syncBookmarks() is
        // wired in for the same consistency, even though in practice it's
        // almost always a no-op here: unpair() already clears the enabled
        // flag along with everything else, so there's rarely a case where
        // sync is already enabled at the exact moment a *new* pairing
        // succeeds. Harmless either way, and correct if that assumption
        // ever stops holding.
        refreshQueueList().catch(() => {});
        syncBookmarks().catch(() => {});
        return config;
      });
    case GET_AUTH_STATE:
      return getAuthState();
    case CAPTURE_ACTIVE_TAB:
      return captureActiveTab();
    case UNPAIR_DEVICE:
      // disableBookmarkSync() has to run before unpair()'s own
      // storage.local.clear() -- it reads the tracked folder id from
      // storage to know what to remove, which unpair's own wipe would
      // otherwise have already erased by the time it ran. Caught here,
      // not just relied on disableBookmarkSync's own internal
      // best-effort handling: unpairing itself must never be blocked by
      // a bookmarks-API hiccup, no matter what.
      return disableBookmarkSync()
        .catch(() => {})
        .then(() => unpair());
    case GET_QUEUE_LIST:
      return getQueueCache();
    case REFRESH_QUEUE_LIST:
      return refreshQueueList();
    case CLAIM_QUEUE_ITEM:
      return claimQueueItem(message.payload.itemId);
    case GET_BOOKMARK_SYNC_STATE:
      return isBookmarkSyncEnabled();
    case ENABLE_BOOKMARK_SYNC:
      return enableBookmarkSync();
    case DISABLE_BOOKMARK_SYNC:
      return disableBookmarkSync();
    default:
      return undefined;
  }
});

// Manual-testing entry points, run from the background service worker's
// own devtools console (chrome://extensions in Chrome, about:debugging in
// Firefox -> "Inspect"). Kept even now that popup.js is real: these call
// the same underlying functions the message listener above dispatches to,
// so they're genuinely redundant with clicking through the popup for
// testing "does capture/pairing work at all" -- but they're what lets you
// tell a popup.js (DOM/message-passing) bug apart from a background-logic
// bug, and they're faster during active development than clicking through
// UI every time.
// TODO: remove them once we're sure that everything is working
globalThis.__recueilPair = pair;
globalThis.__recueilUnpair = unpair;
globalThis.__recueilAuthState = getAuthState;
globalThis.__recueilCapture = captureActiveTab;
globalThis.__recueilRefreshQueue = refreshQueueList;
globalThis.__recueilClaimQueueItem = claimQueueItem;

// Bookmark sync is now driven by the popup's own toggle -- these remain
// for the same reason every other __recueil* global does (faster than
// clicking through the UI during active development, and lets you tell a
// popup.js bug apart from a background-logic one). Still need to grant
// the `bookmarks` permission yourself first if you haven't gone through
// the popup's toggle at least once (this extension's own details page in
// about:addons/chrome://extensions, or
// `await browser.permissions.request({permissions: ["bookmarks"]})` right
// here in this console).
globalThis.__recueilEnableBookmarkSync = enableBookmarkSync;
globalThis.__recueilSyncBookmarks = syncBookmarks;
globalThis.__recueilDisableBookmarkSync = disableBookmarkSync;

// Narrower than __recueilCapture above: runs only the capture-inject step
// (no auth, no upload) against whatever tab is active, for isolating a
// single-file-core/favicon-selection problem from an upload/auth one while
// debugging. Reuses capture.js's own runCaptureInject rather than
// reimplementing the two-step injection dance a second time here.
// TODO: remove this once we're sure that everything is working
globalThis.__recueilTestCaptureOnly = async function () {
  const [tab] = await browser.tabs.query({
    active: true,
    currentWindow: true,
  });
  if (!tab || tab.id === undefined) {
    throw new Error("no active tab found");
  }
  return runCaptureInject(tab.id);
};
