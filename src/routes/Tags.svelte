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
<!-- Flat list, unlike Collections' tree -- tags have no hierarchy. No
     "create a tag" form here, unlike Collections: there's no standalone
     tag-creation endpoint at all, tags only ever come into existence
     through AddPageTag (a page's own tag field) or the AI job, so this
     screen is rename/delete only. Deletion uses a plain confirm(), same
     not-fancy-first-pass spirit as Collections.svelte.

     The slug field starts collapsed behind a preview of what it would
     auto-become from the current name (edit icon reveals a real input) --
     renaming re-derives the slug from the new name by default; an
     explicit override is only sent if that field was actually opened and
     something typed into it. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import Archive from "@lucide/svelte/icons/archive";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type { Tag, TagListResponse } from "../lib/types";
  import { previewSlug } from "../lib/slugPreview";
  import { m } from "../paraglide/messages";

  const SLUG_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/;

  let tags = $state<Tag[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  let editingId = $state<number | null>(null);
  let editingName = $state("");
  let editingSlug = $state("");
  let slugFieldOpen = $state(false);
  let savingRename = $state(false);

  let deletingId = $state<number | null>(null);

  let slugPreview = $derived(previewSlug(editingName));
  let slugValid = $derived(
    !slugFieldOpen ||
      editingSlug.trim() === "" ||
      SLUG_PATTERN.test(editingSlug.trim()),
  );

  async function loadTags() {
    loading = true;
    loadError = null;
    try {
      const res = await apiJSON<TagListResponse>("/tags");
      tags = res.tags;
    } catch (err) {
      loadError = err instanceof ApiError ? err.message : m.tags_load_error();
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    loadTags();
  });

  function startRename(tag: Tag) {
    editingId = tag.id;
    editingName = tag.name;
    editingSlug = "";
    slugFieldOpen = false;
    actionError = null;
  }

  function openSlugField() {
    editingSlug = slugPreview;
    slugFieldOpen = true;
  }

  async function handleRename(event: SubmitEvent, id: number) {
    event.preventDefault();
    const name = editingName.trim();
    if (!name || !slugValid) return;
    savingRename = true;
    actionError = null;
    try {
      const slug = editingSlug.trim();
      const body: { name: string; slug?: string } =
        slugFieldOpen && slug ? { name, slug } : { name };
      const updated = await apiJSON<Tag>(`/tags/${id}`, {
        method: "PATCH",
        body,
      });
      tags = tags.map((t) => (t.id === id ? updated : t));
      editingId = null;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.tags_rename_error();
    } finally {
      savingRename = false;
    }
  }

  async function handleDelete(tag: Tag) {
    if (!confirm(m.tags_delete_confirm({ name: tag.name }))) return;

    deletingId = tag.id;
    actionError = null;
    try {
      await apiJSON(`/tags/${tag.id}`, { method: "DELETE" });
      tags = tags.filter((t) => t.id !== tag.id);
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.tags_delete_error();
    } finally {
      deletingId = null;
    }
  }
</script>

<main class="screen">
  <AppHeader />
  <p class="page-heading">{m.nav_tags()}</p>

  {#if actionError}
    <p class="status error" role="alert">
      <AlertCircle size={15} />
      <span>{actionError}</span>
    </p>
  {/if}

  {#if loading}
    <p class="status">{m.common_loading()}</p>
  {:else if loadError}
    <div class="status-block error" role="alert">
      <AlertCircle size={28} />
      <span>{loadError}</span>
    </div>
  {:else if tags.length === 0}
    <div class="status-block">
      <Archive size={28} />
      <span>{m.tags_no_tags()}</span>
    </div>
  {:else}
    <ul class="tags">
      {#each tags as tag (tag.id)}
        <li class="row">
          {#if editingId === tag.id}
            <form class="inline-form" onsubmit={(e) => handleRename(e, tag.id)}>
              <div class="edit-fields">
                <input
                  type="text"
                  bind:value={editingName}
                  disabled={savingRename}
                />
                {#if slugFieldOpen}
                  <div class="slug-field">
                    <input
                      type="text"
                      class:invalid={!slugValid}
                      placeholder={m.tags_slug_placeholder()}
                      bind:value={editingSlug}
                      disabled={savingRename}
                    />
                    {#if !slugValid}
                      <span class="slug-error">{m.tags_slug_invalid()}</span>
                    {/if}
                  </div>
                {:else}
                  <button
                    type="button"
                    class="slug-preview"
                    onclick={openSlugField}
                  >
                    <Pencil size={10} />
                    {m.tags_slug_preview({ slug: slugPreview })}
                  </button>
                {/if}
              </div>
              <div class="row-actions">
                <button
                  type="submit"
                  disabled={savingRename || !editingName.trim() || !slugValid}
                  >{m.common_save()}</button
                >
                <button
                  type="button"
                  onclick={() => (editingId = null)}
                  disabled={savingRename}>{m.common_cancel()}</button
                >
              </div>
            </form>
          {:else}
            <a class="name" href={`/tags/${tag.slug}`} use:link>
              {tag.name}
              <span class="slug">/tags/{tag.slug}</span>
            </a>
            <div class="row-actions">
              <button
                type="button"
                class="icon-btn"
                aria-label={m.tags_rename_aria({ name: tag.name })}
                onclick={() => startRename(tag)}
              >
                <Pencil size={14} />
              </button>
              <button
                type="button"
                class="icon-btn danger"
                aria-label={m.tags_delete_aria({ name: tag.name })}
                onclick={() => handleDelete(tag)}
                disabled={deletingId === tag.id}
              >
                <Trash2 size={14} />
              </button>
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</main>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;

  .screen {
    max-width: 48rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  .page-heading {
    @include type.eyebrow;
    margin: 0 0 1.25rem;
  }

  .status {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    color: var(--ink-muted);
    margin-bottom: 1rem;

    &.error {
      color: var(--accent);
    }
  }

  .status-block {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.6rem;
    padding: 2.5rem 1rem;
    color: var(--ink-muted);
    text-align: center;

    &.error {
      color: var(--accent);
    }

    :global(svg) {
      opacity: 0.6;
    }
  }

  .tags {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px dotted var(--rule);
  }

  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.7rem 0.25rem;
    @include mix.dotted-rule;
  }

  .name {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    font-size: 0.9375rem;
    color: inherit;
    text-decoration: none;

    &:hover {
      color: var(--accent);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .slug {
    @include type.data-mono;
    font-size: 0.72rem;
    color: var(--ink-muted);
  }

  .inline-form {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 0.75rem;
    width: 100%;
  }

  .edit-fields {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }

  .slug-field {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;

    input {
      @include type.data-mono;
      font-size: 0.8rem;
    }
  }

  .slug-error {
    font-size: 0.72rem;
    color: var(--accent);
  }

  .slug-preview {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    width: fit-content;
    padding: 0.125rem 0;
    border: none;
    background: none;
    color: var(--ink-muted);
    @include type.data-mono;
    font-size: 0.72rem;
    text-decoration: underline dotted;
    cursor: pointer;

    &:hover {
      color: var(--accent);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  input[type="text"] {
    padding: 0.4rem 0.6rem;
    border: 1px solid var(--rule);
    border-radius: 4px;
    background: var(--paper-raised);
    color: var(--ink);
    font: inherit;
    font-size: 0.875rem;

    &.invalid {
      border-color: var(--accent);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  button {
    padding: 0.375rem 0.75rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper-raised);
    color: var(--ink);
    font: inherit;
    font-size: 0.8125rem;
    cursor: pointer;

    &:disabled {
      opacity: 0.5;
      cursor: default;
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.8rem;
    height: 1.8rem;
    padding: 0;
    border: none;
    border-radius: 50%;
    background: transparent;
    color: var(--ink-muted);

    &:hover:not(:disabled) {
      color: var(--accent);
      background: var(--paper-raised);
    }
  }

  .row-actions {
    display: flex;
    gap: 0.3rem;
    flex-shrink: 0;
  }
</style>
