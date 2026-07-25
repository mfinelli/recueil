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
<!-- Shared page-listing UI (list/grid toggle, favicon/thumbnail fallback
     tracking, title/date formatting) factored out of Library.svelte so
     TagDetail/CollectionDetail can render "here are the pages for this
     tag/collection" without re-implementing the same markup. Search and
     pagination stay Library-only concerns -- this component only ever
     renders whatever `pages` it's handed, with no opinion on how that
     list was produced. Reuses Library's own view-mode localStorage key
     and paraglide message keys (library_view_*) rather than introducing
     parallel ones -- "List"/"Grid"/"View" aren't really library-specific
     text, and a person's list-vs-grid preference is one setting, not a
     per-screen one. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import { SvelteSet } from "svelte/reactivity";
  import type { Page } from "../lib/types";
  import { m } from "../paraglide/messages";

  const VIEW_MODE_KEY = "recueil:library-view-mode";

  type ViewMode = "list" | "grid";

  function loadViewMode(): ViewMode {
    const stored = localStorage.getItem(VIEW_MODE_KEY);
    return stored === "grid" ? "grid" : "list";
  }

  let { pages, emptyMessage }: { pages: Page[]; emptyMessage: string } =
    $props();

  let viewMode = $state<ViewMode>(loadViewMode());
  // Tracks page ids whose favicon/thumbnail request 404ed -- see
  // Library.svelte's git history for the original comment on this same
  // pattern. Keyed by page id (covers both the favicon and thumbnail
  // request for that row/card), SvelteSet so .add() itself triggers
  // reactivity.
  let imageLoadFailed = new SvelteSet<number>();

  // Reset per `pages` prop identity, not per individual page: a fresh
  // array (a new search, or this component mounted for a different
  // tag/collection) means any previously-failed image ids no longer
  // necessarily apply to what's now being shown.
  $effect(() => {
    void pages;
    imageLoadFailed.clear();
  });

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

<div class="view-toggle" role="group" aria-label={m.library_view_label()}>
  <button class:active={viewMode === "list"} onclick={() => setViewMode("list")}
    >{m.library_view_list()}</button
  >
  <button class:active={viewMode === "grid"} onclick={() => setViewMode("grid")}
    >{m.library_view_grid()}</button
  >
</div>

{#if pages.length === 0}
  <p class="status">{emptyMessage}</p>
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

<style lang="scss">
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

  .view-toggle {
    display: flex;
    width: fit-content;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    overflow: hidden;
    margin-bottom: 1.5rem;

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
</style>
