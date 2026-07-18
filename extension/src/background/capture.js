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

// Direct capture end to end: inject -> hash -> presign -> upload -> notify.
// This is the "user is already on the page, click save" path -- also now
// how a claimed queue item actually gets captured, once the user has
// solved whatever the page needed by hand: queue.js's claimQueueItem()
// opens a focused tab and records tabId -> queueItemId (storage.js), and
// captureActiveTab() below checks that record to decide whether this
// capture completes via POST /queue/:id/complete instead of its default
// POST /captures/complete. Everything else about the capture itself is
// identical either way -- there's no "queue mode" capture pipeline, only a
// different completion call at the very end.
//
// Talks to two genuinely different endpoint families, on purpose:
// api-client.js's apiRequest() for the Worker's own JSON endpoints
// (upload-urls, complete), and a plain fetch() for the actual R2 PUTs --
// those go straight to R2's presigned URLs, which expect an exact
// x-amz-checksum-sha256 header bound into the signature, not our device
// bearer token; running them through apiRequest would attach an
// Authorization header R2 never asked for and doesn't want.

import browser from "webextension-polyfill";
import {
  getConfig,
  getClaimedTabs,
  clearClaimedTab,
} from "../common/storage.js";
import { apiRequest } from "../common/api-client.js";
import { sha256Hex } from "../common/hash.js";

const CAPTURE_INJECT_FILE = "capture-inject.js";

export class NotPairedError extends Error {
  constructor() {
    super("recueil: this device is not paired with a recueil instance yet");
    this.name = "NotPairedError";
  }
}

/** Captures whichever tab is currently active/focused. */
export async function captureActiveTab() {
  const [tab] = await browser.tabs.query({
    active: true,
    currentWindow: true,
  });
  if (!tab || tab.id === undefined || tab.url === undefined) {
    throw new Error("recueil: no active tab found");
  }

  const claimedTabs = await getClaimedTabs();
  const queueItemId = claimedTabs[String(tab.id)];

  const result = await captureTab(tab.id, tab.url, queueItemId);

  // Only cleared on success -- a failed attempt (a transient network
  // error, say) shouldn't lose the tab's association with the queue item
  // it's fulfilling; the user should just be able to click "Save this
  // page" again on the same tab without needing to go back to the queue
  // list and re-claim (which would be redundant anyway, this device
  // already holds the claim).
  if (queueItemId && tab.id !== undefined) {
    await clearClaimedTab(tab.id);
  }

  return result;
}

/**
 * @param {number} tabId
 * @param {string} url - the tab's current URL, passed separately rather
 *   than re-read from the tab. Only actually used when queueItemId is
 *   unset (POST /captures/complete needs it explicitly; POST
 *   /queue/:id/complete reads the URL back off the queue_items row itself
 *   instead) -- still always passed in either way, so callers don't need
 *   to know which completion path applies.
 * @param {string} [queueItemId] - if set, this capture completes via
 *   POST /queue/:id/complete instead of POST /captures/complete -- see
 *   captureActiveTab above and queue.js's claimQueueItem for where this
 *   comes from.
 */
export async function captureTab(tabId, url, queueItemId) {
  const config = await getConfig();
  if (!config) {
    throw new NotPairedError();
  }

  const captured = await runCaptureInject(tabId);
  const htmlBytes = new TextEncoder().encode(captured.html);
  const contentSha256Html = await sha256Hex(htmlBytes);

  /** @type {Uint8Array<ArrayBuffer>|null} */
  let faviconBytes = null;
  /** @type {string|null} */
  let faviconExt = null;
  /** @type {string|null} */
  let contentSha256Favicon = null;
  if (captured.favicon) {
    faviconBytes = new Uint8Array(captured.favicon.bytes);
    faviconExt = captured.favicon.ext;
    contentSha256Favicon = await sha256Hex(faviconBytes);
  }

  const captureId = crypto.randomUUID();

  const uploadUrls = await apiRequest(config, "/captures/upload-urls", {
    method: "POST",
    body: {
      capture_id: captureId,
      content_sha256_html: contentSha256Html,
      ...(faviconBytes
        ? {
            favicon_ext: faviconExt,
            content_sha256_favicon: contentSha256Favicon,
          }
        : {}),
    },
  });

  await putToPresignedUrl(
    uploadUrls.upload_url_html,
    uploadUrls.required_headers_html,
    htmlBytes,
  );
  if (faviconBytes) {
    await putToPresignedUrl(
      uploadUrls.upload_url_favicon,
      uploadUrls.required_headers_favicon,
      faviconBytes,
    );
  }

  // POST /queue/:id/complete doesn't take url in its body at all -- it
  // reads it back off the queue_items row the Worker already has (see
  // terraform/index.js's handleCompleteQueueItem), unlike
  // POST /captures/complete, which has no queue_items row to fall back on
  // and needs the caller to supply it directly.
  const completion = queueItemId
    ? await apiRequest(config, `/queue/${queueItemId}/complete`, {
        method: "POST",
        body: {
          capture_id: captureId,
          captured_at: new Date().toISOString(),
          ...(faviconBytes ? { favicon_ext: faviconExt } : {}),
        },
      })
    : await apiRequest(config, "/captures/complete", {
        method: "POST",
        body: {
          capture_id: captureId,
          url,
          captured_at: new Date().toISOString(),
          ...(faviconBytes ? { favicon_ext: faviconExt } : {}),
        },
      });

  return { captureId, title: captured.title, ...completion };
}

// Two-step injection, deliberately: load the bundle (defines the global)
// via `files`, then invoke it via a separate, tiny, self-contained `func`
// call -- see capture-inject/bundle-entry.js's own doc comment for why a
// func-injected function can't itself import the bundle, only reference a
// global it already defined.
//
// target.allFrames: true on the *first* call only: every frame needs the
// bundle loaded so its own copy of single-file-core's frame-tree collector
// can serialize that frame's DOM when the top frame walks the tree (see
// bundle-entry.js's CAPTURE_OPTIONS.removeFrames and its doc comment).
// captureFrame() itself is invoked once, in the top frame only -- the
// second executeScript below has no allFrames, so it runs only in frameId
// 0. A subframe has no favicon of its own to select and calling
// getPageData() there would be redundant, not additive; its content
// reaches the top frame through the frame-tree collection, not a second
// captureFrame() call.
/**
 * @param {number} tabId
 * @returns {Promise<import("../capture-inject/bundle-entry.js").CapturedPage>}
 */
export async function runCaptureInject(tabId) {
  await browser.scripting.executeScript({
    target: { tabId, allFrames: true },
    files: [CAPTURE_INJECT_FILE],
  });
  const [{ result }] = await browser.scripting.executeScript({
    target: { tabId },
    func: () => globalThis.__recueilSingleFile.captureFrame(),
  });
  return /** @type {import("../capture-inject/bundle-entry.js").CapturedPage} */ (
    result
  );
}

// R2's presigned PUT, not a Worker call -- see file doc comment for why
// this is a plain fetch() rather than going through apiRequest.
/**
 * @param {string} url
 * @param {Record<string, string>} headers
 * @param {BufferSource} body
 */
async function putToPresignedUrl(url, headers, body) {
  let response;
  try {
    response = await fetch(url, { method: "PUT", headers, body });
  } catch (error) {
    // Same reasoning as api-client.js's apiRequest -- a raw fetch()
    // failure here throws a browser-generic message with zero indication
    // this was even an R2 upload, let alone which one (html vs favicon).
    // This is specifically the fetch most likely to fail from a missing
    // host permission: R2 lives on a different origin than the Worker
    // entirely (see popup.js's handlePairSubmit for why pairing requests
    // <all_urls>, not just the Worker's own origin).
    throw new Error(
      `recueil: network error uploading to R2 (${url}): ${error instanceof Error ? error.message : String(error)}`,
      { cause: error },
    );
  }
  if (!response.ok) {
    const text = await response.text().catch(() => "");
    throw new Error(
      `recueil: uploading to R2 failed: ${response.status} ${text}`,
    );
  }
}
