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
<script lang="ts">
  import Eye from "@lucide/svelte/icons/eye";
  import EyeOff from "@lucide/svelte/icons/eye-off";
  import { m } from "../paraglide/messages";

  let {
    id,
    value = $bindable(""),
    autocomplete,
    required = true,
    disabled = false,
  }: {
    id: string;
    value?: string;
    autocomplete: "current-password" | "new-password";
    required?: boolean;
    disabled?: boolean;
  } = $props();

  let visible = $state(false);
</script>

<div class="password-row">
  <input
    {id}
    type={visible ? "text" : "password"}
    {autocomplete}
    bind:value
    {required}
    {disabled}
  />
  <button
    type="button"
    class="toggle-visibility"
    aria-label={visible ? m.common_hide_password() : m.common_show_password()}
    {disabled}
    onclick={() => (visible = !visible)}
  >
    {#if visible}
      <EyeOff size={15} />
    {:else}
      <Eye size={15} />
    {/if}
  </button>
</div>

<style lang="scss">
  @use "../styles/mixins" as mix;

  .password-row {
    position: relative;
    display: flex;
  }

  input {
    flex: 1;
    padding: 0.55rem 2.3rem 0.55rem 0.7rem;
    border: 1px solid var(--rule);
    border-radius: 4px;
    background: var(--paper);
    color: var(--ink);
    font: inherit;

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .toggle-visibility {
    position: absolute;
    right: 0.4rem;
    top: 50%;
    transform: translateY(-50%);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.7rem;
    height: 1.7rem;
    padding: 0;
    border: none;
    border-radius: 50%;
    background: transparent;
    color: var(--ink-muted);
    cursor: pointer;

    &:hover {
      color: var(--accent);
      background: var(--paper-raised);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }

    &:disabled {
      cursor: not-allowed;
    }
  }
</style>
