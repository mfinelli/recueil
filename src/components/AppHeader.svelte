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
  import { session } from "../lib/session.svelte";
  import { m } from "../paraglide/messages";

  async function handleLogout() {
    await session.logout();
    await push("/login");
  }
</script>

<header>
  <div class="brand">
    <a href="/" use:link>recueil</a>
    <nav>
      <a href="/" use:link>{m.nav_library()}</a>
      <a href="/collections" use:link>{m.nav_collections()}</a>
      <a href="/devices" use:link>{m.nav_devices()}</a>
      <a href="/queue" use:link>{m.nav_queue()}</a>
      <a href="/settings" use:link>{m.settings()}</a>
    </nav>
  </div>
  {#if session.user}
    <div class="account">
      <span>{session.user.username}</span>
      <button onclick={handleLogout}>{m.sign_out()}</button>
    </div>
  {/if}
</header>

<style lang="scss">
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }

  .brand {
    display: flex;
    align-items: baseline;
    gap: 1.25rem;

    > a {
      font-size: 1.25rem;
      font-weight: 700;
      color: inherit;
      text-decoration: none;
    }
  }

  nav {
    display: flex;
    gap: 0.875rem;

    a {
      color: var(--ink-muted);
      text-decoration: none;
      font-size: 0.875rem;

      &:hover {
        color: var(--ink);
      }
    }
  }

  .account {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    font-size: 0.875rem;
  }

  button {
    padding: 0.375rem 0.75rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper-raised);
    color: var(--ink);
    font: inherit;
    cursor: pointer;
  }
</style>
