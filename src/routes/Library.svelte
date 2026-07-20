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
<!-- Still a placeholder for real library browsing (search, tag/collection
     filters, capture list -- GET /api/pages already exists and waits here)
     -- but no longer content-free: shows the logged-in user and a working
     logout, proving the auth loop (Setup/Login -> session -> guarded route)
     end to end, not just that routing itself works. -->
<script lang="ts">
  import { push } from "svelte-spa-router";
  import { session } from "../lib/session.svelte";

  async function handleLogout() {
    await session.logout();
    await push("/login");
  }
</script>

<main class="screen">
  <header>
    <h1>recueil</h1>
    {#if session.user}
      <div class="account">
        <span>{session.user.username}</span>
        <button onclick={handleLogout}>Sign out</button>
      </div>
    {/if}
  </header>
  <p class="sub">library — placeholder</p>
</main>

<style lang="scss">
  .screen {
    padding: 2rem;
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
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

  .sub {
    color: var(--ink-muted);
  }
</style>
