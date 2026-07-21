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
<!-- Failed queue items: URLs a device tried and failed to archive
     (tab crashed, page required login, etc. Only status=failed items
     are ever shown here; pending/claimed in-flight work isn't dashboard-
     visible, it's a device/Worker-only concept. Retrying doesn't
     re-archive immediately -- it flags the item (manual_retry) so some
     device's next poll of GET /queue picks it up again, same as any other
     queue item; there's no synchronous "retry now" here. -->
<script lang="ts">
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type { QueueItem, QueueItemListResponse } from "../lib/types";

  let items = $state<QueueItem[]>([]);
  let itemsLoading = $state(true);

  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let retryingItemId = $state<string | null>(null);

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

  $effect(() => {
    loadItems();
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

<main class="screen">
  <AppHeader />
  <h1>Queue</h1>
  <p class="hint">
    URLs your devices tried and failed to archive. Retrying flags an item to be
    picked up again on a device's next poll -- it isn't retried immediately.
  </p>

  {#if loadError}
    <p class="status error" role="alert">{loadError}</p>
  {/if}
  {#if actionError}
    <p class="status error" role="alert">{actionError}</p>
  {/if}

  <section>
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
</main>

<style lang="scss">
  .screen {
    max-width: 48rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  h1 {
    margin: 0 0 0.375rem;
  }

  .hint {
    margin: 0 0 1.5rem;
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
  }

  .meta {
    color: var(--ink-muted);
    font-size: 0.75rem;
  }

  .pending-retry {
    color: var(--accent);
  }
</style>
