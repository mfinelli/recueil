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
<!-- In-app reader view for reader_text: plain extracted text (Readability's
     textContent, not its HTML content field -- see CaptureDetail's own
     comment), so it's rendered as plain text with white-space: pre-wrap
     rather than {@html}'d or paragraph-split by guesswork -- no need to
     assume a specific paragraph-break convention when the browser can just
     preserve whatever whitespace Readability actually produced.

     The archived HTML snapshot itself is deliberately NOT contained in
     here -- still a plain link to GET /api/captures/{id}/html, opened in a
     new tab. It's a full, self-contained snapshot of the original page's
     own layout/CSS/images; an iframe would mean fighting sizing/scrolling
     for the whole viewing session for little benefit over a new tab, which
     gets native zoom, find-in-page, and the full viewport for free.

     Regenerate-summary/regenerate-readability are both fire-and-forget from
     this screen's own perspective -- neither touches `capture` at all on
     success (the background job runner picks the reset job up on its own
     schedule, same as PageDetail's recapture action never touches `page`), so
     each just shows a transient "Queued!" confirmation, same pattern as
     Devices.svelte/PageDetail.svelte's copy/recapture buttons. Each is also
     hidden once the capture's own stored version/model already matches
     GET /api/capture-config's current one -- regenerating would just
     reproduce what's already there. That comparison is best-effort: if
     capture-config fails to load, both buttons default to showing rather
     than silently disappearing. -->
<script lang="ts">
  import { link, push } from "svelte-spa-router";
  import ChevronLeft from "@lucide/svelte/icons/chevron-left";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import ExternalLink from "@lucide/svelte/icons/external-link";
  import Archive from "@lucide/svelte/icons/archive";
  import Sparkles from "@lucide/svelte/icons/sparkles";
  import RotateCw from "@lucide/svelte/icons/rotate-cw";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import Copy from "@lucide/svelte/icons/copy";
  import { apiJSON, ApiError } from "../lib/api";
  import { formatBytes } from "../lib/format";
  import { languageDisplayName } from "../lib/languageNames";
  import { getLocale } from "../paraglide/runtime";
  import AppHeader from "../components/AppHeader.svelte";
  import type {
    CaptureDetail,
    CaptureConfig,
    TextSearchConfigsResponse,
  } from "../lib/types";
  import { m } from "../paraglide/messages";

  let { params }: { params: { id: string } } = $props();

  const FONT_PREF_KEY = "recueil:capture-reader-font";
  type ReaderFont = "sans" | "serif";

  function loadFontPref(): ReaderFont {
    return localStorage.getItem(FONT_PREF_KEY) === "serif" ? "serif" : "sans";
  }

  let readerFont = $state<ReaderFont>(loadFontPref());

  function setReaderFont(font: ReaderFont) {
    readerFont = font;
    localStorage.setItem(FONT_PREF_KEY, font);
  }

  let capture = $state<CaptureDetail | null>(null);
  let captureConfig = $state<CaptureConfig | null>(null);
  let languageOptions = $state<string[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  let faviconFailed = $state(false);
  let savingLanguage = $state(false);
  let deleting = $state(false);
  let regeneratingSummary = $state(false);
  let summaryQueued = $state(false);
  let regeneratingReadability = $state(false);
  let readabilityQueued = $state(false);
  let copiedField = $state<string | null>(null);

  $effect(() => {
    const id = params.id;
    loading = true;
    loadError = null;
    actionError = null;
    capture = null;
    faviconFailed = false;

    apiJSON<CaptureDetail>(`/captures/${id}`)
      .then((res) => {
        capture = res;
      })
      .catch((err: unknown) => {
        loadError =
          err instanceof ApiError ? err.message : m.capturereader_load_error();
      })
      .finally(() => {
        loading = false;
      });

    // Both best-effort and independent of the main load above: a failure
    // in either just means the regenerate buttons default to showing
    // (capture-config) or the language picker has no options
    // (text-search-configs), not a blocking error for the whole screen.
    apiJSON<CaptureConfig>("/capture-config")
      .then((res) => {
        captureConfig = res;
      })
      .catch(() => {
        captureConfig = null;
      });

    apiJSON<TextSearchConfigsResponse>("/text-search-configs")
      .then((res) => {
        languageOptions = res.languages;
      })
      .catch(() => {
        languageOptions = [];
      });
  });

  function formatDateTime(iso: string): string {
    return new Date(iso).toLocaleString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
  }

  function sourceLabel(source: string): string {
    return source === "manual_upload"
      ? m.capturereader_source_manual_upload()
      : m.capturereader_source_extension();
  }

  // Regenerating would just reproduce what's already stored -- hide the
  // button rather than offer an action that does nothing. Any difference
  // (including capture's own field being null, i.e. never run) shows it.
  // captureConfig === null (still loading, or the fetch failed) also shows
  // it -- fail open, don't hide a real capability just because the
  // comparison endpoint had a hiccup.
  function showReadabilityRegenerate(c: CaptureDetail): boolean {
    return (
      captureConfig === null ||
      c.readability_version !== captureConfig.readability_version
    );
  }

  function showSummaryRegenerate(c: CaptureDetail): boolean {
    return captureConfig === null || c.ai_model !== captureConfig.ai_model;
  }

  async function updateLanguage(newLanguage: string) {
    if (!capture || newLanguage === capture.language) return;
    savingLanguage = true;
    actionError = null;
    try {
      await apiJSON(`/captures/${capture.id}/language`, {
        method: "PATCH",
        body: { language: newLanguage },
      });
      capture.language = newLanguage;
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.capturereader_language_error();
    } finally {
      savingLanguage = false;
    }
  }

  async function copyToClipboard(field: string, value: string) {
    await navigator.clipboard.writeText(value);
    copiedField = field;
    setTimeout(() => {
      copiedField = null;
    }, 2000);
  }

  async function deleteCapture() {
    if (!capture) return;
    if (!confirm(m.capturereader_delete_confirm())) return;

    deleting = true;
    actionError = null;
    try {
      await apiJSON(`/captures/${capture.id}`, { method: "DELETE" });
      await push("/");
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.capturereader_delete_error();
      deleting = false;
    }
  }

  async function regenerateSummary() {
    if (!capture) return;
    regeneratingSummary = true;
    actionError = null;
    try {
      await apiJSON(`/captures/${capture.id}/regenerate-summary`, {
        method: "POST",
      });
      summaryQueued = true;
      setTimeout(() => {
        summaryQueued = false;
      }, 2000);
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.capturereader_regenerate_summary_error();
    } finally {
      regeneratingSummary = false;
    }
  }

  async function regenerateReadability() {
    if (!capture) return;
    regeneratingReadability = true;
    actionError = null;
    try {
      await apiJSON(`/captures/${capture.id}/regenerate-readability`, {
        method: "POST",
      });
      readabilityQueued = true;
      setTimeout(() => {
        readabilityQueued = false;
      }, 2000);
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.capturereader_regenerate_readability_error();
    } finally {
      regeneratingReadability = false;
    }
  }
</script>

<main class="screen">
  <AppHeader />

  {#if loading}
    <p class="status">{m.common_loading()}</p>
  {:else if loadError}
    <div class="status-error" role="alert">
      <AlertCircle size={28} />
      <span>{loadError}</span>
    </div>
  {:else if capture}
    <a class="back" href={`/pages/${capture.page_id}`} use:link>
      <ChevronLeft size={14} />
      {m.capturereader_back()}
    </a>

    <div class="title-row">
      {#if capture.favicon_path !== null && !faviconFailed}
        <img
          class="favicon"
          src={`/api/captures/${capture.id}/favicon`}
          alt=""
          loading="lazy"
          onerror={() => (faviconFailed = true)}
        />
      {/if}
      <h1>{capture.title ?? capture.raw_url}</h1>
    </div>

    <div class="byline">
      <span class="captured-line">
        <span class="captured-at">
          {m.capturereader_captured_via({
            date: formatDateTime(capture.captured_at),
            source: sourceLabel(capture.source),
          })}
        </span>
      </span>
      <a
        class="raw-url"
        href={capture.raw_url}
        target="_blank"
        rel="noreferrer"
      >
        <ExternalLink size={12} />
        {capture.raw_url}
      </a>
    </div>
    <a
      class="archived-link"
      href={`/api/captures/${capture.id}/html`}
      target="_blank"
      rel="noreferrer"
    >
      <Archive size={12} />
      {m.capturereader_view_archived()}
    </a>

    {#if actionError}
      <p class="status error" role="alert">
        <AlertCircle size={15} />
        <span>{actionError}</span>
      </p>
    {/if}

    {#if capture.ai_summary}
      <div class="summary">
        <Sparkles size={16} />
        <div class="summary-body">
          <div class="summary-header">
            <span class="eyebrow">{m.capturereader_ai_summary_heading()}</span>
            <span class="summary-model">
              {capture.ai_model}
              {#if showSummaryRegenerate(capture)}
                <button
                  type="button"
                  class="regen-btn"
                  aria-label={summaryQueued
                    ? m.capturereader_regenerate_summary_queued()
                    : m.capturereader_regenerate_summary()}
                  disabled={regeneratingSummary || summaryQueued}
                  onclick={regenerateSummary}
                >
                  <RotateCw size={12} />
                </button>
              {/if}
            </span>
          </div>
          <p>{capture.ai_summary}</p>
        </div>
      </div>
    {/if}

    <div class="reader-controls">
      <div class="font-toggle" role="group">
        <button
          class:active={readerFont === "sans"}
          onclick={() => setReaderFont("sans")}
          >{m.capturereader_font_sans()}</button
        >
        <button
          class:active={readerFont === "serif"}
          onclick={() => setReaderFont("serif")}
          >{m.capturereader_font_serif()}</button
        >
      </div>
      {#if languageOptions.length > 0}
        <label class="lang-picker">
          {m.common_language()}
          <select
            value={capture.language}
            disabled={savingLanguage}
            onchange={(e) => updateLanguage(e.currentTarget.value)}
          >
            {#each languageOptions as lang (lang)}
              <option value={lang}
                >{lang === "simple"
                  ? m.pagedetail_language_other()
                  : languageDisplayName(lang, getLocale())}</option
              >
            {/each}
          </select>
        </label>
      {/if}
    </div>

    {#if capture.reader_text}
      <div class="reader-text" class:serif={readerFont === "serif"}>
        {capture.reader_text}
      </div>
    {:else}
      <p class="status">{m.capturereader_no_text()}</p>
    {/if}

    <div class="readability-footer">
      {m.capturereader_readability_label()}
      {capture.readability_version ?? "—"}
      {#if showReadabilityRegenerate(capture)}
        <button
          type="button"
          class="regen-btn"
          aria-label={readabilityQueued
            ? m.capturereader_regenerate_readability_queued()
            : m.capturereader_regenerate_readability()}
          disabled={regeneratingReadability || readabilityQueued}
          onclick={regenerateReadability}
        >
          <RotateCw size={12} />
        </button>
      {/if}
    </div>

    <div class="details">
      <span class="eyebrow">{m.capturereader_details_heading()}</span>
      <div class="details-row">
        <span class="label">{m.capturereader_archive_label()}</span>
        <span class="value"
          >{m.capturereader_archive_size({
            compressed: formatBytes(capture.html_compressed_size_bytes),
            uncompressed: formatBytes(capture.html_uncompressed_size_bytes),
          })}</span
        >
      </div>
      <div class="details-row">
        <span class="label">{m.capturereader_archive_sha256_label()}</span>
        <span class="value hash-row">
          <span class="hash" title={capture.content_hash}
            >{capture.content_hash.slice(0, 12)}…</span
          >
          <button
            type="button"
            class="copy-btn"
            aria-label={m.capturereader_copy_aria({
              name: m.capturereader_archive_sha256_label(),
            })}
            onclick={() => copyToClipboard("archive", capture!.content_hash)}
          >
            {#if copiedField === "archive"}
              <span class="copied">{m.capturereader_copied()}</span>
            {:else}
              <Copy size={10} />
            {/if}
          </button>
        </span>
      </div>
      {#if capture.thumbnail_path !== null}
        <div class="details-row">
          <span class="label">{m.capturereader_thumbnail_label()}</span>
          <span class="value"
            >{capture.thumbnail_size_bytes !== null
              ? formatBytes(capture.thumbnail_size_bytes)
              : "—"}</span
          >
        </div>
        {#if capture.thumbnail_hash !== null}
          <div class="details-row">
            <span class="label">{m.capturereader_thumbnail_sha256_label()}</span
            >
            <span class="value hash-row">
              <span class="hash" title={capture.thumbnail_hash}
                >{capture.thumbnail_hash.slice(0, 12)}…</span
              >
              <button
                type="button"
                class="copy-btn"
                aria-label={m.capturereader_copy_aria({
                  name: m.capturereader_thumbnail_sha256_label(),
                })}
                onclick={() =>
                  copyToClipboard("thumbnail", capture!.thumbnail_hash!)}
              >
                {#if copiedField === "thumbnail"}
                  <span class="copied">{m.capturereader_copied()}</span>
                {:else}
                  <Copy size={10} />
                {/if}
              </button>
            </span>
          </div>
        {/if}
      {/if}
      {#if capture.favicon_path !== null}
        <div class="details-row">
          <span class="label">{m.capturereader_favicon_label()}</span>
          <span class="value"
            >{capture.favicon_size_bytes !== null
              ? formatBytes(capture.favicon_size_bytes)
              : "—"}</span
          >
        </div>
        {#if capture.favicon_hash !== null}
          <div class="details-row">
            <span class="label">{m.capturereader_favicon_sha256_label()}</span>
            <span class="value hash-row">
              <span class="hash" title={capture.favicon_hash}
                >{capture.favicon_hash.slice(0, 12)}…</span
              >
              <button
                type="button"
                class="copy-btn"
                aria-label={m.capturereader_copy_aria({
                  name: m.capturereader_favicon_sha256_label(),
                })}
                onclick={() =>
                  copyToClipboard("favicon", capture!.favicon_hash!)}
              >
                {#if copiedField === "favicon"}
                  <span class="copied">{m.capturereader_copied()}</span>
                {:else}
                  <Copy size={10} />
                {/if}
              </button>
            </span>
          </div>
        {/if}
      {/if}
    </div>

    <div class="actions-row">
      <button
        type="button"
        class="danger"
        onclick={deleteCapture}
        disabled={deleting}
      >
        <Trash2 size={14} />
        {m.capturereader_delete()}
      </button>
    </div>
  {/if}
</main>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;
  @use "../styles/components" as comp;

  .screen {
    @include comp.content-screen;
    padding-bottom: 4rem;
  }

  .back {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    margin-bottom: 1.5rem;
    color: var(--ink-muted);
    text-decoration: none;
    font-size: 0.875rem;

    &:hover {
      color: var(--ink);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .status {
    @include comp.status-row;
  }

  // Matches Library/PageDetail's own load-error treatment exactly.
  .status-error {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.6rem;
    padding: 2.5rem 1rem;
    color: var(--accent);
    text-align: center;

    :global(svg) {
      opacity: 0.6;
    }
  }

  .title-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
  }

  .favicon {
    width: 18px;
    height: 18px;
    border-radius: 3px;
    flex: none;
  }

  h1 {
    @include type.heading;
    font-size: 1.6rem;
    line-height: 1.25;
    margin: 0;
  }

  .byline {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.3rem 1rem;
    margin-bottom: 0.4rem;
    color: var(--ink-muted);
    font-size: 0.8125rem;
  }

  .byline .captured-at {
    @include type.data-mono;
    font-size: 0.78rem;
  }

  .raw-url {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.75rem;
    text-decoration: none;
    word-break: break-all;

    &:hover {
      color: var(--accent);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .archived-link {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    margin-bottom: 1.5rem;
    color: var(--ink-muted);
    text-decoration: none;
    font-size: 0.8125rem;

    &:hover {
      color: var(--accent);
      text-decoration: underline;
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .summary {
    display: flex;
    gap: 0.6rem;
    max-width: 42rem;
    padding: 0.85rem 1rem;
    margin: 0 auto 1.75rem;
    border-radius: 4px;
    border: 1px solid color-mix(in srgb, var(--brass) 40%, var(--rule));
    background: var(--paper-raised);

    :global(> svg) {
      flex: none;
      color: var(--brass);
      margin-top: 0.2rem;
    }
  }

  .summary-body {
    flex: 1;
    min-width: 0;
  }

  .summary-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    margin-bottom: 0.3rem;
  }

  .summary-body .eyebrow {
    @include type.eyebrow;
  }

  .summary-model {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    @include type.data-mono;
    font-size: 0.68rem;
    color: var(--ink-muted);
  }

  .summary-body p {
    margin: 0;
    color: var(--ink);
    font-style: italic;
    font-size: 0.9375rem;
    line-height: 1.55;
  }

  .regen-btn {
    @include comp.icon-btn(1.3rem, rgba(0, 0, 0, 0.05));

    &:disabled {
      opacity: 0.6;
      cursor: default;
    }
  }

  .reader-controls {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    max-width: 42rem;
    margin: 0 auto 1.1rem;
  }

  .font-toggle {
    @include comp.segmented-toggle;

    button {
      @include comp.segmented-toggle-option;
      background: var(--paper-raised);
      padding: 0.35rem 0.65rem;
      font: inherit;
      font-size: 0.78rem;
      color: var(--ink-muted);
      cursor: pointer;
    }
  }

  .lang-picker {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    color: var(--ink-muted);
    font-size: 0.78rem;

    select {
      padding: 0.3rem 0.5rem;
      font-size: 0.78rem;
      @include type.data-mono;
      border: 1px solid var(--rule);
      border-radius: 4px;
      background: var(--paper-raised);
      color: var(--ink);

      &:focus-visible {
        @include mix.focus-ring;
      }
    }
  }

  .reader-text {
    max-width: 42rem;
    margin: 0 auto;
    white-space: pre-wrap;
    font-size: 1.0625rem;
    line-height: 1.7;
    color: var(--ink);

    &.serif {
      font-family: Georgia, Cambria, "Times New Roman", Times, serif;
    }
  }

  .readability-footer {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    max-width: 53rem;
    margin: 1rem auto 0;
    padding-top: 0.75rem;
    border-top: 1px dotted var(--rule);
    color: var(--ink-muted);
    @include type.data-mono;
    font-size: 0.72rem;
  }

  .details {
    @include comp.details-card;
    max-width: 53rem;
    margin: 2rem auto 0;
  }

  .details .eyebrow {
    @include type.eyebrow;
    display: block;
    margin-bottom: 0.6rem;
  }

  .details-row {
    @include comp.details-row;
  }

  .details-row .label {
    @include comp.details-label;
  }

  .details-row .value {
    @include comp.details-value;
  }

  .hash-row {
    display: flex;
    align-items: center;
    gap: 0.3rem;
  }

  .hash {
    cursor: default;
  }

  .copy-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    height: 1.1rem;
    padding: 0 0.3rem;
    border: none;
    border-radius: 3px;
    background: transparent;
    color: var(--ink-muted);
    cursor: pointer;

    &:hover {
      color: var(--accent);
      background: rgba(0, 0, 0, 0.05);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .copied {
    @include type.data-mono;
    font-size: 0.65rem;
    color: var(--accent-success);
  }

  .actions-row {
    display: flex;
    max-width: 53rem;
    margin: 2rem auto 0;
    padding-top: 0.9rem;
    border-top: 1px dotted var(--rule);
  }

  button.danger {
    @include comp.bordered-button;
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.4rem 0.75rem;
    border-color: var(--accent);
    color: var(--accent);
    font-size: 0.8125rem;

    &:hover:not(:disabled) {
      background: var(--accent);
      color: var(--paper);
    }

    // A more forgiving disabled state than the default bordered-button's
    // 0.5 -- this action is destructive, so the disabled affordance
    // should still read clearly rather than nearly disappearing.
    &:disabled {
      opacity: 0.6;
    }
  }
</style>
