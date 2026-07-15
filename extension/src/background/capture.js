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
// This is the "user is already on the page, click save" path -- the
// queue-driven path (open a tab nobody has open, wait for it to load, run
// this same capture, close the tab) is real, separable work that reuses
// captureTab() below but isn't built yet; see DESIGN.md/the conversation
// that scoped Phase 5 for why the two are genuinely different beyond just
// "how the tab was obtained."
//
// Talks to two genuinely different endpoint families, on purpose:
// api-client.js's apiRequest() for the Worker's own JSON endpoints
// (upload-urls, complete), and a plain fetch() for the actual R2 PUTs --
// those go straight to R2's presigned URLs, which expect an exact
// x-amz-checksum-sha256 header bound into the signature, not our device
// bearer token; running them through apiRequest would attach an
// Authorization header R2 never asked for and doesn't want.

import browser from "webextension-polyfill";
import { getConfig } from "../common/storage.js";
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
  if (!tab || tab.id === undefined) {
    throw new Error("recueil: no active tab found");
  }
  return captureTab(tab.id, tab.url);
}

/**
 * @param {number} tabId
 * @param {string} url - the tab's current URL, passed separately rather
 *   than re-read from the tab, since the queue-driven path (not built yet)
 *   will want to pass the *enqueued* URL explicitly rather than trust
 *   whatever the tab navigated to.
 */
export async function captureTab(tabId, url) {
  const config = await getConfig();
  if (!config) {
    throw new NotPairedError();
  }

  const captured = await runCaptureInject(tabId);
  const htmlBytes = new TextEncoder().encode(captured.html);
  const contentSha256Html = await sha256Hex(htmlBytes);

  let faviconBytes = null;
  let faviconExt = null;
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

  const completion = await apiRequest(config, "/captures/complete", {
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
async function runCaptureInject(tabId) {
  await browser.scripting.executeScript({
    target: { tabId },
    files: [CAPTURE_INJECT_FILE],
  });
  const [{ result }] = await browser.scripting.executeScript({
    target: { tabId },
    func: () => globalThis.__recueilSingleFile.captureFrame(),
  });
  return result;
}

// R2's presigned PUT, not a Worker call -- see file doc comment for why
// this is a plain fetch() rather than going through apiRequest.
async function putToPresignedUrl(url, headers, body) {
  const response = await fetch(url, { method: "PUT", headers, body });
  if (!response.ok) {
    const text = await response.text().catch(() => "");
    throw new Error(
      `recueil: uploading to R2 failed: ${response.status} ${text}`,
    );
  }
}
