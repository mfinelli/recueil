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
<!-- Page membership itself isn't managed here
     (add/remove-a-page-from-a-collection stays a PageDetail concern); this
     is purely about the collections themselves. Deletion uses a plain
     confirm() -- no custom modal component exists yet, and this is a
     intentionally not-fancy first pass, same spirit as the tag source
     styling. -->
<script lang="ts">
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type { Collection, CollectionListResponse } from "../lib/types";
  import { m } from "../paraglide/messages";

  interface CollectionNode extends Collection {
    children: CollectionNode[];
  }

  let collections = $state<Collection[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);

  let newTopLevelName = $state("");
  let creatingTopLevel = $state(false);

  let editingId = $state<number | null>(null);
  let editingName = $state("");
  let savingRename = $state(false);

  let addingChildTo = $state<number | null>(null);
  let newChildName = $state("");
  let creatingChild = $state(false);

  let deletingId = $state<number | null>(null);

  // Parent_id points at a real id (an FK guarantees that), so a
  // single map-then-attach pass is enough -- no need to handle a
  // "parent not found yet" ordering problem the way you would parsing
  // a stream incrementally.
  function buildTree(flat: Collection[]): CollectionNode[] {
    // Local and disposable -- built and consumed within this one call,
    // never stored in $state -- same reasoning as Library.svelte's
    // URLSearchParams fix.
    // eslint-disable-next-line svelte/prefer-svelte-reactivity
    const nodes = new Map<number, CollectionNode>();
    for (const c of flat) nodes.set(c.id, { ...c, children: [] });
    const roots: CollectionNode[] = [];
    for (const c of flat) {
      const node = nodes.get(c.id)!;
      const parent = c.parent_id !== null ? nodes.get(c.parent_id) : undefined;
      if (parent) {
        parent.children.push(node);
      } else {
        roots.push(node);
      }
    }
    const sortRecursive = (list: CollectionNode[]) => {
      list.sort((a, b) => a.name.localeCompare(b.name));
      list.forEach((n) => sortRecursive(n.children));
    };
    sortRecursive(roots);
    return roots;
  }

  let tree = $derived(buildTree(collections));

  async function loadCollections() {
    loading = true;
    loadError = null;
    try {
      const res = await apiJSON<CollectionListResponse>("/collections");
      collections = res.collections;
    } catch (err) {
      loadError =
        err instanceof ApiError ? err.message : m.collections_load_error();
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    loadCollections();
  });

  async function createCollection(
    name: string,
    parentId: number | null,
  ): Promise<void> {
    const created = await apiJSON<Collection>("/collections", {
      method: "POST",
      body: parentId === null ? { name } : { name, parent_id: parentId },
    });
    collections = [...collections, created];
  }

  async function handleCreateTopLevel(event: SubmitEvent) {
    event.preventDefault();
    const name = newTopLevelName.trim();
    if (!name) return;
    creatingTopLevel = true;
    actionError = null;
    try {
      await createCollection(name, null);
      newTopLevelName = "";
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.collections_create_error();
    } finally {
      creatingTopLevel = false;
    }
  }

  function startAddingChild(parentId: number) {
    addingChildTo = parentId;
    newChildName = "";
  }

  async function handleCreateChild(event: SubmitEvent, parentId: number) {
    event.preventDefault();
    const name = newChildName.trim();
    if (!name) return;
    creatingChild = true;
    actionError = null;
    try {
      await createCollection(name, parentId);
      addingChildTo = null;
      newChildName = "";
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.collections_create_error();
    } finally {
      creatingChild = false;
    }
  }

  function startRename(node: CollectionNode) {
    editingId = node.id;
    editingName = node.name;
  }

  async function handleRename(event: SubmitEvent, id: number) {
    event.preventDefault();
    const name = editingName.trim();
    if (!name) return;
    savingRename = true;
    actionError = null;
    try {
      const updated = await apiJSON<Collection>(`/collections/${id}`, {
        method: "PATCH",
        body: { name },
      });
      collections = collections.map((c) => (c.id === id ? updated : c));
      editingId = null;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.collections_rename_error();
    } finally {
      savingRename = false;
    }
  }

  function countDescendants(node: CollectionNode): number {
    return node.children.reduce(
      (sum, child) => sum + 1 + countDescendants(child),
      0,
    );
  }

  function collectDescendantIds(node: CollectionNode): number[] {
    return node.children.flatMap((child) => [
      child.id,
      ...collectDescendantIds(child),
    ]);
  }

  async function handleDelete(node: CollectionNode) {
    const descendantCount = countDescendants(node);
    const warning =
      descendantCount > 0
        ? descendantCount === 1
          ? m.collections_delete_confirm_with_children_one({
              name: node.name,
              count: descendantCount,
            })
          : m.collections_delete_confirm_with_children_other({
              name: node.name,
              count: descendantCount,
            })
        : m.collections_delete_confirm_simple({ name: node.name });
    if (!confirm(warning)) return;

    deletingId = node.id;
    actionError = null;
    try {
      await apiJSON(`/collections/${node.id}`, { method: "DELETE" });
      const removedIds = new Set([node.id, ...collectDescendantIds(node)]);
      collections = collections.filter((c) => !removedIds.has(c.id));
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.collections_delete_error();
    } finally {
      deletingId = null;
    }
  }
</script>

{#snippet nodeRow(node: CollectionNode, depth: number)}
  <li>
    <div class="row" style={`padding-left: ${depth * 1.25}rem`}>
      {#if editingId === node.id}
        <form class="inline-form" onsubmit={(e) => handleRename(e, node.id)}>
          <input type="text" bind:value={editingName} disabled={savingRename} />
          <button type="submit" disabled={savingRename || !editingName.trim()}
            >{m.common_save()}</button
          >
          <button
            type="button"
            onclick={() => (editingId = null)}
            disabled={savingRename}>{m.common_cancel()}</button
          >
        </form>
      {:else}
        <span class="name">{node.name}</span>
        <div class="row-actions">
          <button type="button" onclick={() => startAddingChild(node.id)}
            >{m.collections_add_subcollection()}</button
          >
          <button type="button" onclick={() => startRename(node)}
            >{m.collections_rename()}</button
          >
          <button
            type="button"
            class="danger"
            onclick={() => handleDelete(node)}
            disabled={deletingId === node.id}
          >
            {m.common_delete()}
          </button>
        </div>
      {/if}
    </div>

    {#if addingChildTo === node.id}
      <form
        class="inline-form child-form"
        style={`padding-left: ${(depth + 1) * 1.25}rem`}
        onsubmit={(e) => handleCreateChild(e, node.id)}
      >
        <input
          type="text"
          placeholder={m.collections_subcollection_placeholder()}
          bind:value={newChildName}
          disabled={creatingChild}
        />
        <button type="submit" disabled={creatingChild || !newChildName.trim()}
          >{m.common_create()}</button
        >
        <button
          type="button"
          onclick={() => (addingChildTo = null)}
          disabled={creatingChild}>{m.common_cancel()}</button
        >
      </form>
    {/if}

    {#if node.children.length > 0}
      <ul class="tree">
        {#each node.children as child (child.id)}
          {@render nodeRow(child, depth + 1)}
        {/each}
      </ul>
    {/if}
  </li>
{/snippet}

<main class="screen">
  <AppHeader />
  <h1>{m.nav_collections()}</h1>

  {#if actionError}
    <p class="status error" role="alert">{actionError}</p>
  {/if}

  <form class="inline-form" onsubmit={handleCreateTopLevel}>
    <input
      type="text"
      placeholder={m.collections_new_top_level_placeholder()}
      bind:value={newTopLevelName}
      disabled={creatingTopLevel}
    />
    <button type="submit" disabled={creatingTopLevel || !newTopLevelName.trim()}
      >{m.common_create()}</button
    >
  </form>

  {#if loading}
    <p class="status">{m.common_loading()}</p>
  {:else if loadError}
    <p class="status error" role="alert">{loadError}</p>
  {:else if tree.length === 0}
    <p class="status">{m.collections_no_collections()}</p>
  {:else}
    <ul class="tree">
      {#each tree as node (node.id)}
        {@render nodeRow(node, 0)}
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

  h1 {
    margin: 0 0 1rem;
  }

  .status {
    color: var(--ink-muted);

    &.error {
      color: var(--accent);
    }
  }

  .inline-form {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 1rem;
  }

  .child-form {
    margin-top: 0.375rem;
    margin-bottom: 0.5rem;
  }

  input[type="text"] {
    padding: 0.375rem 0.5rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper);
    color: var(--ink);
    font: inherit;
    font-size: 0.875rem;
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

    &.danger:hover:not(:disabled) {
      border-color: var(--accent);
      color: var(--accent);
    }
  }

  .tree {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.5rem 0.25rem;
    border-bottom: 1px solid var(--rule);
  }

  .name {
    font-size: 0.9375rem;
  }

  .row-actions {
    display: flex;
    gap: 0.375rem;
    flex-shrink: 0;
  }
</style>
