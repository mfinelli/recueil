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
<!-- First-run: creates the one admin account, gated by the bootstrap token
     printed to the backend's own logs on startup (not emailed, not shown
     anywhere in the UI -- the operator has to go look). Only reachable at
     all when GET /api/setup-status says needs_setup (see lib/routes.ts's
     requireSetup guard); once an account exists this route redirects away
     regardless of what's typed into the URL bar. -->
<script lang="ts">
  import { push } from "svelte-spa-router";
  import { session } from "../lib/session.svelte";
  import { ApiError } from "../lib/api";

  let bootstrapToken = $state("");
  let username = $state("");
  let password = $state("");
  let confirmPassword = $state("");
  let submitting = $state(false);
  let error = $state<string | null>(null);

  async function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    if (password !== confirmPassword) {
      error = "passwords do not match";
      return;
    }
    error = null;
    submitting = true;
    try {
      await session.completeSetup(bootstrapToken, username, password);
      await push("/");
    } catch (err) {
      error = err instanceof ApiError ? err.message : "setup failed";
    } finally {
      submitting = false;
    }
  }
</script>

<main class="screen">
  <form class="card" onsubmit={handleSubmit}>
    <h1>recueil</h1>
    <p class="sub">Create the first admin account to get started.</p>

    <label for="bootstrap-token">Bootstrap token</label>
    <input
      id="bootstrap-token"
      type="text"
      autocomplete="off"
      bind:value={bootstrapToken}
      required
      disabled={submitting}
    />
    <p class="hint">Printed to the backend's logs on startup.</p>

    <label for="username">Username</label>
    <input
      id="username"
      type="text"
      autocomplete="username"
      bind:value={username}
      required
      disabled={submitting}
    />

    <label for="password">Password</label>
    <input
      id="password"
      type="password"
      autocomplete="new-password"
      bind:value={password}
      required
      disabled={submitting}
    />

    <label for="confirm-password">Confirm password</label>
    <input
      id="confirm-password"
      type="password"
      autocomplete="new-password"
      bind:value={confirmPassword}
      required
      disabled={submitting}
    />

    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}

    <button type="submit" disabled={submitting}
      >{submitting ? "Creating…" : "Create admin account"}</button
    >
  </form>
</main>

<style lang="scss">
  .screen {
    display: grid;
    place-items: center;
    min-height: 100vh;
    padding: 1rem;
  }

  .card {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    width: 100%;
    max-width: 22rem;
    padding: 2rem;
    background: var(--paper-raised);
    border: 1px solid var(--rule);
    border-radius: 0.5rem;
  }

  h1 {
    margin: 0;
  }

  .sub {
    margin: 0 0 1rem;
    color: var(--ink-muted);
  }

  label {
    font-size: 0.875rem;
    font-weight: 600;
  }

  input {
    padding: 0.5rem 0.625rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper);
    color: var(--ink);
    font: inherit;
  }

  .hint {
    margin: 0 0 0.5rem;
    font-size: 0.75rem;
    color: var(--ink-muted);
  }

  .error {
    margin: 0;
    color: var(--accent);
    font-size: 0.875rem;
  }

  button {
    margin-top: 0.5rem;
    padding: 0.625rem;
    border: none;
    border-radius: 0.25rem;
    background: var(--accent-success);
    color: var(--paper);
    font: inherit;
    font-weight: 600;
    cursor: pointer;

    &:disabled {
      opacity: 0.6;
      cursor: default;
    }
  }
</style>
