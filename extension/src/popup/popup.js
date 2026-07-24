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
// than reaching for anything heavier. Styling now lives in popup.css's
// token system (see its own file comment); the class names/ids assigned
// below are its hooks, not incidental.
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
  GET_BOOKMARK_SYNC_STATE,
  ENABLE_BOOKMARK_SYNC,
  DISABLE_BOOKMARK_SYNC,
} from "../common/messages.js";
import {
  getPairingDraft,
  setPairingDraft,
  clearPairingDraft,
} from "../common/storage.js";
import { defaultDeviceName } from "../common/device-name.js";
import { t, documentLanguage } from "../common/i18n.js";

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

// Only #queue-status's "Opened in a new tab" success message auto-dismisses
// (see handleQueueItemClick) -- "Claiming..." and errors stay put until the
// next thing overwrites them. Tracked at module scope so a fresh claim can
// cancel a still-pending dismissal from an earlier one instead of letting
// it fire later and blank out whatever the new claim just set.
let queueStatusDismissTimer = /** @type {number|undefined} */ (undefined);

document.title = t("popupTitle");
document.documentElement.lang = documentLanguage();
// popup.html's own static markup ships an English "Loading…" placeholder
// (see its file comment) since it has no JS of its own to localize it --
// this is the earliest point popup.js can overwrite it.
const loadingParagraph = app.querySelector("p");
if (loadingParagraph) loadingParagraph.textContent = t("loading");

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
  heading.textContent = t("pairHeading");
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
      t("fieldInstanceUrl"),
      "url",
      "https://recueil.example.com",
      { value: draft.workerBaseURL },
    ),
    fieldLabel("pairing-token", t("fieldPairingToken"), "text", "", {
      value: draft.pairingToken,
    }),
    fieldLabel(
      "device-name",
      t("fieldDeviceName"),
      "text",
      defaultDeviceName(),
      {
        value: draft.deviceName,
        required: false,
      },
    ),
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
  submitButton.textContent = t("pairButton");
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
    await renderPairingForm(t("errorUrlAndTokenRequired"));
    return;
  }

  try {
    new URL(workerBaseURL);
  } catch {
    await renderPairingForm(t("errorInvalidUrl"));
    return;
  }

  submitButton.disabled = true;
  submitButton.textContent = t("pairButtonPairing");

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
      await renderPairingForm(t("errorPermissionDenied"));
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
  heading.append(document.createTextNode("recueil"));
  const statusSub = document.createElement("span");
  statusSub.className = "sub";
  statusSub.textContent = t("pairedStatusSub");
  heading.append(statusSub);
  app.append(heading);

  const info = document.createElement("dl");
  info.className = "paired-info";
  info.append(
    dtdd(t("labelInstance"), workerBaseURL),
    dtdd(t("labelThisDevice"), deviceName),
  );
  app.append(info);

  const captureBlock = document.createElement("div");
  captureBlock.className = "capture-block";

  const captureButton = document.createElement("button");
  captureButton.type = "button";
  captureButton.className = "capture-button";
  captureButton.textContent = t("captureButton");
  captureButton.addEventListener("click", () =>
    handleCaptureClick(captureButton),
  );
  captureBlock.append(captureButton);

  const status = document.createElement("div");
  status.id = "capture-status";
  captureBlock.append(status);

  app.append(captureBlock);

  app.append(renderQueueSection());

  const divider = document.createElement("hr");
  divider.className = "rule";
  app.append(divider);

  app.append(renderBookmarkSyncSection());

  const unpairLink = document.createElement("a");
  unpairLink.href = "#";
  unpairLink.className = "unpair-link";
  unpairLink.textContent = t("unpairLink");
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
  heading.textContent = t("queueHeading");
  const refreshButton = document.createElement("button");
  refreshButton.type = "button";
  refreshButton.className = "queue-refresh";
  refreshButton.textContent = t("queueRefresh");
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
    empty.textContent = t("queueEmpty");
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
  clearTimeout(queueStatusDismissTimer);
  itemElement.classList.add("queue-item--claiming");
  status.className = "status status--pending-plain";
  status.textContent = t("queueClaiming");

  try {
    await browser.runtime.sendMessage({
      type: CLAIM_QUEUE_ITEM,
      payload: { itemId },
    });
    showAutoDismissingSuccess(status, t("queueOpenedNewTab"));
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

// Auto-dismisses after 10s with a linear countdown bar so that claiming
// several queue items in a row each gets its own visible confirmation
// instead of them piling up or one stale message lingering. Only this
// specific message does this -- "Claiming..." and errors are left for
// handleQueueItemClick's other branches to overwrite instead, since an
// error in particular shouldn't disappear on its own.
const QUEUE_SUCCESS_DISMISS_MS = 10000;

/**
 * @param {HTMLElement} status
 * @param {string} text
 */
function showAutoDismissingSuccess(status, text) {
  clearTimeout(queueStatusDismissTimer);

  status.className = "status status--success-plain dismissing";
  status.replaceChildren();

  const label = document.createElement("span");
  label.textContent = text;

  const track = document.createElement("div");
  track.className = "countdown-track";
  const fill = document.createElement("div");
  fill.className = "countdown-fill running";
  track.append(fill);

  status.append(label, track);

  queueStatusDismissTimer = setTimeout(() => {
    status.className = "status";
    status.replaceChildren();
    queueStatusDismissTimer = undefined;
  }, QUEUE_SUCCESS_DISMISS_MS);
}

// A checkbox toggle, not a button -- reflects an actual on/off state
// (unlike "Save this page"/"Refresh", which are one-shot actions), and
// checkboxes are the conventional control for that.
function renderBookmarkSyncSection() {
  const section = document.createElement("div");
  section.className = "bookmark-sync-section";

  const label = document.createElement("label");
  const labelText = document.createElement("span");
  labelText.textContent = t("bookmarkSyncLabel");
  const checkbox = document.createElement("input");
  checkbox.type = "checkbox";
  checkbox.id = "bookmark-sync-toggle";
  label.append(labelText, checkbox);

  const status = document.createElement("div");
  status.id = "bookmark-sync-status";

  section.append(label, status);

  browser.runtime
    .sendMessage({ type: GET_BOOKMARK_SYNC_STATE })
    .then((/** @type {any} */ enabled) => {
      checkbox.checked = Boolean(enabled);
    });

  checkbox.addEventListener("change", () =>
    handleBookmarkSyncToggle(checkbox, status),
  );

  return section;
}

/**
 * @param {HTMLInputElement} checkbox
 * @param {HTMLElement} status
 */
async function handleBookmarkSyncToggle(checkbox, status) {
  checkbox.disabled = true;
  status.textContent = "";

  try {
    if (checkbox.checked) {
      // Requesting the permission here, synchronously in this change
      // handler, for the same user-gesture reasoning as pairing's own
      // <all_urls> request (see auth.js's doc comment) -- once this
      // crosses the runtime.sendMessage boundary into the background,
      // whether the browser still considers it "triggered by a real user
      // action" isn't reliable to assume across Chrome and Firefox.
      const granted = await browser.permissions.request({
        permissions: ["bookmarks"],
      });
      if (!granted) {
        checkbox.checked = false;
        status.className = "status status--error";
        status.textContent = t("bookmarkSyncPermissionDenied");
        return;
      }
      await browser.runtime.sendMessage({ type: ENABLE_BOOKMARK_SYNC });
    } else {
      await browser.runtime.sendMessage({ type: DISABLE_BOOKMARK_SYNC });
    }
  } catch (error) {
    // Enabling failed (the initial sync itself, most likely -- see
    // bookmarks.js's enableBookmarkSync doc comment for why that error is
    // allowed to propagate this far rather than being swallowed like the
    // alarm's own retries are) -- reflect that the toggle didn't actually
    // end up in the state it looks like from the checkbox alone.
    checkbox.checked = false;
    status.className = "status status--error";
    status.textContent = error instanceof Error ? error.message : String(error);
  } finally {
    checkbox.disabled = false;
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
  status.textContent = t("capturing");

  try {
    await browser.runtime.sendMessage({ type: CAPTURE_ACTIVE_TAB });
    status.className = "status status--success";
    status.textContent = t("captureSaved");
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
  label.append(document.createTextNode(labelText));
  if (required) {
    const marker = document.createElement("span");
    marker.className = "required-marker";
    marker.setAttribute("aria-hidden", "true");
    marker.textContent = " *";
    label.append(marker);
  }

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
