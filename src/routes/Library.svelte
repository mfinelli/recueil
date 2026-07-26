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
     paginated. List/grid rendering itself lives in the shared PageList
     component now -- this file keeps only what's actually Library-specific:
     the search box and pagination. -->
<script lang="ts">
  import Search from "@lucide/svelte/icons/search";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import ChevronLeft from "@lucide/svelte/icons/chevron-left";
  import ChevronRight from "@lucide/svelte/icons/chevron-right";
  import { apiJSON, ApiError } from "../lib/api";
  import type { Page, PageListResponse } from "../lib/types";
  import AppHeader from "../components/AppHeader.svelte";
  import PageList from "../components/PageList.svelte";
  import { m } from "../paraglide/messages";

  const PAGE_SIZE = 50;

  let query = $state("");
  let offset = $state(0);
  let pages = $state<Page[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state<string | null>(null);
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
    } catch (err) {
      error = err instanceof ApiError ? err.message : m.library_load_error();
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
</script>

<main class="screen">
  <AppHeader />

  <p class="page-heading">{m.nav_library()}</p>

  <div class="toolbar">
    <div class="search-wrap">
      <Search size={15} />
      <input
        class="search"
        type="search"
        placeholder={m.library_search_placeholder()}
        oninput={handleSearchInput}
        aria-label={m.library_search_label()}
      />
    </div>
  </div>

  {#if loading}
    <p class="status-text">{m.common_loading()}</p>
  {:else if error}
    <div class="status">
      <AlertCircle size={28} />
      <span>{error}</span>
    </div>
  {:else}
    <PageList
      {pages}
      emptyMessage={query
        ? m.library_no_search_results()
        : m.library_nothing_archived()}
    />
  {/if}

  {#if !loading && !error && pages.length > 0}
    <div class="pagination">
      <button onclick={prevPage} disabled={offset === 0}>
        <ChevronLeft size={14} />
        {m.library_previous()}
      </button>
      <span class="count"
        >{m.library_pagination_summary({
          start: offset + 1,
          end: Math.min(offset + PAGE_SIZE, total),
          total,
        })}</span
      >
      <button onclick={nextPage} disabled={offset + PAGE_SIZE >= total}>
        {m.library_next()}
        <ChevronRight size={14} />
      </button>
    </div>
  {/if}
</main>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;

  .screen {
    max-width: 64rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  .page-heading {
    @include type.eyebrow;
    margin: 0 0 1rem;
  }

  button {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.375rem 0.75rem;
    border: 1px solid var(--rule);
    border-radius: 4px;
    background: var(--paper-raised);
    color: var(--ink);
    font: inherit;
    cursor: pointer;

    &:disabled {
      opacity: 0.5;
      cursor: default;
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .toolbar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    margin-bottom: 1.5rem;
  }

  .search-wrap {
    position: relative;
    flex: 1;
    display: flex;
    align-items: center;

    :global(svg) {
      position: absolute;
      left: 0.65rem;
      color: var(--ink-muted);
      pointer-events: none;
    }
  }

  .search {
    width: 100%;
    padding: 0.625rem 0.75rem 0.625rem 2.1rem;
    border: 1px solid var(--rule);
    border-radius: 4px;
    background: var(--paper-raised);
    color: var(--ink);
    font: inherit;

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .status-text {
    color: var(--ink-muted);
  }

  .status {
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

  .pagination {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 1rem;
    margin-top: 1.5rem;
    font-size: 0.875rem;
    color: var(--ink-muted);
  }

  .pagination .count {
    @include type.data-mono;
    font-size: 0.8125rem;
  }
</style>
