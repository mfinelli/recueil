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
  GET_QUEUE_LIST,
  REFRESH_QUEUE_LIST,
  CLAIM_QUEUE_ITEM,
} from "../common/messages.js";
import {
  getPairingDraft,
  setPairingDraft,
  clearPairingDraft,
} from "../common/storage.js";
import { defaultDeviceName } from "../common/device-name.js";

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
    await renderPairingForm();
  }
}

/** @param {string} [errorMessage] */
async function renderPairingForm(errorMessage) {
  app.replaceChildren();

  const heading = document.createElement("h1");
  heading.textContent = "Pair with your recueil instance";
  app.append(heading);

  const form = document.createElement("form");

  // Restores whatever was last typed -- a popup's entire DOM/JS state is
  // torn down the instant it loses focus (switching windows to go copy a
  // pairing token, in particular), so without this every re-open of the
  // popup mid-pairing would start completely blank. See storage.js's
  // PAIRING_DRAFT_KEY doc comment for why this is a separate, disposable
  // key from the real pairing config.
  const draft = await getPairingDraft();

  form.append(
    fieldLabel(
      "worker-url",
      "Instance URL",
      "url",
      "https://recueil.example.com",
      { value: draft.workerBaseURL },
    ),
    fieldLabel("pairing-token", "Pairing token", "text", "", {
      value: draft.pairingToken,
    }),
    fieldLabel("device-name", "Device name", "text", defaultDeviceName(), {
      value: draft.deviceName,
      required: false,
    }),
  );

  form.addEventListener("input", () => {
    setPairingDraft({
      workerBaseURL: getInputValue("worker-url"),
      pairingToken: getInputValue("pairing-token"),
      deviceName: getInputValue("device-name"),
    });
  });

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
  // Optional -- an empty field falls back to the same computed default
  // shown as its placeholder (defaultDeviceName()), rather than blocking
  // submission over a field most people will just leave alone.
  const deviceName = getInputValue("device-name") || defaultDeviceName();

  if (!workerBaseURL || !pairingToken) {
    await renderPairingForm("Instance URL and pairing token are required.");
    return;
  }

  try {
    new URL(workerBaseURL);
  } catch {
    await renderPairingForm("That doesn't look like a valid URL.");
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
      await renderPairingForm(
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
    await clearPairingDraft();
    renderPairedView({
      workerBaseURL: config.workerBaseURL,
      deviceName: config.deviceName,
    });
  } catch (error) {
    await renderPairingForm(
      error instanceof Error ? error.message : String(error),
    );
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

  app.append(renderQueueSection());

  const unpairLink = document.createElement("a");
  unpairLink.href = "#";
  unpairLink.className = "unpair-link";
  unpairLink.textContent = "Forget this device";
  unpairLink.addEventListener("click", async (event) => {
    event.preventDefault();
    await browser.runtime.sendMessage({ type: UNPAIR_DEVICE });
    await renderPairingForm();
  });
  app.append(unpairLink);
}

// Just a URL list -- id and url are all GET /queue actually returns that's
// meaningful to show, and clicking an item to claim/capture it is its own
// separate piece of work (step 2), not built here. This cache is never
// authoritative (see queue.js's own doc comment) -- displayed as-is,
// refreshed either from whatever the background last cached or, on
// request, live.
function renderQueueSection() {
  const section = document.createElement("div");
  section.className = "queue-section";

  const list = document.createElement("ul");
  list.id = "queue-list";

  const heading = document.createElement("h2");
  heading.textContent = "Queue";
  const refreshButton = document.createElement("button");
  refreshButton.type = "button";
  refreshButton.className = "queue-refresh";
  refreshButton.textContent = "Refresh";
  refreshButton.addEventListener("click", () =>
    handleRefreshQueueClick(refreshButton, list),
  );
  heading.append(refreshButton);

  const status = document.createElement("div");
  status.id = "queue-status";

  section.append(heading, list, status);

  browser.runtime
    .sendMessage({ type: GET_QUEUE_LIST })
    .then((/** @type {any} */ cache) =>
      renderQueueItems(list, cache?.items ?? []),
    );

  return section;
}

/**
 * @param {HTMLElement} list
 * @param {import("../common/storage.js").QueueCacheItem[]} items
 */
function renderQueueItems(list, items) {
  list.replaceChildren();
  if (items.length === 0) {
    const empty = document.createElement("li");
    empty.className = "queue-empty";
    empty.textContent = "Nothing in the queue.";
    list.append(empty);
    return;
  }
  for (const item of items) {
    const entry = document.createElement("li");
    entry.className = "queue-item";
    entry.textContent = item.url;
    entry.addEventListener("click", () =>
      handleQueueItemClick(item.id, entry, list),
    );
    list.append(entry);
  }
}

/**
 * @param {HTMLButtonElement} refreshButton
 * @param {HTMLElement} list
 */
async function handleRefreshQueueClick(refreshButton, list) {
  refreshButton.disabled = true;
  try {
    const cache = /** @type {import("../common/storage.js").QueueCache} */ (
      await browser.runtime.sendMessage({ type: REFRESH_QUEUE_LIST })
    );
    renderQueueItems(list, cache.items);
  } finally {
    refreshButton.disabled = false;
  }
}

/**
 * Claiming is the real, live lock check (see queue.js's claimQueueItem) --
 * the list this renders from is only ever a cache. On success, a new
 * focused tab opens for the user to handle the page by hand (CAPTCHA,
 * paywall, login, whatever); this popup will most likely lose focus and
 * close as that tab takes over, same as any other extension popup does,
 * so there's nothing further to show here in that case. On failure
 * (already claimed elsewhere, already captured, no longer exists -- see
 * queue.js's describeClaimFailure), the message already comes back fully
 * human-readable.
 *
 * @param {string} itemId
 * @param {HTMLElement} itemElement
 * @param {HTMLElement} list
 */
async function handleQueueItemClick(itemId, itemElement, list) {
  const status = /** @type {HTMLElement} */ (
    document.getElementById("queue-status")
  );
  itemElement.classList.add("queue-item--claiming");
  status.className = "status status--pending";
  status.textContent = "Claiming…";

  try {
    await browser.runtime.sendMessage({
      type: CLAIM_QUEUE_ITEM,
      payload: { itemId },
    });
    status.className = "status status--success";
    status.textContent = "Opened in a new tab.";
  } catch (error) {
    status.className = "status status--error";
    status.textContent = error instanceof Error ? error.message : String(error);
  } finally {
    itemElement.classList.remove("queue-item--claiming");
    // Refresh regardless of outcome -- a successful claim makes the item
    // disappear from GET /queue's own results (no longer 'pending'), and a
    // failed one means whatever the popup had cached was already stale;
    // either way, re-fetching is what makes the list honest again.
    const cache = /** @type {import("../common/storage.js").QueueCache} */ (
      await browser.runtime.sendMessage({ type: REFRESH_QUEUE_LIST })
    );
    renderQueueItems(list, cache.items);
  }
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
 * @param {{value?: string, required?: boolean}} [options]
 */
function fieldLabel(id, labelText, type, placeholder, options = {}) {
  const { value = "", required = true } = options;

  const label = document.createElement("label");
  label.htmlFor = id;
  label.textContent = labelText;

  const input = document.createElement("input");
  input.id = id;
  input.type = type;
  input.placeholder = placeholder;
  input.required = required;
  input.value = value;

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
