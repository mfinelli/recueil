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
<!-- A link's details screen: the counterpart of PageDetail minus everything a
     link doesn't have: captures, recapture, AI tags, linked pages, versions,
     and the mirror-exclusion toggle. What's left is the same read/write loop,
     working from each write's response rather than refetching: title and notes
     editing, tag and collection add/remove, and delete.

     The source row shows and opens the link's raw_url, not its normalized_url
     (which drops the fragment, breaking hash-routed apps), unlike a page's,
     which opens its normalized one. The title editor carries a hint because a
     title edit is a plain overwrite: the next save of the link from the browser
     replaces it, the way a recapture replaces a page's. -->
<script lang="ts">
  import { link, push } from "svelte-spa-router";
  import { apiJSON, ApiError } from "../lib/api";
  import { renderMarkdown } from "../lib/markdown";
  import {
    availableCollections as availableIn,
    collectionPath as pathIn,
    collectionNamePath as namePathIn,
  } from "../lib/collections";
  import AppHeader from "../components/AppHeader.svelte";
  import Favicon from "../components/Favicon.svelte";
  import RelatedItems from "../components/RelatedItems.svelte";
  import SameUrlHint from "../components/SameUrlHint.svelte";
  import WaybackBlock from "../components/WaybackBlock.svelte";
  import ChevronLeft from "@lucide/svelte/icons/chevron-left";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import ExternalLink from "@lucide/svelte/icons/external-link";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Check from "@lucide/svelte/icons/check";
  import Plus from "@lucide/svelte/icons/plus";
  import X from "@lucide/svelte/icons/x";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import type {
    Link,
    LinkDetail,
    TagCreated,
    Collection,
    CollectionListResponse,
  } from "../lib/types";
  import { m } from "../paraglide/messages";

  let { params }: { params: { id: string } } = $props();

  let detail = $state<LinkDetail | null>(null);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  // Supplementary data for the collection picker, fetched alongside the link
  // but best-effort: a failure here just leaves the picker with fewer options.
  let allCollections = $state<Collection[]>([]);

  let tagInput = $state("");
  let addingTag = $state(false);
  let selectedCollectionId = $state("");
  let addingToCollection = $state(false);
  let newCollectionName = $state("");
  let creatingCollection = $state(false);
  let editingTitle = $state(false);
  let titleInput = $state("");
  let savingTitle = $state(false);
  let editingNotes = $state(false);
  let notesInput = $state("");
  let savingNotes = $state(false);
  let deleting = $state(false);
  let togglingMirror = $state(false);
  let cleaning = $state(false);
  // Related links' own state (the pencil, the search) lives in RelatedItems; these
  // are its labels, so the strings stay with the rest of this screen's messages.
  const relatedLabels = $derived({
    heading: m.linkdetail_related_heading(),
    empty: m.linkdetail_related_empty(),
    edit: m.linkdetail_edit_related(),
    done: m.linkdetail_done_editing_related(),
    searchPlaceholder: m.linkdetail_related_search_placeholder(),
    noResults: m.linkdetail_related_no_results(),
    remove: (name: string) => m.linkdetail_remove_related_aria({ name }),
    relateError: m.linkdetail_related_error(),
    removeError: m.linkdetail_remove_related_error(),
  });
  // Independent per section -- editing tags shouldn't pop open the collection
  // forms. Pills-only by default; the pencil/check toggle reveals the remove
  // buttons and add forms.
  let editingTags = $state(false);
  let editingCollections = $state(false);

  $effect(() => {
    const id = params.id;
    loading = true;
    loadError = null;
    actionError = null;
    detail = null;
    editingTitle = false;
    editingTags = false;
    editingCollections = false;
    editingNotes = false;

    Promise.allSettled([
      apiJSON<LinkDetail>(`/links/${id}`),
      apiJSON<CollectionListResponse>("/collections"),
    ]).then(([linkResult, collectionsResult]) => {
      if (linkResult.status === "fulfilled") {
        detail = linkResult.value;
      } else {
        loadError =
          linkResult.reason instanceof ApiError
            ? linkResult.reason.message
            : m.linkdetail_load_error();
      }
      allCollections =
        collectionsResult.status === "fulfilled"
          ? collectionsResult.value.collections
          : [];
      loading = false;
    });
  });

  function displayTitle(l: LinkDetail): string {
    return l.title ?? l.normalized_url;
  }

  function startEditingTitle() {
    if (!detail) return;
    titleInput = displayTitle(detail);
    editingTitle = true;
  }

  function cancelEditingTitle() {
    editingTitle = false;
  }

  async function saveTitle(event: SubmitEvent) {
    event.preventDefault();
    if (!detail) return;
    const trimmed = titleInput.trim();
    if (!trimmed) return;
    savingTitle = true;
    actionError = null;
    try {
      const updated = await apiJSON<LinkDetail>(`/links/${detail.id}`, {
        method: "PATCH",
        body: { title: trimmed },
      });
      detail.title = updated.title;
      editingTitle = false;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_title_error();
    } finally {
      savingTitle = false;
    }
  }

  function startEditingNotes() {
    if (!detail) return;
    notesInput = detail.notes ?? "";
    editingNotes = true;
  }

  function cancelEditingNotes() {
    editingNotes = false;
  }

  // An empty/whitespace-only value is a valid save: it clears the note rather
  // than being rejected, matching the backend's trim-then-nullify handling.
  async function saveNotes(event: SubmitEvent) {
    event.preventDefault();
    if (!detail) return;
    savingNotes = true;
    actionError = null;
    try {
      const updated = await apiJSON<LinkDetail>(`/links/${detail.id}`, {
        method: "PATCH",
        body: { notes: notesInput.trim() },
      });
      detail.notes = updated.notes;
      editingNotes = false;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_notes_error();
    } finally {
      savingNotes = false;
    }
  }

  async function addTag(event: SubmitEvent) {
    event.preventDefault();
    const name = tagInput.trim();
    if (!name || !detail) return;
    addingTag = true;
    actionError = null;
    try {
      const created = await apiJSON<TagCreated>(`/links/${detail.id}/tags`, {
        method: "POST",
        body: { name },
      });
      detail.tags = [
        ...detail.tags,
        { id: created.id, name: created.name, slug: created.slug },
      ].sort((a, b) => a.name.localeCompare(b.name));
      tagInput = "";
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_tag_error();
    } finally {
      addingTag = false;
    }
  }

  async function removeTag(tagId: number) {
    if (!detail) return;
    actionError = null;
    try {
      await apiJSON(`/links/${detail.id}/tags/${tagId}`, { method: "DELETE" });
      detail.tags = detail.tags.filter((t) => t.id !== tagId);
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_remove_tag_error();
    }
  }

  async function linkToCollection(collection: Collection) {
    if (!detail) return;
    await apiJSON(`/links/${detail.id}/collections`, {
      method: "POST",
      body: { collection_id: collection.id },
    });
    detail.collections = [
      ...detail.collections,
      {
        id: collection.id,
        name: collection.name,
        parent_id: collection.parent_id,
      },
    ].sort((a, b) => a.name.localeCompare(b.name));
  }

  async function addToCollection(event: SubmitEvent) {
    event.preventDefault();
    if (!detail || selectedCollectionId === "") return;
    const collection = allCollections.find(
      (c) => c.id === Number(selectedCollectionId),
    );
    if (!collection) return;

    addingToCollection = true;
    actionError = null;
    try {
      await linkToCollection(collection);
      selectedCollectionId = "";
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.pagedetail_add_to_collection_error();
    } finally {
      addingToCollection = false;
    }
  }

  // Top-level only, as on PageDetail: there's no parent picker here, so nesting
  // from this form would be choosing a parent blind.
  async function createAndAddCollection(event: SubmitEvent) {
    event.preventDefault();
    const name = newCollectionName.trim();
    if (!name || !detail) return;

    creatingCollection = true;
    actionError = null;
    try {
      const created = await apiJSON<Collection>("/collections", {
        method: "POST",
        body: { name },
      });
      allCollections = [...allCollections, created];
      await linkToCollection(created);
      newCollectionName = "";
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.collections_create_error();
    } finally {
      creatingCollection = false;
    }
  }

  async function removeFromCollection(collectionId: number) {
    if (!detail) return;
    actionError = null;
    try {
      await apiJSON(`/links/${detail.id}/collections/${collectionId}`, {
        method: "DELETE",
      });
      detail.collections = detail.collections.filter(
        (c) => c.id !== collectionId,
      );
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.pagedetail_remove_from_collection_error();
    }
  }

  // The checkbox is framed as the positive (synced), so its PATCH body is the
  // inverse of what it shows -- as on PageDetail.
  async function toggleExcludedFromMirror() {
    if (!detail) return;
    togglingMirror = true;
    actionError = null;
    const next = !detail.excluded_from_mirror;
    try {
      await apiJSON(`/links/${detail.id}`, {
        method: "PATCH",
        body: { excluded_from_mirror: next },
      });
      detail.excluded_from_mirror = next;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_mirror_error();
    } finally {
      togglingMirror = false;
    }
  }

  // Swaps in the cleaned URL. The server computes it (the client can't supply
  // one), so all this sends is the request; what comes back is the link with its new
  // raw_url, after which there is nothing more to clean.
  async function useCleanedURL() {
    if (!detail) return;
    cleaning = true;
    actionError = null;
    try {
      const updated = await apiJSON<Link>(`/links/${detail.id}/clean-url`, {
        method: "POST",
      });
      detail.raw_url = updated.raw_url;
      detail.cleaned_url = null;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.linkdetail_clean_error();
    } finally {
      cleaning = false;
    }
  }

  async function deleteLink() {
    if (!detail) return;
    if (!confirm(m.linkdetail_delete_confirm({ title: displayTitle(detail) })))
      return;

    deleting = true;
    actionError = null;
    try {
      await apiJSON(`/links/${detail.id}`, { method: "DELETE" });
      await push("/links");
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.linkdetail_delete_error();
      deleting = false;
    }
  }
</script>

<main class="screen">
  <AppHeader />
  <a class="back" href="/links" use:link>
    <ChevronLeft size={14} />
    {m.nav_links()}
  </a>

  {#if loading}
    <p class="status">{m.common_loading()}</p>
  {:else if loadError}
    <div class="status-error" role="alert">
      <AlertCircle size={28} />
      <span>{loadError}</span>
    </div>
  {:else if detail}
    {#if editingTitle}
      <form class="title-edit" onsubmit={saveTitle}>
        <input
          type="text"
          placeholder={m.pagedetail_title_placeholder()}
          bind:value={titleInput}
          disabled={savingTitle}
        />
        <p class="title-hint">{m.linkdetail_title_hint()}</p>
        <div class="title-edit-actions">
          <button type="submit" disabled={savingTitle || !titleInput.trim()}
            >{m.common_save()}</button
          >
          <button
            type="button"
            onclick={cancelEditingTitle}
            disabled={savingTitle}>{m.common_cancel()}</button
          >
        </div>
      </form>
    {:else}
      <div class="title-row">
        <h1>{displayTitle(detail)}</h1>
        <button
          type="button"
          class="icon-btn"
          aria-label={m.pagedetail_edit_title_aria()}
          onclick={startEditingTitle}
        >
          <Pencil size={14} />
        </button>
      </div>
    {/if}

    <div class="source-row">
      <!-- As on PageDetail: one image beside the URL has no list to keep aligned,
           so it shows nothing rather than a placeholder when there's no favicon. -->
      <Favicon
        src={detail.favicon_path !== null
          ? `/api/links/${detail.id}/favicon`
          : null}
        size={16}
        placeholder={false}
      />
      <a
        class="source-url"
        href={detail.raw_url}
        target="_blank"
        rel="noreferrer">{detail.raw_url}<ExternalLink size={12} /></a
      >
      {#if detail.source === "manual"}
        <span class="source-note">{m.linkdetail_added_manually()}</span>
      {/if}
    </div>

    {#if detail.cleaned_url}
      <div class="clean-url">
        <span class="clean-label">{m.linkdetail_clean_label()}</span>
        <code class="clean-value">{detail.cleaned_url}</code>
        <button type="button" disabled={cleaning} onclick={useCleanedURL}
          >{m.linkdetail_clean_use()}</button
        >
        <p class="clean-hint">{m.linkdetail_clean_hint()}</p>
      </div>
    {/if}

    {#if detail.matching_page_id !== null}
      <SameUrlHint
        kind="page"
        id={detail.matching_page_id}
        title={detail.matching_page_title ?? detail.normalized_url}
      />
    {/if}

    {#if actionError}
      <p class="status error" role="alert">
        <AlertCircle size={15} />
        <span>{actionError}</span>
      </p>
    {/if}

    <label class="mirror-toggle">
      <input
        type="checkbox"
        checked={!detail.excluded_from_mirror}
        disabled={togglingMirror}
        onchange={toggleExcludedFromMirror}
      />
      {m.pagedetail_mirror_toggle_label()}
    </label>

    <section>
      <div class="block-header">
        <h2>{m.pagedetail_tags_heading()}</h2>
        <button
          type="button"
          class="edit-toggle"
          class:active={editingTags}
          aria-label={editingTags
            ? m.pagedetail_done_editing_tags()
            : m.pagedetail_edit_tags()}
          onclick={() => (editingTags = !editingTags)}
        >
          {#if editingTags}
            <Check size={13} />
          {:else}
            <Pencil size={13} />
          {/if}
        </button>
      </div>
      <ul class="chips" class:editing={editingTags}>
        {#each detail.tags as tag (tag.id)}
          <li>
            {#if editingTags}
              {tag.name}
            {:else}
              <a href={`/tags/${tag.slug}`} use:link>{tag.name}</a>
            {/if}
            {#if editingTags}
              <button
                type="button"
                class="remove"
                aria-label={m.pagedetail_remove_tag_aria({ name: tag.name })}
                onclick={() => removeTag(tag.id)}
              >
                <X size={11} />
              </button>
            {/if}
          </li>
        {/each}
      </ul>
      {#if editingTags}
        <form class="inline-form" onsubmit={addTag}>
          <input
            type="text"
            placeholder={m.pagedetail_add_tag_placeholder()}
            bind:value={tagInput}
            disabled={addingTag}
          />
          <button type="submit" disabled={addingTag || !tagInput.trim()}>
            <Plus size={13} />
            {m.common_add()}
          </button>
        </form>
      {/if}
    </section>

    <section>
      <div class="block-header">
        <h2>{m.nav_collections()}</h2>
        <button
          type="button"
          class="edit-toggle"
          class:active={editingCollections}
          aria-label={editingCollections
            ? m.pagedetail_done_editing_collections()
            : m.pagedetail_edit_collections()}
          onclick={() => (editingCollections = !editingCollections)}
        >
          {#if editingCollections}
            <Check size={13} />
          {:else}
            <Pencil size={13} />
          {/if}
        </button>
      </div>
      <ul class="chips" class:editing={editingCollections}>
        {#each detail.collections as collection (collection.id)}
          {@const path = pathIn(allCollections, collection.id)}
          <li>
            {#if editingCollections || path === null}
              {collection.name}
            {:else}
              <a href={`/collections/${path}`} use:link>{collection.name}</a>
            {/if}
            {#if editingCollections}
              <button
                type="button"
                class="remove"
                aria-label={m.pagedetail_remove_from_collection_aria({
                  name: collection.name,
                })}
                onclick={() => removeFromCollection(collection.id)}
              >
                <X size={11} />
              </button>
            {/if}
          </li>
        {/each}
      </ul>
      {#if editingCollections}
        {@const available = availableIn(
          allCollections,
          detail.collections.map((c) => c.id),
        )}
        {#if available.length > 0}
          <form class="inline-form" onsubmit={addToCollection}>
            <select
              bind:value={selectedCollectionId}
              disabled={addingToCollection}
            >
              <option value=""
                >{m.pagedetail_add_to_collection_placeholder()}</option
              >
              {#each available as collection (collection.id)}
                <option value={collection.id}
                  >{namePathIn(allCollections, collection.id)}</option
                >
              {/each}
            </select>
            <button
              type="submit"
              disabled={addingToCollection || selectedCollectionId === ""}
              >{m.common_add()}</button
            >
          </form>
        {/if}
        <form class="inline-form" onsubmit={createAndAddCollection}>
          <input
            type="text"
            placeholder={m.pagedetail_new_collection_placeholder()}
            bind:value={newCollectionName}
            disabled={creatingCollection}
          />
          <button
            type="submit"
            disabled={creatingCollection || !newCollectionName.trim()}
            >{m.pagedetail_create_and_add()}</button
          >
        </form>
      {/if}
    </section>

    <section>
      <div class="block-header">
        <h2>{m.pagedetail_notes_heading()}</h2>
        {#if !editingNotes}
          <button
            type="button"
            class="edit-toggle"
            aria-label={m.pagedetail_edit_notes()}
            onclick={startEditingNotes}
          >
            <Pencil size={13} />
          </button>
        {/if}
      </div>
      {#if editingNotes}
        <form class="notes-edit" onsubmit={saveNotes}>
          <textarea
            rows="5"
            placeholder={m.pagedetail_notes_placeholder()}
            bind:value={notesInput}
            disabled={savingNotes}></textarea>
          <p class="notes-hint">{m.pagedetail_notes_hint()}</p>
          <div class="title-edit-actions">
            <button type="submit" disabled={savingNotes}
              >{m.common_save()}</button
            >
            <button
              type="button"
              onclick={cancelEditingNotes}
              disabled={savingNotes}>{m.common_cancel()}</button
            >
          </div>
        </form>
      {:else if detail.notes}
        <!-- renderMarkdown only ever emits <strong>/<em>/<ul>/<li>/<p>/<br>
             wrapping HTML-escaped text -- there's no path from detail.notes to
             an injected tag/attribute, which is what this rule exists to
             catch. -->
        <!-- eslint-disable-next-line svelte/no-at-html-tags -->
        <div class="notes-body">{@html renderMarkdown(detail.notes)}</div>
      {:else}
        <p class="empty-note">{m.pagedetail_notes_empty()}</p>
      {/if}
    </section>

    <RelatedItems
      items={detail.related_links}
      kind="link"
      ownerPath={`/links/${detail.id}`}
      excludeId={detail.id}
      labels={relatedLabels}
      onchange={(next) => {
        if (detail) detail.related_links = next;
      }}
      onerror={(message) => (actionError = message)}
    />

    <WaybackBlock
      url={detail.normalized_url}
      wayback={detail.wayback}
      submitPath={`/links/${detail.id}/wayback`}
      onchange={(next) => {
        if (detail) detail.wayback = next;
      }}
    />

    <div class="actions-row">
      <button
        type="button"
        class="danger"
        onclick={deleteLink}
        disabled={deleting}
      >
        <Trash2 size={14} />
        {m.linkdetail_delete()}
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
    margin-bottom: 1rem;
  }

  // Matches Library's own load-error treatment exactly -- one consistent
  // full-block error pattern app-wide.
  .status-error {
    @include comp.status-block;
    color: var(--accent);

    :global(svg) {
      opacity: 0.6;
    }
  }

  .title-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.4rem;
  }

  h1 {
    @include type.heading;
    font-size: 1.6rem;
    margin: 0;
  }

  .icon-btn {
    @include comp.icon-btn(1.5rem);
  }

  // The input takes the exact place of the h1 it replaces, as on PageDetail,
  // with the replace-on-resave hint and Save/Cancel on their own rows below.
  .title-edit {
    margin-bottom: 0.4rem;

    input {
      display: block;
      width: 100%;
      @include type.heading;
      font-size: 1.6rem;
      padding: 0.2rem 0.4rem;
      margin-bottom: 0.5rem;
    }
  }

  .title-hint {
    margin: 0 0 0.6rem;
    color: var(--ink-muted);
    font-size: 0.75rem;
  }

  .title-edit-actions {
    display: flex;
    gap: 0.5rem;
  }

  .source-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin-bottom: 1.25rem;
  }

  .source-url {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.85rem;
    word-break: break-all;
    text-decoration: none;

    &:hover {
      color: var(--accent);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  // The "cleaner URL" offer under the source row: a muted line with the cleaned URL
  // and the one action, and the replaced-on-resave hint beneath.
  .clean-url {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.4rem 0.6rem;
    margin: -0.6rem 0 1.25rem;
    font-size: 0.8125rem;
    color: var(--ink-muted);

    button {
      padding: 0.2rem 0.6rem;
      font-size: 0.8125rem;
    }
  }

  .clean-value {
    @include type.data-mono;
    word-break: break-all;
    color: var(--ink);
  }

  .clean-hint {
    flex-basis: 100%;
    margin: 0;
    font-size: 0.75rem;
  }

  // Where the title and favicon came from, when it isn't a browser tab: the
  // same small mono, muted treatment as a capture's "manual upload" label.
  .source-note {
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.75rem;
    white-space: nowrap;
  }

  section {
    margin-bottom: 1.75rem;
  }

  .block-header {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin-bottom: 0.6rem;
  }

  h2 {
    @include type.eyebrow;
    margin: 0;
  }

  .edit-toggle {
    @include comp.icon-btn(1.4rem);

    &.active {
      color: var(--accent-success);
    }
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    list-style: none;
    margin: 0 0 0.65rem;
    padding: 0;

    li {
      @include comp.pill;
      display: flex;
      align-items: center;
      gap: 0.3rem;
      padding: 0.2rem 0.65rem;
      font-size: 0.78rem;

      a {
        color: inherit;
        text-decoration: none;

        &:hover {
          color: var(--accent);
          text-decoration: underline;
        }

        &:focus-visible {
          @include mix.focus-ring;
        }
      }
    }

    &.editing li {
      padding-right: 0.3rem;
    }
  }

  .remove {
    @include comp.icon-btn(1.1rem, rgba(0, 0, 0, 0.05));
  }

  .inline-form {
    display: flex;
    gap: 0.5rem;
  }

  input[type="text"],
  select {
    @include comp.text-input;
    padding: 0.375rem 0.5rem;
    border-radius: 4px;
    font-size: 0.875rem;
  }

  button {
    @include comp.bordered-button;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.375rem 0.75rem;
    font-size: 0.875rem;
  }

  .notes-body {
    @include mix.card-surface;
    padding: 0.85rem 1rem;
    font-size: 0.9rem;
    line-height: 1.6;

    :global(p) {
      margin: 0 0 0.6rem;

      &:last-child {
        margin-bottom: 0;
      }
    }

    :global(ul) {
      margin: 0 0 0.6rem;
      padding-left: 1.2rem;

      &:last-child {
        margin-bottom: 0;
      }
    }
  }

  .empty-note {
    color: var(--ink-muted);
    font-size: 0.8125rem;
    font-style: italic;
  }

  .notes-edit textarea {
    @include comp.text-input;
    display: block;
    width: 100%;
    padding: 0.65rem 0.75rem;
    border-radius: 4px;
    font-size: 0.9rem;
    line-height: 1.55;
    resize: vertical;
  }

  .notes-hint {
    margin: 0.4rem 0 0;
    color: var(--ink-muted);
    font-size: 0.75rem;
  }

  // Delete only, so it sits at the end of the row rather than spreading apart
  // from a recapture button as on PageDetail.
  .mirror-toggle {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 1.75rem;
    font-size: 0.875rem;
    color: var(--ink-muted);

    input {
      accent-color: var(--accent-success);
      width: 15px;
      height: 15px;
    }
  }

  .actions-row {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 1rem;
    margin-top: 1.75rem;
    padding-top: 0.9rem;
    border-top: 1px dotted var(--rule);
  }

  .danger {
    color: var(--accent);
    border-color: var(--accent);

    &:hover:not(:disabled) {
      background: var(--accent);
      color: var(--paper);
    }
  }
</style>
