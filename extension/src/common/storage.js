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

// Everything this extension needs to remember lives under one
// storage.local key, read/written as a single object rather than several
// independent keys -- the base URL and the bearer token are only ever
// meaningful together (a token from one recueil instance is meaningless
// against another's workerBaseURL), so there's no scenario where writing
// them independently would be correct; a single key just makes "both or
// neither" the only representable state.
//
// storage.local not storage.sync: sync would replicate a live bearer
// credential through the browser's account-sync mechanism (Firefox Sync /
// Chrome Sync) to every other device signed into that browser account -- a
// real, meaningful difference from "just picking a storage API," not a style
// choice.

import browser from "webextension-polyfill";

const STORAGE_KEY = "recueil:config";

/**
 * @typedef {Object} RecueilConfig
 * @property {string} workerBaseURL - no trailing slash, e.g. "https://recueil.example.com"
 * @property {string} token - device bearer token returned by POST /pair
 * @property {number} deviceId
 * @property {string} deviceName
 * @property {string} deviceType
 */

/** @returns {Promise<RecueilConfig|null>} */
export async function getConfig() {
  const stored = await browser.storage.local.get(STORAGE_KEY);
  return /** @type {RecueilConfig|null} */ (stored[STORAGE_KEY] ?? null);
}

/** @param {RecueilConfig} config */
export async function setConfig(config) {
  await browser.storage.local.set({ [STORAGE_KEY]: config });
}

export async function clearConfig() {
  await browser.storage.local.remove(STORAGE_KEY);
}
