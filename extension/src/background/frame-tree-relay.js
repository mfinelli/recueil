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

// The background half of single-file-core's multi-frame (embedded iframe)
// capture. It exists to route two of that library's internal messages --
// nothing recueil's own code ever sends -- from the frame that emits them
// back to the top frame that's actually assembling the capture.
//
// Why this is needed at all: single-file-core's frame-tree collector
// (single-file-core/processors/frame-tree/content/content-frame-tree.js,
// bundled into capture-inject.js) has each frame hand its serialized DOM
// back to the top frame. Its own sendMessage() picks the transport by
// reading globalThis.browser:
//
//     if (targetWindow == top && browser && browser.runtime && ...) {
//         browser.runtime.sendMessage(message);   // <- lands HERE
//     } else {
//         targetWindow.postMessage(...);           // in-page, no background
//     }
//
//   - On Chrome, globalThis.browser is undefined in the content-script
//     world (we bundle webextension-polyfill as a module import, which
//     under esbuild's CJS path never assigns globalThis.browser, and
//     Chrome has no native `browser`), so that code takes the postMessage
//     branch and coordinates entirely in-page -- this relay is never even
//     invoked there.
//   - On Firefox, globalThis.browser IS defined natively, so the frame
//     posts its result via browser.runtime.sendMessage and expects the
//     background to forward it to the top frame. With no listener doing
//     that, the send rejects with "Could not establish connection.
//     Receiving end does not exist." and the top frame's collection times
//     out with empty frames. That rejection is exactly the symptom that
//     appears the moment CAPTURE_OPTIONS.removeFrames flips to false.
//
// So this is a Firefox-path requirement that's a harmless no-op on Chrome;
// registering it unconditionally keeps the two targets on one code path
// here even though the content side diverges. Modeled directly on
// SingleFile-MV3's own background relay
// (src/lib/single-file/frame-tree/bg/frame-tree.js in
// github.com/gildas-lormeau/SingleFile-MV3), which its background.js
// imports right alongside the fetch relay for the same reason.

import browser from "webextension-polyfill";

// single-file-core's own wire constants (INIT_RESPONSE_MESSAGE and
// ACK_INIT_REQUEST_MESSAGE in content-frame-tree.js). Duplicated as
// literals here, not imported, because single-file-core doesn't export
// them and they're part of capture-inject.js's separately-bundled world
// anyway -- there's no shared symbol these could reference. They ride on
// message.method (not recueil's own message.type), which is also why the
// fetch-relay and index.js listeners cleanly ignore them.
const FRAME_TREE_INIT_RESPONSE = "singlefile.frameTree.initResponse";
const FRAME_TREE_ACK_INIT_REQUEST = "singlefile.frameTree.ackInitRequest";

export function registerFrameTreeRelay() {
  browser.runtime.onMessage.addListener(
    (/** @type {any} */ message, /** @type {any} */ sender) => {
      if (
        !message ||
        (message.method !== FRAME_TREE_INIT_RESPONSE &&
          message.method !== FRAME_TREE_ACK_INIT_REQUEST)
      ) {
        // Not a frame-tree message -- returning undefined (rather than a
        // promise) tells the WebExtension messaging system this listener
        // isn't handling it, so the fetch-relay and index.js listeners on
        // the same runtime.onMessage event still get their turn. See
        // fetch-relay.js for the same pattern.
        return undefined;
      }

      // A frame-tree message always originates in a content script, so
      // sender.tab is populated; guard it anyway so a malformed message
      // can't throw inside the listener and take down the send.
      const tabId = sender && sender.tab && sender.tab.id;
      if (tabId !== undefined) {
        // frameId: 0 is the top frame -- the one running getPageData()
        // and waiting on every subframe's initResponse. The .catch keeps a
        // late/absent top-frame listener (e.g. the capture already tore
        // down) from surfacing as an unhandled rejection in the service
        // worker; the collection's own response timeout handles that frame
        // regardless.
        browser.tabs
          .sendMessage(tabId, message, { frameId: 0 })
          .catch(() => {});
      }

      // Resolve so the *sender's* browser.runtime.sendMessage settles
      // instead of rejecting with "Receiving end does not exist." -- that
      // rejection is the whole bug this relay fixes. The forwarding above
      // is intentionally decoupled from this response: the frame that sent
      // the message only needs to know the background accepted it, not
      // that the top frame has finished handling it.
      return Promise.resolve({});
    },
  );
}
