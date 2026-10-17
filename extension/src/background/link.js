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

// Save the current page as a link: its URL, its title and its favicon, with
// no archived HTML. The counterpart of capture.js's captureActiveTab/captureTab
// and deliberately parallel to it: the same "user is already on the page"
// shape, and the same queue handling -- a tab that queue.js's claimQueueItem
// opened is completed through the queue (here POST /queue/:id/complete-link,
// whose URL comes from the queue item, as for a queued capture) instead of the
// direct POST /links, and is then closed.
//
// A link carries only what a live tab can tell us, so nothing here is fetched by
// the backend later: the title and favicon are read in the page by link-inject.js
// (the same favicon selection a capture uses) and sent with the save.
//
// The favicon travels one of two ways. Up to the cap the Worker reports through
// GET /config (cached; see link-config.js) it rides inline in the save request
// as base64. Over it, it takes the capture flow's route: a presigned R2 PUT
// from POST /links/upload-urls, after which the save carries only the format.
// Nothing is ever dropped for being large. If the Worker refuses an inline
// favicon as too large anyway (its cap changed since this was cached), that
// 413 just sends it down the R2 route, which works at any size.
//
// The save's Worker calls go through guarded(), for the one thing a day-long
// config cache can get wrong: the feature having been turned off since. The
// Worker 404s every link route while it's off, so a 404 forces a config refresh,
// and if that now says links are off the error is the clear LinksDisabledError
// instead of a raw "404 Not Found" (a 404 that isn't that, e.g. a queue item that
// no longer exists, is rethrown unchanged).
//
// Best-effort around the page itself: if the tab can't be injected into
// (chrome://, the extension store, a PDF viewer), the link is still saved, with
// the title the browser already has and no favicon, rather than failing -- a
// link to a page we can't read is still a link.

import browser from "webextension-polyfill";
import {
  getConfig,
  getClaimedTabs,
  clearClaimedTab,
} from "../common/storage.js";
import { apiRequest, ApiError } from "../common/api-client.js";
import { sha256Hex } from "../common/hash.js";
import { bytesToBase64 } from "../common/base64.js";
import { t } from "../common/i18n.js";
import { NotPairedError, putToPresignedUrl } from "./capture.js";
import { getLinkFeatures } from "./link-config.js";

const LINK_INJECT_FILE = "link-inject.js";

export class LinksDisabledError extends Error {
  constructor() {
    super(t("linksDisabledError"));
    this.name = "LinksDisabledError";
  }
}

/**
 * Refreshes the cached feature config regardless of its age -- called once per
 * browser start, so a feature switched on or off since the last refresh shows up
 * without waiting out the day-long cache. Best-effort: a failure leaves the
 * cache as it was.
 */
export async function refreshLinkFeatures() {
  const config = await getConfig();
  if (!config) {
    return;
  }
  await getLinkFeatures(config, { force: true }).catch(() => {});
}

/**
 * Runs one Worker request of a link save, turning a 404 into LinksDisabledError
 * when a fresh look at the config says links are now off.
 * @template T
 * @param {import("../common/storage.js").RecueilConfig} config
 * @param {() => Promise<T>} request
 * @returns {Promise<T>}
 */
async function guarded(config, request) {
  try {
    return await request();
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      const features = await getLinkFeatures(config, { force: true }).catch(
        () => null,
      );
      if (features && !features.linksEnabled) {
        throw new LinksDisabledError();
      }
    }
    throw error;
  }
}

/**
 * Whether the popup should offer "Save as link": paired, and the Worker says
 * links are on. Any failure to find out (nothing cached and the Worker can't be
 * reached, a rejected token) just means "no" -- the popup has nothing useful to
 * say about it, and the capture button reports its own failures.
 * @returns {Promise<boolean>}
 */
export async function getLinksEnabled() {
  const config = await getConfig();
  if (!config) {
    return false;
  }
  try {
    return (await getLinkFeatures(config)).linksEnabled;
  } catch {
    return false;
  }
}

/** Saves whichever tab is currently active/focused as a link. */
export async function saveActiveTabAsLink() {
  const [tab] = await browser.tabs.query({
    active: true,
    currentWindow: true,
  });
  if (!tab || tab.id === undefined || tab.url === undefined) {
    throw new Error("recueil: no active tab found");
  }

  const claimedTabs = await getClaimedTabs();
  const queueItemId = claimedTabs[String(tab.id)];

  const result = await saveTabAsLink(tab.id, tab.url, tab.title, queueItemId);

  // Only on success, and only for a queue-driven tab, exactly as
  // captureActiveTab does: a failed attempt keeps its claim so the user can try
  // again on the same tab, and a direct save's tab is the user's own, never ours
  // to close.
  if (queueItemId) {
    await clearClaimedTab(tab.id);
    await browser.tabs.remove(tab.id).catch(() => {});
  }

  return result;
}

/**
 * @param {number} tabId
 * @param {string} url - the tab's current URL. Only sent for a direct save:
 *   POST /queue/:id/complete-link reads the URL from the queue item itself.
 * @param {string|undefined} tabTitle - the title the browser already has for
 *   the tab, used only if the page itself can't be read
 * @param {string} [queueItemId] - if set, this completes via
 *   POST /queue/:id/complete-link instead of POST /links
 */
export async function saveTabAsLink(tabId, url, tabTitle, queueItemId) {
  const config = await getConfig();
  if (!config) {
    throw new NotPairedError();
  }

  const features = await getLinkFeatures(config);
  if (!features.linksEnabled) {
    throw new LinksDisabledError();
  }

  const info = await readLinkInfo(tabId, tabTitle);
  const linkId = crypto.randomUUID();

  const path = queueItemId ? `/queue/${queueItemId}/complete-link` : "/links";
  const body = {
    link_id: linkId,
    ...(queueItemId ? {} : { url }),
    saved_at: new Date().toISOString(),
    ...(info.title ? { title: info.title } : {}),
  };

  const completion = await submit(config, path, body, info.favicon, features);
  return { linkId, title: info.title, ...completion };
}

/**
 * Sends the save, choosing the favicon's route (see the file doc comment).
 *
 * @param {import("../common/storage.js").RecueilConfig} config
 * @param {string} path
 * @param {Record<string, unknown>} body - everything but the favicon
 * @param {{bytes: Uint8Array<ArrayBuffer>, ext: string}|null} favicon
 * @param {import("./link-config.js").LinkFeatures} features
 */
async function submit(config, path, body, favicon, features) {
  if (!favicon) {
    return guarded(config, () =>
      apiRequest(config, path, { method: "POST", body }),
    );
  }

  if (favicon.bytes.length <= features.faviconInlineMaxBytes) {
    try {
      return await guarded(config, () =>
        apiRequest(config, path, {
          method: "POST",
          body: {
            ...body,
            favicon_ext: favicon.ext,
            favicon_data: bytesToBase64(favicon.bytes),
          },
        }),
      );
    } catch (error) {
      if (!(error instanceof ApiError) || error.status !== 413) {
        throw error;
      }
      // The Worker's cap is lower than the one this device cached. Refresh it
      // for next time (best-effort) and take the route that works at any size.
      await getLinkFeatures(config, { force: true }).catch(() => {});
    }
  }

  const sha256 = await sha256Hex(favicon.bytes);
  const uploadUrls = await guarded(config, () =>
    apiRequest(config, "/links/upload-urls", {
      method: "POST",
      body: {
        link_id: body.link_id,
        favicon_ext: favicon.ext,
        content_sha256_favicon: sha256,
      },
    }),
  );
  await putToPresignedUrl(
    uploadUrls.upload_url_favicon,
    uploadUrls.required_headers_favicon,
    favicon.bytes,
  );
  return guarded(config, () =>
    apiRequest(config, path, {
      method: "POST",
      body: { ...body, favicon_ext: favicon.ext },
    }),
  );
}

/**
 * Reads the title and favicon from the live tab, falling back to the title the
 * browser already has (and no favicon) if the page can't be injected into.
 *
 * @param {number} tabId
 * @param {string|undefined} tabTitle
 * @returns {Promise<{title: string|null, favicon: {bytes: Uint8Array<ArrayBuffer>, ext: string}|null}>}
 */
async function readLinkInfo(tabId, tabTitle) {
  try {
    await browser.scripting.executeScript({
      target: { tabId },
      files: [LINK_INJECT_FILE],
    });
    const [{ result }] = await browser.scripting.executeScript({
      target: { tabId },
      func: () => globalThis.__recueilLinkInfo.captureLinkInfo(),
    });
    const info =
      /** @type {import("../capture-inject/link-entry.js").LinkInfo} */ (
        result
      );
    return {
      title: cleanTitle(info.title),
      favicon: info.favicon
        ? {
            bytes: new Uint8Array(info.favicon.bytes),
            ext: info.favicon.ext,
          }
        : null,
    };
  } catch {
    return { title: cleanTitle(tabTitle), favicon: null };
  }
}

/** @param {string|undefined|null} title */
function cleanTitle(title) {
  const trimmed = (title ?? "").trim();
  return trimmed === "" ? null : trimmed;
}
