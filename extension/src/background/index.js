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
// things (fetch-relay.js, auth.js, capture.js) behind a single
// runtime.onMessage listener -- the popup (not built yet) will be the
// real caller of all of this; until then, the __recueil* globals at the
// bottom are how this gets exercised at all.
//
// Still ahead: queue.js (alarm-driven polling + the open-tab/wait/close
// lifecycle a queue-driven capture needs, distinct from capture.js's
// direct-capture path -- see capture.js's own doc comment) and mirror.js
// (pulling the D1 bookmark-list mirror for popup display).
//
// MV3 service workers are non-persistent: nothing in module scope here
// survives an idle-timeout unload. registerFetchRelay() and
// runtime.onMessage.addListener() re-registering on every wake is the
// correct behavior, not a leak -- there's no state to lose because none is
// kept; getConfig() (see storage.js) re-reads from storage.local fresh
// every time it's needed instead.

import browser from "webextension-polyfill";
import { registerFetchRelay } from "./fetch-relay.js";
import { pair, getAuthState, unpair } from "./auth.js";
import { captureActiveTab } from "./capture.js";
import {
  PAIR_DEVICE,
  GET_AUTH_STATE,
  CAPTURE_ACTIVE_TAB,
  UNPAIR_DEVICE,
} from "../common/messages.js";

registerFetchRelay();

browser.runtime.onMessage.addListener((message) => {
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
    default:
      return undefined;
  }
});

// Temporary manual-testing entry points: run these from the background
// service worker's own devtools console (chrome://extensions in Chrome,
// about:debugging in Firefox -> "Inspect") -- no popup UI exists yet to
// drive any of this.
globalThis.__recueilPair = pair;
globalThis.__recueilUnpair = unpair;
globalThis.__recueilAuthState = getAuthState;
globalThis.__recueilCapture = captureActiveTab;

// Narrower than __recueilCapture above: runs only the capture-inject step
// (no auth, no upload) against whatever tab is active, for isolating a
// single-file-core/favicon-selection problem from an upload/auth one while
// debugging.
globalThis.__recueilTestCaptureOnly = async function () {
  const [tab] = await browser.tabs.query({
    active: true,
    currentWindow: true,
  });
  if (!tab) {
    throw new Error("no active tab found");
  }

  await browser.scripting.executeScript({
    target: { tabId: tab.id },
    files: ["capture-inject.js"],
  });

  const [{ result }] = await browser.scripting.executeScript({
    target: { tabId: tab.id },
    func: () => globalThis.__recueilSingleFile.captureFrame(),
  });

  return result;
};
