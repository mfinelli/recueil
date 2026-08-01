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
  import type { Component } from "svelte";
  import Monitor from "@lucide/svelte/icons/monitor";
  import Sun from "@lucide/svelte/icons/sun";
  import Moon from "@lucide/svelte/icons/moon";
  import Check from "@lucide/svelte/icons/check";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type { UserSettings, Stats, AdminStats } from "../lib/types";
  import { formatBytes } from "../lib/format";
  import { m } from "../paraglide/messages";
  import { applyLanguageOverride } from "../lib/locale";
  import { applyTheme } from "../lib/theme";
  import { session } from "../lib/session.svelte";

  const LANGUAGE_OPTIONS: { value: string; label: string }[] = [
    { value: "", label: m.language_option_automatic() },
    { value: "en", label: "English" },
    { value: "fr", label: "Français" },
  ];

  const THEME_OPTIONS: { value: string; label: string; icon: Component }[] = [
    { value: "", label: m.theme_option_automatic(), icon: Monitor },
    { value: "light", label: m.theme_option_light(), icon: Sun },
    { value: "dark", label: m.theme_option_dark(), icon: Moon },
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

  let stats = $state<Stats | null>(null);
  let statsLoading = $state(true);
  let statsError = $state<string | null>(null);

  let adminStats = $state<AdminStats | null>(null);
  let adminStatsLoading = $state(true);
  let adminStatsError = $state<string | null>(null);

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

  // Independent from loadSettings/its own $effect call below -- language
  // and theme are one request/response pair already (settings PATCH is
  // full-replace, both fields together), but stats is a wholly separate
  // resource with its own failure mode, so a stats load failure
  // shouldn't block or blank out the language/theme toggles above it,
  // and vice versa.
  async function loadStats() {
    statsLoading = true;
    try {
      stats = await apiJSON<Stats>("/stats");
    } catch (err) {
      statsError =
        err instanceof ApiError ? err.message : m.settings_stats_load_error();
    } finally {
      statsLoading = false;
    }
  }

  // Only called for an admin (see the $effect below) -- a non-admin's
  // session never even attempts this request, rather than requesting it
  // and hiding a 403. adminStatsLoading starts true regardless of role,
  // but the template only ever consults it inside the
  // `session.user?.role === "admin"` branch, so a non-admin never sees a
  // stuck loading indicator for a request that was never made.
  async function loadAdminStats() {
    adminStatsLoading = true;
    try {
      adminStats = await apiJSON<AdminStats>("/admin/stats");
    } catch (err) {
      adminStatsError =
        err instanceof ApiError
          ? err.message
          : m.settings_admin_stats_load_error();
    } finally {
      adminStatsLoading = false;
    }
  }

  $effect(() => {
    loadSettings();
    loadStats();
    if (session.user?.role === "admin") {
      loadAdminStats();
    }
  });

  // Separate state per field (savingLanguage vs. savingTheme, etc.), not
  // one shared saving/saved/saveError -- both toggles live on the same
  // screen, so a shared "Saved" would appear next to the section the
  // person *didn't* just touch too, and a shared error after, say, an
  // invalid theme value would misleadingly show under Language as well.
  // The PATCH body is still always both fields together, full-replace;
  // only the UI feedback is split, not the request itself.
  async function selectLanguage(value: string) {
    if (value === language) return;
    savingLanguage = true;
    saveErrorLanguage = null;
    savedLanguage = false;
    try {
      const res = await apiJSON<UserSettings>("/settings", {
        method: "PATCH",
        body: { language: value, theme },
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

  // Theme has its own handler, separate from selectLanguage: language's save
  // reloads the page (applyLanguageOverride, above) -- if theme shared
  // that same handler, changing theme would trigger a reload too, for no
  // reason, when a plain DOM mutation (applyTheme) already does
  // everything theme itself needs. Still sends both fields in the same
  // PATCH body, it just doesn't reload afterward.
  async function selectTheme(value: string) {
    if (value === theme) return;
    savingTheme = true;
    saveErrorTheme = null;
    savedTheme = false;
    try {
      const res = await apiJSON<UserSettings>("/settings", {
        method: "PATCH",
        body: { language, theme: value },
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
  <p class="page-heading">{m.settings()}</p>

  {#if loadError}
    <p class="status error" role="alert">
      <AlertCircle size={15} />
      <span>{loadError}</span>
    </p>
  {/if}

  <section>
    <p class="eyebrow">{m.common_language()}</p>
    <p class="hint">
      {m.settings_language_hint()}
    </p>
    {#if loading}
      <p class="status">{m.common_loading()}</p>
    {:else}
      <div class="toggle" role="group" aria-label={m.common_language()}>
        {#each LANGUAGE_OPTIONS as option (option.value)}
          <button
            type="button"
            class:active={language === option.value}
            disabled={savingLanguage}
            onclick={() => selectLanguage(option.value)}
          >
            {option.label}
          </button>
        {/each}
      </div>
      {#if savedLanguage}
        <span class="saved">
          <Check size={13} />
          {m.settings_saved()}
        </span>
      {/if}
      {#if saveErrorLanguage}
        <p class="status error" role="alert">
          <AlertCircle size={15} />
          <span>{saveErrorLanguage}</span>
        </p>
      {/if}
    {/if}
  </section>

  <section>
    <p class="eyebrow">{m.common_theme()}</p>
    <p class="hint">
      {m.settings_theme_hint()}
    </p>
    {#if loading}
      <p class="status">{m.common_loading()}</p>
    {:else}
      <div class="toggle" role="group" aria-label={m.common_theme()}>
        {#each THEME_OPTIONS as option (option.value)}
          {@const OptionIcon = option.icon}
          <button
            type="button"
            class:active={theme === option.value}
            disabled={savingTheme}
            onclick={() => selectTheme(option.value)}
          >
            <OptionIcon size={13} />
            {option.label}
          </button>
        {/each}
      </div>
      {#if savedTheme}
        <span class="saved">
          <Check size={13} />
          {m.settings_saved()}
        </span>
      {/if}
      {#if saveErrorTheme}
        <p class="status error" role="alert">
          <AlertCircle size={15} />
          <span>{saveErrorTheme}</span>
        </p>
      {/if}
    {/if}
  </section>

  <section>
    <p class="eyebrow">{m.settings_stats_heading()}</p>
    {#if statsLoading}
      <p class="status">{m.common_loading()}</p>
    {:else if statsError}
      <p class="status error" role="alert">
        <AlertCircle size={15} />
        <span>{statsError}</span>
      </p>
    {:else if stats}
      <p class="stats-summary">
        {m.settings_stats_pages({
          pages: String(stats.page_count),
          captures: String(stats.capture_count),
        })}
      </p>
      <p class="stats-summary">
        {m.settings_stats_disk_total({
          size: formatBytes(
            stats.html_compressed_bytes +
              stats.favicon_bytes +
              stats.screenshot_bytes,
          ),
        })}
      </p>
      <div class="stats-card">
        <div class="stats-row">
          <span class="label">{m.settings_stats_html_label()}</span>
          <span class="value"
            >{m.settings_stats_html({
              compressed: formatBytes(stats.html_compressed_bytes),
              uncompressed: formatBytes(stats.html_uncompressed_bytes),
            })}</span
          >
        </div>
        <div class="stats-row">
          <span class="label">{m.settings_stats_favicon_label()}</span>
          <span class="value">{formatBytes(stats.favicon_bytes)}</span>
        </div>
        <div class="stats-row">
          <span class="label">{m.settings_stats_screenshots_label()}</span>
          <span class="value">{formatBytes(stats.screenshot_bytes)}</span>
        </div>
      </div>
    {/if}
  </section>

  {#if session.user?.role === "admin"}
    <section>
      <p class="eyebrow">{m.settings_admin_stats_heading()}</p>
      <p class="hint">{m.settings_admin_stats_hint()}</p>
      {#if adminStatsLoading}
        <p class="status">{m.common_loading()}</p>
      {:else if adminStatsError}
        <p class="status error" role="alert">
          <AlertCircle size={15} />
          <span>{adminStatsError}</span>
        </p>
      {:else if adminStats}
        <p class="stats-summary">
          {m.settings_admin_stats_pages({
            pages: String(adminStats.page_count),
            captures: String(adminStats.capture_count),
          })}
        </p>
        <p class="stats-summary">
          {m.settings_admin_stats_disk_total({
            size: formatBytes(
              adminStats.html_compressed_bytes +
                adminStats.favicon_bytes +
                adminStats.screenshot_bytes,
            ),
          })}
        </p>
        <p class="top-users-heading">
          {m.settings_admin_stats_top_users_heading()}
        </p>
        {#if adminStats.top_users.length > 0}
          <table class="top-users">
            <thead>
              <tr>
                <th>{m.settings_admin_stats_user_col()}</th>
                <th>{m.settings_admin_stats_captures_col()}</th>
                <th>{m.settings_admin_stats_html_col()}</th>
                <th>{m.settings_admin_stats_other_col()}</th>
              </tr>
            </thead>
            <tbody>
              {#each adminStats.top_users as topUser (topUser.username)}
                <tr>
                  <td>{topUser.username}</td>
                  <td>{topUser.capture_count}</td>
                  <td>{formatBytes(topUser.html_compressed_bytes)}</td>
                  <td>{formatBytes(topUser.other_bytes)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {:else}
          <p class="empty-note">{m.settings_admin_stats_no_top_users()}</p>
        {/if}
      {/if}
    </section>
  {/if}
</main>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;
  @use "../styles/components" as comp;

  .screen {
    @include comp.content-screen;
  }

  .page-heading {
    @include type.eyebrow;
    margin: 0 0 1.5rem;
  }

  section {
    margin-bottom: 2rem;
  }

  .eyebrow {
    @include type.eyebrow;
    margin: 0 0 0.4rem;
  }

  .hint {
    margin: 0 0 0.85rem;
    color: var(--ink-muted);
    font-size: 0.8125rem;
  }

  .toggle {
    @include comp.segmented-toggle;
    flex-wrap: wrap;

    button {
      @include comp.segmented-toggle-option;
      display: flex;
      align-items: center;
      gap: 0.4rem;
      background: var(--paper-raised);
      padding: 0.5rem 0.85rem;
      font: inherit;
      font-size: 0.8125rem;
      color: var(--ink-muted);
      cursor: pointer;

      &:disabled {
        opacity: 0.6;
        cursor: default;
      }
    }
  }

  .saved {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    margin-left: 0.6rem;
    color: var(--accent-success);
    font-size: 0.78rem;
  }

  .status {
    @include comp.status-row;
  }

  .stats-summary {
    margin: 0 0 0.4rem;
    font-size: 0.875rem;

    &:last-of-type {
      margin-bottom: 0.85rem;
    }
  }

  .stats-card {
    @include comp.details-card;
  }

  .stats-row {
    @include comp.details-row;

    .label {
      @include comp.details-label;
    }

    .value {
      @include comp.details-value;
    }
  }

  .empty-note {
    color: var(--ink-muted);
    font-size: 0.8125rem;
    font-style: italic;
  }

  .top-users-heading {
    margin: 0 0 0.5rem;
    color: var(--ink-muted);
    font-size: 0.8125rem;
    font-weight: 600;
  }

  // .card-surface for the outer shape and a dotted rule between body rows
  // keep it visually consistent with the rest of the app rather than reading
  // like a dropped-in generic HTML table.
  .top-users {
    width: 100%;
    border-collapse: collapse;
    @include mix.card-surface;
    font-size: 0.8125rem;

    th,
    td {
      padding: 0.5rem 0.75rem;
      text-align: left;
    }

    th {
      @include type.eyebrow;
      font-size: 0.68rem;
      color: var(--brass);
    }

    th:not(:first-child) {
      text-align: right;
    }

    td:not(:first-child) {
      @include type.data-mono;
      font-size: 0.75rem;
      text-align: right;
    }

    tbody tr {
      @include mix.dotted-rule;

      &:last-child {
        border-bottom: none;
      }
    }
  }
</style>
