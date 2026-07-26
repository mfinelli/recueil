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
  import { push } from "svelte-spa-router";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import { session } from "../lib/session.svelte";
  import { ApiError } from "../lib/api";
  import { m } from "../paraglide/messages";
  import PasswordInput from "../components/PasswordInput.svelte";

  // Password reset doesn't exist yet (CLI-only for now). Link stays in the
  // markup, gated behind this constant, so the layout doesn't need revisiting
  // later.
  const SHOW_FORGOT_PASSWORD = false;

  let username = $state("");
  let password = $state("");
  let submitting = $state(false);
  let error = $state<string | null>(null);

  async function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    error = null;
    submitting = true;
    try {
      await session.login(username, password);
      await push("/");
    } catch (err) {
      error = err instanceof ApiError ? err.message : m.login_error_generic();
    } finally {
      submitting = false;
    }
  }
</script>

<main class="screen">
  <form class="card" onsubmit={handleSubmit}>
    <h1 class="wordmark">recueil</h1>

    <label class="field-label" for="username">{m.common_username()}</label>
    <input
      id="username"
      type="text"
      autocomplete="username"
      bind:value={username}
      required
      disabled={submitting}
    />

    <div class="field-label-row">
      <label class="field-label" for="password">{m.common_password()}</label>
      {#if SHOW_FORGOT_PASSWORD}
        <a class="forgot-link" href="#/forgot-password"
          >{m.login_forgot_password()}</a
        >
      {/if}
    </div>
    <PasswordInput
      id="password"
      autocomplete="current-password"
      bind:value={password}
      disabled={submitting}
    />

    {#if error}
      <p class="error" role="alert">
        <AlertCircle size={15} />
        <span>{error}</span>
      </p>
    {/if}

    <button class="primary" type="submit" disabled={submitting}
      >{submitting ? m.login_signing_in() : m.login_sign_in()}</button
    >

    {#if session.openRegistration}
      <p class="alt-action">
        {m.login_register_prompt()}
        <a href="#/register">{m.login_register_link()}</a>
      </p>
    {/if}
  </form>
</main>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;

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

  .field-label {
    @include type.eyebrow;
    margin-top: 0.4rem;
  }

  .field-label-row {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
    margin-top: 0.4rem;

    .field-label {
      margin-top: 0;
    }
  }

  .forgot-link {
    font-size: 0.75rem;
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

  input {
    padding: 0.55rem 0.7rem;
    border: 1px solid var(--rule);
    border-radius: 4px;
    background: var(--paper);
    box-shadow: inset 0 1px 3px color-mix(in srgb, var(--ink) 10%, transparent);
    color: var(--ink);
    font: inherit;

    &:focus-visible {
      @include mix.focus-ring;
    }
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
    margin-top: 0.6rem;
    padding: 0.7rem;
    border: none;
    border-radius: 4px;
    background: var(--accent-success);
    color: var(--paper);
    font: inherit;
    font-weight: 600;
    cursor: pointer;

    &:disabled {
      opacity: 0.6;
      cursor: default;
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .alt-action {
    margin: 0.5rem 0 0;
    font-size: 0.875rem;
    color: var(--ink-muted);
    text-align: center;

    a {
      color: var(--accent);
      text-decoration: none;

      &:hover {
        text-decoration: underline;
      }

      &:focus-visible {
        @include mix.focus-ring;
      }
    }
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
