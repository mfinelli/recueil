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

// single-file-core (see capture-inject/bundle-entry.js) needs to fetch
// every resource a captured page references -- images, fonts, stylesheets,
// anything not already inlined -- in order to embed them into the single
// HTML file it produces. Left to its own defaults, it just uses the page's
// own fetch, which is subject to the page's own CORS restrictions: a
// content-script-world fetch() is still the *page's* fetch, so a
// cross-origin image the page itself couldn't read the bytes of (only
// display it) can't be captured that way either.
//
// The background/service-worker context doesn't have that restriction --
// an extension fetch from a context covered by host_permissions bypasses
// CORS entirely, which is *why* it needs those permissions granted in the
// first place (see manifest.base.json's optional_host_permissions and
// wherever that gets requested at capture time). So: the capture-inject
// bundle's fetch/frameFetch override (see bundle-entry.js's relayFetch)
// sends the actual network request here via runtime.sendMessage, and this
// listener is what really calls fetch(), in a context where it isn't
// CORS-restricted, then ships the bytes back.
//
// NOTE: the CORS-bypass behavior this whole relay exists for is asserted
// here from documented WebExtension platform behavior and SingleFile's own
// precedent (its own manifest requests a similarly broad host permission
// for the same reason -- see its faq.md), not from having tested it
// ourselves against a real browser yet. Worth confirming empirically once
// this is actually loaded somewhere real.

import browser from "webextension-polyfill";
import { RELAY_FETCH } from "../common/messages.js";

export function registerFetchRelay() {
  // message is genuinely untyped at this boundary -- anything any script
  // in the extension sends via runtime.sendMessage arrives here, not just
  // RELAY_FETCH messages (see the type-narrowing check right below), so
  // `any` here is the honest type, not a shortcut around one.
  browser.runtime.onMessage.addListener((/** @type {any} */ message) => {
    if (!message || message.type !== RELAY_FETCH) {
      // Not ours -- returning undefined (rather than a promise) tells the
      // WebExtension messaging system this listener isn't handling it, so
      // another listener registered on the same runtime.onMessage event
      // gets a chance to.
      return undefined;
    }
    return handleRelayFetch(message);
  });
}

/**
 * @param {{url: string, init?: {method?: string, headers?: Record<string,string>, referrer?: string}}} message
 */
async function handleRelayFetch({ url, init }) {
  try {
    const response = await fetch(url, init);
    const body = await response.arrayBuffer();

    /** @type {Record<string, string>} */
    const headers = {};
    for (const [key, value] of response.headers.entries()) {
      headers[key] = value;
    }

    return {
      ok: true,
      // response.url reflects the final URL after any redirects --
      // preserved explicitly since a manually-reconstructed Response-like
      // object on the receiving end (see bundle-entry.js) can't recover
      // this any other way; a real `new Response(...)` always has an
      // empty `.url`, it's not settable via the constructor.
      url: response.url,
      status: response.status,
      statusText: response.statusText,
      headers,
      // ArrayBuffer is structured-clonable across runtime messaging, so
      // the bytes cross as-is -- no base64 round-trip needed.
      body,
    };
  } catch (error) {
    return {
      ok: false,
      error: error instanceof Error ? error.message : String(error),
    };
  }
}
