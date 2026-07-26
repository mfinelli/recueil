<!--
recueil: self-hosted webpage bookmarking and archiver
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
<!-- Open registration: only reachable at all when the server has
     enable_open_registration set (see lib/routes.ts's requireOpenRegistration
     guard, which redirects to /login otherwise regardless of what's typed
     into the URL bar). Same shape as Setup.svelte minus the bootstrap-token
     field -- self-hosted, no email verification, no email field on the user
     at all -- and lands the new member straight in the app on success, same
     as Setup does for the first admin. -->
<script lang="ts">
  import { push } from "svelte-spa-router";
  import { session } from "../lib/session.svelte";
  import { ApiError } from "../lib/api";
  import { m } from "../paraglide/messages";

  let username = $state("");
  let password = $state("");
  let confirmPassword = $state("");
  let submitting = $state(false);
  let error = $state<string | null>(null);

  async function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    if (password !== confirmPassword) {
      error = m.register_error_password_mismatch();
      return;
    }
    error = null;
    submitting = true;
    try {
      await session.register(username, password);
      await push("/");
    } catch (err) {
      error =
        err instanceof ApiError ? err.message : m.register_error_generic();
    } finally {
      submitting = false;
    }
  }
</script>

<main class="screen">
  <form class="card" onsubmit={handleSubmit}>
    <h1>recueil</h1>
    <p class="sub">{m.register_subtitle()}</p>

    <label for="username">{m.common_username()}</label>
    <input
      id="username"
      type="text"
      autocomplete="username"
      bind:value={username}
      required
      disabled={submitting}
    />

    <label for="password">{m.common_password()}</label>
    <input
      id="password"
      type="password"
      autocomplete="new-password"
      bind:value={password}
      required
      disabled={submitting}
    />

    <label for="confirm-password">{m.register_confirm_password()}</label>
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
      >{submitting
        ? m.register_creating()
        : m.register_create_account()}</button
    >

    <p class="alt-action">
      {m.register_login_prompt()}
      <a href="#/login">{m.register_login_link()}</a>
    </p>
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

  .alt-action {
    margin: 0.5rem 0 0;
    font-size: 0.875rem;
    color: var(--ink-muted);
    text-align: center;
  }
</style>
