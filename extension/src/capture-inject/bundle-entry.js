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
// MULTI-FRAME CAPTURE: this same bundle is injected into every frame on
// the page (background/capture.js's runCaptureInject uses
// target.allFrames: true for the `files` step), but captureFrame() below
// is only ever invoked in the top frame (the second, func-based
// executeScript call is never allFrames). That asymmetry is deliberate,
// not an oversight: single-file-core/single-file.js unconditionally
// imports processors/index.js, which unconditionally imports
// content-frame-tree.js -- the actual frame-tree collection logic already
// runs its own postMessage-based coordination protocol as an import-time
// side effect (a MutationObserver plus window "message" listeners), in
// every frame that has this bundle loaded, regardless of whether
// captureFrame() itself is ever called there. So merely loading this
// bundle in a subframe is what lets it participate -- calling
// captureFrame() a second time there would be redundant (and wrong: a
// subframe has no favicon of its own to select, for one). getPageData()'s
// own removeFrames: false below is what makes it actually go looking for
// those other frames' data at all, via the same content-frame-tree module,
// which the top frame's copy also has loaded.

import * as singlefile from "single-file-core/single-file.js";
import { relayFetch } from "./relay-fetch.js";
import { selectFavicon } from "./favicon.js";

const CAPTURE_OPTIONS = {
  removeFrames: false, // collect embedded iframes too -- see file doc comment
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
    // frameFetch shares relayFetch with fetch -- both need the same
    // background-context CORS bypass (see background/fetch-relay.js),
    // and single-file-core's own default (frameFetch || fetch) would
    // already end up here anyway; passing it explicitly just documents
    // that this isn't an oversight now that frames are actually in play.
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
