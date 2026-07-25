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
  import { link, push } from "svelte-spa-router";
  import active from "svelte-spa-router/active";
  import LogOut from "@lucide/svelte/icons/log-out";
  import { session } from "../lib/session.svelte";
  import { m } from "../paraglide/messages";

  // Mobile nav is a disclosure, not a second breakpoint-specific markup
  // tree -- same links, same DOM, just toggled visibility so there's one
  // source of truth for the six routes rather than a duplicated mobile
  // menu to keep in sync.
  let navOpen = $state(false);

  function closeNav() {
    navOpen = false;
  }

  async function handleLogout() {
    await session.logout();
    await push("/login");
  }

  // Library/Collections/Tags each have a screen reachable only by drilling
  // in from their own nav item (PageDetail/CaptureReader from Library,
  // CollectionDetail from Collections, TagDetail from Tags -- see
  // routes.ts), so their nav link should stay active on those drill-down
  // routes too, not just the exact path. Devices/Queue/Settings have no
  // such nested routes, so the default (element's own href, exact match)
  // is already correct for them.
  const libraryActive = { path: /^\/($|pages\/|captures\/)/ };
  const collectionsActive = { path: /^\/collections(\/.*)?$/ };
  const tagsActive = { path: /^\/tags(\/.*)?$/ };
</script>

<header>
  <div class="bar">
    <a class="wordmark" href="/" use:link onclick={closeNav}>recueil</a>
    <button
      class="nav-toggle"
      aria-expanded={navOpen}
      aria-controls="primary-nav"
      aria-label={m.nav_toggle()}
      onclick={() => (navOpen = !navOpen)}
    >
      <span class="hamburger" class:open={navOpen} aria-hidden="true">
        <span class="bar"></span>
        <span class="bar"></span>
        <span class="bar"></span>
      </span>
    </button>
  </div>

  <div class="collapsible" class:open={navOpen} id="primary-nav">
    <nav>
      <a href="/" use:link use:active={libraryActive} onclick={closeNav}
        >{m.nav_library()}</a
      >
      <a
        href="/collections"
        use:link
        use:active={collectionsActive}
        onclick={closeNav}>{m.nav_collections()}</a
      >
      <a href="/tags" use:link use:active={tagsActive} onclick={closeNav}
        >{m.nav_tags()}</a
      >
      <a href="/devices" use:link use:active onclick={closeNav}
        >{m.nav_devices()}</a
      >
      <a href="/queue" use:link use:active onclick={closeNav}>{m.nav_queue()}</a
      >
      <a href="/settings" use:link use:active onclick={closeNav}
        >{m.settings()}</a
      >
    </nav>
    {#if session.user}
      <div class="account">
        <span class="username">{session.user.username}</span>
        <button
          class="icon-btn"
          aria-label={m.sign_out()}
          onclick={handleLogout}
        >
          <LogOut size={16} />
        </button>
      </div>
    {/if}
  </div>
</header>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;

  header {
    border-bottom: 1px solid var(--rule);
    margin-bottom: 1.5rem;
    padding-bottom: 0.75rem;
  }

  .bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .wordmark {
    @include type.heading;
    font-size: 1.4rem;
    color: inherit;
    text-decoration: none;
  }

  // Hidden by default -- only shown once .collapsible starts stacking
  // under the header's own collapse point below.
  .nav-toggle {
    display: none;
    align-items: center;
    justify-content: center;
    width: 2.25rem;
    height: 2.25rem;
    padding: 0;
    @include mix.card-surface;
    color: var(--ink);
    cursor: pointer;

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  // Hand-built rather than swapping between two @lucide/svelte icons -- the
  // classic 3-bars-morph-to-X effect needs both states built from the same
  // three shapes so they can animate into each other; two unrelated icon
  // glyphs can only cross-fade as a pair, not morph line-by-line.
  //
  // Sized to match the 18px/2px global icon defaults set via
  // setLucideProps in App.svelte, so it reads as the same weight as every
  // other icon even though it isn't one.
  .hamburger {
    position: relative;
    display: inline-block;
    width: 18px;
    height: 14px;
  }

  .bar {
    position: absolute;
    left: 0;
    width: 100%;
    height: 2px;
    border-radius: 1px;
    background: currentColor;
    transition:
      top 0.2s ease,
      transform 0.2s ease,
      opacity 0.2s ease;
  }

  .bar:nth-child(1) {
    top: 0;
  }
  .bar:nth-child(2) {
    top: 6px;
  }
  .bar:nth-child(3) {
    top: 12px;
  }

  .hamburger.open {
    .bar:nth-child(1) {
      top: 6px;
      transform: rotate(45deg);
    }
    .bar:nth-child(2) {
      opacity: 0;
    }
    .bar:nth-child(3) {
      top: 6px;
      transform: rotate(-45deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .bar {
      transition: none;
    }
  }

  .collapsible {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-top: 0.75rem;
  }

  nav {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem 1.125rem;

    a {
      @include type.eyebrow;
      color: var(--ink-muted);
      text-decoration: none;
      padding-bottom: 0.35rem;
      position: relative;

      &:hover {
        color: var(--ink);
      }

      // Applied by svelte-spa-router/active's `use:active` action -- see
      // libraryActive/collectionsActive/tagsActive above. :global() because
      // the class is added by that action at runtime, not present in this
      // component's own template, so Svelte's scoped-CSS analysis can't see
      // it's used without this.
      &:global(.active) {
        color: var(--accent);

        &::after {
          content: "";
          position: absolute;
          left: 0;
          right: 0;
          bottom: 0;
          height: 2px;
          background: var(--accent);
        }
      }
    }
  }

  .account {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.875rem;
    white-space: nowrap;
  }

  .username {
    @include type.data-mono;
    color: var(--ink-muted);
  }

  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.9rem;
    height: 1.9rem;
    padding: 0;
    border: none;
    border-radius: 50%;
    background: transparent;
    color: var(--ink-muted);
    cursor: pointer;

    &:hover {
      color: var(--accent);
      background: var(--paper-raised);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  @include mix.header-collapse {
    .nav-toggle {
      display: inline-flex;
    }

    .collapsible {
      display: none;
      flex-direction: column;
      align-items: stretch;
      gap: 0.875rem;

      &.open {
        display: flex;
      }
    }

    nav {
      flex-direction: column;
      gap: 0.75rem;
    }

    .account {
      justify-content: flex-end;
    }
  }
</style>
