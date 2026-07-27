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

// Bundles "am I logged in", "does this instance need first-run setup", and
// "is open registration enabled" into one module: all three are read
// together, once, at app bootstrap, to decide which of Setup/Login/Register/
// the real app to show first -- splitting them into separate single-purpose
// stores would just mean coordinating multiple async reads instead of one
// for that one shared use.
import { apiFetch, apiJSON } from "./api";
import { setCachedLanguage } from "./locale";

export interface CurrentUser {
  id: number;
  username: string;
  role: string;
}

class SessionState {
  user = $state<CurrentUser | null>(null);
  needsSetup = $state(false);
  openRegistration = $state(false);
  // null means "automatic". Read once by App.svelte after sessionReady;
  // nothing else in the app needs to read this reactively, so it's a plain
  // field here rather than anything more elaborate.
  theme = $state<string | null>(null);

  async login(username: string, password: string): Promise<void> {
    this.user = await apiJSON<CurrentUser>("/auth/login", {
      method: "POST",
      body: { username, password },
    });
    this.needsSetup = false;
  }

  async register(username: string, password: string): Promise<void> {
    this.user = await apiJSON<CurrentUser>("/auth/register", {
      method: "POST",
      body: { username, password },
    });
    this.needsSetup = false;
  }

  async completeSetup(
    bootstrapToken: string,
    username: string,
    password: string,
  ): Promise<void> {
    this.user = await apiJSON<CurrentUser>("/setup", {
      method: "POST",
      body: { bootstrap_token: bootstrapToken, username, password },
    });
    this.needsSetup = false;
  }

  async logout(): Promise<void> {
    await apiFetch("/auth/logout", { method: "POST" });
    this.user = null;
  }
}

export const session = new SessionState();

// Bootstrap check, run once, kicked off at import time (see sessionReady
// below) rather than from a component's onMount -- App.svelte awaits it
// before ever rendering the Router, so every route's precondition can
// assume session.user/needsSetup already reflect reality by the time it
// runs, with no per-route "have we checked yet" bookkeeping of its own.
//
// GET /auth/me, GET /setup-status, and GET /settings all run in parallel,
// not sequentially -- three independent reads with nothing for one to gate
// another on. /settings feeds locale.ts's cache (see its own comment) so
// Paraglide's custom-userSettings strategy already has an answer by the
// time App.svelte mounts the Router and any component calls an m.*()
// message function, and populates session.theme for App.svelte's
// applyTheme() call at that same point. A guest (no session yet) gets a
// 401 here, same as /auth/me -- that's a normal, expected outcome (falls
// through to the preferredLanguage/baseLocale strategies, and to
// automatic/prefers-color-scheme for theme), not a load failure, so it's
// not distinguished from any other failure mode below; either way the
// cache/session.theme are simply left unset.
//
// Tolerant of any of the three failing outright (backend unreachable, a
// transient network error): Promise.allSettled means one rejection can't
// stop the others from being read, and bootstrap() itself never throws --
// worst case is "not logged in, setup status unknown, no locale
// override," not sessionReady left permanently rejected and the app
// stranded on App.svelte's loading state with no way to recover short of
// a manual reload.
async function bootstrap(): Promise<void> {
  const [meResult, statusResult, settingsResult] = await Promise.allSettled([
    apiFetch("/auth/me"),
    apiFetch("/setup-status"),
    apiFetch("/settings"),
  ]);

  if (meResult.status === "fulfilled" && meResult.value.ok) {
    session.user = (await meResult.value.json()) as CurrentUser;
  }
  if (statusResult.status === "fulfilled" && statusResult.value.ok) {
    const body = (await statusResult.value.json()) as {
      needs_setup: boolean;
      open_registration: boolean;
    };
    session.needsSetup = body.needs_setup;
    session.openRegistration = body.open_registration;
  }
  if (settingsResult.status === "fulfilled" && settingsResult.value.ok) {
    const body = (await settingsResult.value.json()) as {
      language: string | null;
      theme: string | null;
    };
    setCachedLanguage(body.language);
    session.theme = body.theme;
  }
}

export const sessionReady: Promise<void> = bootstrap();

// Called after a successful login/register/completeSetup, in place of an
// SPA-only push("/") -- bootstrap() above runs exactly once, at module-load
// time, before anyone's necessarily logged in yet, so session.theme and
// locale.ts's language cache both reflect whatever (if anything) was true
// *before* this sign-in, not the newly-authenticated user's  saved
// preferences. A plain client-side route change never re-runs bootstrap, so
// those two would silently stay wrong. A real page reload re-runs everything
// from scratch (the inline theme script in index.html, this file's
// bootstrap, and App.svelte's post-sessionReady reconciliation), the
// same mechanism Settings.svelte's language change already relies on (this
// just applies that same fix to the sign-in moment too, not only to changing
// the preference once already signed in).
export function reloadIntoLibrary(): void {
  window.location.hash = "/";
  window.location.reload();
}
