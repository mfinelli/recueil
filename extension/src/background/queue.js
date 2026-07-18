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

// Keeps a cached copy of GET /queue (storage.js's QueueCache) fresh, and
// the toolbar badge in sync with it -- both entirely for display/awareness.
// This is NOT the authoritative source for whether an item is actually
// claimable: that check only ever happens live, via POST /queue/:id/claim,
// at the moment the user actually clicks an item. A queue item's real state
// can change at any time from another device entirely outside this extension's
// knowledge, so treating this cache as anything more than "roughly what's out
// there right now, good enough to show a list and a badge count" would be
// wrong.
//
// Refreshed from three places, explicitly not on every service-worker
// wake (which would mean an extra Worker round-trip on nearly every
// message this background handles, including ones with nothing to do with
// the queue): browser.runtime.onStartup/onInstalled (once per real browser
// start, not per SW wake), a recurring alarm (REFRESH_PERIOD_MINUTES, "once
// or twice a day" is plenty), and the popup's own manual refresh button
// (REFRESH_QUEUE_LIST, background/index.js).

import browser from "webextension-polyfill";
import { getConfig, setQueueCache, setClaimedTab } from "../common/storage.js";
import { apiRequest, ApiError } from "../common/api-client.js";
import { NotPairedError } from "./capture.js";

const REFRESH_ALARM_NAME = "recueil-queue-refresh";
const REFRESH_PERIOD_MINUTES = 360; // 6 hours
const BADGE_BACKGROUND_COLOR = "#d32f2f";

/**
 * @returns {Promise<import("../common/storage.js").QueueCache>}
 */
export async function refreshQueueList() {
  const config = await getConfig();
  if (!config) {
    // Can legitimately run unattended (the alarm, or onStartup) long
    // before pairing ever happens -- not an error case, just nothing to
    // refresh yet.
    const empty = { items: [], fetchedAt: new Date().toISOString() };
    await setQueueCache(empty);
    await updateBadge(empty.items);
    return empty;
  }

  const response =
    /** @type {{items: import("../common/storage.js").QueueCacheItem[]}} */ (
      await apiRequest(config, "/queue")
    );
  const cache = {
    items: response?.items ?? [],
    fetchedAt: new Date().toISOString(),
  };
  await setQueueCache(cache);
  await updateBadge(cache.items);
  return cache;
}

/** @param {import("../common/storage.js").QueueCacheItem[]} items */
async function updateBadge(items) {
  const count = items.length;
  await browser.action.setBadgeText({ text: count > 0 ? String(count) : "" });
  if (count > 0) {
    await browser.action.setBadgeBackgroundColor({
      color: BADGE_BACKGROUND_COLOR,
    });
  }
}

/**
 * Sets up the recurring refresh alarm and its listener. Idempotent to call
 * on every background startup (alarms.create with the same name just
 * resets the existing one rather than creating a duplicate), matching how
 * registerFetchRelay()/registerFrameTreeRelay() already work the same way
 * in background/index.js.
 */
export function registerQueueRefreshAlarm() {
  browser.alarms.create(REFRESH_ALARM_NAME, {
    periodInMinutes: REFRESH_PERIOD_MINUTES,
  });
  browser.alarms.onAlarm.addListener((alarm) => {
    if (alarm.name === REFRESH_ALARM_NAME) {
      // Best-effort: a failed background refresh (offline, instance
      // temporarily down) isn't worth surfacing anywhere, the next alarm
      // or manual refresh will just try again.
      refreshQueueList().catch(() => {});
    }
  });
}

/**
 * Sends the real, live claim request (POST /queue/:id/claim) for one
 * queue item -- the cached list this same module refreshes is never
 * authoritative on its own, this is the actual lock check, at the moment
 * the user actually wants to work on it. On success, opens a new, focused
 * tab for the user to handle whatever the page needs entirely by hand (no
 * detection, no automation attempted -- see DESIGN.md's queue-driven
 * capture writeup for why) and tracks tabId -> itemId so the existing
 * "Save this page" button knows to complete this one via
 * POST /queue/:id/complete instead of its usual POST /captures/complete
 * (capture.js's captureActiveTab).
 *
 * @param {string} itemId
 * @returns {Promise<{id: string, url: string}>}
 */
export async function claimQueueItem(itemId) {
  const config = await getConfig();
  if (!config) {
    throw new NotPairedError();
  }

  /** @type {{id: string, url: string}} */
  let claimed;
  try {
    claimed = /** @type {{id: string, url: string}} */ (
      await apiRequest(config, `/queue/${itemId}/claim`, { method: "POST" })
    );
  } catch (error) {
    // Translated here, before crossing back to the popup via
    // runtime.sendMessage -- a custom property like ApiError's .status
    // isn't reliably preserved across that boundary (only .message
    // reliably is), so the friendly message has to be fully baked in on
    // this side, not reconstructed from a status code the popup might
    // never actually see.
    throw new Error(describeClaimFailure(error), { cause: error });
  }

  const tab = await browser.tabs.create({ url: claimed.url, active: true });
  if (tab.id !== undefined) {
    await setClaimedTab(tab.id, itemId);
  }

  return claimed;
}

/** @param {unknown} error */
function describeClaimFailure(error) {
  if (error instanceof ApiError) {
    if (error.status === 409) {
      return "recueil: this item is already being worked on from another device";
    }
    if (error.status === 410) {
      return "recueil: this item has already been captured (or permanently failed)";
    }
    if (error.status === 404) {
      return "recueil: this item no longer exists";
    }
  }
  return error instanceof Error ? error.message : String(error);
}
