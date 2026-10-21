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
<!-- A list of the items related to one item, with a pencil that reveals remove buttons
     and a title-or-URL search for adding more: "Related pages" on a page, "Related
     links" on a link (and, across kinds, a page's related links and a link's related
     pages). One component for all of them, driven entirely by props.

     `kind` is what the list holds, which fixes the routes and the row: the rows open
     `/{kind}s/{id}`, the search is `/{kind}s/relation-candidates` and answers
     `{ {kind}s: [...] }`, and adding sends `{ {kind}_id }`. `ownerPath` is the item
     the list belongs to (`/pages/42`), so adding posts to
     `{ownerPath}/related-{kind}s` and removing deletes `.../{id}`. `excludeId` is the
     owner's own id when the list holds the owner's own kind, so it can't be offered
     to itself; a list of the other kind leaves it out, since ids from different
     tables aren't comparable.

     The list lives in the parent (it arrives with the item's detail), so changes go
     back through `onchange`, and failures through `onerror` so they land in the
     screen's own error banner like every other action on it. -->
<script lang="ts">
  import Pencil from "@lucide/svelte/icons/pencil";
  import Check from "@lucide/svelte/icons/check";
  import X from "@lucide/svelte/icons/x";
  import { link } from "svelte-spa-router";
  import Favicon from "./Favicon.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type { RelatedItem } from "../lib/types";

  let {
    items,
    kind,
    ownerPath,
    excludeId,
    labels,
    onchange,
    onerror,
  }: {
    items: RelatedItem[];
    kind: "page" | "link";
    ownerPath: string;
    excludeId?: number;
    labels: {
      heading: string;
      empty: string;
      edit: string;
      done: string;
      searchPlaceholder: string;
      noResults: string;
      remove: (name: string) => string;
      relateError: string;
      removeError: string;
    };
    onchange: (items: RelatedItem[]) => void;
    onerror: (message: string | null) => void;
  } = $props();

  let editing = $state(false);
  let searchQuery = $state("");
  let searchResults = $state<RelatedItem[]>([]);
  let adding = $state(false);
  // A setTimeout handle consumed only by clearTimeout/reassignment, never read
  // reactively, so not $state (same as Library's searchDebounce).
  let searchDebounce: ReturnType<typeof setTimeout> | undefined;

  // Navigating to another item reuses this instance, so start clean: no half-typed
  // search or open editing state carried over, and no pending search for the old one.
  $effect(() => {
    void ownerPath;
    editing = false;
    searchQuery = "";
    searchResults = [];
    clearTimeout(searchDebounce);
  });

  const title = (item: RelatedItem) => item.title ?? item.normalized_url;

  // `items` is already loaded with the owner, so search results are only filtered
  // against it client-side, rather than asking the backend to.
  const availableResults = $derived.by(() => {
    const relatedIds = new Set(items.map((i) => i.id));
    return searchResults.filter((r) => !relatedIds.has(r.id));
  });

  // Paired with `bind:value={searchQuery}` so a successful add can clear the
  // visible text programmatically.
  function handleSearchInput() {
    clearTimeout(searchDebounce);
    const value = searchQuery;
    if (!value.trim()) {
      searchResults = [];
      return;
    }
    searchDebounce = setTimeout(async () => {
      const params = new URLSearchParams(
        excludeId === undefined
          ? { q: value }
          : { q: value, exclude: String(excludeId) },
      );
      try {
        const res = await apiJSON<Record<string, RelatedItem[]>>(
          `/${kind}s/relation-candidates?${params.toString()}`,
        );
        // A slower, now-stale response must not overwrite a newer one: the query
        // being looked at now decides whether this one is still relevant.
        if (searchQuery === value) {
          searchResults = res[`${kind}s`] ?? [];
        }
      } catch {
        // Best-effort: a failed search just leaves the dropdown empty.
        searchResults = [];
      }
    }, 300);
  }

  async function add(target: RelatedItem) {
    adding = true;
    onerror(null);
    try {
      const related = await apiJSON<RelatedItem>(
        `${ownerPath}/related-${kind}s`,
        { method: "POST", body: { [`${kind}_id`]: target.id } },
      );
      onchange(
        [...items, related].sort((a, b) => title(a).localeCompare(title(b))),
      );
      searchQuery = "";
      searchResults = [];
    } catch (err) {
      onerror(err instanceof ApiError ? err.message : labels.relateError);
    } finally {
      adding = false;
    }
  }

  async function remove(relatedId: number) {
    onerror(null);
    try {
      await apiJSON(`${ownerPath}/related-${kind}s/${relatedId}`, {
        method: "DELETE",
      });
      onchange(items.filter((i) => i.id !== relatedId));
    } catch (err) {
      onerror(err instanceof ApiError ? err.message : labels.removeError);
    }
  }
</script>

<section>
  <div class="block-header">
    <h2>{labels.heading}</h2>
    <button
      type="button"
      class="edit-toggle"
      class:active={editing}
      aria-label={editing ? labels.done : labels.edit}
      onclick={() => (editing = !editing)}
    >
      {#if editing}
        <Check size={13} />
      {:else}
        <Pencil size={13} />
      {/if}
    </button>
  </div>
  {#if items.length > 0}
    <ul class="related-list">
      {#each items as item (item.id)}
        <li>
          <a href={`/${kind}s/${item.id}`} use:link class="related-row">
            <span class="related-favicon-cell">
              <Favicon
                src={item.favicon_path !== null
                  ? `/api/${kind}s/${item.id}/favicon`
                  : null}
                size={16}
              />
            </span>
            <span class="related-title">{title(item)}</span>
            <span class="related-url">{item.normalized_url}</span>
          </a>
          {#if editing}
            <button
              type="button"
              class="remove"
              aria-label={labels.remove(title(item))}
              onclick={() => remove(item.id)}
            >
              <X size={11} />
            </button>
          {/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p class="empty-note">{labels.empty}</p>
  {/if}
  {#if editing}
    <div class="related-search-wrap">
      <input
        type="text"
        placeholder={labels.searchPlaceholder}
        bind:value={searchQuery}
        oninput={handleSearchInput}
        disabled={adding}
      />
      {#if availableResults.length > 0}
        <ul class="related-results">
          {#each availableResults as candidate (candidate.id)}
            <li>
              <button
                type="button"
                onclick={() => add(candidate)}
                disabled={adding}
              >
                <span class="result-title">{title(candidate)}</span>
                <span class="result-url">{candidate.normalized_url}</span>
              </button>
            </li>
          {/each}
        </ul>
      {:else if searchQuery.trim()}
        <ul class="related-results">
          <li class="no-results">{labels.noResults}</li>
        </ul>
      {/if}
    </div>
  {/if}
</section>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;
  @use "../styles/components" as comp;

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

  .remove {
    @include comp.icon-btn(1.1rem, rgba(0, 0, 0, 0.05));
  }

  .empty-note {
    color: var(--ink-muted);
    font-size: 0.8125rem;
    font-style: italic;
  }

  // Related links: the same compact list and typeahead as a page's related pages.
  .related-list {
    list-style: none;
    margin: 0 0 0.65rem;
    padding: 0;
    border-top: 1px dotted var(--rule);
  }

  .related-list li {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    border-bottom: 1px dotted var(--rule);
  }

  .related-row {
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

  .related-favicon-cell {
    display: flex;
    grid-column: 1;
    grid-row: 1 / 3;
  }

  .related-title {
    grid-column: 2;
    font-size: 0.85rem;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .related-url {
    grid-column: 2;
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.75rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .related-list .remove {
    flex: none;
    margin-right: 0.4rem;
  }

  .related-search-wrap {
    position: relative;
    max-width: 26rem;

    input[type="text"] {
      display: block;
      width: 100%;
      padding: 0.45rem 0.65rem;
    }
  }

  .related-results {
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
</style>
