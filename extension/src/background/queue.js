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
import { getConfig, setQueueCache } from "../common/storage.js";
import { apiRequest } from "../common/api-client.js";

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
