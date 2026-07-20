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

// Bundles "am I logged in" and "does this instance need first-run setup"
// into one module: both are read together, once, at app bootstrap, to
// decide which of Setup/Login/the real app to show first -- splitting them
// into two single-purpose stores would just mean coordinating two async
// reads instead of one for that one shared use.
import { apiFetch, apiJSON } from "./api";

export interface CurrentUser {
  id: number;
  username: string;
  role: string;
}

class SessionState {
  user = $state<CurrentUser | null>(null);
  needsSetup = $state(false);

  async login(username: string, password: string): Promise<void> {
    this.user = await apiJSON<CurrentUser>("/auth/login", {
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
// GET /auth/me and GET /setup-status run in parallel, not sequentially --
// they're independent reads with nothing for one to gate the other on.
// Tolerant of a failed fetch (backend unreachable, a transient network
// error): defaults to "not logged in, setup status unknown" rather than
// leaving sessionReady permanently rejected, which would strand the app
// on App.svelte's loading state forever with no way to recover without a
// manual reload.
async function bootstrap(): Promise<void> {
  const [meResult, statusResult] = await Promise.allSettled([
    apiFetch("/auth/me"),
    apiFetch("/setup-status"),
  ]);

  if (meResult.status === "fulfilled" && meResult.value.ok) {
    session.user = (await meResult.value.json()) as CurrentUser;
  }
  if (statusResult.status === "fulfilled" && statusResult.value.ok) {
    const body = (await statusResult.value.json()) as { needs_setup: boolean };
    session.needsSetup = body.needs_setup;
  }
}

export const sessionReady: Promise<void> = bootstrap();
