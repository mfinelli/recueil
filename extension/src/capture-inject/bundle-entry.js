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
// IMPORTANT CAVEAT: multi-frame capture (embedded iframes) is deliberately
// not implemented here yet -- removeFrames: true below means only the top
// document is captured. Being reintroduced as a sequence of isolated
// steps rather than all at once this time (an earlier attempt bundled
// this bundle's allFrames injection together with actually turning on
// frame-tree collection, and something in that combination broke even
// single-frame pages in a way that was hard to isolate). Right now: this
// bundle *is* already being injected into every frame
// (background/capture.js's runCaptureInject uses target.allFrames: true
// for that step), but removeFrames stays true here, so single-file-core's
// frame-tree collection itself never actually runs -- this step is purely
// testing whether the injection alone is safe.

import * as singlefile from "single-file-core/single-file.js";
import { relayFetch } from "./relay-fetch.js";
import { selectFavicon } from "./favicon.js";

const CAPTURE_OPTIONS = {
  removeFrames: true, // top document only for now -- see caveat above
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
    // frameFetch intentionally omitted: single-file-core's own default is
    // `frameFetch || fetch`, and there are no frames to fetch from yet
    // (see removeFrames above) -- nothing to wire up until multi-frame
    // capture lands.
    fetch: relayFetch,
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
