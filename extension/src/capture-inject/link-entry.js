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

// Bundled by build.js into its own small, dependency-free IIFE
// (link-inject.js) and injected via scripting.executeScript({files:
// ["link-inject.js"]) only into the tab the user is saving as a link. It reads
// the two things a link keeps from a live page -- its title and its favicon --
// and nothing else: no SingleFile, no frame walking, none of the capture
// bundle's weight, since a link has no archived HTML.
//
// The favicon is chosen by exactly the same selectFavicon the capture bundle
// uses, fetched through the same relay (relay-fetch.js, handled by
// background/fetch-relay.js), so a page and a link saved from the same tab
// get the same icon. Exposed as a global for the same reason bundle-entry.js
// is: a `files`-injected script can't be imported by the follow-up `func`
// call that invokes it, only referenced through a global.

import { relayFetch } from "./relay-fetch.js";
import { selectFavicon } from "./favicon.js";

/**
 * What captureLinkInfo returns, crossing back to background/link.js as the
 * result of a scripting.executeScript({func}) call (a structured clone, so the
 * favicon's bytes travel as a plain array, as in bundle-entry.js).
 * @typedef {Object} LinkInfo
 * @property {string} title
 * @property {{bytes: number[], ext: string}|null} favicon
 */

/** @returns {Promise<LinkInfo>} */
async function captureLinkInfo() {
  const favicon = await selectFavicon(relayFetch);
  return {
    title: document.title,
    favicon: favicon && {
      bytes: Array.from(new Uint8Array(favicon.bytes)),
      ext: favicon.ext,
    },
  };
}

globalThis.__recueilLinkInfo = { captureLinkInfo };
