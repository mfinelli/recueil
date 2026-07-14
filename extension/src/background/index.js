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

// Background entry point. Deliberately minimal so far -- this is scaffolding
// to prove the capture-inject bundle + fetch relay actually work end to end
// (triggerCapture below, invokable from the extension's own service-worker
// console for now, since there's no popup UI yet), not the real orchestration
// layer. Auth/pairing, queue polling (queue.js), and upload (capture.js's
// real home once it exists) all land here as separate modules once this
// core piece is proven -- see DESIGN.md §3g/§8 and IMPLEMENTATION.md for
// what's still ahead.
//
// MV3 service workers are non-persistent: nothing in module scope here
// survives an idle-timeout unload. registerFetchRelay() re-registering its
// listener on every wake is the correct behavior, not a leak -- there's no
// state to lose because none is kept.

import browser from "webextension-polyfill";
import { registerFetchRelay } from "./fetch-relay.js";
import { pair, getAuthState, unpair } from "./auth.js";
import { PAIR_DEVICE, GET_AUTH_STATE } from "../common/messages.js";

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

// Temporary manual-testing entry point: run this from the background
// service worker's own devtools console (chrome://extensions in Chrome,
// about:debugging in Firefox -> "Inspect") against whatever tab is
// currently active, to confirm the capture-inject bundle loads and
// single-file-core actually produces output before any real UI exists to
// trigger it.
globalThis.__recueilTestCapture = async function () {
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
