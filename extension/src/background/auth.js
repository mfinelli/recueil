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
// pair() below. That call deliberately doesn't happen in here, because
// permissions.request() needs to run inside the same user-gesture-driven
// call stack as the pairing form's submit handler -- once this crosses a
// runtime.sendMessage boundary from the popup into this background
// context, whether the browser still considers it "triggered by a real
// user action" (transient activation) isn't something to rely on across
// Chrome and Firefox without having actually tested it. So: the popup
// requests the permission itself, synchronously in its own submit
// handler, and only sends PAIR_DEVICE once that's confirmed granted.

import { getConfig, setConfig, clearConfig } from "../common/storage.js";

/**
 * @param {{workerBaseURL: string, pairingToken: string, deviceName: string}} params
 * @returns {Promise<import("../common/storage.js").RecueilConfig>}
 */
export async function pair({ workerBaseURL, pairingToken, deviceName }) {
  const normalizedBaseURL = workerBaseURL.replace(/\/+$/, "");

  const response = await fetch(`${normalizedBaseURL}/pair`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      pairing_token: pairingToken,
      device_name: deviceName,
      device_type: "extension",
    }),
  });

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
  // Deliberately never returns config.token to a caller -- the popup only
  // ever needs to know *whether* pairing succeeded and which
  // instance/device it's talking to, not the credential itself.
  return {
    paired: true,
    workerBaseURL: config.workerBaseURL,
    deviceName: config.deviceName,
  };
}

export async function unpair() {
  // Local-only: there's no device-facing endpoint to revoke this token
  // server-side yet. /internal/tokens/:id (DELETE) exists but is gated by
  // X-Service-Key, not a device bearer token -- it's the dashboard's own
  // admin-side device-management endpoint (DESIGN.md's "Device management
  // UI", still on the horizon), not something a paired device can call on
  // itself. So "unpair" here really means "forget this device's own
  // stored credential" -- the token itself stays valid server-side until
  // an operator revokes it from the dashboard, once that exists.
  await clearConfig();
}
