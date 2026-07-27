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

// This is much simpler than the similar module in locale.ts: that one exists
// because Paraglide's getLocale() is called synchronously, many times,
// from anywhere -- it needs a cache. Theme has no equivalent repeated-read
// caller anywhere in the *Svelte* app; applyTheme() below is just one DOM
// mutation, and CSS (src/styles/_tokens.scss's `[data-theme]` selectors)
// does everything else from there.
//
// STORAGE_KEY is genuinely load-bearing, though, not just a nice-to-have:
// index.html ships its own tiny inline (non-module, synchronous, blocking)
// script that reads this exact same localStorage key before any of this
// app's real JS runs at all, and applies data-theme immediately -- the
// only way to actually avoid a flash of the wrong theme on an explicit
// light/dark override, since waiting for sessionReady (a network request)
// can never complete before first paint no matter how fast it is. That
// inline script can't import this module (a module import would defer it
// past first paint, defeating the entire point), so the key is duplicated
// there as a literal string. Keep the two in sync by hand if this one ever
// changes.
//
// The backend (user_settings.theme via GET/PATCH /api/settings) is still
// the real source of truth either way -- this cache only exists to make
// first paint match what the backend will confirm a moment later. A
// stale/missing cache (a different account previously used in this same
// browser, or nothing cached yet) just means one frame at the wrong
// theme, corrected the instant sessionReady resolves and App.svelte's own
// applyTheme() call runs -- never a lasting state.
const STORAGE_KEY = "recueil-theme";

// "automatic" is represented by applying no `data-theme` attribute at all
// (and clearing any cached override), not by storing some literal
// "automatic" value -- that's what lets the plain `prefers-color-scheme`
// media query in _tokens.scss keep working unmodified, including
// reacting live to the OS's own theme changes with zero JS involvement.
// An explicit "light"/"dark" override sets the attribute (and the cache)
// and pins the theme regardless of what the OS prefers.
export function applyTheme(theme: string | null): void {
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
  } else {
    // Assigning undefined here would stringify to the literal attribute
    // value "undefined" (dataset's setter coerces via ToString, it
    // doesn't special-case undefined) -- delete is the only way to
    // actually remove data-theme and let the plain prefers-color-scheme
    // media query take back over.
    delete document.documentElement.dataset.theme;
  }

  // Storage can throw (private browsing in some browsers, an extension
  // blocking it, a full quota) -- theme still applies for this load via
  // the DOM mutation above regardless; only the "avoid a flash next load"
  // benefit is lost, which isn't worth surfacing as a user-facing error.
  try {
    if (theme === "light" || theme === "dark") {
      localStorage.setItem(STORAGE_KEY, theme);
    } else {
      localStorage.removeItem(STORAGE_KEY);
    }
  } catch {
    // See above.
  }
}
