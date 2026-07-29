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
<!-- Rendered once at the App.svelte root, not per-route like AppHeader -- a
     footer belongs on every page including Login/Setup/Register, and the
     "stick to the bottom even on a short page" requirement is one flex-column
     layout at the root rather than the same trick repeated in every route's
     own markup. -->
<script lang="ts">
  import type { InfoResponse } from "../lib/types";

  const GITHUB_URL = "https://github.com/mfinelli/recueil";
  const HOMEPAGE_URL = "https://recueil.app";

  let info = $state<InfoResponse | null>(null);

  async function loadInfo() {
    try {
      // Not apiJSON/apiFetch -- both hardcode the /api prefix, and /info
      // is deliberately unprefixed (see InfoResponse's own comment in
      // lib/types.ts). Fails silently: version/commit is decorative
      // footer content, not worth surfacing an error state for.
      const res = await fetch("/info");
      if (res.ok) {
        info = (await res.json()) as InfoResponse;
      }
    } catch {
      // See above -- omit the badge, nothing else to do here.
    }
  }

  loadInfo();
</script>

<footer>
  <div class="about">
    <span class="mark">recueil</span>
    <span class="sep">·</span>
    <span>© 2026 Mario Finelli</span>
    <span class="sep">·</span>
    <span>AGPL-3.0</span>
  </div>
  <div class="links">
    {#if info}
      <span class="version">v{info.version} · {info.commit}</span>
    {/if}
    <a href={GITHUB_URL} target="_blank" rel="noopener noreferrer">GitHub</a>
    <a href={HOMEPAGE_URL} target="_blank" rel="noopener noreferrer"
      >recueil.app</a
    >
  </div>
</footer>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;

  footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 0.5rem 1.5rem;
    padding: 1rem 1.5rem;
    border-top: 1px dotted var(--rule);
    font-size: 0.78rem;
    color: var(--ink-muted);
  }

  .mark {
    @include type.heading;
    font-size: 0.9rem;
  }

  .sep {
    color: var(--rule);
  }

  .links {
    display: flex;
    align-items: center;
    gap: 1.25rem;
  }

  .version {
    @include type.data-mono;
    font-size: 0.72rem;
    opacity: 0.85;
  }

  a {
    color: var(--ink-muted);
    text-decoration: none;

    &:hover {
      color: var(--accent);
      text-decoration: underline;
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  @include mix.mobile {
    footer {
      flex-direction: column;
      justify-content: center;
      text-align: center;
    }

    // .links is its own flex row -- text-align on an ancestor doesn't
    // reach into a flex container's own item positioning, only
    // justify-content does. Without this, "GitHub"/"recueil.app" would
    // stay left-justified within an otherwise-centered footer.
    .links {
      justify-content: center;
    }
  }
</style>
