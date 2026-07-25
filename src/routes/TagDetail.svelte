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
<!-- Routed by slug (/tags/:slug), not id: this is the browsable,
     bookmarkable dashboard URL, so it uses the same identifier a person
     would actually see and share.  This is unrelated to the id-keyed
     PATCH/DELETE /api/tags/{id} calls Tags.svelte makes for
     rename/delete -- those never appear in an address bar, so there's no
     reason for them to be slug-based too.

     GET /api/tags/{slug}/pages returns the tag itself alongside its
     pages in one round trip, so this needs only one request, not a separate
     lookup against the full tag list to get a name for the heading. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import AppHeader from "../components/AppHeader.svelte";
  import PageList from "../components/PageList.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type { Tag, TagPagesResponse, Page } from "../lib/types";
  import { m } from "../paraglide/messages";

  let { params }: { params: { slug: string } } = $props();

  let tag = $state<Tag | null>(null);
  let pages = $state<Page[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);

  async function load(slug: string) {
    loading = true;
    error = null;
    try {
      const res = await apiJSON<TagPagesResponse>(`/tags/${slug}/pages`);
      tag = res.tag;
      pages = res.pages;
    } catch (err) {
      error = err instanceof ApiError ? err.message : m.tagdetail_load_error();
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load(params.slug);
  });
</script>

<main class="screen">
  <AppHeader />
  <a class="back" href="/tags" use:link>{m.tagdetail_back()}</a>

  {#if loading}
    <p class="status">{m.common_loading()}</p>
  {:else if error}
    <p class="status error" role="alert">{error}</p>
  {:else}
    <h1>{tag?.name}</h1>
    <PageList {pages} emptyMessage={m.tagdetail_no_pages()} />
  {/if}
</main>

<style lang="scss">
  .screen {
    max-width: 64rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  .back {
    display: inline-block;
    margin-bottom: 1rem;
    color: var(--ink-muted);
    font-size: 0.875rem;
    text-decoration: none;

    &:hover {
      color: var(--ink);
    }
  }

  h1 {
    margin: 0 0 1.25rem;
  }

  .status {
    color: var(--ink-muted);

    &.error {
      color: var(--accent);
    }
  }
</style>
