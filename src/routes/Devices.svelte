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
<!-- Pairing token and paired devices live on the same screen -- they're
     the two halves of the same story (get a device paired, then see/revoke
     what's paired). The pairing token is shown plainly, not once-then-
     hashed. -->
<script lang="ts">
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type {
    Device,
    DeviceListResponse,
    PairingTokenResponse,
  } from "../lib/types";

  let pairingToken = $state<string | null>(null);
  let pairingTokenLoading = $state(true);
  let regenerating = $state(false);
  let revokingPairing = $state(false);
  let copied = $state(false);

  let devices = $state<Device[]>([]);
  let devicesLoading = $state(true);

  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let revokingDeviceId = $state<number | null>(null);

  async function loadPairingToken() {
    pairingTokenLoading = true;
    try {
      const res = await apiJSON<PairingTokenResponse>("/pairing-token");
      pairingToken = res.pairing_token;
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        // No token yet -- a normal starting state (e.g. right after
        // setup), not a load failure.
        pairingToken = null;
      } else {
        loadError =
          err instanceof ApiError
            ? err.message
            : "failed to load pairing token";
      }
    } finally {
      pairingTokenLoading = false;
    }
  }

  async function loadDevices() {
    devicesLoading = true;
    try {
      const res = await apiJSON<DeviceListResponse>("/devices");
      devices = res.devices;
    } catch (err) {
      loadError =
        err instanceof ApiError ? err.message : "failed to load devices";
    } finally {
      devicesLoading = false;
    }
  }

  $effect(() => {
    loadPairingToken();
    loadDevices();
  });

  async function regeneratePairingToken() {
    if (
      pairingToken &&
      !confirm(
        "Generate a new pairing token? The current one will stop working for pairing new devices (already-paired devices are unaffected).",
      )
    ) {
      return;
    }
    regenerating = true;
    actionError = null;
    try {
      const res = await apiJSON<PairingTokenResponse>(
        "/pairing-token/regenerate",
        { method: "POST" },
      );
      pairingToken = res.pairing_token;
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : "failed to regenerate pairing token";
    } finally {
      regenerating = false;
    }
  }

  async function revokePairingToken() {
    if (
      !confirm(
        "Revoke the pairing token? No new devices can pair until you generate a new one.",
      )
    )
      return;
    revokingPairing = true;
    actionError = null;
    try {
      await apiJSON("/pairing-token", { method: "DELETE" });
      pairingToken = null;
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : "failed to revoke pairing token";
    } finally {
      revokingPairing = false;
    }
  }

  async function copyPairingToken() {
    if (!pairingToken) return;
    await navigator.clipboard.writeText(pairingToken);
    copied = true;
    setTimeout(() => {
      copied = false;
    }, 2000);
  }

  async function revokeDevice(device: Device) {
    if (
      !confirm(
        `Revoke "${device.device_name}"? It will need to be paired again to archive pages.`,
      )
    )
      return;
    revokingDeviceId = device.id;
    actionError = null;
    try {
      await apiJSON(`/devices/${device.id}`, { method: "DELETE" });
      devices = devices.filter((d) => d.id !== device.id);
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : "failed to revoke device";
    } finally {
      revokingDeviceId = null;
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
  <h1>Devices</h1>

  {#if loadError}
    <p class="status error" role="alert">{loadError}</p>
  {/if}
  {#if actionError}
    <p class="status error" role="alert">{actionError}</p>
  {/if}

  <section>
    <h2>Pairing token</h2>
    <p class="hint">
      Used once to pair a new device (the extension, CLI, or PWA) to your
      account.
    </p>
    {#if pairingTokenLoading}
      <p class="status">Loading…</p>
    {:else}
      {#if pairingToken}
        <div class="token-row">
          <code class="token">{pairingToken}</code>
          <button type="button" onclick={copyPairingToken}
            >{copied ? "Copied!" : "Copy"}</button
          >
        </div>
      {:else}
        <p class="status">No pairing token yet.</p>
      {/if}
      <div class="token-actions">
        <button
          type="button"
          onclick={regeneratePairingToken}
          disabled={regenerating}
        >
          {pairingToken ? "Regenerate" : "Generate"}
        </button>
        {#if pairingToken}
          <button
            type="button"
            class="danger"
            onclick={revokePairingToken}
            disabled={revokingPairing}>Revoke</button
          >
        {/if}
      </div>
    {/if}
  </section>

  <section>
    <h2>Paired devices</h2>
    {#if devicesLoading}
      <p class="status">Loading…</p>
    {:else if devices.length === 0}
      <p class="status">No devices paired yet.</p>
    {:else}
      <ul class="devices">
        {#each devices as device (device.id)}
          <li>
            <div class="device-info">
              <span class="name">{device.device_name}</span>
              <span class="meta">
                {device.device_type} · paired {formatDateTime(
                  device.created_at,
                )} · last used {device.last_used_at
                  ? formatDateTime(device.last_used_at)
                  : "never"}
              </span>
            </div>
            <button
              type="button"
              class="danger"
              onclick={() => revokeDevice(device)}
              disabled={revokingDeviceId === device.id}
            >
              Revoke
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
    margin: 0 0 1rem;
  }

  section {
    margin-bottom: 2rem;
  }

  h2 {
    font-size: 1rem;
    margin-bottom: 0.375rem;
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

  .token-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.75rem;
  }

  .token {
    flex: 1;
    padding: 0.5rem 0.625rem;
    border: 1px solid var(--rule);
    border-radius: 0.25rem;
    background: var(--paper-raised);
    font-family: ui-monospace, monospace;
    font-size: 0.8125rem;
    overflow-x: auto;
    white-space: nowrap;
  }

  .token-actions {
    display: flex;
    gap: 0.5rem;
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

  .devices {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--rule);
  }

  .devices li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.625rem 0.25rem;
    border-bottom: 1px solid var(--rule);
  }

  .device-info {
    display: flex;
    flex-direction: column;
    gap: 0.125rem;
    min-width: 0;
  }

  .name {
    font-weight: 600;
    font-size: 0.9375rem;
  }

  .meta {
    color: var(--ink-muted);
    font-size: 0.75rem;
  }
</style>
