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

// Intentionally not app.js: this page's own job (hand a raw bearer token to
// a human, once, then forget it) doesn't overlap with app.js's (pair once,
// store credentials, enqueue) -- see token.html's own top comment for why
// they're kept as two separate pages rather than a mode flag on one.

function $(id) {
  return document.getElementById(id);
}

function showView(name) {
  document.querySelectorAll("[data-view]").forEach((el) => {
    el.hidden = el.dataset.view !== name;
  });
}

$("token-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const pairingToken = $("pairing-token").value.trim();
  const deviceName = $("device-name").value.trim();
  if (!pairingToken || !deviceName) return;

  const submit = $("token-submit");
  const status = $("token-status");
  submit.disabled = true;
  status.textContent = "";
  status.className = "";

  try {
    const res = await fetch("/pair", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        pairing_token: pairingToken,
        device_name: deviceName,
        device_type: "shortcut",
      }),
    });
    if (res.status === 401) {
      throw new Error("That pairing token isn't valid, or has expired.");
    }
    if (!res.ok) {
      throw new Error(`Pairing failed (status ${res.status}).`);
    }
    const body = await res.json();
    $("token-value").textContent = body.token;
    showView("result");
  } catch (err) {
    status.textContent = err.message;
    status.className = "status error";
    submit.disabled = false;
  }
});

$("copy-button").addEventListener("click", async () => {
  await navigator.clipboard.writeText($("token-value").textContent);
  const button = $("copy-button");
  button.textContent = "Copied!";
  setTimeout(() => {
    button.textContent = "Copy";
  }, 2000);
});
