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

// No framework -- this is small enough (two views: pairing form, paired
// state) that plain DOM manipulation is less code and less to reason about
// than reaching for anything heavier, especially while the popup's actual
// shape is still expected to move (see popup.html's comment on styling
// being deferred).
//
// The permissions.request() call in handlePairSubmit is the one thing in
// here that HAS to stay exactly where it is, synchronously inside the
// form's submit handler, not moved into background.js -- see
// background/auth.js's own doc comment for why crossing a
// runtime.sendMessage boundary first isn't safe to assume preserves the
// "this was triggered by a real user action" state every browser needs to
// honor permissions.request() at all.

import browser from "webextension-polyfill";
import {
  PAIR_DEVICE,
  GET_AUTH_STATE,
  CAPTURE_ACTIVE_TAB,
  UNPAIR_DEVICE,
} from "../common/messages.js";

/**
 * The shape background/auth.js's getAuthState() actually returns --
 * runtime.sendMessage's return type is necessarily generic (see
 * relay-fetch.js's own RelayFetchResponse typedef for the same reasoning),
 * so this connects the two sides for the type checker.
 * @typedef {{paired: false}|{paired: true, workerBaseURL: string, deviceName: string}} AuthState
 */

// popup.html always has this div -- a genuine invariant we control (see
// popup.html), not an assumption worth a runtime null-check every call
// site below would otherwise need.
const app = /** @type {HTMLElement} */ (document.getElementById("app"));

async function main() {
  const authState = /** @type {AuthState} */ (
    await browser.runtime.sendMessage({ type: GET_AUTH_STATE })
  );
  if (authState.paired) {
    renderPairedView(authState);
  } else {
    renderPairingForm();
  }
}

/** @param {string} [errorMessage] */
function renderPairingForm(errorMessage) {
  app.replaceChildren();

  const heading = document.createElement("h1");
  heading.textContent = "Pair with your recueil instance";
  app.append(heading);

  const form = document.createElement("form");

  form.append(
    fieldLabel(
      "worker-url",
      "Instance URL",
      "url",
      "https://recueil.example.com",
    ),
    fieldLabel("pairing-token", "Pairing token", "text", ""),
    fieldLabel("device-name", "Device name", "text", "e.g. Firefox on laptop"),
  );

  const submitButton = document.createElement("button");
  submitButton.type = "submit";
  submitButton.textContent = "Pair";
  form.append(submitButton);

  const status = document.createElement("div");
  if (errorMessage) {
    status.className = "status status--error";
    status.textContent = errorMessage;
    form.append(status);
  }

  form.addEventListener("submit", (event) =>
    handlePairSubmit(event, submitButton),
  );

  app.append(form);
}

/**
 * The three fields in the pairing form are all elements we created
 * ourselves via fieldLabel() just above -- asserting non-null and casting
 * to HTMLInputElement here reflects a real invariant (we control the
 * markup), the same reasoning as the top-level `app` assertion.
 * @param {string} id
 * @returns {string}
 */
function getInputValue(id) {
  const input = /** @type {HTMLInputElement} */ (document.getElementById(id));
  return input.value.trim();
}

/**
 * @param {SubmitEvent} event
 * @param {HTMLButtonElement} submitButton
 */
async function handlePairSubmit(event, submitButton) {
  // Everything up to and including permissions.request() below must stay
  // synchronous-enough to still count as "within" this submit handler --
  // see file doc comment.
  event.preventDefault();

  const workerBaseURL = getInputValue("worker-url").replace(/\/+$/, "");
  const pairingToken = getInputValue("pairing-token");
  const deviceName = getInputValue("device-name");

  if (!workerBaseURL || !pairingToken || !deviceName) {
    renderPairingForm("Every field is required.");
    return;
  }

  try {
    new URL(workerBaseURL);
  } catch {
    renderPairingForm("That doesn't look like a valid URL.");
    return;
  }

  submitButton.disabled = true;
  submitButton.textContent = "Pairing…";

  try {
    // Requesting <all_urls> here, not just originPattern, is deliberate --
    // this device needs more than just permission to talk to the Worker
    // itself: capturing any page also means the background relay
    // (fetch-relay.js) fetching that page's own arbitrary third-party
    // resources, and uploading to R2's presigned URL, which lives on a
    // completely different origin than the Worker. <all_urls> is the
    // manifest's own declared ceiling for optional_host_permissions
    // (manifest.base.json) precisely for this reason -- asking for only
    // originPattern would pair successfully but silently break the very
    // first real capture.
    const granted = await browser.permissions.request({
      origins: ["<all_urls>"],
    });
    if (!granted) {
      renderPairingForm(
        "recueil needs permission to talk to your instance to pair -- please allow it and try again.",
      );
      return;
    }

    const config = /** @type {import("../common/storage.js").RecueilConfig} */ (
      await browser.runtime.sendMessage({
        type: PAIR_DEVICE,
        payload: { workerBaseURL, pairingToken, deviceName },
      })
    );
    renderPairedView({
      workerBaseURL: config.workerBaseURL,
      deviceName: config.deviceName,
    });
  } catch (error) {
    renderPairingForm(error instanceof Error ? error.message : String(error));
  }
}

/** @param {{workerBaseURL: string, deviceName: string}} config */
function renderPairedView({ workerBaseURL, deviceName }) {
  app.replaceChildren();

  const heading = document.createElement("h1");
  heading.textContent = "recueil";
  app.append(heading);

  const info = document.createElement("dl");
  info.className = "paired-info";
  info.append(dtdd("Instance", workerBaseURL), dtdd("This device", deviceName));
  app.append(info);

  const captureButton = document.createElement("button");
  captureButton.type = "button";
  captureButton.textContent = "Save this page";
  captureButton.addEventListener("click", () =>
    handleCaptureClick(captureButton),
  );
  app.append(captureButton);

  const status = document.createElement("div");
  status.id = "capture-status";
  app.append(status);

  const unpairLink = document.createElement("a");
  unpairLink.href = "#";
  unpairLink.className = "unpair-link";
  unpairLink.textContent = "Forget this device";
  unpairLink.addEventListener("click", async (event) => {
    event.preventDefault();
    await browser.runtime.sendMessage({ type: UNPAIR_DEVICE });
    renderPairingForm();
  });
  app.append(unpairLink);
}

/** @param {HTMLButtonElement} captureButton */
async function handleCaptureClick(captureButton) {
  // Created by renderPairedView just above, in the same view this button
  // only ever exists within -- a real invariant, same reasoning as the
  // top-level `app` assertion.
  const status = /** @type {HTMLElement} */ (
    document.getElementById("capture-status")
  );
  captureButton.disabled = true;
  status.className = "status status--pending";
  status.textContent = "Capturing…";

  try {
    await browser.runtime.sendMessage({ type: CAPTURE_ACTIVE_TAB });
    status.className = "status status--success";
    status.textContent = "Saved.";
  } catch (error) {
    status.className = "status status--error";
    status.textContent = error instanceof Error ? error.message : String(error);
  } finally {
    captureButton.disabled = false;
  }
}

/**
 * @param {string} id
 * @param {string} labelText
 * @param {string} type
 * @param {string} placeholder
 */
function fieldLabel(id, labelText, type, placeholder) {
  const label = document.createElement("label");
  label.htmlFor = id;
  label.textContent = labelText;

  const input = document.createElement("input");
  input.id = id;
  input.type = type;
  input.placeholder = placeholder;
  input.required = true;

  label.append(input);
  return label;
}

/**
 * @param {string} term
 * @param {string} description
 */
function dtdd(term, description) {
  const dt = document.createElement("dt");
  dt.textContent = term;
  const dd = document.createElement("dd");
  dd.textContent = description;
  const fragment = document.createDocumentFragment();
  fragment.append(dt, dd);
  return fragment;
}

main();
