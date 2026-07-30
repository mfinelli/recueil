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
<!-- Routed by a wildcard path (/collections/*, e.g.
     /collections/cooking/recipes), not a single :slug param like
     TagDetail -- collections nest, tags don't. Resolution happens
     entirely client-side against the same flat GET /collections list
     Collections.svelte already uses to build its tree: split the
     wildcard on "/", then walk each segment matching (slug, parent_id)
     against the list, one level at a time. No backend path-resolving
     endpoint exists or is needed.

     This is NOT recursive: this only ever shows pages filed directly in
     the resolved collection (GET /collections/{id}/pages, same endpoint
     and shape as the management screen already uses), not pages anywhere
     in its subtree. Sub-collections are surfaced as their own links instead,
     so browsing into a child is a click away without conflating "this
     collection's own pages" with "everything under it." -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import ChevronLeft from "@lucide/svelte/icons/chevron-left";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import AppHeader from "../components/AppHeader.svelte";
  import PageList from "../components/PageList.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type {
    Collection,
    CollectionListResponse,
    CollectionPagesResponse,
    Page,
  } from "../lib/types";
  import { m } from "../paraglide/messages";

  let { params }: { params: { wild: string } } = $props();

  let matched = $state<Collection[]>([]);
  let children = $state<Collection[]>([]);
  let pages = $state<Page[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let notFound = $state(false);

  // Walks the wildcard path's segments against the flat collection list,
  // one level at a time: each segment must match a collection whose slug
  // equals it AND whose parent_id equals the previous segment's resolved
  // id (null for the first segment). Returns the full chain of matched
  // collections (ancestors then the target), or null if any segment
  // doesn't resolve.
  function resolvePath(all: Collection[], wild: string): Collection[] | null {
    const segments = wild.split("/").filter((s) => s.length > 0);
    let parentId: number | null = null;
    const chain: Collection[] = [];
    for (const segment of segments) {
      const found = all.find(
        (c) => c.slug === segment && c.parent_id === parentId,
      );
      if (!found) return null;
      chain.push(found);
      parentId = found.id;
    }
    return chain.length > 0 ? chain : null;
  }

  async function load(wild: string) {
    loading = true;
    error = null;
    notFound = false;
    try {
      const listRes = await apiJSON<CollectionListResponse>("/collections");
      const chain = resolvePath(listRes.collections, wild);
      if (!chain) {
        notFound = true;
        return;
      }
      matched = chain;
      const target = chain[chain.length - 1];
      children = listRes.collections
        .filter((c) => c.parent_id === target.id)
        .sort((a, b) => a.name.localeCompare(b.name));

      const pagesRes = await apiJSON<CollectionPagesResponse>(
        `/collections/${target.id}/pages`,
      );
      pages = pagesRes.pages;
    } catch (err) {
      error =
        err instanceof ApiError ? err.message : m.collectiondetail_load_error();
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load(params.wild);
  });

  // The href for the collection at chain position `index` -- the path
  // up to and including that ancestor, joined by slug, same shape the
  // route itself expects.
  function pathHref(index: number): string {
    return (
      "/collections/" +
      matched
        .slice(0, index + 1)
        .map((c) => c.slug)
        .join("/")
    );
  }
</script>

<main class="screen">
  <AppHeader />
  <a class="back" href="/collections" use:link>
    <ChevronLeft size={14} />
    {m.collectiondetail_back()}
  </a>

  {#if loading}
    <p class="status">{m.common_loading()}</p>
  {:else if notFound}
    <div class="status-block error" role="alert">
      <AlertCircle size={28} />
      <span>{m.collectiondetail_not_found()}</span>
    </div>
  {:else if error}
    <div class="status-block error" role="alert">
      <AlertCircle size={28} />
      <span>{error}</span>
    </div>
  {:else}
    {@const target = matched[matched.length - 1]}
    {#if matched.length > 1}
      <nav class="breadcrumb" aria-label={m.collectiondetail_breadcrumb_aria()}>
        {#each matched.slice(0, -1) as ancestor, i (ancestor.id)}
          <a href={pathHref(i)} use:link>{ancestor.name}</a>
          <span class="sep">/</span>
        {/each}
        <span>{target.name}</span>
      </nav>
    {/if}

    <h1>{target.name}</h1>
    {#if target.description}
      <p class="description">{target.description}</p>
    {/if}

    {#if children.length > 0}
      <section class="subcollections">
        <p class="eyebrow">{m.collectiondetail_subcollections_heading()}</p>
        <ul class="chips">
          {#each children as child (child.id)}
            <li>
              <a href={pathHref(matched.length) + "/" + child.slug} use:link>
                {child.name}
              </a>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    <PageList {pages} emptyMessage={m.collectiondetail_no_pages()} />
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

  .breadcrumb {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    margin-bottom: 0.5rem;
    @include type.data-mono;
    font-size: 0.8125rem;
    color: var(--ink-muted);

    a {
      color: inherit;
      text-decoration: none;

      &:hover {
        color: var(--accent);
      }

      &:focus-visible {
        @include mix.focus-ring;
      }
    }
  }

  h1 {
    @include type.heading;
    font-size: 1.6rem;
    margin: 0 0 0.4rem;
  }

  .description {
    margin: 0 0 1.5rem;
    color: var(--ink-muted);
    font-size: 0.9375rem;
  }

  .subcollections {
    margin-bottom: 1.75rem;
  }

  .eyebrow {
    @include type.eyebrow;
    margin: 0 0 0.6rem;
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    list-style: none;
    margin: 0;
    padding: 0;

    a {
      @include comp.pill;
      display: block;
      padding: 0.2rem 0.75rem;
      font-size: 0.8125rem;
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

  .status {
    color: var(--ink-muted);
  }

  .status-block {
    @include comp.status-block;
    color: var(--accent);

    :global(svg) {
      opacity: 0.6;
    }
  }
</style>
