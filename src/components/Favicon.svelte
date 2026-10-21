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
<!-- A page's or link's favicon, wherever one appears: the image when there is one
     and it loads, otherwise a small Globe in a bordered box (or nothing, where the
     caller says so). It owns its own failure state, so a caller just says where
     the icon lives and how big it should be:

       - no `src` (the page or link has no favicon) skips the request entirely,
         straight to the fallback, rather than waiting on one that was always going to
         404;
       - a `src` whose request fails falls back for as long as this instance shows that
         same `src`. Another item reusing the instance starts fresh, so there is no
         list-level bookkeeping to reset.

     `placeholder={false}` is for the one place that shows nothing instead of the
     Globe, the source row on the detail screens. -->
<script lang="ts">
  import Globe from "@lucide/svelte/icons/globe";

  let {
    src,
    size,
    placeholder = true,
  }: {
    src: string | null;
    // Width and height in px; the Globe scales with it.
    size: number;
    placeholder?: boolean;
  } = $props();

  // The src that failed to load, not just that something did.
  let failedSrc = $state<string | null>(null);
  const showImage = $derived(src !== null && src !== failedSrc);
</script>

{#if showImage}
  <img
    class="favicon"
    {src}
    alt=""
    loading="lazy"
    style:width={`${size}px`}
    style:height={`${size}px`}
    onerror={() => (failedSrc = src)}
  />
{:else if placeholder}
  <span
    class="favicon-placeholder"
    aria-hidden="true"
    style:width={`${size}px`}
    style:height={`${size}px`}
  >
    <Globe size={Math.round(size * 0.62)} />
  </span>
{/if}

<style lang="scss">
  .favicon,
  .favicon-placeholder {
    flex: none;
    border-radius: 15%;
  }

  .favicon-placeholder {
    display: grid;
    place-items: center;
    background: var(--paper-raised);
    border: 1px solid var(--rule);
    color: var(--ink-muted);
  }
</style>
