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
<!-- Now the full read/write loop: tag add/remove, collection add/remove,
     the excluded_from_mirror toggle, and per-capture language correction
     all call their real backend endpoints. All of page/collections/
     languageOptions are updated optimistically from each write's own
     response rather than refetching the whole page afterward -- a normal
     tradeoff for a single-user personal tool, not something defended
     against concurrent-editor conflicts.

     Capture rows now link to the in-app reader view (/captures/{id}) --
     the raw archived HTML itself still opens as a plain new-tab link, but
     from inside that reader view now, not directly from this list. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import { apiJSON, ApiError } from "../lib/api";
  import AppHeader from "../components/AppHeader.svelte";
  import type {
    PageDetail,
    CaptureSummary,
    TagCreated,
    Collection,
    CollectionListResponse,
    TextSearchConfigsResponse,
  } from "../lib/types";

  let { params }: { params: { id: string } } = $props();

  let page = $state<PageDetail | null>(null);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  // Supplementary metadata for the write-action UI (the "add to
  // collection" picker's options, the language-correction dropdown's
  // valid values) -- fetched alongside the page itself, but best-effort:
  // a failure here shouldn't block viewing the page, just leave the
  // relevant picker with no/fewer options.
  let allCollections = $state<Collection[]>([]);
  let languageOptions = $state<string[]>([]);

  let tagInput = $state("");
  let addingTag = $state(false);
  let selectedCollectionId = $state("");
  let addingToCollection = $state(false);
  let togglingMirror = $state(false);
  let savingLanguageFor = $state<number | null>(null);

  $effect(() => {
    const id = params.id;
    loading = true;
    loadError = null;
    actionError = null;
    page = null;

    Promise.allSettled([
      apiJSON<PageDetail>(`/pages/${id}`),
      apiJSON<CollectionListResponse>("/collections"),
      apiJSON<TextSearchConfigsResponse>("/text-search-configs"),
    ]).then(([pageResult, collectionsResult, languagesResult]) => {
      if (pageResult.status === "fulfilled") {
        page = pageResult.value;
      } else {
        loadError =
          pageResult.reason instanceof ApiError
            ? pageResult.reason.message
            : "failed to load page";
      }
      allCollections =
        collectionsResult.status === "fulfilled"
          ? collectionsResult.value.collections
          : [];
      languageOptions =
        languagesResult.status === "fulfilled"
          ? languagesResult.value.languages
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

  function formatBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    return `${(n / (1024 * 1024)).toFixed(1)} MB`;
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
        { id: created.id, name: created.name, source: "manual" as const },
      ].sort((a, b) => a.name.localeCompare(b.name));
      tagInput = "";
    } catch (err) {
      actionError = err instanceof ApiError ? err.message : "failed to add tag";
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
        err instanceof ApiError ? err.message : "failed to remove tag";
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
        err instanceof ApiError ? err.message : "failed to add to collection";
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
        err instanceof ApiError ? err.message : "failed to create collection";
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
          : "failed to remove from collection";
    }
  }

  // Collections this page isn't already in -- what the picker should
  // actually offer, rather than letting someone "add" a membership that
  // already exists.
  function availableCollections(p: PageDetail): Collection[] {
    const memberIds = new Set(p.collections.map((c) => c.id));
    return allCollections.filter((c) => !memberIds.has(c.id));
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
        err instanceof ApiError
          ? err.message
          : "failed to update mirror setting";
    } finally {
      togglingMirror = false;
    }
  }

  async function updateCaptureLanguage(
    capture: CaptureSummary,
    newLanguage: string,
  ) {
    if (newLanguage === capture.language) return;
    savingLanguageFor = capture.id;
    actionError = null;
    try {
      await apiJSON(`/captures/${capture.id}/language`, {
        method: "PATCH",
        body: { language: newLanguage },
      });
      capture.language = newLanguage;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : "failed to update language";
    } finally {
      savingLanguageFor = null;
    }
  }
</script>

<main class="screen">
  <AppHeader />
  <a class="back" href="/" use:link>&larr; Library</a>

  {#if loading}
    <p class="status">Loading…</p>
  {:else if loadError}
    <p class="status error" role="alert">{loadError}</p>
  {:else if page}
    <h1>{page.title ?? page.normalized_url}</h1>
    <a
      class="source-url"
      href={page.normalized_url}
      target="_blank"
      rel="noreferrer">{page.normalized_url}</a
    >

    {#if actionError}
      <p class="status error" role="alert">{actionError}</p>
    {/if}

    <label class="mirror-toggle">
      <input
        type="checkbox"
        checked={page.excluded_from_mirror}
        disabled={togglingMirror}
        onchange={toggleExcludedFromMirror}
      />
      Exclude from bookmark-list mirror
    </label>

    <section>
      <h2>Tags</h2>
      <ul class="chips">
        {#each page.tags as tag (tag.id)}
          <li class:ai={tag.source === "ai"}>
            {tag.name}
            {#if tag.source === "ai"}
              <span class="source-label">AI</span>
            {/if}
            <button
              type="button"
              class="remove"
              aria-label={`Remove tag ${tag.name}`}
              onclick={() => removeTag(tag.id)}>&times;</button
            >
          </li>
        {/each}
      </ul>
      <form class="inline-form" onsubmit={addTag}>
        <input
          type="text"
          placeholder="Add a tag…"
          bind:value={tagInput}
          disabled={addingTag}
        />
        <button type="submit" disabled={addingTag || !tagInput.trim()}
          >Add</button
        >
      </form>
    </section>

    <section>
      <h2>Collections</h2>
      <ul class="chips">
        {#each page.collections as collection (collection.id)}
          <li>
            {collection.name}
            <button
              type="button"
              class="remove"
              aria-label={`Remove from ${collection.name}`}
              onclick={() => removeFromCollection(collection.id)}
              >&times;</button
            >
          </li>
        {/each}
      </ul>
      {#if availableCollections(page).length > 0}
        <form class="inline-form" onsubmit={addToCollection}>
          <select
            bind:value={selectedCollectionId}
            disabled={addingToCollection}
          >
            <option value="">Add to a collection…</option>
            {#each availableCollections(page) as collection (collection.id)}
              <option value={collection.id}>{collection.name}</option>
            {/each}
          </select>
          <button
            type="submit"
            disabled={addingToCollection || selectedCollectionId === ""}
            >Add</button
          >
        </form>
      {/if}
      <form class="inline-form" onsubmit={createAndAddCollection}>
        <input
          type="text"
          placeholder="Or create a new collection…"
          bind:value={newCollectionName}
          disabled={creatingCollection}
        />
        <button
          type="submit"
          disabled={creatingCollection || !newCollectionName.trim()}
          >Create &amp; add</button
        >
      </form>
    </section>

    <h2>Captures</h2>
    <ul class="captures">
      {#each page.captures as capture (capture.id)}
        <li>
          <a href={`/captures/${capture.id}`} use:link>
            <span class="captured-at"
              >{formatDateTime(capture.captured_at)}</span
            >
            <span class="meta"
              >{capture.source} · {formatBytes(
                capture.html_uncompressed_size_bytes,
              )}</span
            >
          </a>
          {#if languageOptions.length > 0}
            <label class="language-picker">
              Language
              <select
                value={capture.language}
                disabled={savingLanguageFor === capture.id}
                onchange={(e) =>
                  updateCaptureLanguage(capture, e.currentTarget.value)}
              >
                {#each languageOptions as lang (lang)}
                  <option value={lang}>{lang}</option>
                {/each}
              </select>
            </label>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</main>

<style lang="scss">
  .screen {
    max-width: 48rem;
    margin: 0 auto;
    padding: 2rem 1rem;
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

  h1 {
    margin: 0 0 0.25rem;
  }

  .source-url {
    display: inline-block;
    margin-bottom: 1rem;
    color: var(--focus);
    font-size: 0.875rem;
    word-break: break-all;
  }

  .mirror-toggle {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 1.5rem;
    font-size: 0.875rem;
    color: var(--ink-muted);
  }

  section {
    margin-bottom: 1.5rem;
  }

  h2 {
    font-size: 1rem;
    margin-bottom: 0.5rem;
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
    list-style: none;
    margin: 0 0 0.5rem;
    padding: 0;

    li {
      display: flex;
      align-items: center;
      gap: 0.25rem;
      padding: 0.125rem 0.375rem 0.125rem 0.625rem;
      border-radius: 999px;
      background: var(--paper-raised);
      border: 1px solid var(--rule);
      font-size: 0.75rem;

      // Minimal AI/manual distinction for now (existing tokens only, no
      // new color) -- a real visual treatment is a styling-pass concern,
      // not this one.
      &.ai {
        border-style: dashed;
      }
    }
  }

  .source-label {
    color: var(--ink-muted);
    font-size: 0.625rem;
    letter-spacing: 0.03em;
  }

  .remove {
    padding: 0 0.25rem;
    border: none;
    background: none;
    color: var(--ink-muted);
    font-size: 0.9375rem;
    line-height: 1;
    cursor: pointer;

    &:hover {
      color: var(--accent);
    }
  }

  .inline-form {
    display: flex;
    gap: 0.5rem;
  }

  input[type="text"],
  select {
    padding: 0.375rem 0.5rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper);
    color: var(--ink);
    font: inherit;
    font-size: 0.875rem;
  }

  button {
    padding: 0.375rem 0.75rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper-raised);
    color: var(--ink);
    font: inherit;
    font-size: 0.875rem;
    cursor: pointer;

    &:disabled {
      opacity: 0.5;
      cursor: default;
    }
  }

  .captures {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--rule);
  }

  .captures li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.625rem 0.25rem;
    border-bottom: 1px solid var(--rule);
  }

  .captures a {
    display: flex;
    align-items: baseline;
    gap: 1rem;
    flex: 1;
    min-width: 0;
    text-decoration: none;
    color: inherit;
  }

  .meta {
    color: var(--ink-muted);
    font-size: 0.8125rem;
    white-space: nowrap;
  }

  .language-picker {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    color: var(--ink-muted);
    font-size: 0.75rem;
    white-space: nowrap;

    select {
      padding: 0.125rem 0.25rem;
      font-size: 0.75rem;
    }
  }
</style>
