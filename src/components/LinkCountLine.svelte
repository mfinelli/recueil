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
<!-- The one-line "this tag / collection also has links" note on the tag and
     collection screens: "{n} links have this tag." or "{n} links in this
     collection.", with a "View links" link to the Links page filtered by it.
     It's a pointer, not a second list: a link has no thumbnail and the page
     lists are built around grid and list views of pages.

     Renders nothing when the count is 0 or links are off, so a tag or
     collection with no links looks exactly as it did before links existed. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import Link2 from "@lucide/svelte/icons/link";
  import { session } from "../lib/session.svelte";
  import { m } from "../paraglide/messages";

  let {
    count,
    kind,
    href,
  }: { count: number; kind: "tag" | "collection"; href: string } = $props();

  // One and not-one, not a language-specific plural rule: the line only appears
  // for 1 or more, and French groups 1 with 0 anyway, so the split that
  // matters is the same in both.
  const text = $derived(
    kind === "tag"
      ? count === 1
        ? m.linkcount_tag_one()
        : m.linkcount_tag_other({ count })
      : count === 1
        ? m.linkcount_collection_one()
        : m.linkcount_collection_other({ count }),
  );
</script>

{#if session.linksEnabled && count > 0}
  <p class="xref">
    <Link2 size={15} />
    <span>{text}</span>
    <a {href} use:link>{m.linkcount_view()}</a>
  </p>
{/if}

<style lang="scss">
  @use "../styles/mixins" as mix;

  .xref {
    display: flex;
    align-items: center;
    gap: 0.45rem;
    margin: 0 0 1.5rem;
    color: var(--ink-muted);
    font-size: 0.9375rem;

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
      }

      &:focus-visible {
        @include mix.focus-ring;
      }
    }
  }
</style>
