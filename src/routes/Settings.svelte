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
     untranslated by oversight.

     THEME_OPTIONS is the same shape, but its labels ARE run through
     m.*() -- "Light"/"Dark" aren't autonyms the way a language name is,
     they're ordinary words that should read in whatever language this
     screen itself is already in.

     Unlike language (which needs a full page reload), theme applies live:
     applyTheme() is a plain DOM attribute mutation, nothing else on the page
     needs to reload or re-render for it to take effect immediately. -->
<script lang="ts">
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type { UserSettings } from "../lib/types";
  import { m } from "../paraglide/messages";
  import { applyLanguageOverride } from "../lib/locale";
  import { applyTheme } from "../lib/theme";

  const LANGUAGE_OPTIONS: { value: string; label: string }[] = [
    { value: "", label: m.language_option_automatic() },
    { value: "en", label: "English" },
    { value: "fr", label: "Français" },
  ];

  const THEME_OPTIONS: { value: string; label: string }[] = [
    { value: "", label: m.theme_option_automatic() },
    { value: "light", label: m.theme_option_light() },
    { value: "dark", label: m.theme_option_dark() },
  ];

  let language = $state("");
  let theme = $state("");
  let loading = $state(true);
  let loadError = $state<string | null>(null);

  let savingLanguage = $state(false);
  let saveErrorLanguage = $state<string | null>(null);
  let savedLanguage = $state(false);

  let savingTheme = $state(false);
  let saveErrorTheme = $state<string | null>(null);
  let savedTheme = $state(false);

  async function loadSettings() {
    loading = true;
    try {
      const res = await apiJSON<UserSettings>("/settings");
      language = res.language ?? "";
      theme = res.theme ?? "";
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

  // Separate state per field (savingLanguage vs. savingTheme, etc.), not
  // one shared saving/saved/saveError -- both selects live on the same
  // screen, so a shared "Saved" would appear next to the section the
  // person *didn't* just touch too, and a shared error after, say, an
  // invalid theme value would misleadingly show under Language as well.
  // The PATCH body is still always both fields together, full-replace;
  // only the UI feedback is split, not the request itself.
  async function handleLanguageChange() {
    savingLanguage = true;
    saveErrorLanguage = null;
    savedLanguage = false;
    try {
      const res = await apiJSON<UserSettings>("/settings", {
        method: "PATCH",
        body: { language, theme },
      });
      language = res.language ?? "";
      theme = res.theme ?? "";
      savedLanguage = true;
      setTimeout(() => {
        savedLanguage = false;
      }, 2000);
      // Persistence to the backend is already done above -- this only
      // makes Paraglide itself pick up the change on reload. See
      // locale.ts's own comment for why this goes through
      // applyLanguageOverride() rather than Paraglide's own exported
      // setLocale() (which has no way to express "clear the override").
      applyLanguageOverride(language || null);
    } catch (err) {
      saveErrorLanguage =
        err instanceof ApiError ? err.message : m.settings_save_error();
    } finally {
      savingLanguage = false;
    }
  }

  // Theme's own handler, separate from handleLanguageChange: language's
  // save reloads the page (applyLanguageOverride, above) -- if theme
  // shared that same handler, changing theme would trigger a reload too,
  // for no reason, when a plain DOM mutation (applyTheme) already does
  // everything theme itself needs. Still sends both fields in the same
  // PATCH body, it just doesn't reload afterward.
  async function handleThemeChange() {
    savingTheme = true;
    saveErrorTheme = null;
    savedTheme = false;
    try {
      const res = await apiJSON<UserSettings>("/settings", {
        method: "PATCH",
        body: { language, theme },
      });
      language = res.language ?? "";
      theme = res.theme ?? "";
      savedTheme = true;
      setTimeout(() => {
        savedTheme = false;
      }, 2000);
      applyTheme(theme || null);
    } catch (err) {
      saveErrorTheme =
        err instanceof ApiError ? err.message : m.settings_save_error();
    } finally {
      savingTheme = false;
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
    <h2>{m.common_language()}</h2>
    <p class="hint">
      {m.settings_language_hint()}
    </p>
    {#if loading}
      <p class="status">{m.common_loading()}</p>
    {:else}
      <select
        aria-label={m.common_language()}
        bind:value={language}
        onchange={handleLanguageChange}
        disabled={savingLanguage}
      >
        {#each LANGUAGE_OPTIONS as option (option.value)}
          <option value={option.value}>{option.label}</option>
        {/each}
      </select>
      {#if savedLanguage}
        <span class="status success">{m.settings_saved()}</span>
      {/if}
      {#if saveErrorLanguage}
        <p class="status error" role="alert">{saveErrorLanguage}</p>
      {/if}
    {/if}
  </section>

  <section>
    <h2>{m.common_theme()}</h2>
    <p class="hint">
      {m.settings_theme_hint()}
    </p>
    {#if loading}
      <p class="status">{m.common_loading()}</p>
    {:else}
      <select
        aria-label={m.common_theme()}
        bind:value={theme}
        onchange={handleThemeChange}
        disabled={savingTheme}
      >
        {#each THEME_OPTIONS as option (option.value)}
          <option value={option.value}>{option.label}</option>
        {/each}
      </select>
      {#if savedTheme}
        <span class="status success">{m.settings_saved()}</span>
      {/if}
      {#if saveErrorTheme}
        <p class="status error" role="alert">{saveErrorTheme}</p>
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
