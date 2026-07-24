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

// Paraglide's client-side custom strategies must be synchronous (see
// defineCustomClientStrategy's own docs) -- getLocale() can be called
// many times per render, so it can't itself make a network request. The
// real source of truth is the backend (user_settings.language via
// GET/PATCH /api/settings), so this module keeps a plain in-memory cache
// that session.svelte.ts's bootstrap populates once, in parallel with
// its existing /auth/me and /setup-status reads, before App.svelte ever
// mounts the Router -- by the time any component calls an m.*() message
// function, this cache is already settled.
//
// This is intentionally NOT a Svelte rune. setLocale() (from
// ../paraglide/runtime) reloads the whole page by default. Paraglide's own
// docs call that the right tradeoff for a "user picks a language once" flow,
// and it means no component anywhere needs to reactively re-render when the
// locale changes: a reload just re-runs bootstrap from scratch with the
// new value already persisted.
import { defineCustomClientStrategy } from "../paraglide/runtime";

let cachedLanguage: string | undefined;

// Called once by session.svelte.ts's bootstrap, whether or not a user is
// logged in (GET /api/settings 401s for a guest, same as /auth/me --
// that's fine, it just means the preferredLanguage/baseLocale strategies
// take over, exactly as intended for someone with no account yet).
export function setCachedLanguage(language: string | null | undefined): void {
  cachedLanguage = language ?? undefined;
}

defineCustomClientStrategy("custom-userSettings", {
  getLocale: () => cachedLanguage,
  // Paraglide calls this when something calls its own setLocale() with
  // this strategy active. It only updates the cache -- it does NOT PATCH
  // the backend itself, since Settings.svelte already owns persisting the
  // choice (its existing save flow, from the user_settings phase) before
  // it ever calls anything here. Keeping persistence and cache updates in
  // two different places would invite them drifting apart; this way
  // there's exactly one path that writes to the backend.
  setLocale: (locale) => {
    cachedLanguage = locale;
  },
});

// This is NOT a call through Paraglide's own exported setLocale().
// That function's signature only accepts a concrete Locale ("en" | "fr"),
// with no way to express "clear the override, fall back to
// preferredLanguage/baseLocale" -- there's no empty/undefined value in
// its type. Since custom-userSettings is the only configured strategy
// that persists anything at all (preferredLanguage reads
// navigator.languages fresh every time; baseLocale is a constant), this
// helper updates this module's own cache directly -- which is the entire
// thing Paraglide's setLocale would have delegated to here anyway -- and
// then reloads, the same end behavior Paraglide's own setLocale defaults
// to and that this project deliberately leans into (see this file's own
// top comment).
export function applyLanguageOverride(language: string | null): void {
  setCachedLanguage(language);
  window.location.reload();
}
