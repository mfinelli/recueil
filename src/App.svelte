<!--
recueil: self-hosted webpage bookmarker and archiver
Copyright © 2026 Mario Finelli

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
-->
<script lang="ts">
  import Router from "svelte-spa-router";
  import { setLucideProps } from "@lucide/svelte";
  import routes from "./lib/routes";
  import { session, sessionReady } from "./lib/session.svelte";
  import { getLocale, getTextDirection } from "./paraglide/runtime";
  import { applyTheme } from "./lib/theme";

  // One default for every icon on the dashboard (18px, 2px stroke) via
  // @lucide/svelte's own context API -- individual call sites still pass
  // their own `size`/`strokeWidth` when they need something different
  // (e.g. AppHeader's smaller sign-out icon), this just avoids repeating
  // the common case everywhere. setContext (which this wraps) must run
  // during component initialization, hence top-level here rather than in
  // an effect/onMount.
  setLucideProps({ size: 18, strokeWidth: 2 });

  // index.html ships a static lang="en" fallback (see its own comment) --
  // this is the first point where the real, resolved locale is known:
  // sessionReady only resolves once session.svelte.ts's bootstrap has
  // populated locale.ts's cache from GET /settings, so getLocale() here
  // already reflects the custom-userSettings/preferredLanguage/baseLocale
  // strategy chain, not just the static fallback.
  //
  // applyTheme() here is NOT what prevents a flash of the wrong theme --
  // that's index.html's inline blocking script, which already applies
  // a cached override (if any) before this file, or any of this app's
  // real JS, ever runs. This call is the reconciliation step: it
  // overwrites whatever that cache guessed with the backend's actual,
  // authoritative value (session.theme, populated by the same GET
  // /settings bootstrap read as the locale above) and refreshes the
  // cache to match -- covering a stale/missing cache (a different
  // account previously used in this browser, first visit ever, the
  // preference changed on another device) without that case lingering
  // past this one reconciliation.
  async function applyDocumentPreferences() {
    await sessionReady;
    document.documentElement.lang = getLocale();
    document.documentElement.dir = getTextDirection();
    applyTheme(session.theme);
  }
  const documentPreferencesReady = applyDocumentPreferences();
</script>

{#await Promise.all([sessionReady, documentPreferencesReady])}
  <main class="boot">
    <p>Loading…</p>
  </main>
{:then}
  <Router {routes} />
{/await}

<style lang="scss">
  .boot {
    display: grid;
    place-items: center;
    min-height: 100vh;
    color: var(--ink-muted);
  }
</style>
