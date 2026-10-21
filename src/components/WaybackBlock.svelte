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
<!-- The "Internet Archive" block on a page's and a link's detail screens:
     whether the URL has been submitted to the Wayback Machine, a button to
     submit it, and links to the public calendar of snapshots.

     It is deliberately plain. The calendar links point at the Wayback
     Machine's  list for the URL (web.archive.org/web/*/<url>), so no snapshot
     URL or timestamp is stored or shown; "Check for existing snapshots" before
     a submission and "View snapshots" after one are the same link. And there
     is no polling and no live progress: submitting returns the pending state,
     the button stays off while it is pending, and the result appears on a
     later load of the screen, once the agent has recorded how the job went
     (the server only ever reads that stored result, and never asks archive.org
     itself). Without a configured key pair only the links are offered, with
     no button. -->
<script lang="ts">
  import Landmark from "@lucide/svelte/icons/landmark";
  import ExternalLink from "@lucide/svelte/icons/external-link";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import { apiJSON, ApiError } from "../lib/api";
  import { session } from "../lib/session.svelte";
  import type { WaybackState } from "../lib/types";
  import { m } from "../paraglide/messages";

  let {
    url,
    wayback,
    submitPath,
    onchange,
  }: {
    // The normalized URL, which is what the server submits and so what the
    // calendar is for.
    url: string;
    wayback: WaybackState | null;
    // POST route that starts a submission, e.g. "/links/42/wayback".
    submitPath: string;
    // Called with the state the server answers a submission with.
    onchange: (wayback: WaybackState) => void;
  } = $props();

  let submitting = $state(false);
  let error = $state<string | null>(null);

  const calendarUrl = $derived(`https://web.archive.org/web/*/${url}`);
  const pending = $derived(wayback?.status === "pending");

  function formatDateTime(iso: string): string {
    return new Date(iso).toLocaleString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
  }

  // What the button says: the first submission, one after a success, or one after
  // a failure; and "Submitting…" (disabled) while the request is in flight or the
  // job is pending.
  const buttonLabel = $derived(
    submitting || pending
      ? m.wayback_submitting()
      : wayback === null
        ? m.wayback_submit()
        : wayback.status === "succeeded"
          ? m.wayback_submit_again()
          : m.wayback_try_again(),
  );

  async function submit() {
    submitting = true;
    error = null;
    try {
      onchange(await apiJSON<WaybackState>(submitPath, { method: "POST" }));
    } catch (err) {
      error = err instanceof ApiError ? err.message : m.wayback_submit_error();
    } finally {
      submitting = false;
    }
  }
</script>

<section>
  <div class="block-header">
    <h2>{m.wayback_heading()}</h2>
  </div>

  <div class="wb">
    <div>
      <div class="state">
        {#if wayback === null}
          {m.wayback_not_submitted()}
        {:else if wayback.status === "pending"}
          {m.wayback_pending()}
          <span class="when">{m.wayback_pending_hint()}</span>
        {:else if wayback.status === "succeeded"}
          {m.wayback_submitted()}
        {:else}
          {m.wayback_failed()}
          {#if wayback.error}
            <span class="when">{wayback.error}</span>
          {/if}
        {/if}
        {#if wayback?.last_submitted_at}
          <span class="when"
            >{m.wayback_last_submitted({
              when: formatDateTime(wayback.last_submitted_at),
            })}</span
          >
        {/if}
      </div>
      <a class="check" href={calendarUrl} target="_blank" rel="noreferrer">
        {wayback?.last_submitted_at ? m.wayback_view() : m.wayback_check()}
        <ExternalLink size={12} />
      </a>
    </div>

    {#if session.waybackEnabled}
      <button type="button" disabled={pending || submitting} onclick={submit}>
        <Landmark size={14} />
        {buttonLabel}
      </button>
    {/if}
  </div>

  {#if error}
    <p class="error" role="alert">
      <AlertCircle size={15} />
      <span>{error}</span>
    </p>
  {/if}

  {#if session.waybackEnabled}
    <p class="priv">{m.wayback_privacy()}</p>
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

  .wb {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    background: var(--paper-raised);
    border: 1px solid var(--rule);
    border-radius: 3px;
    padding: 0.75rem 1rem;
  }

  .state {
    font-size: 0.875rem;

    .when {
      @include type.data-mono;
      display: block;
      margin-top: 0.15rem;
      font-size: 0.8rem;
      color: var(--ink-muted);
    }
  }

  .check {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    margin-top: 0.35rem;
    font-size: 0.8rem;
    color: var(--ink-muted);
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

  button {
    @include comp.bordered-button;
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 0.3rem;
    padding: 0.375rem 0.75rem;
    font-size: 0.875rem;
  }

  .error {
    display: flex;
    align-items: flex-start;
    gap: 0.4rem;
    margin: 0.6rem 0 0;
    color: var(--accent);
    font-size: 0.85rem;
    line-height: 1.35;

    :global(svg) {
      flex: none;
      margin-top: 0.1rem;
    }
  }

  .priv {
    margin: 0.5rem 0 0;
    font-size: 0.75rem;
    color: var(--ink-muted);
    line-height: 1.5;
    max-width: 40rem;
  }
</style>
