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

// Every UI-string lookup in this extension goes through t() here, rather
// than calling browser.i18n.getMessage() directly from popup.js/background
// modules even though today this is a one-line delegation with nothing else
// going on.
//
// The native WebExtensions i18n API (_locales/<locale>/messages.json,
// browser.i18n.getMessage()) has no supported way to force a locale other
// than the browser's own UI language -- there's no "pass a locale"
// parameter. If this extension ever grows a manual language override the
// only way to implement it is for this module to stop delegating to
// browser.i18n.getMessage() and instead fetch a specific
// _locales/<lang>/messages.json itself and look keys up from that. Keeping
// every call site funneled through this one function is what keeps that a
// change confined to this file, not a rearchitecture of popup.js.

import browser from "webextension-polyfill";

/**
 * Looks up a message key in the current locale's messages.json (see
 * extension/_locales/). Throws on a missing/empty key rather than
 * returning the native API's own silent empty string, so a mistyped key or
 * a locale file that's fallen out of sync with the code fails loudly
 * during development instead of shipping a blank label.
 *
 * @param {string} key
 * @param {string|string[]} [substitutions]
 * @returns {string}
 */
export function t(key, substitutions) {
  const message = browser.i18n.getMessage(key, substitutions);
  if (!message) {
    throw new Error(`recueil: missing i18n message for key "${key}"`);
  }
  return message;
}

// Keep this in sync with extension/_locales/ -- there's no runtime API to
// list which locale subdirectories actually shipped (unlike enumerating
// files on disk at build time), so this is a hand-maintained mirror of it,
// same acknowledged limitation as the dashboard's own LANGUAGE_OPTIONS list
// (src/routes/Settings.svelte).
const SHIPPED_LOCALES = ["en", "fr"];

/**
 * The locale popup.html's own <html lang> should actually claim -- NOT
 * simply browser.i18n.getUILanguage() (the browser's raw UI language),
 * because that isn't necessarily the language t() is actually rendering.
 * browser.i18n.getMessage() silently falls back to default_locale ("en")
 * whenever the browser's UI language isn't one of this extension's own
 * shipped locales (e.g. a browser set to German sees English popup text,
 * since there's no German translation) -- claiming lang="de" over English
 * text would be actively wrong, not just imprecise, for anything that
 * reads the lang attribute (screen readers' pronunciation rules, browser
 * spell-check/translate prompts). Matching only the primary subtag (the
 * "fr" in "fr-CA") against SHIPPED_LOCALES is a close enough approximation
 * of browser.i18n's own real locale-negotiation algorithm for a
 * two-locale extension; it doesn't need to be exact.
 *
 * @returns {string}
 */
export function documentLanguage() {
  const uiLanguage = browser.i18n.getUILanguage();
  const primary = uiLanguage.split("-")[0];
  return SHIPPED_LOCALES.includes(primary) ? primary : "en";
}
