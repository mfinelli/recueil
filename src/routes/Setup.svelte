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
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import { session, reloadIntoLibrary } from "../lib/session.svelte";
  import { ApiError } from "../lib/api";
  import { m } from "../paraglide/messages";
  import PasswordInput from "../components/PasswordInput.svelte";

  let bootstrapToken = $state("");
  let username = $state("");
  let password = $state("");
  let confirmPassword = $state("");
  let submitting = $state(false);
  let error = $state<string | null>(null);

  async function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    if (password !== confirmPassword) {
      error = m.setup_error_password_mismatch();
      return;
    }
    error = null;
    submitting = true;
    try {
      await session.completeSetup(bootstrapToken, username, password);
      reloadIntoLibrary();
    } catch (err) {
      error = err instanceof ApiError ? err.message : m.setup_error_generic();
    } finally {
      submitting = false;
    }
  }
</script>

<main class="screen">
  <form class="card" onsubmit={handleSubmit}>
    <h1 class="wordmark">recueil</h1>
    <p class="sub">{m.setup_subtitle()}</p>

    <label class="field-label" for="bootstrap-token"
      >{m.setup_bootstrap_token()}</label
    >
    <input
      id="bootstrap-token"
      class="mono"
      type="text"
      autocomplete="off"
      placeholder="rcl_bootstrap_…"
      bind:value={bootstrapToken}
      required
      disabled={submitting}
    />
    <p class="hint">{m.setup_bootstrap_token_hint()}</p>

    <label class="field-label" for="username">{m.common_username()}</label>
    <input
      id="username"
      type="text"
      autocomplete="username"
      bind:value={username}
      required
      disabled={submitting}
    />

    <label class="field-label" for="password">{m.common_password()}</label>
    <PasswordInput
      id="password"
      autocomplete="new-password"
      bind:value={password}
      disabled={submitting}
    />

    <label class="field-label" for="confirm-password"
      >{m.setup_confirm_password()}</label
    >
    <PasswordInput
      id="confirm-password"
      autocomplete="new-password"
      bind:value={confirmPassword}
      disabled={submitting}
    />

    {#if error}
      <p class="error" role="alert">
        <AlertCircle size={15} />
        <span>{error}</span>
      </p>
    {/if}

    <button class="primary" type="submit" disabled={submitting}
      >{submitting ? m.setup_creating() : m.setup_create_account()}</button
    >
  </form>
</main>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;
  @use "../styles/components" as comp;

  .screen {
    display: grid;
    place-items: center;
    min-height: 100vh;
    padding: 1rem;
  }

  .card {
    display: flex;
    flex-direction: column;
    gap: 0.65rem;
    width: 100%;
    max-width: 22rem;
    padding: 2rem;
    @include mix.card-surface;
  }

  .wordmark {
    @include type.heading;
    font-size: 1.9rem;
    margin: 0 0 0.15rem;
  }

  .sub {
    margin: 0 0 0.75rem;
    color: var(--ink-muted);
    font-size: 0.9rem;
    line-height: 1.4;
  }

  .field-label {
    @include type.eyebrow;
    margin-top: 0.4rem;
  }

  input {
    @include comp.auth-input;
  }

  input.mono {
    @include type.data-mono;
    font-size: 0.85rem;
  }

  .hint {
    margin: 0.2rem 0 0.3rem;
    font-size: 0.75rem;
    color: var(--ink-muted);
  }

  .error {
    display: flex;
    align-items: flex-start;
    gap: 0.4rem;
    margin: 0;
    color: var(--accent);
    font-size: 0.85rem;
    line-height: 1.35;

    :global(svg) {
      flex: none;
      margin-top: 0.1rem;
    }
  }

  .primary {
    @include comp.primary-button;
  }

  @include mix.mobile {
    .card {
      max-width: 20rem;
      padding: 1.5rem;
    }

    .wordmark {
      font-size: 1.6rem;
    }
  }
</style>
