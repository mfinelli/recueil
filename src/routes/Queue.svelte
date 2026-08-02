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
<!-- Full queue visibility.

     Retrying a job updates it in place to status: "pending" rather
     than removing it from its list.

     Manual refresh (button) plus a light 15-minute poll -- not more frequent
     than that, matching the same window everything else here already uses,
     and light enough not to lean on the Worker's free tier for something a
     person is unlikely to be staring at continuously. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import RefreshCw from "@lucide/svelte/icons/refresh-cw";
  import ClockIcon from "@lucide/svelte/icons/clock";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type {
    Job,
    JobsResponse,
    PendingCapture,
    PendingCaptureListResponse,
    QueueItem,
    QueueItemListResponse,
  } from "../lib/types";
  import { m } from "../paraglide/messages";
  import { getLocale } from "../paraglide/runtime";

  type JobKind = "screenshot" | "readability" | "ai";
  type StatusCategory = "pending" | "active" | "done" | "failed";

  const REFRESH_INTERVAL_MS = 15 * 60 * 1000;

  let items = $state<QueueItem[]>([]);
  let itemsLoading = $state(true);

  let pendingCaptures = $state<PendingCapture[]>([]);
  let pendingCapturesLoading = $state(true);

  let screenshotJobs = $state<Job[]>([]);
  let readabilityJobs = $state<Job[]>([]);
  let aiJobs = $state<Job[]>([]);
  let jobsLoading = $state(true);

  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let retryingItemId = $state<string | null>(null);
  let retryingJobKey = $state<string | null>(null);
  let lastUpdatedAt = $state<Date | null>(null);

  async function loadItems() {
    itemsLoading = true;
    try {
      const res = await apiJSON<QueueItemListResponse>("/queue-items");
      items = res.items;
    } catch (err) {
      loadError =
        err instanceof ApiError ? err.message : m.queue_load_items_error();
    } finally {
      itemsLoading = false;
    }
  }

  async function loadPendingCaptures() {
    pendingCapturesLoading = true;
    try {
      const res =
        await apiJSON<PendingCaptureListResponse>("/pending-captures");
      pendingCaptures = res.pending_captures;
    } catch (err) {
      loadError =
        err instanceof ApiError
          ? err.message
          : m.queue_load_pending_captures_error();
    } finally {
      pendingCapturesLoading = false;
    }
  }

  async function loadJobs() {
    jobsLoading = true;
    try {
      const res = await apiJSON<JobsResponse>("/jobs");
      screenshotJobs = res.screenshot_jobs;
      readabilityJobs = res.readability_jobs;
      aiJobs = res.ai_jobs;
    } catch (err) {
      loadError =
        err instanceof ApiError ? err.message : m.queue_load_jobs_error();
    } finally {
      jobsLoading = false;
    }
  }

  async function loadAll() {
    loadError = null;
    await Promise.all([loadItems(), loadPendingCaptures(), loadJobs()]);
    lastUpdatedAt = new Date();
  }

  $effect(() => {
    loadAll();
    const interval = setInterval(loadAll, REFRESH_INTERVAL_MS);
    return () => clearInterval(interval);
  });

  function categoryForItem(status: QueueItem["status"]): StatusCategory {
    switch (status) {
      case "pending":
        return "pending";
      case "claimed":
        return "active";
      case "captured":
        return "done";
      case "failed":
        return "failed";
    }
  }

  // (fetched_by_backend, claimed_at) is the whole state -- D1 has no status
  // column for this table. Note the absence of "failed": a capture whose
  // ingestion keeps failing is indistinguishable from one merely waiting
  // its turn, so an old timestamp is the only signal available and the
  // section hint states the expected window rather than pretending to a
  // precision the data doesn't have.
  function categoryForPendingCapture(pc: PendingCapture): StatusCategory {
    if (pc.fetched_by_backend) return "done";
    return pc.claimed_at ? "active" : "pending";
  }

  function categoryForJob(status: Job["status"]): StatusCategory {
    switch (status) {
      case "pending":
        return "pending";
      case "processing":
        return "active";
      case "done":
        return "done";
      case "failed":
        return "failed";
    }
  }

  let summary = $derived.by(() => {
    const counts: Record<StatusCategory, number> = {
      pending: 0,
      active: 0,
      done: 0,
      failed: 0,
    };
    for (const item of items) counts[categoryForItem(item.status)]++;
    for (const pc of pendingCaptures) counts[categoryForPendingCapture(pc)]++;
    for (const job of [...screenshotJobs, ...readabilityJobs, ...aiJobs]) {
      counts[categoryForJob(job.status)]++;
    }
    return counts;
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
        err instanceof ApiError ? err.message : m.queue_retry_item_error();
    } finally {
      retryingItemId = null;
    }
  }

  function jobsListFor(kind: JobKind): Job[] {
    if (kind === "screenshot") return screenshotJobs;
    if (kind === "readability") return readabilityJobs;
    return aiJobs;
  }

  function setJobsListFor(kind: JobKind, jobs: Job[]) {
    if (kind === "screenshot") screenshotJobs = jobs;
    else if (kind === "readability") readabilityJobs = jobs;
    else aiJobs = jobs;
  }

  async function retryJob(job: Job, kind: JobKind) {
    retryingJobKey = `${kind}:${job.id}`;
    actionError = null;
    try {
      await apiJSON(`/jobs/${kind}/${job.id}/retry`, { method: "POST" });
      // Updates the job in place to "pending" rather than removing it
      setJobsListFor(
        kind,
        jobsListFor(kind).map((j) =>
          j.id === job.id
            ? { ...j, status: "pending" as const, error: null }
            : j,
        ),
      );
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.queue_retry_job_error();
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

  // Intl.RelativeTimeFormat already produces a complete, correctly-ordered
  // phrase per locale ("2 minutes ago" / "il y a 2 minutes").
  function formatRelativeTime(iso: string): string {
    const diffSeconds = Math.round(
      (Date.now() - new Date(iso).getTime()) / 1000,
    );
    const rtf = new Intl.RelativeTimeFormat(getLocale(), { numeric: "auto" });
    if (Math.abs(diffSeconds) < 60) {
      return rtf.format(-diffSeconds, "second");
    }
    return rtf.format(-Math.round(diffSeconds / 60), "minute");
  }
</script>

{#snippet statusBadge(category: StatusCategory, label: string)}
  <span class="badge {category}">{label}</span>
{/snippet}

{#snippet jobList(jobs: Job[], kind: JobKind, label: string)}
  <div class="job-section">
    <h3>{label}</h3>
    {#if jobs.length === 0}
      <p class="status">{m.queue_no_jobs()}</p>
    {:else}
      <ul class="items">
        {#each jobs as job (job.id)}
          {@const category = categoryForJob(job.status)}
          <li>
            <div class="item-info">
              <div class="item-top">
                {@render statusBadge(
                  category,
                  category === "pending"
                    ? m.queue_status_pending()
                    : category === "active"
                      ? m.queue_status_processing()
                      : category === "done"
                        ? m.queue_status_done()
                        : m.queue_status_failed(),
                )}
                <a href={`/pages/${job.page_id}`} use:link class="url"
                  >{job.title || job.url}</a
                >
              </div>
              {#if job.status === "failed"}
                <span class="meta">
                  {job.attempts === 1
                    ? m.queue_attempts_one({ count: job.attempts })
                    : m.queue_attempts_other({ count: job.attempts })}
                  {#if job.completed_at}
                    · {m.queue_last_tried({
                      date: formatDateTime(job.completed_at),
                    })}
                  {/if}
                </span>
                {#if job.error}
                  <span class="error-detail">{job.error}</span>
                {/if}
              {:else if job.status === "processing" && job.claimed_at}
                <span class="meta"
                  >{m.queue_job_started({
                    time: formatRelativeTime(job.claimed_at),
                  })}</span
                >
              {:else if job.status === "done" && job.completed_at}
                <span class="meta"
                  >{m.queue_job_completed({
                    time: formatRelativeTime(job.completed_at),
                  })}</span
                >
              {/if}
            </div>
            {#if job.status === "failed"}
              <button
                type="button"
                onclick={() => retryJob(job, kind)}
                disabled={retryingJobKey === `${kind}:${job.id}`}
              >
                {m.common_retry()}
              </button>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </div>
{/snippet}

<main class="screen">
  <AppHeader />
  <p class="page-heading">{m.nav_queue()}</p>

  <div class="toolbar">
    {#if lastUpdatedAt}
      <span class="updated-at">
        <ClockIcon size={13} />
        {m.queue_updated_ago({
          time: formatRelativeTime(lastUpdatedAt.toISOString()),
        })}
      </span>
    {/if}
    <button type="button" class="refresh-btn" onclick={loadAll}>
      <RefreshCw size={13} />
      {m.queue_refresh()}
    </button>
  </div>

  {#if loadError}
    <p class="status error" role="alert">
      <AlertCircle size={15} />
      <span>{loadError}</span>
    </p>
  {/if}
  {#if actionError}
    <p class="status error" role="alert">
      <AlertCircle size={15} />
      <span>{actionError}</span>
    </p>
  {/if}

  {#if !itemsLoading && !pendingCapturesLoading && !jobsLoading}
    <div class="summary">
      {#if summary.pending > 0}
        <span class="stat pending"
          ><span class="count">{summary.pending}</span><span class="stat-label"
            >{m.queue_stat_pending()}</span
          ></span
        >
      {/if}
      {#if summary.active > 0}
        <span class="stat active"
          ><span class="count">{summary.active}</span><span class="stat-label"
            >{m.queue_stat_active()}</span
          ></span
        >
      {/if}
      {#if summary.failed > 0}
        <span class="stat failed"
          ><span class="count">{summary.failed}</span><span class="stat-label"
            >{m.queue_stat_failed()}</span
          ></span
        >
      {/if}
      {#if summary.done > 0}
        <span class="stat done"
          ><span class="count">{summary.done}</span><span class="stat-label"
            >{m.queue_stat_done()}</span
          ></span
        >
      {/if}
    </div>
  {/if}

  <section>
    <p class="eyebrow">{m.queue_capture_queue_heading()}</p>
    <p class="hint">
      {m.queue_capture_queue_hint()}
    </p>
    {#if itemsLoading}
      <p class="status">{m.common_loading()}</p>
    {:else if items.length === 0}
      <p class="status">{m.queue_no_items()}</p>
    {:else}
      <ul class="items">
        {#each items as item (item.id)}
          {@const category = categoryForItem(item.status)}
          <li>
            <div class="item-info">
              <div class="item-top">
                {@render statusBadge(
                  category,
                  category === "pending"
                    ? m.queue_status_pending()
                    : category === "active"
                      ? m.queue_status_claimed()
                      : category === "done"
                        ? m.queue_status_captured()
                        : m.queue_status_failed(),
                )}
                <span class="url">{item.url}</span>
              </div>
              <span class="meta">
                {#if item.status === "pending"}
                  {m.queue_item_added({
                    time: formatRelativeTime(item.created_at),
                  })}
                {:else if item.status === "claimed" && item.claimed_at}
                  {m.queue_item_claimed({
                    time: formatRelativeTime(item.claimed_at),
                  })}
                {:else if item.status === "captured" && item.claimed_at}
                  {m.queue_item_captured({
                    time: formatRelativeTime(item.claimed_at),
                  })}
                {:else}
                  {m.queue_item_added_at({
                    date: formatDateTime(item.created_at),
                  })}
                {/if}
                <!-- Which device to go and finish the capture in, for a
                     claimed item -- and where it went wrong, for a failed
                     one. Absent both when nothing has claimed the item yet
                     and when the device that did has since been revoked
                     (tokens are revoked by row delete, so the name is
                     genuinely gone, not hidden). -->
                {#if item.claimed_by_device}
                  <span class="device"
                    >{m.queue_item_by_device({
                      device: item.claimed_by_device,
                    })}</span
                  >
                {/if}
                {#if item.status === "failed" && item.manual_retry}
                  · <span class="pending-retry">{m.queue_retry_pending()}</span>
                {/if}
              </span>
            </div>
            {#if item.status === "failed"}
              <button
                type="button"
                onclick={() => retryItem(item)}
                disabled={item.manual_retry || retryingItemId === item.id}
              >
                {item.manual_retry ? m.queue_retry_queued() : m.common_retry()}
              </button>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section>
    <p class="eyebrow">{m.queue_ingest_heading()}</p>
    <p class="hint">
      {m.queue_ingest_hint()}
    </p>
    {#if pendingCapturesLoading}
      <p class="status">{m.common_loading()}</p>
    {:else if pendingCaptures.length === 0}
      <p class="status">{m.queue_no_pending_captures()}</p>
    {:else}
      <ul class="items">
        {#each pendingCaptures as capture (capture.id)}
          {@const category = categoryForPendingCapture(capture)}
          <li>
            <div class="item-info">
              <div class="item-top">
                {@render statusBadge(
                  category,
                  category === "pending"
                    ? m.queue_status_waiting()
                    : category === "active"
                      ? m.queue_status_ingesting()
                      : m.queue_status_ingested(),
                )}
                <span class="url">{capture.url}</span>
              </div>
              <span class="meta">
                {#if category === "done" && capture.claimed_at}
                  {m.queue_pending_ingested({
                    time: formatRelativeTime(capture.claimed_at),
                  })}
                {:else if category === "active" && capture.claimed_at}
                  {m.queue_pending_started({
                    time: formatRelativeTime(capture.claimed_at),
                  })}
                {:else}
                  {m.queue_pending_captured({
                    time: formatRelativeTime(capture.captured_at),
                  })}
                {/if}
              </span>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section>
    <p class="eyebrow">{m.queue_jobs_heading()}</p>
    <p class="hint">
      {m.queue_jobs_hint()}
    </p>
    {#if jobsLoading}
      <p class="status">{m.common_loading()}</p>
    {:else}
      {@render jobList(
        screenshotJobs,
        "screenshot",
        m.queue_screenshots_label(),
      )}
      {@render jobList(
        readabilityJobs,
        "readability",
        m.queue_readability_label(),
      )}
      {@render jobList(aiJobs, "ai", m.queue_ai_summaries_label())}
    {/if}
  </section>
</main>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;
  @use "../styles/components" as comp;

  .screen {
    @include comp.content-screen;
  }

  .page-heading {
    @include type.eyebrow;
    margin: 0 0 1rem;
  }

  .toolbar {
    display: flex;
    align-items: center;
    margin-bottom: 1.25rem;
  }

  .updated-at {
    display: flex;
    align-items: center;
    gap: 0.3rem;
    color: var(--ink-muted);
    font-size: 0.75rem;
  }

  .refresh-btn {
    @include comp.bordered-button;
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    margin-left: auto;
    padding: 0.35rem 0.65rem;
    font-size: 0.78rem;
  }

  .summary {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-bottom: 1.75rem;
  }

  .stat {
    @include comp.pill;
    display: flex;
    align-items: baseline;
    gap: 0.35rem;
    padding: 0.4rem 0.75rem;
    font-size: 0.78rem;

    .count {
      @include type.data-mono;
      font-weight: 600;
    }

    .stat-label {
      color: var(--ink-muted);
    }

    &.pending .count {
      color: var(--ink-muted);
    }

    &.active .count {
      color: var(--brass);
    }

    &.failed .count {
      color: var(--accent);
    }

    &.done .count {
      color: var(--accent-success);
    }
  }

  section {
    margin-bottom: 2rem;
  }

  .eyebrow {
    @include type.eyebrow;
    margin: 0 0 0.4rem;
  }

  h3 {
    @include type.eyebrow;
    font-size: 0.68rem;
    color: var(--ink-muted);
    margin: 1.25rem 0 0.4rem;
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
    @include comp.status-row;
  }

  button {
    @include comp.bordered-button;
    padding: 0.375rem 0.75rem;
    font-size: 0.8125rem;
    flex-shrink: 0;
  }

  .items {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px dotted var(--rule);
  }

  .items li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.625rem 0.25rem;
    @include mix.dotted-rule;
  }

  .item-info {
    display: flex;
    flex-direction: column;
    gap: 0.2rem;
    min-width: 0;
  }

  .item-top {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    min-width: 0;
  }

  .badge {
    display: inline-flex;
    align-items: center;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    @include type.data-mono;
    font-size: 0.65rem;
    font-weight: 500;
    letter-spacing: 0.03em;
    text-transform: uppercase;
    flex: none;

    &.pending {
      color: var(--ink-muted);
      background: var(--paper-raised);
      border: 1px solid var(--rule);
    }

    &.active {
      color: var(--brass);
      background: color-mix(in srgb, var(--brass) 12%, var(--paper-raised));
      border: 1px solid color-mix(in srgb, var(--brass) 40%, var(--rule));
    }

    &.failed {
      color: var(--accent);
      background: color-mix(in srgb, var(--accent) 10%, var(--paper-raised));
      border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--rule));
    }

    &.done {
      color: var(--accent-success);
      background: color-mix(
        in srgb,
        var(--accent-success) 10%,
        var(--paper-raised)
      );
      border: 1px solid
        color-mix(in srgb, var(--accent-success) 35%, var(--rule));
    }
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

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .meta {
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.75rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .device {
    color: var(--ink-muted);
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
