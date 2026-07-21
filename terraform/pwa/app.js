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

// Vanilla JS, no bundler, no dependencies -- same "no build step"
// constraint as terraform/worker/index.js, for the same reason: this
// deploys as a plain static file, not a build artifact.

const STORAGE_KEY = "recueil.credentials";

/** @returns {{token: string, deviceId: number, deviceName: string} | null} */
function loadCredentials() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

function saveCredentials(creds) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(creds));
}

function clearCredentials() {
  localStorage.removeItem(STORAGE_KEY);
}

// Pairs against this same origin -- no Worker URL field anywhere in this
// app, since it's served by the Worker it talks to (see index.html's own
// comment on the pair view).
async function pair(pairingToken, deviceName) {
  const res = await fetch("/pair", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      pairing_token: pairingToken,
      device_name: deviceName,
      device_type: "pwa",
    }),
  });
  if (res.status === 401) {
    throw new Error("That pairing token isn't valid, or has expired.");
  }
  if (!res.ok) {
    throw new Error(`Pairing failed (status ${res.status}).`);
  }
  const body = await res.json();
  return {
    token: body.token,
    deviceId: body.device_id,
    deviceName: body.device_name,
  };
}

// id is generated here, not by the server -- the same client-generated,
// idempotent-retry id every other client (extension/CLI) already uses
// (see terraform/worker/index.js's handleEnqueue).
async function enqueue(token, url) {
  const res = await fetch("/queue", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${token}`,
    },
    body: JSON.stringify({ id: crypto.randomUUID(), url }),
  });
  if (res.status === 401) {
    throw new Error("This device's connection was revoked. Reconnect below.");
  }
  if (!res.ok) {
    throw new Error(`Couldn't save this page (status ${res.status}).`);
  }
}

// Android's share sheet doesn't consistently put the shared link in the
// `url` param -- many apps hand off `text` instead (sometimes a caption
// plus the link together). Prefer `url`; otherwise pull the first
// URL-shaped token out of `text` rather than giving up.
function extractSharedUrl(params) {
  const rawUrl = params.get("url");
  if (rawUrl) return rawUrl;
  const text = params.get("text");
  if (text) {
    const match = text.match(/https?:\/\/\S+/);
    if (match) return match[0];
  }
  return null;
}

function showView(name) {
  document.querySelectorAll("[data-view]").forEach((el) => {
    el.hidden = el.dataset.view !== name;
  });
}

function setStatus(el, message, kind) {
  el.textContent = message;
  el.className = message ? `status ${kind}` : "";
}

function wirePairForm() {
  const form = document.getElementById("pair-form");
  const status = document.getElementById("pair-status");
  const submit = document.getElementById("pair-submit");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const token = document.getElementById("pairing-token").value.trim();
    const deviceName = document.getElementById("device-name").value.trim();
    if (!token || !deviceName) return;

    submit.disabled = true;
    setStatus(status, "", "");
    try {
      const creds = await pair(token, deviceName);
      saveCredentials(creds);
      window.location.reload();
    } catch (err) {
      setStatus(status, err.message, "error");
      submit.disabled = false;
    }
  });
}

function wireManualForm(creds) {
  const form = document.getElementById("manual-form");
  const status = document.getElementById("manual-status");
  const submit = document.getElementById("manual-submit");
  const input = document.getElementById("manual-url");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const url = input.value.trim();
    if (!url) return;

    submit.disabled = true;
    setStatus(status, "", "");
    try {
      await enqueue(creds.token, url);
      setStatus(status, "Added to your queue.", "success");
      input.value = "";
    } catch (err) {
      setStatus(status, err.message, "error");
    } finally {
      submit.disabled = false;
    }
  });
}

function wireUnpair() {
  document.getElementById("unpair-button").addEventListener("click", () => {
    if (
      !confirm(
        "Disconnect this device? You'll need a new pairing token from the dashboard to reconnect.",
      )
    ) {
      return;
    }
    clearCredentials();
    window.location.reload();
  });
}

async function handleIncomingShare(creds, sharedUrl) {
  showView("sharing");
  const statusEl = document.getElementById("sharing-status");
  const urlEl = document.getElementById("sharing-url");
  const retryButton = document.getElementById("sharing-retry");
  const doneButton = document.getElementById("sharing-done");
  urlEl.textContent = sharedUrl;

  async function attempt() {
    setStatus(statusEl, "Saving…", "");
    retryButton.hidden = true;
    doneButton.hidden = true;
    try {
      await enqueue(creds.token, sharedUrl);
      setStatus(statusEl, "Saved to your queue.", "success");
      doneButton.hidden = false;
    } catch (err) {
      setStatus(statusEl, err.message, "error");
      retryButton.hidden = false;
      doneButton.hidden = false;
    }
  }

  retryButton.addEventListener("click", attempt);
  doneButton.addEventListener("click", () => {
    window.location.href = "/";
  });

  await attempt();
}

async function init() {
  if ("serviceWorker" in navigator) {
    navigator.serviceWorker.register("sw.js").catch(() => {
      // Installability/offline-shell caching only -- nothing in this app
      // depends on the service worker actually registering, so a failure
      // here is silently non-fatal.
    });
  }

  // Clears the query string immediately so a page refresh after a share
  // doesn't resubmit the same URL a second time.
  const params = new URLSearchParams(window.location.search);
  const sharedUrl = extractSharedUrl(params);
  if (params.toString()) {
    window.history.replaceState({}, "", window.location.pathname);
  }

  const creds = loadCredentials();
  if (!creds) {
    showView("pair");
    wirePairForm();
    return;
  }

  document.getElementById("device-name-display").textContent = creds.deviceName;
  wireUnpair();
  wireManualForm(creds);

  if (sharedUrl) {
    await handleIncomingShare(creds, sharedUrl);
  } else {
    showView("main");
  }
}

init();
