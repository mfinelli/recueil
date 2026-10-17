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
<!-- Links browsing: search (GET /api/links?q=) and plain listing (both
     paginated) Library's shape, minus the view toggle (a link has no
     thumbnail, so there is only a list). Row rendering lives in
     LinkList. Only routed when links are enabled (see routes.ts). -->
<script lang="ts">
  import Search from "@lucide/svelte/icons/search";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import ChevronLeft from "@lucide/svelte/icons/chevron-left";
  import ChevronRight from "@lucide/svelte/icons/chevron-right";
  import Plus from "@lucide/svelte/icons/plus";
  import X from "@lucide/svelte/icons/x";
  import { push, router } from "svelte-spa-router";
  import { apiJSON, ApiError } from "../lib/api";
  import type {
    Link,
    LinkListResponse,
    PageCollection,
    Tag,
  } from "../lib/types";
  import AppHeader from "../components/AppHeader.svelte";
  import LinkList from "../components/LinkList.svelte";
  import AddLinkModal from "../components/AddLinkModal.svelte";
  import { m } from "../paraglide/messages";

  const PAGE_SIZE = 50;

  let query = $state("");
  let offset = $state(0);
  let links = $state<Link[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let searchDebounce: ReturnType<typeof setTimeout> | undefined;
  let showAddModal = $state(false);

  // The filter comes from the URL, not component state, so "View links" on a tag
  // or collection screen is a plain link and the filtered view can be bookmarked
  // or reloaded: /links?tag=rust, /links?collection=12. What it was filtered by
  // (the tag's or collection's name, for the pill) comes back in the response.
  const urlParams = $derived(new URLSearchParams(router.querystring ?? ""));
  const tagParam = $derived(urlParams.get("tag") ?? "");
  const collectionParam = $derived(urlParams.get("collection") ?? "");
  let filterTag = $state<Tag | null>(null);
  let filterCollection = $state<PageCollection | null>(null);

  // Changing the filter starts again from the first page (a new filter reached
  // by back/forward navigation, say, mustn't keep the old filter's offset).
  let previousFilterKey: string | undefined;
  $effect.pre(() => {
    const key = `${tagParam}|${collectionParam}`;
    if (previousFilterKey !== undefined && previousFilterKey !== key) {
      offset = 0;
    }
    previousFilterKey = key;
  });

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
      if (tagParam) params.set("tag", tagParam);
      if (collectionParam) params.set("collection", collectionParam);
      const res = await apiJSON<LinkListResponse>(
        `/links?${params.toString()}`,
      );
      links = res.links;
      total = res.total;
      filterTag = res.tag ?? null;
      filterCollection = res.collection ?? null;
    } catch (err) {
      error = err instanceof ApiError ? err.message : m.links_load_error();
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

  function clearFilter() {
    offset = 0;
    push("/links");
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

  <p class="page-heading">{m.nav_links()}</p>

  <div class="toolbar">
    <div class="search-wrap">
      <Search size={15} />
      <input
        class="search"
        type="search"
        placeholder={m.links_search_placeholder()}
        oninput={handleSearchInput}
        aria-label={m.library_search_label()}
      />
    </div>
    <button type="button" onclick={() => (showAddModal = true)}>
      <Plus size={14} />
      {m.links_add_button()}
    </button>
  </div>

  {#if (tagParam && filterTag) || (collectionParam && filterCollection)}
    <div class="filter-row">
      <span
        >{tagParam
          ? m.links_filtered_by_tag()
          : m.links_filtered_by_collection()}</span
      >
      <span class="filter-pill">
        {tagParam ? filterTag?.name : filterCollection?.name}
        <button
          type="button"
          class="clear-filter"
          aria-label={m.links_clear_filter()}
          onclick={clearFilter}
        >
          <X size={11} />
        </button>
      </span>
    </div>
  {/if}

  {#if loading}
    <p class="status-text">{m.common_loading()}</p>
  {:else if error}
    <div class="status">
      <AlertCircle size={28} />
      <span>{error}</span>
    </div>
  {:else}
    <LinkList
      {links}
      emptyMessage={query
        ? m.links_no_search_results()
        : tagParam || collectionParam
          ? m.links_nothing_in_filter()
          : m.links_nothing_saved()}
      emptyHint={query || tagParam || collectionParam
        ? undefined
        : m.links_nothing_saved_hint()}
    />
  {/if}

  {#if !loading && !error && links.length > 0}
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

<AddLinkModal bind:open={showAddModal} />

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;
  @use "../styles/components" as comp;

  .screen {
    @include comp.content-screen;
  }

  .page-heading {
    @include type.eyebrow;
    margin: 0 0 1rem;
  }

  button {
    @include comp.bordered-button;
    display: flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.375rem 0.75rem;
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

  // "Filtered by tag [rust x]", tucked up under the toolbar it qualifies.
  .filter-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin: -0.75rem 0 1.25rem;
    font-size: 0.8125rem;
    color: var(--ink-muted);
  }

  .filter-pill {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.2rem 0.35rem 0.2rem 0.7rem;
    border-radius: 999px;
    background: var(--paper-raised);
    border: 1px solid color-mix(in srgb, var(--brass) 45%, var(--rule));
    color: var(--ink);
    font-size: 0.78rem;
  }

  .clear-filter {
    @include comp.icon-btn(1.1rem, rgba(0, 0, 0, 0.05));
  }

  .status-text {
    color: var(--ink-muted);
  }

  .status {
    @include comp.status-block;
    color: var(--accent);

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
