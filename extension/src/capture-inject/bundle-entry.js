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

// This is bundled by build.js into a single, dependency-free IIFE
// (capture-inject.js) and injected via scripting.executeScript({files:
// ["capture-inject.js"]}) -- NOT declared as a manifest content_script, so
// it only ever runs in a tab we've explicitly chosen to capture, never on
// every page load. It runs in the content-script ("ISOLATED") world by
// default, which is what gives it access to browser.runtime.sendMessage
// for relay-fetch.js -- don't set world: "MAIN" on the executeScript call
// that loads this, that would lose it.
//
// Exposes a single global (rather than being ES-module-importable) because
// it's loaded via `files`, not `func` -- see background/index.js's
// __recueilTestCapture for the two-step pattern this is meant to support:
// load this bundle once to define the global, then a second, tiny,
// self-contained executeScript({func}) call invokes it and returns the
// result. That second step can't itself import anything (func-injected
// functions can't close over module scope), which is exactly why this
// needs to be a global rather than an export.
//
// MULTI-FRAME CAPTURE (embedded iframes) is on: removeFrames is false
// below, so single-file-core walks the frame tree and inlines each
// subframe's content into the top document. Two pieces outside this file
// make that work, and both have to be present for it to:
//
//   1. background/capture.js injects this bundle into every frame
//      (target.allFrames: true on the `files` step), so each subframe has
//      single-file-core's frame-tree collector loaded and can serialize
//      its own DOM when the top frame asks. captureFrame() itself still
//      runs in the top frame only.
//   2. background/frame-tree-relay.js forwards the collector's cross-frame
//      messages to the top frame. That relay is what a subframe's result
//      travels through on Firefox (where the collector uses
//      runtime.sendMessage); on Chrome the collector coordinates in-page
//      via postMessage and the relay isn't involved. Without it, flipping
//      removeFrames to false is exactly what surfaces "Could not establish
//      connection. Receiving end does not exist." -- see that file's doc
//      comment for the full mechanism.

import * as singlefile from "single-file-core/single-file.js";
import { relayFetch } from "./relay-fetch.js";
import { selectFavicon } from "./favicon.js";

const CAPTURE_OPTIONS = {
  removeFrames: false, // inline embedded iframes too -- see file doc comment
  compressHTML: true,
  removeHiddenElements: true,
  removeUnusedStyles: true,
  removeUnusedFonts: true,
  removeImports: true,
  blockScripts: true,
  blockAudios: true,
  blockVideos: true,
  removeAlternativeFonts: true,
  removeAlternativeMedias: true,
  removeAlternativeImages: true,
};

/**
 * What captureFrame returns -- crosses back to background/capture.js as
 * the result of a scripting.executeScript({func}) call, not a real import
 * (these are two independently-bundled files, see file doc comment above),
 * so this typedef exists purely for capture.js to reference via
 * `import("...")` JSDoc syntax without an actual runtime dependency
 * between the two bundles.
 * @typedef {Object} CapturedPage
 * @property {string} html
 * @property {string} title
 * @property {{bytes: number[], ext: string}|null} favicon
 */

/** @returns {Promise<CapturedPage>} */
async function captureFrame() {
  singlefile.init({
    // fetch and frameFetch share relayFetch: both want the same
    // direct-then-background-relay behavior (see relay-fetch.js).
    // frameFetch is single-file-core's per-frame resource fetch, used when
    // a resource is pulled on behalf of a specific frameId. Passing it is
    // redundant with core/util.js's own `frameFetch || fetch` default
    // (which already resolves to relayFetch here) -- named explicitly so
    // it's clear the frame path isn't silently falling through to
    // something else now that iframe capture is on.
    fetch: relayFetch,
    frameFetch: relayFetch,
  });

  const pageData = await singlefile.getPageData(CAPTURE_OPTIONS);
  const favicon = await selectFavicon(relayFetch);

  return {
    html: pageData.content,
    title: pageData.title,
    favicon: favicon && {
      // Transferred as a plain array, not the raw ArrayBuffer -- the
      // result of a scripting.executeScript call goes through the same
      // structured-clone boundary as runtime messaging, so this isn't
      // strictly required, but keeping the two transfer points (this one
      // and relay-fetch.js's) consistent is one less thing to remember
      // differs between them.
      bytes: Array.from(new Uint8Array(favicon.bytes)),
      ext: favicon.ext,
    },
  };
}

globalThis.__recueilSingleFile = { captureFrame };
