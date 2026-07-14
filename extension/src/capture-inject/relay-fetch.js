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

// The other half of background/fetch-relay.js -- see that file's doc
// comment for why this needs to exist at all. This is passed to
// single-file-core as its `fetch`/`frameFetch` init option (see
// bundle-entry.js), so its signature has to match what core/util.js
// actually calls: fetch(url, { headers, referrer, frameId }) and reads
// back a Response-shaped object (.status, .url, .headers.get(...),
// .arrayBuffer()).

import browser from "webextension-polyfill";
import { RELAY_FETCH } from "../common/messages.js";

/**
 * @param {string} url
 * @param {{method?: string, headers?: HeadersInit, referrer?: string}} [init]
 * @returns {Promise<{status: number, statusText: string, url: string, headers: {get(name: string): string|null}, arrayBuffer(): Promise<ArrayBuffer>}>}
 */
export async function relayFetch(url, init = {}) {
  const response = await browser.runtime.sendMessage({
    type: RELAY_FETCH,
    url,
    init: {
      method: init.method,
      headers: normalizeHeaders(init.headers),
      referrer: init.referrer,
    },
  });

  if (!response || !response.ok) {
    throw new Error(
      `recueil: relayed fetch failed for ${url}: ${response && response.error}`,
    );
  }

  return {
    status: response.status,
    statusText: response.statusText,
    url: response.url,
    headers: {
      get(name) {
        return response.headers[String(name).toLowerCase()] ?? null;
      },
    },
    async arrayBuffer() {
      return response.body;
    },
  };
}

// A plain, structured-clonable {[name]: value} object -- init.headers as
// single-file-core supplies it is already a plain object today, but
// normalizing here means this doesn't silently break if that ever becomes
// a real Headers instance instead, which wouldn't survive
// runtime.sendMessage's structured clone with its case-insensitive
// get/set semantics intact.
function normalizeHeaders(headers) {
  if (!headers) {
    return undefined;
  }
  if (headers instanceof Headers) {
    return Object.fromEntries(headers.entries());
  }
  return headers;
}
