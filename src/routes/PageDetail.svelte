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
<!-- The full read/write loop: tag add/remove, collection add/remove, the
     excluded_from_mirror toggle, title editing, delete, and manual
     recapture all call their real backend endpoints. page/collections are
     updated optimistically from each write's response rather than
     refetching the whole page afterward -- a normal tradeoff for a
     single-user personal tool, not something defended against
     concurrent-editor conflicts.

     Recapture (POST /pages/{id}/recapture) doesn't touch `page` at all --
     it only re-enqueues the latest capture's URL for a device to pick up
     later, so its button just shows a transient "queued" confirmation,
     same pattern as Devices.svelte's copy-to-clipboard button. Delete
     navigates back to the library on success, same as Devices'/Tags'
     confirm()-gated deletes but the first one on this screen that leaves the
     page entirely afterward.

     Capture rows now link to the in-app reader view (/captures/{id}) --
     the raw archived HTML itself still opens as a plain new-tab link, but
     from inside that reader view now, not directly from this list.

     The language picker's options are labeled in the dashboard's
     current locale (lib/languageNames.ts), not the raw pg_ts_config name
     GET /api/text-search-configs actually returns -- explicitly the
     opposite direction from Settings' language picker, which shows
     each option self-named so you can recognize *your own* language among
     others; here you're already reading the dashboard in your language and
     labeling someone else's content, so every option is translated into
     that same language. "simple" (not a real language, Postgres's own name
     for "no language-specific stemming") is relabeled "Other" rather than
     run through that translation at all. -->
<script lang="ts">
  import { link, push } from "svelte-spa-router";
  import { apiJSON, ApiError } from "../lib/api";
  import { formatBytes } from "../lib/format";
  import { renderMarkdown } from "../lib/markdown";
  import {
    availableCollections as availableIn,
    collectionPath as pathIn,
    collectionNamePath as namePathIn,
  } from "../lib/collections";
  import AppHeader from "../components/AppHeader.svelte";
  import SameUrlHint from "../components/SameUrlHint.svelte";
  import WaybackBlock from "../components/WaybackBlock.svelte";
  import ChevronLeft from "@lucide/svelte/icons/chevron-left";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import ExternalLink from "@lucide/svelte/icons/external-link";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Check from "@lucide/svelte/icons/check";
  import Plus from "@lucide/svelte/icons/plus";
  import X from "@lucide/svelte/icons/x";
  import Sparkles from "@lucide/svelte/icons/sparkles";
  import RotateCw from "@lucide/svelte/icons/rotate-cw";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import Upload from "@lucide/svelte/icons/upload";
  import Favicon from "../components/Favicon.svelte";
  import RelatedItems from "../components/RelatedItems.svelte";
  import { session } from "../lib/session.svelte";
  import type {
    PageDetail,
    TagCreated,
    Collection,
    CollectionListResponse,
  } from "../lib/types";
  import { m } from "../paraglide/messages";

  let { params }: { params: { id: string } } = $props();

  let page = $state<PageDetail | null>(null);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  // Supplementary metadata for the write-action UI (the "add to
  // collection" picker's options) -- fetched alongside the page itself,
  // but best-effort: a failure here shouldn't block viewing the page,
  // just leave the picker with fewer options.
  let allCollections = $state<Collection[]>([]);

  let tagInput = $state("");
  let addingTag = $state(false);
  let selectedCollectionId = $state("");
  let addingToCollection = $state(false);
  let togglingMirror = $state(false);
  let editingTitle = $state(false);
  let titleInput = $state("");
  let savingTitle = $state(false);
  let editingNotes = $state(false);
  let notesInput = $state("");
  let savingNotes = $state(false);
  let recapturing = $state(false);
  let recaptureQueued = $state(false);
  let deleting = $state(false);

  // Independent per section (not one shared "edit mode" for the whole
  // page) -- editing tags shouldn't also pop open the collections forms.
  // Pills-only by default; the pencil/check toggle reveals the remove
  // buttons and add forms.
  let editingTags = $state(false);
  let editingCollections = $state(false);

  // Related pages' own state (the pencil, the search) lives in RelatedItems; these
  // are its labels, so the strings stay with the rest of this screen's messages.
  const relatedLabels = $derived({
    heading: m.pagedetail_related_heading(),
    empty: m.pagedetail_related_empty(),
    edit: m.pagedetail_edit_related(),
    done: m.pagedetail_done_editing_related(),
    searchPlaceholder: m.pagedetail_related_search_placeholder(),
    noResults: m.pagedetail_related_no_results(),
    remove: (name: string) => m.pagedetail_remove_related_aria({ name }),
    relateError: m.pagedetail_related_error(),
    removeError: m.pagedetail_remove_related_error(),
  });

  // The same, for the links related to this page (a page and a link can be related,
  // from either end).
  const relatedLinksLabels = $derived({
    heading: m.pagedetail_related_links_heading(),
    empty: m.pagedetail_related_links_empty(),
    edit: m.pagedetail_edit_related_links(),
    done: m.pagedetail_done_editing_related_links(),
    searchPlaceholder: m.pagedetail_related_search_placeholder(),
    noResults: m.pagedetail_related_links_no_results(),
    remove: (name: string) => m.pagedetail_remove_related_aria({ name }),
    relateError: m.pagedetail_related_links_error(),
    removeError: m.pagedetail_remove_related_error(),
  });

  $effect(() => {
    const id = params.id;
    loading = true;
    loadError = null;
    actionError = null;
    page = null;
    editingTags = false;
    editingCollections = false;
    editingNotes = false;

    Promise.allSettled([
      apiJSON<PageDetail>(`/pages/${id}`),
      apiJSON<CollectionListResponse>("/collections"),
    ]).then(([pageResult, collectionsResult]) => {
      if (pageResult.status === "fulfilled") {
        page = pageResult.value;
      } else {
        loadError =
          pageResult.reason instanceof ApiError
            ? pageResult.reason.message
            : m.pagedetail_load_error();
      }
      allCollections =
        collectionsResult.status === "fulfilled"
          ? collectionsResult.value.collections
          : [];
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

  async function addTag(event: SubmitEvent) {
    event.preventDefault();
    const name = tagInput.trim();
    if (!name || !page) return;
    addingTag = true;
    actionError = null;
    try {
      const created = await apiJSON<TagCreated>(`/pages/${page.id}/tags`, {
        method: "POST",
        body: { name },
      });
      // source: "manual" isn't in the response -- the backend hardcodes
      // it for anything added through this endpoint, so there's nothing
      // to read it from; see TagCreated's own comment.
      page.tags = [
        ...page.tags,
        {
          id: created.id,
          name: created.name,
          slug: created.slug,
          source: "manual" as const,
        },
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
    if (!page) return;
    actionError = null;
    try {
      await apiJSON(`/pages/${page.id}/tags/${tagId}`, { method: "DELETE" });
      page.tags = page.tags.filter((t) => t.id !== tagId);
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_remove_tag_error();
    }
  }

  async function linkPageToCollection(collection: Collection) {
    if (!page) return;
    await apiJSON(`/pages/${page.id}/collections`, {
      method: "POST",
      body: { collection_id: collection.id },
    });
    page.collections = [
      ...page.collections,
      {
        id: collection.id,
        name: collection.name,
        parent_id: collection.parent_id,
      },
    ].sort((a, b) => a.name.localeCompare(b.name));
  }

  async function addToCollection(event: SubmitEvent) {
    event.preventDefault();
    if (!page || selectedCollectionId === "") return;
    const collection = allCollections.find(
      (c) => c.id === Number(selectedCollectionId),
    );
    if (!collection) return;

    addingToCollection = true;
    actionError = null;
    try {
      await linkPageToCollection(collection);
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

  let newCollectionName = $state("");
  let creatingCollection = $state(false);

  // Top-level only for now -- no parent picker. There's nowhere in the
  // dashboard yet to browse/manage the collection tree itself (this is
  // the closest thing to one so far: create-a-collection-while-adding-a-
  // page-to-it), so nesting from here would be choosing a parent blind.
  async function createAndAddCollection(event: SubmitEvent) {
    event.preventDefault();
    const name = newCollectionName.trim();
    if (!name || !page) return;

    creatingCollection = true;
    actionError = null;
    try {
      const created = await apiJSON<Collection>("/collections", {
        method: "POST",
        body: { name },
      });
      allCollections = [...allCollections, created];
      await linkPageToCollection(created);
      newCollectionName = "";
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.collections_create_error();
    } finally {
      creatingCollection = false;
    }
  }

  async function removeFromCollection(collectionId: number) {
    if (!page) return;
    actionError = null;
    try {
      await apiJSON(`/pages/${page.id}/collections/${collectionId}`, {
        method: "DELETE",
      });
      page.collections = page.collections.filter((c) => c.id !== collectionId);
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.pagedetail_remove_from_collection_error();
    }
  }

  // The collection-tree helpers live in lib/collections.ts (shared with
  // LinkDetail); these just bind them to this screen's own collection list.
  function availableCollections(p: PageDetail): Collection[] {
    return availableIn(
      allCollections,
      p.collections.map((c) => c.id),
    );
  }

  function collectionPath(collectionId: number): string | null {
    return pathIn(allCollections, collectionId);
  }

  function collectionNamePath(collectionId: number): string {
    return namePathIn(allCollections, collectionId);
  }

  async function toggleExcludedFromMirror() {
    if (!page) return;
    togglingMirror = true;
    actionError = null;
    const next = !page.excluded_from_mirror;
    try {
      await apiJSON(`/pages/${page.id}`, {
        method: "PATCH",
        body: { excluded_from_mirror: next },
      });
      page.excluded_from_mirror = next;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_mirror_error();
    } finally {
      togglingMirror = false;
    }
  }

  function startEditingTitle() {
    if (!page) return;
    titleInput = page.title ?? page.normalized_url;
    editingTitle = true;
  }

  function cancelEditingTitle() {
    editingTitle = false;
  }

  async function saveTitle(event: SubmitEvent) {
    event.preventDefault();
    if (!page) return;
    const trimmed = titleInput.trim();
    if (!trimmed) return;
    savingTitle = true;
    actionError = null;
    try {
      const updated = await apiJSON<PageDetail>(`/pages/${page.id}`, {
        method: "PATCH",
        body: { title: trimmed },
      });
      page.title = updated.title;
      editingTitle = false;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_title_error();
    } finally {
      savingTitle = false;
    }
  }

  function startEditingNotes() {
    if (!page) return;
    notesInput = page.notes ?? "";
    editingNotes = true;
  }

  function cancelEditingNotes() {
    editingNotes = false;
  }

  // Unlike saveTitle, an empty/whitespace-only value is a valid save --
  // it clears the note rather than being rejected, matching PatchPage's
  // own trim-then-nullify-if-empty handling on the backend.
  async function saveNotes(event: SubmitEvent) {
    event.preventDefault();
    if (!page) return;
    savingNotes = true;
    actionError = null;
    try {
      const updated = await apiJSON<PageDetail>(`/pages/${page.id}`, {
        method: "PATCH",
        body: { notes: notesInput.trim() },
      });
      page.notes = updated.notes;
      editingNotes = false;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_notes_error();
    } finally {
      savingNotes = false;
    }
  }

  // Fire-and-forget from the dashboard's perspective: this only asks a
  // device to redo the capture, so there's nothing on `page` itself to update
  // afterward -- just a transient "queued" confirmation, same pattern as
  // Devices.svelte's own copy-to-clipboard button.
  async function recapture() {
    if (!page) return;
    recapturing = true;
    actionError = null;
    try {
      await apiJSON(`/pages/${page.id}/recapture`, { method: "POST" });
      recaptureQueued = true;
      setTimeout(() => {
        recaptureQueued = false;
      }, 2000);
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_recapture_error();
    } finally {
      recapturing = false;
    }
  }

  async function deletePage() {
    if (!page) return;
    if (
      !confirm(
        m.pagedetail_delete_confirm({
          title: page.title ?? page.normalized_url,
        }),
      )
    )
      return;

    deleting = true;
    actionError = null;
    try {
      await apiJSON(`/pages/${page.id}`, { method: "DELETE" });
      await push("/");
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_delete_error();
      deleting = false;
    }
  }
</script>

<main class="screen">
  <AppHeader />
  <a class="back" href="/" use:link>
    <ChevronLeft size={14} />
    {m.nav_library()}
  </a>

  {#if loading}
    <p class="status">{m.common_loading()}</p>
  {:else if loadError}
    <div class="status-error" role="alert">
      <AlertCircle size={28} />
      <span>{loadError}</span>
    </div>
  {:else if page}
    {#if editingTitle}
      <form class="title-edit" onsubmit={saveTitle}>
        <input
          type="text"
          placeholder={m.pagedetail_title_placeholder()}
          bind:value={titleInput}
          disabled={savingTitle}
        />
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
        <h1>{page.title ?? page.normalized_url}</h1>
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
      <!-- One image beside the URL has no list to keep aligned, so it shows nothing
           rather than a placeholder when there's no favicon. -->
      <Favicon
        src={page.favicon_path !== null
          ? `/api/pages/${page.id}/favicon`
          : null}
        size={16}
        placeholder={false}
      />
      <a
        class="source-url"
        href={page.normalized_url}
        target="_blank"
        rel="noreferrer">{page.normalized_url}<ExternalLink size={12} /></a
      >
    </div>

    {#if page.matching_link_id !== null}
      <SameUrlHint
        kind="link"
        id={page.matching_link_id}
        title={page.matching_link_title ?? page.normalized_url}
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
        checked={!page.excluded_from_mirror}
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
        {#each page.tags as tag (tag.id)}
          <li class:ai={tag.source === "ai"}>
            {#if tag.source === "ai"}
              <Sparkles size={12} aria-label={m.pagedetail_ai_label()} />
            {/if}
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
        {#each page.collections as collection (collection.id)}
          {@const path = collectionPath(collection.id)}
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
        {#if availableCollections(page).length > 0}
          <form class="inline-form" onsubmit={addToCollection}>
            <select
              bind:value={selectedCollectionId}
              disabled={addingToCollection}
            >
              <option value=""
                >{m.pagedetail_add_to_collection_placeholder()}</option
              >
              {#each availableCollections(page) as collection (collection.id)}
                <option value={collection.id}
                  >{collectionNamePath(collection.id)}</option
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
      {:else if page.notes}
        <!-- renderMarkdown only ever emits <strong>/<em>/<ul>/<li>/<p>/<br>
             wrapping HTML-escaped text -- there's no path from page.notes to
             an injected tag/attribute, which is what this rule exists to
             catch. -->
        <!-- eslint-disable-next-line svelte/no-at-html-tags -->
        <div class="notes-body">{@html renderMarkdown(page.notes)}</div>
      {:else}
        <p class="empty-note">{m.pagedetail_notes_empty()}</p>
      {/if}
    </section>

    <RelatedItems
      items={page.related_pages}
      kind="page"
      ownerPath={`/pages/${page.id}`}
      excludeId={page.id}
      labels={relatedLabels}
      onchange={(next) => {
        if (page) page.related_pages = next;
      }}
      onerror={(message) => (actionError = message)}
    />

    <!-- Links only exist when the instance has them on. No excludeId: a link's id and
         a page's id are different things, so nothing is excluded from the search. -->
    {#if session.linksEnabled}
      <RelatedItems
        items={page.related_links}
        kind="link"
        ownerPath={`/pages/${page.id}`}
        labels={relatedLinksLabels}
        onchange={(next) => {
          if (page) page.related_links = next;
        }}
        onerror={(message) => (actionError = message)}
      />
    {/if}

    <h2>{m.pagedetail_captures_heading()}</h2>
    <ul class="captures">
      {#each page.captures as capture (capture.id)}
        <li>
          <a href={`/captures/${capture.id}`} use:link>
            <span class="captured-at"
              >{formatDateTime(capture.captured_at)}</span
            >
            <span class="meta">
              {#if capture.source === "manual_upload"}
                <Upload size={11} />
                {m.pagedetail_manual_upload_label()}
                ·
              {/if}
              {formatBytes(capture.html_uncompressed_size_bytes)}
            </span>
          </a>
        </li>
      {/each}
    </ul>

    <WaybackBlock
      url={page.normalized_url}
      wayback={page.wayback}
      submitPath={`/pages/${page.id}/wayback`}
      onchange={(next) => {
        if (page) page.wayback = next;
      }}
    />

    <div class="actions-row">
      <button
        type="button"
        onclick={recapture}
        disabled={recapturing || recaptureQueued}
      >
        {#if recaptureQueued}
          <Check size={14} />
        {:else}
          <RotateCw size={14} />
        {/if}
        {recaptureQueued
          ? m.pagedetail_recapture_queued()
          : recapturing
            ? m.pagedetail_recapture_queuing()
            : m.pagedetail_recapture()}
      </button>

      <button
        type="button"
        class="danger"
        onclick={deletePage}
        disabled={deleting}
      >
        <Trash2 size={14} />
        {m.pagedetail_delete()}
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
  // full-block error pattern app-wide, not a screen-specific variant.
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

  // The input takes the exact place of the h1 it replaces -- same
  // typographic treatment (Fraunces italic, same size) rather than a
  // generic-sized text input -- with Save/Cancel on their own row below
  // rather than crammed into the same row as a heading-sized field.
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

  .title-edit-actions {
    display: flex;
    gap: 0.5rem;
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
    justify-content: space-between;
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

      // AI-sourced tags get a brass-tinted border + the Sparkles icon
      // (rendered inline in the template).
      &.ai {
        color: var(--brass);
        border-color: color-mix(in srgb, var(--brass) 45%, var(--rule));
      }

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

  .captures {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px dotted var(--rule);
  }

  .captures li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.625rem 0.5rem;
    @include mix.dotted-rule;

    &:hover {
      background: var(--paper-raised);
    }
  }

  .captures a {
    display: flex;
    align-items: baseline;
    gap: 1rem;
    flex: 1;
    min-width: 0;
    text-decoration: none;
    color: inherit;

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .captured-at {
    @include type.data-mono;
  }

  .meta {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.8125rem;
    white-space: nowrap;
  }
</style>
