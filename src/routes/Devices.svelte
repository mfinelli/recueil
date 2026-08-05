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
  import Key from "@lucide/svelte/icons/key";
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
    ApiToken,
    ApiTokenCreateResponse,
    ApiTokenListResponse,
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

  // Set from the same /pairing-token (and /pairing-token/regenerate)
  // responses that carry the token itself -- see PairingTokenResponse.
  // Stays null whenever pairingToken does (a 404, meaning no token has
  // ever been generated yet): there's genuinely nothing to pair until
  // then, so the worker URL has nothing useful to accompany.
  let workerURL = $state<string | null>(null);
  let workerUrlCopied = $state(false);

  let devices = $state<Device[]>([]);
  let devicesLoading = $state(true);

  let apiTokens = $state<ApiToken[]>([]);
  let apiTokensLoading = $state(true);
  let newTokenName = $state("");
  let creatingToken = $state(false);
  // The raw value of a just-created token -- held only in memory, never
  // persisted anywhere client-side, and cleared as soon as the reveal
  // callout is dismissed or another token is created. This is the one
  // and only place the raw value ever exists after the create response.
  let revealedToken = $state<ApiTokenCreateResponse | null>(null);
  let tokenCopied = $state(false);
  let revokingTokenId = $state<number | null>(null);

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
      workerURL = res.worker_url;
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        // No token yet -- a normal starting state (e.g. right after
        // setup), not a load failure.
        pairingToken = null;
        workerURL = null;
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

  async function loadApiTokens() {
    apiTokensLoading = true;
    try {
      const res = await apiJSON<ApiTokenListResponse>("/tokens");
      apiTokens = res.tokens;
    } catch (err) {
      loadError =
        err instanceof ApiError
          ? err.message
          : m.devices_apitokens_load_error();
    } finally {
      apiTokensLoading = false;
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
    loadApiTokens();
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
      workerURL = res.worker_url;
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
      workerURL = null;
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

  async function copyWorkerUrl() {
    if (!workerURL) return;
    await navigator.clipboard.writeText(workerURL);
    workerUrlCopied = true;
    setTimeout(() => {
      workerUrlCopied = false;
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

  async function createApiToken() {
    const name = newTokenName.trim();
    if (!name) {
      actionError = m.devices_apitokens_name_required_error();
      return;
    }
    creatingToken = true;
    actionError = null;
    try {
      const res = await apiJSON<ApiTokenCreateResponse>("/tokens", {
        method: "POST",
        body: { name },
      });
      revealedToken = res;
      tokenCopied = false;
      newTokenName = "";
      apiTokens = [
        {
          id: res.id,
          name: res.name,
          created_at: res.created_at,
          last_used_at: null,
        },
        ...apiTokens,
      ];
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.devices_apitokens_create_error();
    } finally {
      creatingToken = false;
    }
  }

  async function copyRevealedToken() {
    if (!revealedToken) return;
    await navigator.clipboard.writeText(revealedToken.token);
    tokenCopied = true;
    setTimeout(() => {
      tokenCopied = false;
    }, 2000);
  }

  function dismissRevealedToken() {
    revealedToken = null;
    tokenCopied = false;
  }

  async function revokeApiToken(token: ApiToken) {
    if (!confirm(m.devices_apitokens_revoke_confirm({ name: token.name })))
      return;
    revokingTokenId = token.id;
    actionError = null;
    try {
      await apiJSON(`/tokens/${token.id}`, { method: "DELETE" });
      apiTokens = apiTokens.filter((t) => t.id !== token.id);
    } catch (err) {
      actionError =
        err instanceof ApiError
          ? err.message
          : m.devices_apitokens_revoke_error();
    } finally {
      revokingTokenId = null;
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
      {#if workerURL}
        <p class="eyebrow worker-url-heading">
          {m.devices_worker_url_heading()}
        </p>
        <p class="hint">
          {m.devices_worker_url_hint()}
        </p>
        <div class="token-row">
          <code class="token">{workerURL}</code>
          <button
            type="button"
            class="copy-btn"
            class:copied={workerUrlCopied}
            aria-label={workerUrlCopied
              ? m.devices_copied()
              : m.devices_copy_worker_url_aria()}
            onclick={copyWorkerUrl}
          >
            {#if workerUrlCopied}
              <Check size={13} />
              {m.devices_copied()}
            {:else}
              <Copy size={13} />
            {/if}
          </button>
        </div>
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
    <p class="eyebrow">{m.devices_apitokens_heading()}</p>
    <p class="hint">
      {m.devices_apitokens_hint()}
    </p>

    <div class="create-row">
      <input
        type="text"
        placeholder={m.devices_apitokens_name_placeholder()}
        bind:value={newTokenName}
        disabled={creatingToken}
      />
      <button
        type="button"
        class="primary"
        onclick={createApiToken}
        disabled={creatingToken}
      >
        <Plus size={13} />
        {m.devices_apitokens_create()}
      </button>
    </div>

    {#if revealedToken}
      <div class="reveal">
        <p class="reveal-heading">
          <Check size={14} />
          {m.devices_apitokens_created_heading()}
        </p>
        <p class="reveal-warning">{m.devices_apitokens_created_warning()}</p>
        <div class="token-row">
          <code class="token">{revealedToken.token}</code>
          <button
            type="button"
            class="copy-btn"
            class:copied={tokenCopied}
            aria-label={tokenCopied
              ? m.devices_copied()
              : m.devices_apitokens_copy_aria()}
            onclick={copyRevealedToken}
          >
            {#if tokenCopied}
              <Check size={13} />
              {m.devices_copied()}
            {:else}
              <Copy size={13} />
            {/if}
          </button>
        </div>
        <button
          type="button"
          class="reveal-dismiss"
          onclick={dismissRevealedToken}
        >
          {m.devices_apitokens_dismiss()}
        </button>
      </div>
    {/if}

    {#if apiTokensLoading}
      <p class="status">{m.common_loading()}</p>
    {:else if apiTokens.length === 0}
      <div class="status-block">
        <Key size={24} />
        <span>{m.devices_apitokens_no_tokens()}</span>
      </div>
    {:else}
      <ul class="devices">
        {#each apiTokens as token (token.id)}
          <li>
            <div class="device-left">
              <span
                class="type-icon"
                role="img"
                aria-label={m.devices_apitokens_type_label()}
                title={m.devices_apitokens_type_label()}
              >
                <Key size={15} />
              </span>
              <div class="device-info">
                <span class="name">{token.name}</span>
                <span class="meta">
                  {m.devices_apitokens_created_at({
                    date: formatDateTime(token.created_at),
                  })}
                  ·
                  {m.devices_last_used_at({
                    value: token.last_used_at
                      ? formatDateTime(token.last_used_at)
                      : m.devices_never(),
                  })}
                </span>
              </div>
            </div>
            <button
              type="button"
              class="icon-btn"
              aria-label={m.devices_apitokens_revoke_aria({ name: token.name })}
              onclick={() => revokeApiToken(token)}
              disabled={revokingTokenId === token.id}
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
  @use "../styles/components" as comp;

  .screen {
    @include comp.content-screen;
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

  // A second .eyebrow nested inside the same section as the pairing
  // token's own -- needs breathing room above it that the bare .eyebrow
  // rule doesn't have, since it's not the first thing in the section.
  .worker-url-heading {
    margin-top: 1.1rem;
  }

  .hint {
    margin: 0 0 0.85rem;
    color: var(--ink-muted);
    font-size: 0.8125rem;
  }

  .status {
    @include comp.status-row;
  }

  .status-block {
    @include comp.status-block(2rem 1rem);
    color: var(--ink-muted);

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
    @include comp.bordered-button;
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.4rem 0.75rem;
    font-size: 0.8125rem;

    &.danger {
      color: var(--accent);
      border-color: var(--accent);

      &:hover:not(:disabled) {
        background: var(--accent);
        color: var(--paper);
      }
    }

    // Used only by the "Create token" action -- an affirmative,
    // filled-surface variant distinct from the bordered default, the
    // same way primary-button distinguishes itself on the auth screens.
    // Kept as a button-level modifier rather than pulling in
    // comp.primary-button, since that mixin also sets margin-top/padding
    // sized for a standalone auth-form submit, not an inline row button.
    &.primary {
      background: var(--accent-success);
      border-color: var(--accent-success);
      color: var(--paper);

      &:hover:not(:disabled) {
        opacity: 0.9;
        color: var(--paper);
      }
    }
  }

  .create-row {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 0.85rem;

    input {
      flex: 1;
      max-width: 20rem;
      padding: 0.55rem 0.7rem;
      @include comp.text-input;
      border-radius: 0.25rem;

      &::placeholder {
        color: var(--ink-muted);
      }
    }
  }

  // The one-time raw-token reveal, shown immediately after Create until
  // dismissed -- deliberately not part of .devices' list rows, since
  // (unlike the pairing token above) this value can never be shown again
  // once the callout is dismissed.
  .reveal {
    margin-bottom: 1rem;
    padding: 0.85rem 1rem;
    border: 1px solid var(--accent-success);
    border-radius: 3px;
    background: color-mix(
      in srgb,
      var(--accent-success) 8%,
      var(--paper-raised)
    );
  }

  .reveal-heading {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin: 0 0 0.5rem;
    font-weight: 600;
    font-size: 0.85rem;
    color: var(--accent-success);
  }

  .reveal-warning {
    margin: 0 0 0.65rem;
    color: var(--ink-muted);
    font-size: 0.78rem;
  }

  .reveal-dismiss {
    margin-top: 0.65rem;
    padding: 0;
    border: none;
    background: transparent;
    color: var(--ink-muted);
    font-size: 0.75rem;
    text-decoration: underline;
    cursor: pointer;

    &:hover {
      color: var(--accent);
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
    @include comp.icon-btn(1.9rem);
    flex: none;
  }
</style>
