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
<!-- The list of links on the Links page: the counterpart of PageList's list
     view and, like it, a pure renderer of whatever `links` it's handed --
     search and pagination stay the page's concern. A separate component
     rather than PageList with a mode, because a link row is genuinely
     different: there is no grid (a link has no thumbnail), and the row has
     two destinations.

     The row opens the link's own detail screen; the trailing icon button opens
     the saved URL itself in a new tab. They are sibling links, not nested ones
     (anchors can't nest): the <li> is a two-column grid with the row's link
     in the first column and the icon button in the second. The saved URL is the
     link's raw_url, not the normalized_url shown in the row -- normalization
     drops the fragment, which breaks hash-routed apps.

     "Also archived" is a plain indicator, not a link, for the same reason it
     can't nest inside the row link: the detail screen carries the real link to
     the matching page. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import Favicon from "./Favicon.svelte";
  import Archive from "@lucide/svelte/icons/archive";
  import ExternalLink from "@lucide/svelte/icons/external-link";
  import Link2 from "@lucide/svelte/icons/link";
  import type { Link } from "../lib/types";
  import { m } from "../paraglide/messages";

  let {
    links,
    emptyMessage,
    emptyHint,
  }: { links: Link[]; emptyMessage: string; emptyHint?: string } = $props();

  // A link with no favicon (link.favicon_path is null) passes no src, so the
  // request is skipped entirely (see Favicon.svelte).
  function faviconSrc(l: Link): string | null {
    return l.favicon_path !== null ? `/api/links/${l.id}/favicon` : null;
  }

  function displayTitle(l: Link): string {
    return l.title ?? l.normalized_url;
  }

  function formatDate(iso: string): string {
    return new Date(iso).toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  }
</script>

{#if links.length === 0}
  <div class="status">
    <Link2 size={28} />
    <span>{emptyMessage}</span>
    {#if emptyHint}
      <span class="sub">{emptyHint}</span>
    {/if}
  </div>
{:else}
  <ul class="links-list">
    {#each links as l (l.id)}
      <li>
        <a class="row" href={`/links/${l.id}`} use:link>
          <span class="favicon-cell">
            <Favicon src={faviconSrc(l)} size={20} />
          </span>
          <span class="title">{displayTitle(l)}</span>
          <span class="url">{l.normalized_url}</span>
          <span class="date">{formatDate(l.created_at)}</span>
          {#if l.matching_page_id !== null}
            <span class="meta">
              <span
                class="ind"
                role="img"
                aria-label={m.links_also_archived_label()}
              >
                <Archive
                  size={11}
                  aria-hidden="true"
                />{m.links_also_archived()}
              </span>
            </span>
          {/if}
        </a>
        <a
          class="open"
          href={l.raw_url}
          target="_blank"
          rel="noreferrer"
          aria-label={m.links_open_in_new_tab({ title: displayTitle(l) })}
        >
          <ExternalLink size={16} />
        </a>
      </li>
    {/each}
  </ul>
{/if}

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;
  @use "../styles/components" as comp;

  .status {
    @include comp.status-block;
    color: var(--ink-muted);

    :global(svg) {
      color: var(--rule);
    }

    .sub {
      font-size: 0.8125rem;
      max-width: 26rem;
    }
  }

  .links-list {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px dotted var(--rule);
  }

  // The whole row (both columns) takes the hover background, so the trailing
  // icon button is part of the row visually even though it's a separate link.
  .links-list li {
    @include mix.dotted-rule;
    display: grid;
    grid-template-columns: 1fr auto;
    align-items: center;

    &:hover {
      background: var(--paper-raised);
    }
  }

  .row {
    display: grid;
    grid-template-columns: auto 1fr auto;
    align-items: center;
    gap: 0.125rem 0.75rem;
    padding: 0.625rem 0.25rem;
    min-width: 0;
    text-decoration: none;
    color: inherit;
  }

  // The row's favicon sits in the first grid column, spanning title and URL.
  .favicon-cell {
    display: flex;
    grid-column: 1;
    grid-row: 1 / 3;
  }

  .title {
    grid-column: 2;
    font-weight: 600;
    min-width: 0;
  }

  .url {
    grid-column: 2;
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.8125rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .date {
    grid-column: 3;
    grid-row: 1 / 3;
    align-self: center;
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.8125rem;
    white-space: nowrap;
  }

  // The "Also archived" line: its own third row under the URL.
  .meta {
    grid-column: 2 / 4;
    grid-row: 3;
    display: flex;
    flex-wrap: wrap;
    gap: 0.1rem 0.9rem;
    margin-top: 0.1rem;
  }

  .ind {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    @include type.data-mono;
    font-size: 0.7rem;
    color: var(--ink-muted);
  }

  .open {
    @include comp.icon-btn(1.9rem);
    margin-right: 0.35rem;
    text-decoration: none;
  }
</style>
