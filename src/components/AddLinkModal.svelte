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
<!-- POST /api/links -- adding a link by hand: the link counterpart of
     ManualUploadModal and built the same way: the same overlay and spacing,
     the Escape key and the close button dismiss it, there's no
     backdrop-click-to-close (a stray click shouldn't discard a part-filled
     form), and no full focus trap. Recueil never visits the page itself, so
     the URL is the only required field; the title and favicon are only there
     if given.

     Unlike the upload it's plain JSON: the favicon is read in the browser and
     sent as base64 (favicon_ext + favicon_data), within the API's ordinary
     request limit. A URL that's already a link is refused by the server with a
     409 naming the existing link, which this offers to open rather than just
     reporting. -->
<script lang="ts">
  import { link, push } from "svelte-spa-router";
  import X from "@lucide/svelte/icons/x";
  import Image from "@lucide/svelte/icons/image";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import { apiJSON, ApiError } from "../lib/api";
  import { fileExtension, readFileAsBase64 } from "../lib/files";
  import { formatBytes } from "../lib/format";
  import type { Link } from "../lib/types";
  import { m } from "../paraglide/messages";

  // The largest favicon the server accepts (maxManualLinkFaviconBytes in
  // internal/httpapi). Checked here only so an obviously too-large file isn't
  // read and sent just to be refused; the server enforces the real limit.
  const MAX_FAVICON_BYTES = 512 * 1024;

  let { open = $bindable(false) }: { open?: boolean } = $props();

  let url = $state("");
  let title = $state("");
  let faviconFile = $state<File | null>(null);
  let submitting = $state(false);
  let error = $state<string | null>(null);
  // Set when the server says this URL is already a link, so the message can
  // point at it instead of just reporting the conflict.
  let existingLinkId = $state<number | null>(null);
  let urlInputEl = $state<HTMLInputElement | undefined>();

  $effect(() => {
    if (open) urlInputEl?.focus();
  });

  function resetAndClose() {
    open = false;
    url = "";
    title = "";
    faviconFile = null;
    error = null;
    existingLinkId = null;
  }

  function handleKeydown(e: KeyboardEvent) {
    if (open && e.key === "Escape" && !submitting) {
      resetAndClose();
    }
  }

  function onFaviconChange(e: Event & { currentTarget: HTMLInputElement }) {
    faviconFile = e.currentTarget.files?.[0] ?? null;
  }

  // Also clears the underlying <input>'s value, not just the $state reference:
  // otherwise re-selecting the exact same file right after removing it wouldn't
  // fire another change event.
  function clearFaviconFile(input: HTMLInputElement) {
    faviconFile = null;
    input.value = "";
  }

  async function handleSubmit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    existingLinkId = null;

    const trimmedURL = url.trim();
    if (!trimmedURL) {
      error = m.manualupload_error_url_required();
      return;
    }
    if (faviconFile && faviconFile.size > MAX_FAVICON_BYTES) {
      error = m.addlink_error_favicon_too_large({
        size: formatBytes(MAX_FAVICON_BYTES),
      });
      return;
    }

    submitting = true;
    try {
      const body: Record<string, string> = { url: trimmedURL };
      if (title.trim()) body.title = title.trim();
      if (faviconFile) {
        body.favicon_ext = fileExtension(faviconFile.name);
        body.favicon_data = await readFileAsBase64(faviconFile);
      }
      const created = await apiJSON<Link>("/links", {
        method: "POST",
        body,
      });
      resetAndClose();
      await push(`/links/${created.id}`);
    } catch (err) {
      if (
        err instanceof ApiError &&
        err.status === 409 &&
        typeof err.details?.link_id === "number"
      ) {
        existingLinkId = err.details.link_id;
      } else {
        error =
          err instanceof ApiError
            ? err.message
            : m.manualupload_error_generic();
      }
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
      aria-labelledby="add-link-title"
    >
      <div class="modal-header">
        <h2 id="add-link-title">{m.addlink_modal_title()}</h2>
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
      <p class="subtitle">{m.addlink_modal_subtitle()}</p>

      <form onsubmit={handleSubmit}>
        {#if error}
          <p class="error" role="alert">
            <AlertCircle size={15} />
            <span>{error}</span>
          </p>
        {/if}
        {#if existingLinkId !== null}
          <p class="error" role="alert">
            <AlertCircle size={15} />
            <span
              >{m.addlink_exists()}
              <a
                href={`/links/${existingLinkId}`}
                use:link
                onclick={resetAndClose}>{m.addlink_open_existing()}</a
              ></span
            >
          </p>
        {/if}

        <div class="field">
          <label for="add-link-url">{m.manualupload_url_label()}</label>
          <input
            id="add-link-url"
            type="url"
            bind:value={url}
            bind:this={urlInputEl}
            placeholder={m.manualupload_url_placeholder()}
            disabled={submitting}
          />
        </div>

        <div class="field">
          <label for="add-link-title-input">{m.addlink_title_label()}</label>
          <input
            id="add-link-title-input"
            type="text"
            bind:value={title}
            placeholder={m.addlink_title_placeholder()}
            disabled={submitting}
          />
        </div>

        <div class="field">
          <label for="add-link-favicon">{m.manualupload_favicon_label()}</label>
          <div class="file-row">
            <label
              class="file-drop"
              class:has-file={faviconFile}
              for="add-link-favicon"
            >
              <Image size={16} />
              <span class="filename"
                >{faviconFile
                  ? faviconFile.name
                  : m.manualupload_choose_file()}</span
              >
            </label>
            <input
              id="add-link-favicon"
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
                    "add-link-favicon",
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
            >{submitting ? m.addlink_submitting() : m.addlink_submit()}</button
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

  input[type="url"],
  input[type="text"] {
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

    a {
      color: inherit;
      text-decoration: underline;

      &:focus-visible {
        @include mix.focus-ring;
      }
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
