/*
 * recueil: self-hosted webpage bookmarker and archiver
 * Copyright © 2026 Mario Finelli
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

// Pairing exchanges a one-time pairing token (generated on the recueil
// dashboard, out of scope of this extension) for a long-lived device
// bearer token -- see terraform/index.js's handlePair for the exact
// contract this is written against: POST {pairing_token, device_name,
// device_type} -> {token, device_id, device_name, device_type}.
//
// IMPORTANT PRECONDITION this module does *not* enforce itself: the
// caller must already hold a granted host_permission for workerBaseURL
// (via browser.permissions.request({origins: [...]})) before calling
// pair() below. That call doesn't happen in here, because
// permissions.request() needs to run inside the same user-gesture-driven
// call stack as the pairing form's submit handler -- once this crosses a
// runtime.sendMessage boundary from the popup into this background
// context, whether the browser still considers it "triggered by a real
// user action" (transient activation) isn't something to rely on across
// Chrome and Firefox. So: the popup requests the permission itself,
// synchronously in its own submit handler, and only sends PAIR_DEVICE once
// that's confirmed granted.

import browser from "webextension-polyfill";
import { getConfig, setConfig } from "../common/storage.js";

/**
 * @param {{workerBaseURL: string, pairingToken: string, deviceName: string}} params
 * @returns {Promise<import("../common/storage.js").RecueilConfig>}
 */
export async function pair({ workerBaseURL, pairingToken, deviceName }) {
  const normalizedBaseURL = workerBaseURL.replace(/\/+$/, "");

  let response;
  try {
    response = await fetch(`${normalizedBaseURL}/pair`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        pairing_token: pairingToken,
        device_name: deviceName,
        device_type: "extension",
      }),
    });
  } catch (error) {
    // Same reasoning as api-client.js's apiRequest -- see there for why
    // this matters.
    throw new Error(
      `recueil: network error pairing with ${normalizedBaseURL}: ${error instanceof Error ? error.message : String(error)}`,
      { cause: error },
    );
  }

  if (!response.ok) {
    const text = await response.text().catch(() => "");
    throw new Error(
      `recueil: pairing with ${normalizedBaseURL} failed: ${response.status} ${text}`,
    );
  }

  const data = await response.json();
  /** @type {import("../common/storage.js").RecueilConfig} */
  const config = {
    workerBaseURL: normalizedBaseURL,
    token: data.token,
    deviceId: data.device_id,
    deviceName: data.device_name,
    deviceType: data.device_type,
  };
  await setConfig(config);
  return config;
}

export async function getAuthState() {
  const config = await getConfig();
  if (!config) {
    return { paired: false };
  }
  // Never returns config.token to a caller -- the popup only ever needs to
  // know *whether* pairing succeeded and which instance/device it's talking
  // to, not the credential itself.
  return {
    paired: true,
    workerBaseURL: config.workerBaseURL,
    deviceName: config.deviceName,
  };
}

export async function unpair() {
  // Wipes storage.local entirely, not just the config. Every key this
  // extension currently stores (config, the pairing-form draft, the queue
  // cache, the claimed-tabs map) is scoped to "the currently paired
  // instance/account" one way or another; leaving any of them behind after
  // an explicit sign-out would mean stale queue URLs, a stale tab
  // association, or a stale draft pairing form silently carrying over
  // into whatever the user does next -- re-pairing to the same instance,
  // a completely different one, or just leaving the extension unpaired for
  // a while. storage.local.clear() rather than clearing each key individually
  // means this stays correct even if a future key gets added and someone
  // forgets to list it here; if a genuinely account-independent preference
  // is ever introduced, this call would need revisiting, but nothing in
  // storage.local today is that.
  //
  // Also local-only in a second sense: there's no device-facing endpoint
  // to revoke the token server-side yet either. /internal/tokens/:id
  // (DELETE) exists but is gated by X-Service-Key, not a device bearer
  // token -- it's the dashboard's own admin-side device-management
  // endpoint, not something a paired device can call on itself. So the
  // token itself stays valid server-side until an operator revokes it
  // from the dashboard, once that exists.
  await browser.storage.local.clear();
}
