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
// queue.js) behind a single runtime.onMessage listener -- popup.js is the
// real caller of all of this via that listener; the __recueil* globals at
// the bottom exist alongside it, not instead of it.
//
// queue.js so far only covers the list-refresh-and-badge half (this is
// NOT the same thing as actually claiming/capturing a queue item): the
// claim flow and the completion-routing change to the existing capture
// button (so it completes via POST /queue/:id/complete instead of
// POST /captures/complete when running against a claimed queue item's
// tab) are still ahead.
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
import { refreshQueueList, registerQueueRefreshAlarm } from "./queue.js";
import { getQueueCache } from "../common/storage.js";
import {
  PAIR_DEVICE,
  GET_AUTH_STATE,
  CAPTURE_ACTIVE_TAB,
  UNPAIR_DEVICE,
  GET_QUEUE_LIST,
  REFRESH_QUEUE_LIST,
} from "../common/messages.js";

registerFetchRelay();
// Routes single-file-core's cross-frame collection messages to the top
// frame so embedded iframes get captured -- required on Firefox, a no-op
// on Chrome (which coordinates frames in-page). See frame-tree-relay.js.
registerFrameTreeRelay();

registerQueueRefreshAlarm();
// Once per real browser start/extension install-or-update, not per
// service-worker wake (see queue.js's own doc comment for why that
// distinction matters) -- gets the badge roughly right without waiting for
// the first 6-hour alarm tick after e.g. a fresh install.
browser.runtime.onStartup.addListener(() => {
  refreshQueueList().catch(() => {});
});
browser.runtime.onInstalled.addListener(() => {
  refreshQueueList().catch(() => {});
});

// message is genuinely untyped at this boundary -- see fetch-relay.js's
// own listener for the same reasoning.
browser.runtime.onMessage.addListener((/** @type {any} */ message) => {
  if (!message || typeof message.type !== "string") {
    return undefined;
  }
  switch (message.type) {
    case PAIR_DEVICE:
      return pair(message.payload);
    case GET_AUTH_STATE:
      return getAuthState();
    case CAPTURE_ACTIVE_TAB:
      return captureActiveTab();
    case UNPAIR_DEVICE:
      return unpair();
    case GET_QUEUE_LIST:
      return getQueueCache();
    case REFRESH_QUEUE_LIST:
      return refreshQueueList();
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
