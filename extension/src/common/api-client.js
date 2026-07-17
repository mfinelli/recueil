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

// Shared client for every *authenticated* call to a paired recueil
// instance's Worker -- POST /pair itself doesn't use this (there's no
// token yet to attach), see background/auth.js for that one call that
// necessarily works differently.
//
// This runs in the background/service-worker context, which is what
// makes it a plain fetch() rather than needing the relay
// capture-inject/relay-fetch.js exists for: a background-context fetch to
// an origin covered by granted host_permissions isn't subject to the
// CORS restrictions a *page's* fetch would be -- see
// background/fetch-relay.js's doc comment for the fuller version of this
// same reasoning.

export class ApiAuthError extends Error {
  /** @param {string} message */
  constructor(message) {
    super(message);
    this.name = "ApiAuthError";
  }
}

export class ApiError extends Error {
  /**
   * @param {string} message
   * @param {number} status
   */
  constructor(message, status) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

/**
 * @param {import("./storage.js").RecueilConfig} config
 * @param {string} path - e.g. "/queue", must start with "/"
 * @param {{method?: string, body?: unknown, headers?: Record<string,string>}} [options]
 */
export async function apiRequest(config, path, options = {}) {
  const { method = "GET", body, headers = {} } = options;
  const url = `${config.workerBaseURL}${path}`;

  const response = await fetch(url, {
    method,
    headers: {
      Authorization: `Bearer ${config.token}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...headers,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });

  if (response.status === 401) {
    // Distinguished from a generic ApiError specifically so callers (the
    // queue poller, a direct capture, anything running unattended) can
    // tell "the device's token was revoked or the instance forgot us" --
    // which needs surfacing to the user as "please re-pair" -- apart from
    // an ordinary transient failure worth just retrying later.
    throw new ApiAuthError(
      `recueil: device token was rejected by ${config.workerBaseURL} -- re-pairing required`,
    );
  }
  if (!response.ok) {
    const text = await response.text().catch(() => "");
    throw new ApiError(
      `recueil: ${method} ${path} failed: ${response.status} ${text}`,
      response.status,
    );
  }
  if (response.status === 204) {
    return null;
  }

  const contentType = response.headers.get("content-type") || "";
  if (contentType.includes("application/json")) {
    return response.json();
  }
  return null;
}
