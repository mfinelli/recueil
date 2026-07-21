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
<!-- Library browsing: search (GET /api/pages?q=) and plain listing, both
     paginated, in either a list view (favicon next to each row) or a grid
     view (thumbnail-first). Display/navigation only in this pass --
     tagging, collection membership editing, and the mirror-exclusion
     toggle are PageDetail concerns for a later round, not this list. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import { SvelteSet } from "svelte/reactivity";
  import { apiJSON, ApiError } from "../lib/api";
  import type { Page, PageListResponse } from "../lib/types";
  import AppHeader from "../components/AppHeader.svelte";

  const PAGE_SIZE = 50;
  const VIEW_MODE_KEY = "recueil:library-view-mode";

  type ViewMode = "list" | "grid";

  function loadViewMode(): ViewMode {
    const stored = localStorage.getItem(VIEW_MODE_KEY);
    return stored === "grid" ? "grid" : "list";
  }

  let query = $state("");
  let offset = $state(0);
  let pages = $state<Page[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let viewMode = $state<ViewMode>(loadViewMode());
  // Tracks page ids whose favicon/thumbnail request 404ed, so a broken
  // <img> icon never shows -- the fallback (no icon / a plain placeholder
  // tile) renders instead. Keyed by page id, not URL: the same page id
  // covers both the favicon and thumbnail request for that row/card,
  // which never both exist independently of each other failing.
  // SvelteSet (not a plain Set) so .add() itself triggers reactivity --
  // see App/Library's own eslint fix for the same rule elsewhere in this
  // file, applied here instead of disabled, since this one really is
  // read reactively across renders.
  let imageLoadFailed = new SvelteSet<number>();
  let searchDebounce: ReturnType<typeof setTimeout> | undefined;

  async function load() {
    loading = true;
    error = null;
    try {
      // Local and disposable -- built and consumed within this one call,
      // never stored in $state -- so the reactive wrapper the linter
      // otherwise wants here (SvelteURLSearchParams) has nothing to add.
      // eslint-disable-next-line svelte/prefer-svelte-reactivity
      const params = new URLSearchParams({
        limit: String(PAGE_SIZE),
        offset: String(offset),
      });
      if (query) params.set("q", query);
      const res = await apiJSON<PageListResponse>(
        `/pages?${params.toString()}`,
      );
      pages = res.pages;
      total = res.total;
      imageLoadFailed.clear();
    } catch (err) {
      error = err instanceof ApiError ? err.message : "failed to load pages";
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  function handleSearchInput(event: Event) {
    const value = (event.target as HTMLInputElement).value;
    clearTimeout(searchDebounce);
    searchDebounce = setTimeout(() => {
      query = value;
      offset = 0;
    }, 300);
  }

  function nextPage() {
    if (offset + PAGE_SIZE < total) offset += PAGE_SIZE;
  }

  function prevPage() {
    offset = Math.max(0, offset - PAGE_SIZE);
  }

  function setViewMode(mode: ViewMode) {
    viewMode = mode;
    localStorage.setItem(VIEW_MODE_KEY, mode);
  }

  function markImageFailed(pageId: number) {
    imageLoadFailed.add(pageId);
  }

  function displayTitle(page: Page): string {
    return page.title ?? page.normalized_url;
  }

  function formatDate(iso: string): string {
    return new Date(iso).toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  }
</script>

<main class="screen">
  <AppHeader />

  <div class="toolbar">
    <input
      class="search"
      type="search"
      placeholder="Search your archive…"
      oninput={handleSearchInput}
      aria-label="Search"
    />
    <div class="view-toggle" role="group" aria-label="View">
      <button
        class:active={viewMode === "list"}
        onclick={() => setViewMode("list")}>List</button
      >
      <button
        class:active={viewMode === "grid"}
        onclick={() => setViewMode("grid")}>Grid</button
      >
    </div>
  </div>

  {#if loading}
    <p class="status">Loading…</p>
  {:else if error}
    <p class="status error" role="alert">{error}</p>
  {:else if pages.length === 0}
    <p class="status">
      {query ? "No pages match your search." : "Nothing archived yet."}
    </p>
  {:else if viewMode === "list"}
    <ul class="pages-list">
      {#each pages as page (page.id)}
        <li>
          <a href={`/pages/${page.id}`} use:link>
            {#if !imageLoadFailed.has(page.id)}
              <img
                class="favicon"
                src={`/api/pages/${page.id}/favicon`}
                alt=""
                loading="lazy"
                onerror={() => markImageFailed(page.id)}
              />
            {:else}
              <span class="favicon-placeholder" aria-hidden="true"></span>
            {/if}
            <span class="title">{displayTitle(page)}</span>
            <span class="url">{page.normalized_url}</span>
            <span class="date">{formatDate(page.latest_capture_at)}</span>
          </a>
        </li>
      {/each}
    </ul>
  {:else}
    <ul class="pages-grid">
      {#each pages as page (page.id)}
        <li>
          <a href={`/pages/${page.id}`} use:link>
            <span class="thumbnail-frame">
              {#if !imageLoadFailed.has(page.id)}
                <img
                  class="thumbnail"
                  src={`/api/pages/${page.id}/thumbnail`}
                  alt=""
                  loading="lazy"
                  onerror={() => markImageFailed(page.id)}
                />
              {:else}
                <span class="thumbnail-placeholder" aria-hidden="true"
                  >{displayTitle(page).charAt(0)}</span
                >
              {/if}
            </span>
            <span class="title">{displayTitle(page)}</span>
            <span class="date">{formatDate(page.latest_capture_at)}</span>
          </a>
        </li>
      {/each}
    </ul>
  {/if}

  {#if !loading && !error && pages.length > 0}
    <div class="pagination">
      <button onclick={prevPage} disabled={offset === 0}>Previous</button>
      <span>{offset + 1}–{Math.min(offset + PAGE_SIZE, total)} of {total}</span>
      <button onclick={nextPage} disabled={offset + PAGE_SIZE >= total}
        >Next</button
      >
    </div>
  {/if}
</main>

<style lang="scss">
  .screen {
    max-width: 64rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  button {
    padding: 0.375rem 0.75rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper-raised);
    color: var(--ink);
    font: inherit;
    cursor: pointer;

    &:disabled {
      opacity: 0.5;
      cursor: default;
    }
  }

  .toolbar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 1.5rem;
  }

  .search {
    flex: 1;
    padding: 0.625rem 0.75rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper);
    color: var(--ink);
    font: inherit;
  }

  .view-toggle {
    display: flex;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    overflow: hidden;

    button {
      border: none;
      border-radius: 0;
      font-size: 0.8125rem;

      &.active {
        background: var(--accent-success);
        color: var(--paper);
      }
    }
  }

  .status {
    color: var(--ink-muted);

    &.error {
      color: var(--accent);
    }
  }

  // List view
  .pages-list {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--rule);
  }

  .pages-list li {
    border-bottom: 1px solid var(--rule);
  }

  .pages-list a {
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: center;
    gap: 0.125rem 0.75rem;
    padding: 0.625rem 0.25rem;
    text-decoration: none;
    color: inherit;

    &:hover {
      background: var(--paper-raised);
    }
  }

  .favicon,
  .favicon-placeholder {
    grid-column: 1;
    grid-row: 1 / 3;
    width: 1.25rem;
    height: 1.25rem;
  }

  .favicon-placeholder {
    border-radius: 0.1875rem;
    background: var(--rule);
  }

  .pages-list .title {
    grid-column: 2;
    font-weight: 600;
  }

  .pages-list .url {
    grid-column: 2;
    color: var(--ink-muted);
    font-size: 0.8125rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pages-list .date {
    grid-column: 3;
    grid-row: 1 / 3;
    align-self: center;
    color: var(--ink-muted);
    font-size: 0.8125rem;
    white-space: nowrap;
  }

  // Grid view
  .pages-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(11rem, 1fr));
    gap: 1rem;
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .pages-grid a {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
    text-decoration: none;
    color: inherit;
  }

  .thumbnail-frame {
    display: block;
    aspect-ratio: 4 / 3;
    border: 1px solid var(--rule);
    border-radius: 0.375rem;
    overflow: hidden;
    background: var(--paper-raised);
  }

  .thumbnail {
    width: 100%;
    height: 100%;
    object-fit: cover;
    object-position: top;
  }

  .thumbnail-placeholder {
    display: grid;
    place-items: center;
    width: 100%;
    height: 100%;
    color: var(--ink-muted);
    font-size: 2rem;
    text-transform: uppercase;
  }

  .pages-grid .title {
    font-size: 0.875rem;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pages-grid .date {
    color: var(--ink-muted);
    font-size: 0.75rem;
  }

  .pagination {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 1rem;
    margin-top: 1.5rem;
    font-size: 0.875rem;
    color: var(--ink-muted);
  }
</style>
