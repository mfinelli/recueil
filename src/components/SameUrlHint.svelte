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
<!-- The same-URL hint: a one-line callout on a page's detail screen when the
     user also has a link with the same normalized URL, and on a link's when
     they also have a page, naming the other and linking to it. `kind` is what
     the OTHER entity is: "page" on a link's screen, "link" on a page's. It's
     a read-time join on normalized_url, not a stored relationship, so there's
     nothing to keep consistent. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import Archive from "@lucide/svelte/icons/archive";
  import Link2 from "@lucide/svelte/icons/link";
  import { m } from "../paraglide/messages";

  let {
    kind,
    id,
    title,
  }: { kind: "page" | "link"; id: number; title: string } = $props();
</script>

<div class="same-url">
  {#if kind === "page"}
    <Archive size={15} />
  {:else}
    <Link2 size={15} />
  {/if}
  <span
    >{kind === "page" ? m.sameurl_also_archived() : m.sameurl_also_saved()}
    <a href={kind === "page" ? `/pages/${id}` : `/links/${id}`} use:link
      >{title}</a
    ></span
  >
</div>

<style lang="scss">
  @use "../styles/mixins" as mix;

  .same-url {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    margin: 0 0 1.5rem;
    padding: 0.55rem 0.8rem;
    background: var(--paper-raised);
    border: 1px solid var(--rule);
    border-radius: 3px;
    font-size: 0.8125rem;
    color: var(--ink-muted);

    :global(svg) {
      flex: none;
    }

    a {
      color: var(--ink);
      text-decoration: underline;
      text-decoration-color: var(--rule);
      text-underline-offset: 2px;

      &:hover {
        color: var(--accent);
        text-decoration-color: currentColor;
      }

      &:focus-visible {
        @include mix.focus-ring;
      }
    }
  }
</style>
