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
 * The shape background/fetch-relay.js's handleRelayFetch actually returns
 * -- webextension-polyfill's own types have no way to know this (
 * runtime.sendMessage's return type is necessarily generic, since it has
 * no idea what a particular listener replies with), so this is the single
 * place that connects the two sides' shapes for the type checker.
 * @typedef {Object} RelayFetchResponse
 * @property {boolean} ok
 * @property {string} [error]
 * @property {number} [status]
 * @property {string} [statusText]
 * @property {string} [url]
 * @property {Record<string, string>} [headers]
 * @property {ArrayBuffer} [body]
 */

/**
 * relayFetch's own return shape -- exported so other modules that accept
 * "a fetch-like function" as a parameter (favicon.js, bundle-entry.js) can
 * reference the same type rather than each redeclaring an equivalent
 * shape that could quietly drift out of sync with this one.
 * @typedef {Object} FetchLikeResponse
 * @property {number} status
 * @property {string} statusText
 * @property {string} url
 * @property {{get(name: string): string|null}} headers
 * @property {() => Promise<ArrayBuffer>} arrayBuffer
 */

/**
 * @typedef {(url: string, init?: {method?: string, headers?: HeadersInit, referrer?: string}) => Promise<FetchLikeResponse>} FetchLike
 */

/**
 * @param {string} url
 * @param {{method?: string, headers?: HeadersInit, referrer?: string}} [init]
 * @returns {Promise<FetchLikeResponse>}
 */
export async function relayFetch(url, init = {}) {
  const response = /** @type {RelayFetchResponse} */ (
    await browser.runtime.sendMessage({
      type: RELAY_FETCH,
      url,
      init: {
        method: init.method,
        headers: normalizeHeaders(init.headers),
        referrer: init.referrer,
      },
    })
  );

  if (!response || !response.ok) {
    throw new Error(
      `recueil: relayed fetch failed for ${url}: ${response && response.error}`,
    );
  }

  return {
    status: /** @type {number} */ (response.status),
    statusText: /** @type {string} */ (response.statusText),
    url: /** @type {string} */ (response.url),
    headers: {
      /** @param {string} name */
      get(name) {
        return response.headers?.[name.toLowerCase()] ?? null;
      },
    },
    async arrayBuffer() {
      return /** @type {ArrayBuffer} */ (response.body);
    },
  };
}

// A plain, structured-clonable {[name]: value} object -- init.headers as
// single-file-core supplies it is already a plain object today, but
// normalizing here means this doesn't silently break if that ever becomes
// a real Headers instance instead, which wouldn't survive
// runtime.sendMessage's structured clone with its case-insensitive
// get/set semantics intact.
/**
 * @param {HeadersInit} [headers]
 * @returns {Record<string, string>|undefined}
 */
function normalizeHeaders(headers) {
  if (!headers) {
    return undefined;
  }
  if (headers instanceof Headers) {
    return Object.fromEntries(headers.entries());
  }
  if (Array.isArray(headers)) {
    return Object.fromEntries(headers);
  }
  return headers;
}
