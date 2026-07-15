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

// Message type constants shared between the background service worker and
// anything that sends it a runtime.sendMessage -- the capture-inject bundle
// (relaying fetches, see fetch-relay.js) today, the popup (triggering a
// direct capture, reading pairing/bookmark state) once that's built.
//
// A single shared source for these strings exists specifically so the
// sender and the background listener can never drift out of sync on the
// literal message shape -- they're bundled separately (capture-inject.js
// vs background.js are two independent esbuild entry points, see
// build.js), so there's no other mechanism (like a shared function
// signature) that would catch a typo'd message type at build time.

// Sent by the capture-inject bundle (running in a page's content-script
// world) to ask the background service worker to fetch a resource on its
// behalf -- see fetch-relay.js's doc comment for why this exists at all:
// single-file-core's own fetch defaults to the page's fetch, which is
// subject to the page's CORS restrictions, and the whole point of routing
// through the background context is to not be.
export const RELAY_FETCH = "recueil:relay-fetch";

// Sent by the popup (once it exists) to exchange a pairing token for a
// device bearer token -- see background/auth.js. The popup, not the
// background script, is responsible for calling browser.permissions
// .request() for the target origin *before* sending this message: that
// call needs to happen inside the same user-gesture-driven event handler
// as the pairing form's submit, not after crossing a runtime.sendMessage
// boundary into the background context, where the "was this triggered by
// a real user action" transient-activation state may not carry over
// reliably across browsers. See auth.js's own doc comment.
export const PAIR_DEVICE = "recueil:pair-device";

// Sent by the popup to read current pairing state (paired or not, and
// which instance/device if so) without needing to know storage.js's
// internal key shape itself.
export const GET_AUTH_STATE = "recueil:get-auth-state";

// Sent by the popup's "save this page" button to capture and upload
// whatever tab is currently active -- see background/capture.js.
export const CAPTURE_ACTIVE_TAB = "recueil:capture-active-tab";

// Sent by the popup to forget this device's locally-stored credential --
// see auth.js's unpair() doc comment for why this is local-only for now.
export const UNPAIR_DEVICE = "recueil:unpair-device";

export function isRecueilMessage(message) {
  return typeof message === "object" && message !== null && "type" in message;
}
