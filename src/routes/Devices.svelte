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
<!-- Pairing token, paired devices, and active sessions all live on the same
     screen -- paired devices are the extension/CLI/PWA/Shortcut clients that
     archive pages on your behalf, sessions are the browsers currently signed
     in to the dashboard itself; related but genuinely different things, so
     they're kept as separate sections rather than merged into one list. The
     pairing token is shown plainly, not once-then-hashed.

     Each device gets a small type icon (Puzzle/Terminal/Zap/AppWindow for
     extension/cli/shortcut/pwa) with a title tooltip -- none of the four
     is unambiguous as a bare icon on its own, so the tooltip carries the
     same "hover to confirm" role Collections' add-sub-collection button
     already established, rather than adding visible text back. Sessions get
     the same icon+tooltip treatment, picked from the parsed device_class
     (Monitor/Smartphone/Tablet for desktop/mobile/tablet), with a distinct
     CircleHelp fallback for anything device_class doesn't recognize. -->
<script lang="ts">
  import type { Component } from "svelte";
  import Copy from "@lucide/svelte/icons/copy";
  import Check from "@lucide/svelte/icons/check";
  import RotateCw from "@lucide/svelte/icons/rotate-cw";
  import Trash2 from "@lucide/svelte/icons/trash-2";
  import Plus from "@lucide/svelte/icons/plus";
  import Smartphone from "@lucide/svelte/icons/smartphone";
  import Monitor from "@lucide/svelte/icons/monitor";
  import Tablet from "@lucide/svelte/icons/tablet";
  import CircleHelp from "@lucide/svelte/icons/circle-help";
  import AlertCircle from "@lucide/svelte/icons/circle-alert";
  import Puzzle from "@lucide/svelte/icons/puzzle";
  import Terminal from "@lucide/svelte/icons/terminal";
  import Zap from "@lucide/svelte/icons/zap";
  import AppWindow from "@lucide/svelte/icons/app-window";
  import AppHeader from "../components/AppHeader.svelte";
  import { apiJSON, ApiError } from "../lib/api";
  import type {
    Device,
    DeviceListResponse,
    PairingTokenResponse,
    Session,
    SessionListResponse,
  } from "../lib/types";
  import { m } from "../paraglide/messages";

  const TYPE_ICONS: Record<Device["device_type"], Component> = {
    extension: Puzzle,
    cli: Terminal,
    shortcut: Zap,
    pwa: AppWindow,
  };

  function typeIcon(type: Device["device_type"]) {
    return TYPE_ICONS[type] ?? Smartphone;
  }

  function typeLabel(type: Device["device_type"]): string {
    switch (type) {
      case "extension":
        return m.devices_type_extension();
      case "cli":
        return m.devices_type_cli();
      case "shortcut":
        return m.devices_type_shortcut();
      case "pwa":
        return m.devices_type_pwa();
      default:
        return type;
    }
  }

  const DEVICE_CLASS_ICONS: Partial<
    Record<Session["device_class"], Component>
  > = {
    desktop: Monitor,
    mobile: Smartphone,
    tablet: Tablet,
  };

  function sessionIcon(deviceClass: Session["device_class"]) {
    return DEVICE_CLASS_ICONS[deviceClass] ?? CircleHelp;
  }

  function sessionDeviceLabel(session: Session): string {
    if (session.browser && session.os) {
      return `${session.browser} · ${session.os}`;
    }
    return session.browser || session.os || m.devices_session_unknown_device();
  }

  let pairingToken = $state<string | null>(null);
  let pairingTokenLoading = $state(true);
  let regenerating = $state(false);
  let revokingPairing = $state(false);
  let copied = $state(false);

  let devices = $state<Device[]>([]);
  let devicesLoading = $state(true);

  let sessions = $state<Session[]>([]);
  let sessionsLoading = $state(true);
  let revokingSessionId = $state<number | null>(null);

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
          err instanceof ApiError ? err.message : m.devices_load_token_error();
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
        err instanceof ApiError ? err.message : m.devices_load_devices_error();
    } finally {
      devicesLoading = false;
    }
  }

  async function loadSessions() {
    sessionsLoading = true;
    try {
      const res = await apiJSON<SessionListResponse>("/sessions");
      sessions = res.sessions;
    } catch (err) {
      loadError =
        err instanceof ApiError ? err.message : m.devices_load_sessions_error();
    } finally {
      sessionsLoading = false;
    }
  }

  $effect(() => {
    loadPairingToken();
    loadDevices();
    loadSessions();
  });

  async function regeneratePairingToken() {
    if (pairingToken && !confirm(m.devices_regenerate_confirm())) {
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
        err instanceof ApiError ? err.message : m.devices_regenerate_error();
    } finally {
      regenerating = false;
    }
  }

  async function revokePairingToken() {
    if (!confirm(m.devices_revoke_token_confirm())) return;
    revokingPairing = true;
    actionError = null;
    try {
      await apiJSON("/pairing-token", { method: "DELETE" });
      pairingToken = null;
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.devices_revoke_token_error();
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
    if (!confirm(m.devices_revoke_device_confirm({ name: device.device_name })))
      return;
    revokingDeviceId = device.id;
    actionError = null;
    try {
      await apiJSON(`/devices/${device.id}`, { method: "DELETE" });
      devices = devices.filter((d) => d.id !== device.id);
    } catch (err) {
      actionError =
        err instanceof ApiError ? err.message : m.devices_revoke_device_error();
    } finally {
      revokingDeviceId = null;
    }
  }

  // No confirm-and-remove path for the current session at all -- it never
  // renders a revoke control in the first place, so this is only ever called
  // for one of the *other* sessions.
  async function revokeSession(session: Session) {
    if (!confirm(m.devices_revoke_session_confirm())) return;
    revokingSessionId = session.id;
    actionError = null;
    try {
      await apiJSON(`/sessions/${session.id}`, { method: "DELETE" });
      sessions = sessions.filter((s) => s.id !== session.id);
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.devices_revoke_session_error();
    } finally {
      revokingSessionId = null;
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
  <p class="page-heading">{m.nav_devices()}</p>

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

  <section>
    <p class="eyebrow">{m.devices_pairing_token_heading()}</p>
    <p class="hint">
      {m.devices_pairing_token_hint()}
    </p>
    {#if pairingTokenLoading}
      <p class="status">{m.common_loading()}</p>
    {:else}
      {#if pairingToken}
        <div class="token-row">
          <code class="token">{pairingToken}</code>
          <button
            type="button"
            class="copy-btn"
            class:copied
            aria-label={copied ? m.devices_copied() : m.devices_copy_aria()}
            onclick={copyPairingToken}
          >
            {#if copied}
              <Check size={13} />
              {m.devices_copied()}
            {:else}
              <Copy size={13} />
            {/if}
          </button>
        </div>
      {:else}
        <p class="status">{m.devices_no_token()}</p>
      {/if}
      <div class="token-actions">
        <button
          type="button"
          onclick={regeneratePairingToken}
          disabled={regenerating}
        >
          {#if pairingToken}
            <RotateCw size={13} />
          {:else}
            <Plus size={13} />
          {/if}
          {pairingToken ? m.devices_regenerate() : m.devices_generate()}
        </button>
        {#if pairingToken}
          <button
            type="button"
            class="danger"
            onclick={revokePairingToken}
            disabled={revokingPairing}
          >
            <Trash2 size={13} />
            {m.devices_revoke()}
          </button>
        {/if}
      </div>
    {/if}
  </section>

  <section>
    <p class="eyebrow">{m.devices_paired_heading()}</p>
    {#if devicesLoading}
      <p class="status">{m.common_loading()}</p>
    {:else if devices.length === 0}
      <div class="status-block">
        <Smartphone size={24} />
        <span>{m.devices_no_devices()}</span>
      </div>
    {:else}
      <ul class="devices">
        {#each devices as device (device.id)}
          {@const TypeIcon = typeIcon(device.device_type)}
          <li>
            <div class="device-left">
              <span
                class="type-icon"
                role="img"
                aria-label={typeLabel(device.device_type)}
                title={typeLabel(device.device_type)}
              >
                <TypeIcon size={15} />
              </span>
              <div class="device-info">
                <span class="name">{device.device_name}</span>
                <span class="meta">
                  {m.devices_paired_at({
                    date: formatDateTime(device.created_at),
                  })}
                  ·
                  {m.devices_last_used_at({
                    value: device.last_used_at
                      ? formatDateTime(device.last_used_at)
                      : m.devices_never(),
                  })}
                </span>
              </div>
            </div>
            <button
              type="button"
              class="icon-btn"
              aria-label={m.devices_revoke_aria({ name: device.device_name })}
              onclick={() => revokeDevice(device)}
              disabled={revokingDeviceId === device.id}
            >
              <Trash2 size={14} />
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <section>
    <p class="eyebrow">{m.devices_sessions_heading()}</p>
    <p class="hint">
      {m.devices_sessions_hint()}
    </p>
    {#if sessionsLoading}
      <p class="status">{m.common_loading()}</p>
    {:else}
      <ul class="devices">
        {#each sessions as session (session.id)}
          {@const SessionIcon = sessionIcon(session.device_class)}
          <li class:current={session.is_current}>
            <div class="device-left">
              <span
                class="type-icon"
                role="img"
                aria-label={sessionDeviceLabel(session)}
                title={sessionDeviceLabel(session)}
              >
                <SessionIcon size={15} />
              </span>
              <div class="device-info">
                <span class="name">
                  {sessionDeviceLabel(session)}
                  {#if session.is_current}
                    <span class="current-badge"
                      >{m.devices_session_current()}</span
                    >
                  {/if}
                </span>
                <span class="meta">
                  {m.devices_session_signed_in_at({
                    date: formatDateTime(session.created_at),
                  })}
                  ·
                  {m.devices_session_last_active_at({
                    date: formatDateTime(session.last_seen_at),
                  })}
                </span>
              </div>
            </div>
            {#if !session.is_current}
              <button
                type="button"
                class="icon-btn"
                aria-label={m.devices_revoke_session_aria({
                  device: sessionDeviceLabel(session),
                })}
                onclick={() => revokeSession(session)}
                disabled={revokingSessionId === session.id}
              >
                <Trash2 size={14} />
              </button>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</main>

<style lang="scss">
  @use "../styles/typography" as type;
  @use "../styles/mixins" as mix;

  .screen {
    max-width: 48rem;
    margin: 0 auto;
    padding: 2rem 1rem;
  }

  .page-heading {
    @include type.eyebrow;
    margin: 0 0 1.25rem;
  }

  section {
    margin-bottom: 2rem;
  }

  .eyebrow {
    @include type.eyebrow;
    margin: 0 0 0.4rem;
  }

  .hint {
    margin: 0 0 0.85rem;
    color: var(--ink-muted);
    font-size: 0.8125rem;
  }

  .status {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    color: var(--ink-muted);

    &.error {
      color: var(--accent);
    }
  }

  .status-block {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.6rem;
    padding: 2rem 1rem;
    color: var(--ink-muted);
    text-align: center;

    :global(svg) {
      opacity: 0.6;
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
    padding: 0.55rem 0.7rem;
    @include mix.card-surface;
    @include type.data-mono;
    font-size: 0.8125rem;
    color: var(--ink);
    overflow-x: auto;
    white-space: nowrap;
  }

  .copy-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.3rem;
    width: 2.1rem;
    height: 2.1rem;
    flex: none;
    padding: 0;
    border: 1px solid var(--rule);
    border-radius: 4px;
    background: var(--paper-raised);
    color: var(--ink-muted);
    font: inherit;
    cursor: pointer;

    &:hover {
      color: var(--accent);
    }

    &.copied {
      width: auto;
      padding: 0 0.6rem;
      color: var(--accent-success);
      @include type.data-mono;
      font-size: 0.7rem;
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .token-actions {
    display: flex;
    gap: 0.5rem;
  }

  button {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.4rem 0.75rem;
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

    &.danger {
      color: var(--accent);
      border-color: var(--accent);

      &:hover:not(:disabled) {
        background: var(--accent);
        color: var(--paper);
      }
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }

  .devices {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px dotted var(--rule);
  }

  .devices li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 0.65rem 0.25rem;
    @include mix.dotted-rule;

    // The current session's row -- a left accent stripe rather than
    // a filled background, so it reads as "marked," not "selected".
    &.current {
      position: relative;
      padding-left: 0.6rem;

      &::before {
        content: "";
        position: absolute;
        left: 0;
        top: 0.15rem;
        bottom: 0.15rem;
        width: 2px;
        background: var(--accent-success);
      }
    }
  }

  .current-badge {
    margin-left: 0.4rem;
    padding: 0.05rem 0.4rem;
    border-radius: 1rem;
    background: var(--accent-success);
    color: var(--paper);
    font-weight: 600;
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.02em;
  }

  .device-left {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    min-width: 0;
  }

  .type-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.9rem;
    height: 1.9rem;
    flex: none;
    @include mix.card-surface;
    border-radius: 50%;
    color: var(--ink-muted);
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
    @include type.data-mono;
    color: var(--ink-muted);
    font-size: 0.75rem;
  }

  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 1.9rem;
    height: 1.9rem;
    flex: none;
    padding: 0;
    border: none;
    border-radius: 50%;
    background: transparent;
    color: var(--ink-muted);
    cursor: pointer;

    &:hover:not(:disabled) {
      color: var(--accent);
      background: var(--paper-raised);
    }

    &:focus-visible {
      @include mix.focus-ring;
    }
  }
</style>
