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
<!-- LANGUAGE_OPTIONS is a small hardcoded list, not fetched from the
     backend -- there's no server-side registry of "languages the
     dashboard supports" (unlike the extension's own _locales/
     directories, which the browser can enumerate on its own). The
     "English"/"Français" labels are deliberately left as plain literals,
     not run through m.*() -- a language picker conventionally shows each
     language's own autonym regardless of the current UI language (so a
     French-reading user still sees "English" as "English", not a
     translation of it), so these two are invariant by design, not
     untranslated by oversight. -->
<script lang="ts">
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type { UserSettings } from "../lib/types";
  import { m } from "../paraglide/messages";
  import { applyLanguageOverride } from "../lib/locale";

  const LANGUAGE_OPTIONS: { value: string; label: string }[] = [
    { value: "", label: m.language_option_automatic() },
    { value: "en", label: "English" },
    { value: "fr", label: "Français" },
  ];

  let language = $state("");
  let loading = $state(true);
  let saving = $state(false);
  let loadError = $state<string | null>(null);
  let saveError = $state<string | null>(null);
  let saved = $state(false);

  async function loadSettings() {
    loading = true;
    try {
      const res = await apiJSON<UserSettings>("/settings");
      language = res.language ?? "";
    } catch (err) {
      loadError =
        err instanceof ApiError ? err.message : m.settings_load_error();
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    loadSettings();
  });

  async function handleChange() {
    saving = true;
    saveError = null;
    saved = false;
    try {
      const res = await apiJSON<UserSettings>("/settings", {
        method: "PATCH",
        body: { language },
      });
      language = res.language ?? "";
      saved = true;
      setTimeout(() => {
        saved = false;
      }, 2000);
      // Persistence to the backend is already done above -- this only
      // makes Paraglide itself pick up the change on reload. See
      // locale.ts's own comment for why this goes through
      // applyLanguageOverride() rather than Paraglide's own exported
      // setLocale() (which has no way to express "clear the override").
      applyLanguageOverride(language || null);
    } catch (err) {
      saveError =
        err instanceof ApiError ? err.message : m.settings_save_error();
    } finally {
      saving = false;
    }
  }
</script>

<main class="screen">
  <AppHeader />
  <h1>{m.settings()}</h1>

  {#if loadError}
    <p class="status error" role="alert">{loadError}</p>
  {/if}

  <section>
    <h2>{m.settings_language_heading()}</h2>
    <p class="hint">
      {m.settings_language_hint()}
    </p>
    {#if loading}
      <p class="status">{m.settings_loading()}</p>
    {:else}
      <select bind:value={language} onchange={handleChange} disabled={saving}>
        {#each LANGUAGE_OPTIONS as option (option.value)}
          <option value={option.value}>{option.label}</option>
        {/each}
      </select>
      {#if saved}
        <span class="status success">{m.settings_saved()}</span>
      {/if}
      {#if saveError}
        <p class="status error" role="alert">{saveError}</p>
      {/if}
    {/if}
  </section>
</main>

<style lang="scss">
  .screen {
    max-width: 48rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  h1 {
    margin: 0 0 1rem;
  }

  section {
    margin-bottom: 2rem;
  }

  h2 {
    font-size: 1rem;
    margin-bottom: 0.375rem;
  }

  .hint {
    margin: 0 0 0.75rem;
    color: var(--ink-muted);
    font-size: 0.8125rem;
  }

  .status {
    color: var(--ink-muted);
    font-size: 0.8125rem;

    &.error {
      color: var(--accent);
    }

    &.success {
      margin-left: 0.5rem;
    }
  }

  select {
    padding: 0.375rem 0.625rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper-raised);
    color: var(--ink);
    font: inherit;
    font-size: 0.8125rem;

    &:disabled {
      opacity: 0.5;
    }
  }
</style>
