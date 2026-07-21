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
<!-- Display only in this pass: capture (version) history, tags, and
     collection memberships are all read-only here. Editing any of them
     (adding/removing a tag or collection, the excluded_from_mirror
     toggle, the language-correction endpoint) is a later round's work --
     all three GET-side pieces already exist server-side, this just
     doesn't call the write endpoints yet.

     Capture rows link straight to GET /api/captures/{id}/html (a real
     backend URL, opened in a new tab) rather than through the SPA --
     there's no in-app reader view built yet, and the browser already
     knows how to render an HTML document on its own. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import { apiJSON, ApiError } from "../lib/api";
  import type { PageDetail } from "../lib/types";

  let { params }: { params: { id: string } } = $props();

  let page = $state<PageDetail | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);

  $effect(() => {
    const id = params.id;
    loading = true;
    error = null;
    page = null;
    apiJSON<PageDetail>(`/pages/${id}`)
      .then((res) => {
        page = res;
      })
      .catch((err: unknown) => {
        error = err instanceof ApiError ? err.message : "failed to load page";
      })
      .finally(() => {
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
</script>

<main class="screen">
  <a class="back" href="/" use:link>&larr; Library</a>

  {#if loading}
    <p class="status">Loading…</p>
  {:else if error}
    <p class="status error" role="alert">{error}</p>
  {:else if page}
    <h1>{page.title ?? page.normalized_url}</h1>
    <a
      class="source-url"
      href={page.normalized_url}
      target="_blank"
      rel="noreferrer">{page.normalized_url}</a
    >

    {#if page.tags.length > 0}
      <ul class="tags">
        {#each page.tags as tag (tag.id)}
          <li>{tag.name}</li>
        {/each}
      </ul>
    {/if}

    {#if page.collections.length > 0}
      <p class="collections">
        In: {page.collections.map((c) => c.name).join(", ")}
      </p>
    {/if}

    <h2>Captures</h2>
    <ul class="captures">
      {#each page.captures as capture (capture.id)}
        <li>
          <a
            href={`/api/captures/${capture.id}/html`}
            target="_blank"
            rel="noreferrer"
          >
            <span class="captured-at"
              >{formatDateTime(capture.captured_at)}</span
            >
            <span class="meta"
              >{capture.source} · {formatBytes(
                capture.html_uncompressed_size_bytes,
              )}</span
            >
          </a>
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

  .tags {
    display: flex;
    flex-wrap: wrap;
    gap: 0.375rem;
    list-style: none;
    margin: 0 0 0.75rem;
    padding: 0;

    li {
      padding: 0.125rem 0.5rem;
      border-radius: 999px;
      background: var(--paper-raised);
      border: 1px solid var(--rule);
      font-size: 0.75rem;
    }
  }

  .collections {
    margin: 0 0 1.5rem;
    color: var(--ink-muted);
    font-size: 0.875rem;
  }

  h2 {
    font-size: 1rem;
    margin-bottom: 0.5rem;
  }

  .captures {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--rule);
  }

  .captures li {
    border-bottom: 1px solid var(--rule);
  }

  .captures a {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.625rem 0.25rem;
    text-decoration: none;
    color: inherit;

    &:hover {
      background: var(--paper-raised);
    }
  }

  .meta {
    color: var(--ink-muted);
    font-size: 0.8125rem;
    white-space: nowrap;
  }
</style>
