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
     Devices.svelte/PageDetail.svelte's copy/recapture buttons. -->
<script lang="ts">
  import { link, push } from "svelte-spa-router";
  import { apiJSON, ApiError } from "../lib/api";
  import AppHeader from "../components/AppHeader.svelte";
  import type { CaptureDetail } from "../lib/types";
  import { m } from "../paraglide/messages";

  let { params }: { params: { id: string } } = $props();

  let capture = $state<CaptureDetail | null>(null);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  let deleting = $state(false);
  let regeneratingSummary = $state(false);
  let summaryQueued = $state(false);
  let regeneratingReadability = $state(false);
  let readabilityQueued = $state(false);

  $effect(() => {
    const id = params.id;
    loading = true;
    loadError = null;
    capture = null;
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
    <p class="status error" role="alert">{loadError}</p>
  {:else if capture}
    <a class="back" href={`/pages/${capture.page_id}`} use:link
      >&larr; {m.capturereader_back()}</a
    >

    <h1>{capture.title ?? capture.raw_url}</h1>
    <div class="byline">
      <span
        >{m.capturereader_captured_at({
          date: formatDateTime(capture.captured_at),
        })}</span
      >
      <a href={capture.raw_url} target="_blank" rel="noreferrer"
        >{m.capturereader_original_url()}</a
      >
      <a
        href={`/api/captures/${capture.id}/html`}
        target="_blank"
        rel="noreferrer">{m.capturereader_view_archived()}</a
      >
    </div>

    {#if actionError}
      <p class="status error" role="alert">{actionError}</p>
    {/if}

    <div class="actions">
      <button
        type="button"
        onclick={regenerateReadability}
        disabled={regeneratingReadability}
        >{readabilityQueued
          ? m.capturereader_regenerate_readability_queued()
          : regeneratingReadability
            ? m.capturereader_regenerate_readability_queuing()
            : m.capturereader_regenerate_readability()}</button
      >
      <button
        type="button"
        onclick={regenerateSummary}
        disabled={regeneratingSummary}
        >{summaryQueued
          ? m.capturereader_regenerate_summary_queued()
          : regeneratingSummary
            ? m.capturereader_regenerate_summary_queuing()
            : m.capturereader_regenerate_summary()}</button
      >
      <button
        type="button"
        class="danger"
        onclick={deleteCapture}
        disabled={deleting}>{m.capturereader_delete()}</button
      >
    </div>

    {#if capture.ai_summary}
      <p class="summary">{capture.ai_summary}</p>
    {/if}

    {#if capture.reader_text}
      <div class="reader-text">{capture.reader_text}</div>
    {:else}
      <p class="status">{m.capturereader_no_text()}</p>
    {/if}
  {/if}
</main>

<style lang="scss">
  .screen {
    max-width: 38rem;
    margin: 0 auto;
    padding: 2rem 1rem 4rem;
  }

  .back {
    display: inline-block;
    margin-bottom: 1.5rem;
    color: var(--ink-muted);
    text-decoration: none;
    font-size: 0.875rem;

    &:hover {
      color: var(--ink);
    }
  }

  .status {
    color: var(--ink-muted);

    &.error {
      color: var(--accent);
    }
  }

  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    margin-bottom: 1.5rem;

    button {
      padding: 0.4rem 0.75rem;
      border: 1px solid var(--rule);
      border-radius: 0.25rem;
      background: var(--paper-raised);
      color: var(--ink);
      font: inherit;
      font-size: 0.8125rem;
      cursor: pointer;

      &:disabled {
        opacity: 0.6;
        cursor: default;
      }
    }

    .danger {
      color: var(--accent);
      border-color: var(--accent);

      &:hover:not(:disabled) {
        background: var(--accent);
        color: var(--paper);
      }
    }
  }

  h1 {
    margin: 0 0 0.5rem;
    font-size: 1.5rem;
    line-height: 1.25;
  }

  .byline {
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem 0.75rem;
    margin-bottom: 1.5rem;
    color: var(--ink-muted);
    font-size: 0.8125rem;

    a {
      color: var(--focus);
    }
  }

  .summary {
    padding: 0.75rem 1rem;
    margin-bottom: 1.5rem;
    border-left: 3px solid var(--rule);
    background: var(--paper-raised);
    color: var(--ink-muted);
    font-style: italic;
    font-size: 0.9375rem;
  }

  .reader-text {
    white-space: pre-wrap;
    font-size: 1.0625rem;
    line-height: 1.65;
  }
</style>
