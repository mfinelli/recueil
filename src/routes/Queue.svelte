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
<!-- One screen for everything currently stuck, in two flavors:
     - Failed to capture (queue_items): a URL a device tried and failed to
       archive at all -- there's no page/capture to show yet. Retrying
       flags the item (manual_retry) so some device's next poll of
       GET /queue picks it up again; there's no synchronous "retry now"
       here, and the item legitimately stays in this list (still 'failed',
       now flagged) until a device actually claims it.
     - Failed to process (screenshot/readability/AI jobs): the capture
       itself archived fine, but one of the async enrichment steps that
       runs against it afterward permanently failed. Unlike queue items,
       these are backend-owned -- no device claims them -- so retrying
       resets the job straight back to 'pending' and the backend's own
       next poll picks it up, no flag to show; a successful retry call
       means it's no longer 'failed' at all, so it's just removed from the
       list here rather than shown as "pending retry."
     A capture whose readability extraction permanently failed never gets
     an AI job at all (see internal/httpapi's ListFailedJobs doc comment)
     -- it shows up under Readability, not AI, until that's retried.
     The error message itself is shown on its own line, not folded into
     the meta line -- "rate limited by the AI provider" vs. some other
     failure is often the single most useful piece of information here,
     worth more visual weight than attempts/last-tried. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type {
    FailedJob,
    FailedJobsResponse,
    QueueItem,
    QueueItemListResponse,
  } from "../lib/types";

  type JobKind = "screenshot" | "readability" | "ai";

  let items = $state<QueueItem[]>([]);
  let itemsLoading = $state(true);

  let screenshotJobs = $state<FailedJob[]>([]);
  let readabilityJobs = $state<FailedJob[]>([]);
  let aiJobs = $state<FailedJob[]>([]);
  let jobsLoading = $state(true);

  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let retryingItemId = $state<string | null>(null);
  let retryingJobKey = $state<string | null>(null);

  async function loadItems() {
    itemsLoading = true;
    try {
      const res = await apiJSON<QueueItemListResponse>("/queue-items");
      items = res.items;
    } catch (err) {
      loadError =
        err instanceof ApiError ? err.message : "failed to load queue items";
    } finally {
      itemsLoading = false;
    }
  }

  async function loadJobs() {
    jobsLoading = true;
    try {
      const res = await apiJSON<FailedJobsResponse>("/jobs");
      screenshotJobs = res.screenshot_jobs;
      readabilityJobs = res.readability_jobs;
      aiJobs = res.ai_jobs;
    } catch (err) {
      loadError =
        err instanceof ApiError ? err.message : "failed to load failed jobs";
    } finally {
      jobsLoading = false;
    }
  }

  $effect(() => {
    loadItems();
    loadJobs();
  });

  async function retryItem(item: QueueItem) {
    retryingItemId = item.id;
    actionError = null;
    try {
      await apiJSON(`/queue-items/${item.id}/retry`, { method: "POST" });
      // Optimistic update, same pattern PageDetail's write actions use --
      // reflects the flag immediately rather than refetching the whole
      // list for a single-user tool. The item stays in the list (still
      // 'failed' until some device actually claims and either archives
      // or re-fails it) with its retry state now visibly flagged.
      items = items.map((i) =>
        i.id === item.id ? { ...i, manual_retry: true } : i,
      );
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : "failed to retry item";
    } finally {
      retryingItemId = null;
    }
  }

  function jobsListFor(kind: JobKind): FailedJob[] {
    if (kind === "screenshot") return screenshotJobs;
    if (kind === "readability") return readabilityJobs;
    return aiJobs;
  }

  function setJobsListFor(kind: JobKind, jobs: FailedJob[]) {
    if (kind === "screenshot") screenshotJobs = jobs;
    else if (kind === "readability") readabilityJobs = jobs;
    else aiJobs = jobs;
  }

  async function retryJob(job: FailedJob, kind: JobKind) {
    retryingJobKey = `${kind}:${job.id}`;
    actionError = null;
    try {
      await apiJSON(`/jobs/${kind}/${job.id}/retry`, { method: "POST" });
      // Not an optimistic flag like queue items above -- a successful
      // retry call means this job is no longer 'failed' server-side at
      // all (see this file's own top comment), so it's simply removed
      // from its list rather than shown with a "retry pending" state.
      setJobsListFor(
        kind,
        jobsListFor(kind).filter((j) => j.id !== job.id),
      );
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : "failed to retry job";
    } finally {
      retryingJobKey = null;
    }
  }

  function formatDateTime(iso: string): string {
    return new Date(iso).toLocaleString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
  }
</script>

{#snippet jobList(jobs: FailedJob[], kind: JobKind, label: string)}
  <div class="job-section">
    <h3>{label}</h3>
    {#if jobs.length === 0}
      <p class="status">Nothing failed.</p>
    {:else}
      <ul class="items">
        {#each jobs as job (job.id)}
          <li>
            <div class="item-info">
              <a href={`/pages/${job.page_id}`} use:link class="url"
                >{job.title || job.url}</a
              >
              <span class="meta">
                {job.attempts} attempt{job.attempts === 1 ? "" : "s"}
                {#if job.completed_at}
                  · last tried {formatDateTime(job.completed_at)}
                {/if}
              </span>
              {#if job.error}
                <span class="error-detail">{job.error}</span>
              {/if}
            </div>
            <button
              type="button"
              onclick={() => retryJob(job, kind)}
              disabled={retryingJobKey === `${kind}:${job.id}`}
            >
              Retry
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
{/snippet}

<main class="screen">
  <AppHeader />
  <h1>Queue</h1>

  {#if loadError}
    <p class="status error" role="alert">{loadError}</p>
  {/if}
  {#if actionError}
    <p class="status error" role="alert">{actionError}</p>
  {/if}

  <section>
    <h2>Failed to capture</h2>
    <p class="hint">
      URLs your devices tried and failed to archive. Retrying flags an item to
      be picked up again on a device's next poll -- it isn't retried
      immediately.
    </p>
    {#if itemsLoading}
      <p class="status">Loading…</p>
    {:else if items.length === 0}
      <p class="status">No failed items.</p>
    {:else}
      <ul class="items">
        {#each items as item (item.id)}
          <li>
            <div class="item-info">
              <span class="url">{item.url}</span>
              <span class="meta">
                failed · added {formatDateTime(item.created_at)}
                {#if item.manual_retry}
                  · <span class="pending-retry">retry pending</span>
                {/if}
              </span>
            </div>
            <button
              type="button"
              onclick={() => retryItem(item)}
              disabled={item.manual_retry || retryingItemId === item.id}
            >
              {item.manual_retry ? "Retry queued" : "Retry"}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section>
    <h2>Failed to process</h2>
    <p class="hint">
      Captures that archived fine, but where a follow-up step (screenshot,
      article extraction, or AI summary) permanently failed. Retrying tries
      again on the backend's own next pass.
    </p>
    {#if jobsLoading}
      <p class="status">Loading…</p>
    {:else}
      {@render jobList(screenshotJobs, "screenshot", "Screenshots")}
      {@render jobList(
        readabilityJobs,
        "readability",
        "Readability extraction",
      )}
      {@render jobList(aiJobs, "ai", "AI summaries")}
    {/if}
  </section>
</main>

<style lang="scss">
  .screen {
    max-width: 48rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  h1 {
    margin: 0 0 1rem;
  }

  section {
    margin-bottom: 2rem;
  }

  h2 {
    font-size: 1rem;
    margin-bottom: 0.375rem;
  }

  h3 {
    font-size: 0.8125rem;
    font-weight: 600;
    color: var(--ink-muted);
    margin: 1rem 0 0.25rem;
    text-transform: uppercase;
    letter-spacing: 0.02em;
  }

  .job-section:first-child h3 {
    margin-top: 0;
  }

  .hint {
    margin: 0 0 0.75rem;
    color: var(--ink-muted);
    font-size: 0.8125rem;
  }

  .status {
    color: var(--ink-muted);

    &.error {
      color: var(--accent);
    }
  }

  button {
    padding: 0.375rem 0.75rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper-raised);
    color: var(--ink);
    font: inherit;
    font-size: 0.8125rem;
    cursor: pointer;
    flex-shrink: 0;

    &:disabled {
      opacity: 0.5;
      cursor: default;
    }
  }

  .items {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--rule);
  }

  .items li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.625rem 0.25rem;
    border-bottom: 1px solid var(--rule);
  }

  .item-info {
    display: flex;
    flex-direction: column;
    gap: 0.125rem;
    min-width: 0;
  }

  .url {
    font-weight: 600;
    font-size: 0.9375rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--ink);
    text-decoration: none;

    &:hover {
      text-decoration: underline;
    }
  }

  .meta {
    color: var(--ink-muted);
    font-size: 0.75rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .pending-retry {
    color: var(--accent);
  }

  // Distinct from .meta: the error string (e.g. "rate limited by the AI
  // provider" vs. some other failure) is often the single most actionable
  // piece of information on this screen, so it gets its own line and
  // its own visual weight rather than being folded into the meta line
  // alongside attempts/last-tried.
  .error-detail {
    color: var(--accent);
    font-size: 0.75rem;
    font-style: italic;
    overflow-wrap: break-word;
  }
</style>
