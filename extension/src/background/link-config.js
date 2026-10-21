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

// What the Worker says about the links feature, read and cached from GET
// /config. The extension only ever talks to the Worker, so this is how it
// learns whether to offer "Save as link" at all and how big a favicon the
// Worker will take inline.
//
// Cached so that opening the popup and saving a link don't each cost a Worker
// request to ask a question whose answer almost never changes -- the Worker's
// free tier is a real constraint. This is a setting an operator turns on once and
// then leaves alone, so a day is the freshness window. What staleness could cost
// is covered elsewhere rather than by a shorter window: the cache is refreshed on
// every browser start (index.js), and a save that the Worker refuses because the
// feature has since been turned off forces a refresh and reports that clearly
// (link.js).
//
// If the Worker can't be reached, a cached answer (however stale) is used rather
// than failing: a save would fail on its own if the Worker really is down, and
// a flaky moment shouldn't make the button vanish. With nothing cached the error
// propagates, and there is deliberately no default compiled in to fall back on:
// the favicon cap lives in the Worker alone, so the extension never carries its
// own copy to keep in step with it.

import { apiRequest, ApiAuthError, ApiError } from "../common/api-client.js";
import { getLinkFeatureCache, setLinkFeatureCache } from "../common/storage.js";

export const LINK_FEATURES_TTL_MS = 24 * 60 * 60 * 1000;

/**
 * @typedef {Object} LinkFeatures
 * @property {boolean} linksEnabled
 * @property {number} faviconInlineMaxBytes
 */

/**
 * @param {import("../common/storage.js").RecueilConfig} config
 * @param {{force?: boolean, now?: number}} [options] - force skips the
 *   freshness check (used after the Worker proves a cached cap stale)
 * @returns {Promise<LinkFeatures>}
 */
export async function getLinkFeatures(config, { force = false, now } = {}) {
  const cached = await getLinkFeatureCache();
  const usable = cached && cached.workerBaseURL === config.workerBaseURL;
  const currentTime = now ?? Date.now();

  if (
    usable &&
    !force &&
    currentTime - Date.parse(cached.fetchedAt) < LINK_FEATURES_TTL_MS
  ) {
    return fromCache(cached);
  }

  try {
    const body = /** @type {any} */ (await apiRequest(config, "/config"));
    const max = Number(body?.favicon_inline_max_bytes);
    return await remember(config, currentTime, {
      linksEnabled: body?.links_enabled === true,
      // Anything unusable means "send everything through R2", which always works.
      faviconInlineMaxBytes: Number.isFinite(max) && max > 0 ? max : 0,
    });
  } catch (error) {
    // A rejected token is the one failure no cache should paper over.
    if (error instanceof ApiAuthError) {
      throw error;
    }
    // A Worker that predates links has no /config at all: links are simply not
    // available there, which is an answer, not a failure (and worth caching, so
    // an old Worker isn't asked again on every popup open).
    if (error instanceof ApiError && error.status === 404) {
      return remember(config, currentTime, {
        linksEnabled: false,
        faviconInlineMaxBytes: 0,
      });
    }
    if (usable) {
      return fromCache(cached);
    }
    throw error;
  }
}

/**
 * @param {import("../common/storage.js").RecueilConfig} config
 * @param {number} fetchedAt
 * @param {LinkFeatures} features
 * @returns {Promise<LinkFeatures>}
 */
async function remember(config, fetchedAt, features) {
  await setLinkFeatureCache({
    workerBaseURL: config.workerBaseURL,
    ...features,
    fetchedAt: new Date(fetchedAt).toISOString(),
  });
  return features;
}

/**
 * @param {import("../common/storage.js").LinkFeatureCache} cache
 * @returns {LinkFeatures}
 */
function fromCache(cache) {
  return {
    linksEnabled: cache.linksEnabled,
    faviconInlineMaxBytes: cache.faviconInlineMaxBytes,
  };
}
