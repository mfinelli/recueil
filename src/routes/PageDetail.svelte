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
  import { SvelteSet } from "svelte/reactivity";
  import { apiJSON, ApiError } from "../lib/api";
  import { formatBytes } from "../lib/format";
  import { renderMarkdown } from "../lib/markdown";
  import AppHeader from "../components/AppHeader.svelte";
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
  import Globe from "@lucide/svelte/icons/globe";
  import type {
    PageDetail,
    TagCreated,
    Collection,
    CollectionListResponse,
    PageLink,
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

  // Unlike PageList's favicon handling, there's no placeholder/fallback
  // shown here on failure -- a single one-off image next to the title
  // doesn't need to preserve alignment across many rows the way a list
  // does, so a missing/broken favicon just means nothing renders there.
  let faviconFailed = $state(false);
  // Keyed by linked page id, same reasoning as PageList's
  // faviconLoadFailed: several linked pages render at once, so one
  // shared boolean would incorrectly hide every favicon just because
  // one of them 404ed.
  let linkFaviconFailed = new SvelteSet<number>();

  // Independent per section (not one shared "edit mode" for the whole
  // page) -- editing tags shouldn't also pop open the collections forms.
  // Pills-only by default; the pencil/check toggle reveals the remove
  // buttons and add forms.
  let editingTags = $state(false);
  let editingCollections = $state(false);
  let editingLinks = $state(false);
  let linkSearchQuery = $state("");
  let linkSearchResults = $state<PageLink[]>([]);
  let addingLink = $state(false);
  // Plain variable, not $state -- it's a setTimeout handle consumed only
  // by clearTimeout/reassignment within handleLinkSearchInput itself,
  // never read reactively, same as Library.svelte's searchDebounce.
  let linkSearchDebounce: ReturnType<typeof setTimeout> | undefined;

  $effect(() => {
    const id = params.id;
    loading = true;
    loadError = null;
    actionError = null;
    page = null;
    faviconFailed = false;
    editingTags = false;
    editingCollections = false;
    editingNotes = false;
    editingLinks = false;
    linkSearchQuery = "";
    linkSearchResults = [];

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

  function markLinkFaviconFailed(pageId: number) {
    linkFaviconFailed.add(pageId);
  }

  // Same reasoning as PageList's showFavicon: skip the image request
  // entirely (straight to the placeholder) for a link that never had a
  // favicon captured, rather than waiting on a request that was always
  // going to 404.
  function showLinkFavicon(linked: PageLink): boolean {
    return linked.favicon_path !== null && !linkFaviconFailed.has(linked.id);
  }

  // page.links already loaded with the page, so this only needs to
  // exclude those from the *search* results client-side -- same
  // approach as availableCollections above, and the same reasoning
  // SearchPagesForLinking's backend comment gives for not also
  // doing this filtering server-side.
  function availableLinkResults(p: PageDetail): PageLink[] {
    const linkedIds = new Set(p.links.map((l) => l.id));
    return linkSearchResults.filter((r) => !linkedIds.has(r.id));
  }

  // Paired with `bind:value={linkSearchQuery}` on the input rather than
  // Library's uncontrolled-input pattern -- a successful addLink()
  // below needs to clear the visible search text programmatically, which
  // a controlled input makes trivial and an uncontrolled one wouldn't.
  function handleLinkSearchInput() {
    clearTimeout(linkSearchDebounce);
    const value = linkSearchQuery;
    if (!value.trim()) {
      linkSearchResults = [];
      return;
    }
    linkSearchDebounce = setTimeout(async () => {
      if (!page) return;
      const params = new URLSearchParams({
        q: value,
        exclude: String(page.id),
      });
      try {
        const res = await apiJSON<{ pages: PageLink[] }>(
          `/pages/link-candidates?${params.toString()}`,
        );
        // Guards against a slower, now-stale request's response landing
        // after a faster, more recent one and overwriting it -- the
        // query the person is currently looking at is the source of
        // truth for whether this response is still relevant.
        if (linkSearchQuery === value) {
          linkSearchResults = res.pages;
        }
      } catch {
        // Best-effort, same reasoning as allCollections' load above:
        // a failed search just leaves the dropdown with fewer/no
        // results rather than surfacing its own error banner.
        linkSearchResults = [];
      }
    }, 300);
  }

  async function addLink(target: PageLink) {
    if (!page) return;
    addingLink = true;
    actionError = null;
    try {
      const linked = await apiJSON<PageLink>(`/pages/${page.id}/links`, {
        method: "POST",
        body: { link_page_id: target.id },
      });
      page.links = [...page.links, linked].sort((a, b) =>
        (a.title ?? a.normalized_url).localeCompare(
          b.title ?? b.normalized_url,
        ),
      );
      linkSearchQuery = "";
      linkSearchResults = [];
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.pagedetail_link_error();
    } finally {
      addingLink = false;
    }
  }

  async function removeLink(linkPageId: number) {
    if (!page) return;
    actionError = null;
    try {
      await apiJSON(`/pages/${page.id}/links/${linkPageId}`, {
        method: "DELETE",
      });
      page.links = page.links.filter((l) => l.id !== linkPageId);
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.pagedetail_remove_link_error();
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

  // Collections this page isn't already in -- what the picker should
  // actually offer, rather than letting someone "add" a membership that
  // already exists. Sorted by full name path (not the fetch's own
  // parent_id-then-name order, which doesn't group a collection near its
  // actual children) so that once collectionNamePath's disambiguating
  // labels are in play below, same-named collections under different
  // parents land near their real siblings instead of scattered
  // arbitrarily through the list.
  function availableCollections(p: PageDetail): Collection[] {
    const memberIds = new Set(p.collections.map((c) => c.id));
    return allCollections
      .filter((c) => !memberIds.has(c.id))
      .map((c) => ({ collection: c, path: collectionNamePath(c.id) }))
      .sort((a, b) => a.path.localeCompare(b.path))
      .map((entry) => entry.collection);
  }

  // Walks a collection's parent_id chain in allCollections, collecting
  // one segment per ancestor via `pick`. Shared by collectionPath (slugs,
  // below) and collectionNamePath (names, for the "add to collection"
  // picker) -- same traversal, just a different field per ancestor.
  function collectionAncestorSegments(
    collectionId: number,
    pick: (c: Collection) => string,
  ): string[] {
    const byId = new Map(allCollections.map((c) => [c.id, c]));
    const segments: string[] = [];
    let current = byId.get(collectionId);
    while (current) {
      segments.unshift(pick(current));
      current =
        current.parent_id !== null ? byId.get(current.parent_id) : undefined;
    }
    return segments;
  }

  // page.collections (PageCollection) doesn't carry a slug, and routes.ts
  // wildcards /collections/* to the full nested path (e.g.
  // "zebra/side-dishes"), not just one collection's own slug -- but
  // allCollections (fetched for the picker above) already has every
  // collection's slug + parent_id, which is enough to walk the chain and
  // rebuild the same path CollectionDetail/Collections.svelte's own
  // rename-preview logic produces. Returns null if the collection can't
  // be found there (allCollections fetch failed independently -- see the
  // Promise.allSettled above -- or some other inconsistency), in which
  // case the caller renders plain text instead of a broken link.
  function collectionPath(collectionId: number): string | null {
    const segments = collectionAncestorSegments(collectionId, (c) => c.slug);
    return segments.length > 0 ? segments.join("/") : null;
  }

  // Same ancestor walk as collectionPath, but names joined with " / "
  // for human display rather than slugs joined with "/" for a URL --
  // used as the "add to collection" picker's option labels, so two
  // same-named collections under different parents (e.g. two separate
  // "Recipes" collections) read as "Zebra / Recipes" vs
  // "Cookbook / Recipes" instead of two indistinguishable "Recipes"
  // entries. Every id this is called with comes from allCollections
  // itself (see availableCollections above), so unlike collectionPath
  // there's no fetch-failed/not-found case to handle here -- the walk
  // always resolves at least the collection's own name.  A "/" inside a
  // collection's own name would read oddly here (indistinguishable from
  // an extra path segment), but that's an edge case not worth guarding
  // against right now.
  function collectionNamePath(collectionId: number): string {
    return collectionAncestorSegments(collectionId, (c) => c.name).join(" / ");
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
      {#if page.favicon_path !== null && !faviconFailed}
        <img
          class="favicon"
          src={`/api/pages/${page.id}/favicon`}
          alt=""
          loading="lazy"
          onerror={() => (faviconFailed = true)}
        />
      {/if}
      <a
        class="source-url"
        href={page.normalized_url}
        target="_blank"
        rel="noreferrer">{page.normalized_url}<ExternalLink size={12} /></a
      >
    </div>

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

    <section>
      <div class="block-header">
        <h2>{m.pagedetail_links_heading()}</h2>
        <button
          type="button"
          class="edit-toggle"
          class:active={editingLinks}
          aria-label={editingLinks
            ? m.pagedetail_done_editing_links()
            : m.pagedetail_edit_links()}
          onclick={() => (editingLinks = !editingLinks)}
        >
          {#if editingLinks}
            <Check size={13} />
          {:else}
            <Pencil size={13} />
          {/if}
        </button>
      </div>
      {#if page.links.length > 0}
        <ul class="linked-list">
          {#each page.links as linked (linked.id)}
            <li>
              <a href={`/pages/${linked.id}`} use:link class="linked-row">
                {#if showLinkFavicon(linked)}
                  <img
                    class="linked-favicon"
                    src={`/api/pages/${linked.id}/favicon`}
                    alt=""
                    loading="lazy"
                    onerror={() => markLinkFaviconFailed(linked.id)}
                  />
                {:else}
                  <span class="linked-favicon-placeholder" aria-hidden="true">
                    <Globe size={11} />
                  </span>
                {/if}
                <span class="linked-title"
                  >{linked.title ?? linked.normalized_url}</span
                >
                <span class="linked-url">{linked.normalized_url}</span>
              </a>
              {#if editingLinks}
                <button
                  type="button"
                  class="remove"
                  aria-label={m.pagedetail_remove_link_aria({
                    name: linked.title ?? linked.normalized_url,
                  })}
                  onclick={() => removeLink(linked.id)}
                >
                  <X size={11} />
                </button>
              {/if}
            </li>
          {/each}
        </ul>
      {:else}
        <p class="empty-note">{m.pagedetail_links_empty()}</p>
      {/if}
      {#if editingLinks}
        <div class="link-search-wrap">
          <input
            type="text"
            placeholder={m.pagedetail_link_search_placeholder()}
            bind:value={linkSearchQuery}
            oninput={handleLinkSearchInput}
            disabled={addingLink}
          />
          {#if availableLinkResults(page).length > 0}
            <ul class="link-results">
              {#each availableLinkResults(page) as candidate (candidate.id)}
                <li>
                  <button
                    type="button"
                    onclick={() => addLink(candidate)}
                    disabled={addingLink}
                  >
                    <span class="result-title"
                      >{candidate.title ?? candidate.normalized_url}</span
                    >
                    <span class="result-url">{candidate.normalized_url}</span>
                  </button>
                </li>
              {/each}
            </ul>
          {:else if linkSearchQuery.trim()}
            <ul class="link-results">
              <li class="no-results">{m.pagedetail_link_no_results()}</li>
            </ul>
          {/if}
        </div>
      {/if}
    </section>

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

  // A lightweight version of Library's list-view row (favicon, with the
  // title and url on two lines).
  .linked-list {
    list-style: none;
    margin: 0 0 0.65rem;
    padding: 0;
    border-top: 1px dotted var(--rule);
  }

  .linked-list li {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    border-bottom: 1px dotted var(--rule);
  }

  .linked-row {
    display: grid;
    grid-template-columns: auto 1fr;
    align-items: center;
    gap: 0.05rem 0.6rem;
    flex: 1;
    min-width: 0;
    padding: 0.45rem 0.25rem;
    text-decoration: none;
    color: inherit;

    &:hover {
      background: var(--paper-raised);
    }

    &:focus-visible {
      @include mix.focus-ring;
      outline-offset: -2px;
    }
  }

  .linked-favicon,
  .linked-favicon-placeholder {
    grid-column: 1;
    grid-row: 1 / 3;
    width: 1.05rem;
    height: 1.05rem;
    border-radius: 0.15rem;
  }

  .linked-favicon-placeholder {
    display: grid;
    place-items: center;
    background: var(--paper-raised);
    border: 1px solid var(--rule);
    color: var(--ink-muted);
  }

  .linked-title {
    grid-column: 2;
    font-size: 0.85rem;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .linked-url {
    grid-column: 2;
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.75rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .linked-list .remove {
    flex: none;
    margin-right: 0.4rem;
  }

  .link-search-wrap {
    position: relative;
    max-width: 26rem;

    input[type="text"] {
      @include comp.text-input;
      display: block;
      width: 100%;
      padding: 0.45rem 0.65rem;
      border-radius: 4px;
      font-size: 0.875rem;
    }
  }

  .link-results {
    list-style: none;
    margin: 0.3rem 0 0;
    padding: 0;
    border: 1px solid var(--rule);
    border-radius: 4px;
    background: var(--paper-raised);
    overflow: hidden;

    li + li {
      border-top: 1px dotted var(--rule);
    }

    .no-results {
      padding: 0.6rem 0.7rem;
      color: var(--ink-muted);
      font-size: 0.8rem;
      font-style: italic;
    }

    button {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: 0.1rem;
      width: 100%;
      text-align: left;
      border: none;
      border-radius: 0;
      background: transparent;
      padding: 0.5rem 0.7rem;
      cursor: pointer;

      &:hover:not(:disabled) {
        background: var(--paper);
      }

      &:disabled {
        opacity: 0.5;
        cursor: default;
      }

      &:focus-visible {
        @include mix.focus-ring;
        outline-offset: -2px;
      }
    }

    .result-title {
      font-size: 0.85rem;
      color: var(--ink);
    }

    .result-url {
      @include type.data-mono;
      font-size: 0.7rem;
      color: var(--ink-muted);
    }
  }

  .source-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin-bottom: 1.25rem;
  }

  .favicon {
    width: 16px;
    height: 16px;
    border-radius: 3px;
    flex: none;
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
