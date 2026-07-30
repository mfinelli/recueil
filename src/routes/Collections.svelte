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
     styling.

     Slug/description editing lives only in the rename form, not create --
     same reasoning as Tags.svelte: create stays a quick "type a name" flow
     with an auto-generated slug, and a person who wants to customize it
     does that as an edit right after. -->
<script lang="ts">
  import { link } from "svelte-spa-router";
  import Plus from "@lucide/svelte/icons/plus";
  import Pencil from "@lucide/svelte/icons/pencil";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import Archive from "@lucide/svelte/icons/archive";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type { Collection, CollectionListResponse } from "../lib/types";
  import { previewSlug } from "../lib/slugPreview";
  import { m } from "../paraglide/messages";

  const SLUG_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/;

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
  let editingDescription = $state("");
  let editingSlug = $state("");
  let slugFieldOpen = $state(false);
  // The path of this node's *parent* (empty for a top-level collection),
  // captured when rename starts -- used only to preview the full URL a
  // rename would produce (see fullSlugPreview below), same slug-first
  // segments the actual route uses once saved.
  let editingParentPath = $state("");
  let savingRename = $state(false);

  let addingChildTo = $state<number | null>(null);
  let newChildName = $state("");
  let creatingChild = $state(false);

  let deletingId = $state<number | null>(null);

  let slugPreview = $derived(previewSlug(editingName));
  let fullSlugPreview = $derived(
    editingParentPath ? `${editingParentPath}/${slugPreview}` : slugPreview,
  );
  let slugValid = $derived(
    !slugFieldOpen ||
      editingSlug.trim() === "" ||
      SLUG_PATTERN.test(editingSlug.trim()),
  );

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

  function startRename(node: CollectionNode, path: string) {
    editingId = node.id;
    editingName = node.name;
    editingDescription = node.description ?? "";
    editingSlug = "";
    slugFieldOpen = false;
    const lastSlash = path.lastIndexOf("/");
    editingParentPath = lastSlash === -1 ? "" : path.slice(0, lastSlash);
    actionError = null;
  }

  function openSlugField() {
    editingSlug = slugPreview;
    slugFieldOpen = true;
  }

  async function handleRename(event: SubmitEvent, id: number) {
    event.preventDefault();
    const name = editingName.trim();
    if (!name || !slugValid) return;
    savingRename = true;
    actionError = null;
    try {
      const slug = editingSlug.trim();
      const body: { name: string; slug?: string; description: string } = {
        name,
        description: editingDescription.trim(),
        ...(slugFieldOpen && slug ? { slug } : {}),
      };
      const updated = await apiJSON<Collection>(`/collections/${id}`, {
        method: "PATCH",
        body,
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

{#snippet nodeRow(node: CollectionNode, depth: number, path: string)}
  <li>
    <div class="row">
      {#if editingId === node.id}
        <form
          class="inline-form rename-form"
          onsubmit={(e) => handleRename(e, node.id)}
        >
          <div class="edit-fields">
            <input
              type="text"
              bind:value={editingName}
              disabled={savingRename}
            />
            {#if slugFieldOpen}
              <div class="slug-field">
                <input
                  type="text"
                  class:invalid={!slugValid}
                  placeholder={m.tags_slug_placeholder()}
                  bind:value={editingSlug}
                  disabled={savingRename}
                />
                {#if !slugValid}
                  <span class="slug-error">{m.tags_slug_invalid()}</span>
                {/if}
              </div>
            {:else}
              <button
                type="button"
                class="slug-preview"
                onclick={openSlugField}
              >
                <Pencil size={10} />
                {m.collections_slug_preview({ path: fullSlugPreview })}
              </button>
            {/if}
            <input
              type="text"
              class="desc-input"
              placeholder={m.collections_description_placeholder()}
              bind:value={editingDescription}
              disabled={savingRename}
            />
          </div>
          <div class="row-actions">
            <button
              type="submit"
              disabled={savingRename || !editingName.trim() || !slugValid}
              >{m.common_save()}</button
            >
            <button
              type="button"
              onclick={() => (editingId = null)}
              disabled={savingRename}>{m.common_cancel()}</button
            >
          </div>
        </form>
      {:else}
        <div class="name-block">
          <a class="name" href={`/collections/${path}`} use:link>{node.name}</a>
          <span class="slug">/collections/{path}</span>
          {#if node.description}
            <span class="node-description">{node.description}</span>
          {/if}
        </div>
        <div class="row-actions">
          <button
            type="button"
            class="icon-btn"
            aria-label={m.collections_add_subcollection_aria({
              name: node.name,
            })}
            title={m.collections_add_subcollection()}
            onclick={() => startAddingChild(node.id)}
          >
            <Plus size={14} />
          </button>
          <button
            type="button"
            class="icon-btn"
            aria-label={m.collections_rename_aria({ name: node.name })}
            onclick={() => startRename(node, path)}
          >
            <Pencil size={14} />
          </button>
          <button
            type="button"
            class="icon-btn"
            aria-label={m.collections_delete_aria({ name: node.name })}
            onclick={() => handleDelete(node)}
            disabled={deletingId === node.id}
          >
            <Trash2 size={14} />
          </button>
        </div>
      {/if}
    </div>

    {#if addingChildTo === node.id}
      <form
        class="inline-form child-form"
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
          {@render nodeRow(child, depth + 1, `${path}/${child.slug}`)}
        {/each}
      </ul>
    {/if}
  </li>
{/snippet}

<main class="screen">
  <AppHeader />
  <p class="page-heading">{m.nav_collections()}</p>

  {#if actionError}
    <p class="status error" role="alert">
      <AlertCircle size={15} />
      <span>{actionError}</span>
    </p>
  {/if}

  <form class="inline-form" onsubmit={handleCreateTopLevel}>
    <input
      type="text"
      placeholder={m.collections_new_top_level_placeholder()}
      bind:value={newTopLevelName}
      disabled={creatingTopLevel}
    />
    <button
      type="submit"
      disabled={creatingTopLevel || !newTopLevelName.trim()}
    >
      <Plus size={13} />
      {m.common_create()}
    </button>
  </form>

  {#if loading}
    <p class="status">{m.common_loading()}</p>
  {:else if loadError}
    <div class="status-block error" role="alert">
      <AlertCircle size={28} />
      <span>{loadError}</span>
    </div>
  {:else if tree.length === 0}
    <div class="status-block">
      <Archive size={28} />
      <span>{m.collections_no_collections()}</span>
    </div>
  {:else}
    <ul class="tree">
      {#each tree as node (node.id)}
        {@render nodeRow(node, 0, node.slug)}
      {/each}
    </ul>
  {/if}
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
    margin: 0 0 1.25rem;
  }

  .status {
    @include comp.status-row;
    margin-bottom: 1rem;
  }

  .status-block {
    @include comp.status-block;
    color: var(--ink-muted);

    &.error {
      color: var(--accent);
    }

    :global(svg) {
      opacity: 0.6;
    }
  }

  .inline-form {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 1.5rem;
  }

  .rename-form {
    align-items: flex-start;
    justify-content: space-between;
    width: 100%;
    margin-bottom: 0;
  }

  .child-form {
    padding-left: 1.1rem;
    margin: 0.5rem 0 0.75rem;
  }

  input[type="text"] {
    @include comp.text-input;
    padding: 0.4rem 0.6rem;
    border-radius: 4px;
    font-size: 0.875rem;

    &.invalid {
      border-color: var(--accent);
    }
  }

  .desc-input {
    font-size: 0.8125rem;
    color: var(--ink-muted);
  }

  button {
    @include comp.bordered-button;
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    padding: 0.375rem 0.75rem;
    font-size: 0.8125rem;
  }

  .icon-btn {
    @include comp.icon-btn(1.8rem);
  }

  .tree {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px dotted var(--rule);

    .tree {
      margin-top: 0.15rem;
      padding-left: 1.1rem;
      border-top: none;
      border-left: 1px dotted var(--rule);
    }
  }

  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.6rem 0.25rem;
    @include mix.dotted-rule;
  }

  .name-block {
    display: flex;
    flex-direction: column;
    gap: 0.1rem;
    min-width: 0;
  }

  .name {
    font-size: 0.9375rem;
    color: inherit;
    text-decoration: none;

    &:hover {
      color: var(--accent);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .slug {
    @include type.data-mono;
    font-size: 0.72rem;
    color: var(--ink-muted);
  }

  .node-description {
    font-size: 0.78rem;
    color: var(--ink-muted);
  }

  .edit-fields {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
    flex: 1;
  }

  .slug-field {
    display: flex;
    flex-direction: column;
    gap: 0.125rem;

    input {
      @include type.data-mono;
      font-size: 0.8rem;
    }
  }

  .slug-error {
    font-size: 0.75rem;
    color: var(--accent);
  }

  .slug-preview {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    width: fit-content;
    padding: 0.125rem 0;
    border: none;
    background: none;
    color: var(--ink-muted);
    @include type.data-mono;
    font-size: 0.72rem;
    text-decoration: underline dotted;
    cursor: pointer;

    &:hover {
      color: var(--accent);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .row-actions {
    display: flex;
    gap: 0.15rem;
    flex-shrink: 0;
  }
</style>
