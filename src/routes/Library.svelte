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
     paginated. Display/navigation only in this pass -- tagging,
     collection membership editing, and the mirror-exclusion toggle are
     PageDetail concerns for a later round, not this list. -->
<script lang="ts">
  import { link, push } from "svelte-spa-router";
  import { apiJSON, ApiError } from "../lib/api";
  import type { Page, PageListResponse } from "../lib/types";
  import { session } from "../lib/session.svelte";

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

  async function handleLogout() {
    await session.logout();
    await push("/login");
  }
</script>

<main class="screen">
  <header>
    <h1>recueil</h1>
    {#if session.user}
      <div class="account">
        <span>{session.user.username}</span>
        <button onclick={handleLogout}>Sign out</button>
      </div>
    {/if}
  </header>

  <input
    class="search"
    type="search"
    placeholder="Search your archive…"
    oninput={handleSearchInput}
    aria-label="Search"
  />

  {#if loading}
    <p class="status">Loading…</p>
  {:else if error}
    <p class="status error" role="alert">{error}</p>
  {:else if pages.length === 0}
    <p class="status">
      {query ? "No pages match your search." : "Nothing archived yet."}
    </p>
  {:else}
    <ul class="pages">
      {#each pages as page (page.id)}
        <li>
          <a href={`/pages/${page.id}`} use:link>
            <span class="title">{displayTitle(page)}</span>
            <span class="url">{page.normalized_url}</span>
            <span class="date">{formatDate(page.latest_capture_at)}</span>
          </a>
        </li>
      {/each}
    </ul>

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
    max-width: 48rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
  }

  .account {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    font-size: 0.875rem;
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

  .search {
    width: 100%;
    padding: 0.625rem 0.75rem;
    margin-bottom: 1.5rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper);
    color: var(--ink);
    font: inherit;
  }

  .status {
    color: var(--ink-muted);

    &.error {
      color: var(--accent);
    }
  }

  .pages {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--rule);
  }

  .pages li {
    border-bottom: 1px solid var(--rule);
  }

  .pages a {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.125rem 1rem;
    padding: 0.75rem 0.25rem;
    text-decoration: none;
    color: inherit;

    &:hover {
      background: var(--paper-raised);
    }
  }

  .title {
    grid-column: 1;
    font-weight: 600;
  }

  .date {
    grid-column: 2;
    grid-row: 1 / 3;
    align-self: center;
    color: var(--ink-muted);
    font-size: 0.8125rem;
    white-space: nowrap;
  }

  .url {
    grid-column: 1;
    color: var(--ink-muted);
    font-size: 0.8125rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
