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
<!-- POST /api/manual-upload -- for a page captured somewhere the
     extension wasn't installed. For the modal escape and an explicit close
     button dismiss it, but there's no backdrop-click-to-close (a stray click
     shouldn't discard a part-filled form) and no full focus trap (Tab can
     leave the modal) -->
<script lang="ts">
  import { push } from "svelte-spa-router";
  import X from "@lucide/svelte/icons/x";
  import FileText from "@lucide/svelte/icons/file-text";
  import Image from "@lucide/svelte/icons/image";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import { apiJSON, ApiError, uploadManualCapture } from "../lib/api";
  import type { CaptureConfig, ManualUploadResponse } from "../lib/types";
  import { formatBytes } from "../lib/format";
  import { m } from "../paraglide/messages";

  let { open = $bindable(false) }: { open?: boolean } = $props();

  let url = $state("");
  let htmlFile = $state<File | null>(null);
  let faviconFile = $state<File | null>(null);
  let submitting = $state(false);
  let error = $state<string | null>(null);
  // null until GET /api/capture-config resolves, and stays null if it
  // fails (a missing size limit just means no client-side check runs; the
  // server still enforces its own ceiling either way).
  let maxUploadBytes = $state<number | null>(null);

  let urlInputEl = $state<HTMLInputElement | undefined>();

  // Fetches the current size ceiling once per time the modal opens
  $effect(() => {
    if (!open) return;

    apiJSON<CaptureConfig>("/capture-config")
      .then((res) => {
        maxUploadBytes = res.manual_upload_max_bytes;
      })
      .catch(() => {
        maxUploadBytes = null;
      });

    urlInputEl?.focus();
  });

  function resetAndClose() {
    open = false;
    url = "";
    htmlFile = null;
    faviconFile = null;
    error = null;
  }

  function handleKeydown(e: KeyboardEvent) {
    if (open && e.key === "Escape" && !submitting) {
      resetAndClose();
    }
  }

  function onHtmlChange(e: Event & { currentTarget: HTMLInputElement }) {
    htmlFile = e.currentTarget.files?.[0] ?? null;
  }

  function onFaviconChange(e: Event & { currentTarget: HTMLInputElement }) {
    faviconFile = e.currentTarget.files?.[0] ?? null;
  }

  // clearFile also clears the underlying <input>'s value, not just the
  // $state reference otherwise re-selecting the exact same file right
  // after removing it wouldn't fire another change event (the browser
  // sees no value change), and the chip would never come back.
  function clearHtmlFile(input: HTMLInputElement) {
    htmlFile = null;
    input.value = "";
  }
  function clearFaviconFile(input: HTMLInputElement) {
    faviconFile = null;
    input.value = "";
  }

  async function handleSubmit(e: SubmitEvent) {
    e.preventDefault();
    error = null;

    const trimmedURL = url.trim();
    if (!trimmedURL) {
      error = m.manualupload_error_url_required();
      return;
    }
    if (!htmlFile) {
      error = m.manualupload_error_html_required();
      return;
    }
    if (
      maxUploadBytes !== null &&
      htmlFile.size + (faviconFile?.size ?? 0) > maxUploadBytes
    ) {
      error = m.manualupload_error_file_too_large({
        size: formatBytes(maxUploadBytes),
      });
      return;
    }

    submitting = true;
    try {
      const res = await uploadManualCapture<ManualUploadResponse>(
        trimmedURL,
        htmlFile,
        faviconFile,
      );
      resetAndClose();
      await push(`/pages/${res.page_id}`);
    } catch (err) {
      error =
        err instanceof ApiError ? err.message : m.manualupload_error_generic();
    } finally {
      submitting = false;
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
  <div class="overlay">
    <div
      class="modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="manual-upload-title"
    >
      <div class="modal-header">
        <h2 id="manual-upload-title">{m.manualupload_modal_title()}</h2>
        <button
          type="button"
          class="close-btn"
          aria-label={m.manualupload_close()}
          disabled={submitting}
          onclick={resetAndClose}
        >
          <X size={18} />
        </button>
      </div>
      <p class="subtitle">{m.manualupload_modal_subtitle()}</p>

      <form onsubmit={handleSubmit}>
        {#if error}
          <p class="error" role="alert">
            <AlertCircle size={15} />
            <span>{error}</span>
          </p>
        {/if}

        <div class="field">
          <label for="manual-upload-url">{m.manualupload_url_label()}</label>
          <input
            id="manual-upload-url"
            type="url"
            bind:value={url}
            bind:this={urlInputEl}
            placeholder={m.manualupload_url_placeholder()}
            disabled={submitting}
          />
        </div>

        <div class="field">
          <label for="manual-upload-html">{m.manualupload_html_label()}</label>
          <div class="file-row">
            <label
              class="file-drop"
              class:has-file={htmlFile}
              for="manual-upload-html"
            >
              <FileText size={16} />
              <span class="filename"
                >{htmlFile ? htmlFile.name : m.manualupload_choose_file()}</span
              >
            </label>
            <input
              id="manual-upload-html"
              type="file"
              accept=".html,.htm"
              disabled={submitting}
              onchange={onHtmlChange}
            />
            {#if htmlFile}
              <button
                type="button"
                class="remove-file-btn"
                aria-label={m.manualupload_remove_file()}
                disabled={submitting}
                onclick={() => {
                  const input = document.getElementById(
                    "manual-upload-html",
                  ) as HTMLInputElement;
                  clearHtmlFile(input);
                }}
              >
                <X size={14} />
              </button>
            {/if}
          </div>
          <span class="hint">
            {maxUploadBytes !== null
              ? m.manualupload_html_hint({
                  size: formatBytes(maxUploadBytes),
                })
              : ""}
          </span>
        </div>

        <div class="field">
          <label for="manual-upload-favicon"
            >{m.manualupload_favicon_label()}</label
          >
          <div class="file-row">
            <label
              class="file-drop"
              class:has-file={faviconFile}
              for="manual-upload-favicon"
            >
              <Image size={16} />
              <span class="filename"
                >{faviconFile
                  ? faviconFile.name
                  : m.manualupload_choose_file()}</span
              >
            </label>
            <input
              id="manual-upload-favicon"
              type="file"
              accept=".svg,.png,.ico"
              disabled={submitting}
              onchange={onFaviconChange}
            />
            {#if faviconFile}
              <button
                type="button"
                class="remove-file-btn"
                aria-label={m.manualupload_remove_file()}
                disabled={submitting}
                onclick={() => {
                  const input = document.getElementById(
                    "manual-upload-favicon",
                  ) as HTMLInputElement;
                  clearFaviconFile(input);
                }}
              >
                <X size={14} />
              </button>
            {/if}
          </div>
          <span class="hint">{m.manualupload_favicon_hint()}</span>
        </div>

        <div class="actions">
          <button
            type="button"
            class="cancel-btn"
            disabled={submitting}
            onclick={resetAndClose}>{m.common_cancel()}</button
          >
          <button type="submit" class="submit-btn" disabled={submitting}
            >{submitting
              ? m.manualupload_submitting()
              : m.manualupload_submit()}</button
          >
        </div>
      </form>
    </div>
  </div>
{/if}

<style lang="scss">
  @use "../styles/mixins" as mix;
  @use "../styles/typography" as type;
  @use "../styles/components" as comp;

  .overlay {
    position: fixed;
    inset: 0;
    background: color-mix(in srgb, black 45%, transparent);
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding: 4rem 1rem;
    z-index: 100;
  }

  .modal {
    width: 100%;
    max-width: 30rem;
    background: var(--paper-raised);
    border: 1px solid var(--rule);
    border-radius: 3px;
    box-shadow: 0 12px 40px color-mix(in srgb, black 25%, transparent);
    padding: 1.5rem;
  }

  .modal-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 0.35rem;
  }

  h2 {
    @include type.heading;
    font-size: 1.3rem;
  }

  .close-btn {
    @include comp.icon-btn(28px, var(--paper));
    flex-shrink: 0;
  }

  .subtitle {
    color: var(--ink-muted);
    font-size: 0.85rem;
    margin: 0 0 1.25rem;
    line-height: 1.4;
  }

  .field {
    margin-bottom: 1.1rem;
  }

  .field label {
    display: block;
    font-size: 0.75rem;
    font-weight: 600;
    margin-bottom: 0.35rem;
  }

  .hint {
    display: block;
    font-size: 0.72rem;
    color: var(--ink-muted);
    margin-top: 0.3rem;
    line-height: 1.4;
    min-height: 1em;
  }

  input[type="url"] {
    width: 100%;
    @include comp.text-input;
    padding: 0.55rem 0.7rem;
    border-radius: 0.25rem;
  }

  .file-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
  }

  // The real <input type="file"> is visually hidden but still present and
  // labelled: clicking the styled label activates it.
  input[type="file"] {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    border: 0;
  }

  .file-drop {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 0.6rem;
    border: 1px dashed var(--rule);
    border-radius: 0.25rem;
    background: var(--paper);
    padding: 0.65rem 0.75rem;
    color: var(--ink-muted);
    font-size: 0.82rem;
    cursor: pointer;
    min-width: 0;

    &:hover {
      border-color: var(--accent);
      color: var(--accent);
    }

    &.has-file {
      border-style: solid;
      color: var(--ink);
    }
  }

  .filename {
    @include type.data-mono;
    font-size: 0.78rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .remove-file-btn {
    @include comp.icon-btn(24px);
    flex-shrink: 0;
  }

  .error {
    display: flex;
    align-items: flex-start;
    gap: 0.4rem;
    margin: 0 0 1rem;
    color: var(--accent);
    font-size: 0.85rem;
    line-height: 1.35;

    :global(svg) {
      flex: none;
      margin-top: 0.1rem;
    }
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.6rem;
    margin-top: 1.4rem;
  }

  .cancel-btn {
    @include comp.bordered-button;
    padding: 0.55rem 1rem;
  }

  .submit-btn {
    @include comp.primary-button;
    margin-top: 0;
  }
</style>
