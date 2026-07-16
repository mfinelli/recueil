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

// Favicon selection, per DESIGN.md §3g: link-level selection, not
// pixel-level -- we never decode image bytes to compare resolutions, we
// only compare what the page itself declares. If any <link rel="icon">
// (or apple-touch-icon) tag exists, use it -- svg preferred outright over
// any raster candidate, else the raster candidate with the largest
// declared `sizes`, and if that single candidate's fetch fails we give up
// entirely rather than falling through to the conventional paths below (a
// declared-but-broken link is still a real answer to "does this page have
// an icon", just a broken one). Only when there's no <link> tag at all do
// we fall back to probing the conventional root-relative paths, in
// preference order, stopping at the first one that actually exists.
//
// The returned bytes are stored completely unprocessed -- see the
// package doc on why no image decoding/resizing happens anywhere in this
// pipeline, here included.

const ICON_LINK_SELECTOR =
  'link[rel="icon" i], link[rel="shortcut icon" i], link[rel="apple-touch-icon" i], link[rel="apple-touch-icon-precomposed" i]';

const FALLBACK_PATHS = ["/favicon.svg", "/favicon.png", "/favicon.ico"];

/**
 * @param {import("./relay-fetch.js").FetchLike} fetchFn
 * @returns {Promise<{url: string, bytes: ArrayBuffer, ext: string}|null>}
 */
export async function selectFavicon(fetchFn) {
  const linkURL = selectFromLinkTags();
  if (linkURL) {
    // A declared-but-unreachable icon is a real (if broken) answer -- we
    // don't fall through to guessing conventional paths just because the
    // page's own declaration didn't pan out.
    return fetchFavicon(fetchFn, linkURL);
  }

  for (const path of FALLBACK_PATHS) {
    const candidateURL = new URL(path, document.baseURI).href;
    const result = await fetchFavicon(fetchFn, candidateURL);
    if (result) {
      return result;
    }
  }

  return null;
}

function selectFromLinkTags() {
  const links = Array.from(document.querySelectorAll(ICON_LINK_SELECTOR));
  if (links.length === 0) {
    return null;
  }

  const svgLink = links.find((link) => isSVGCandidate(link));
  if (svgLink) {
    return resolveHref(svgLink);
  }

  /** @type {Element|null} */
  let best = null;
  let bestArea = -1;
  for (const link of links) {
    const area = declaredArea(link);
    if (area > bestArea) {
      best = link;
      bestArea = area;
    }
  }
  return resolveHref(best ?? links[0]);
}

/** @param {Element} link */
function isSVGCandidate(link) {
  const type = (link.getAttribute("type") || "").toLowerCase();
  if (type === "image/svg+xml") {
    return true;
  }
  const href = link.getAttribute("href") || "";
  return href.toLowerCase().split("?")[0].endsWith(".svg");
}

// "any" (a valid value per the HTML spec, meaning "scales to any size,
// typically SVG") sorts above every raster candidate; a missing/malformed
// `sizes` attribute sorts as the smallest, since it tells us nothing.
/** @param {Element} link */
function declaredArea(link) {
  const sizes = (link.getAttribute("sizes") || "").trim().toLowerCase();
  if (sizes === "any") {
    return Infinity;
  }
  const [first] = sizes.split(/\s+/);
  const match = first && first.match(/^(\d+)x(\d+)$/);
  if (!match) {
    return 0;
  }
  return Number(match[1]) * Number(match[2]);
}

/** @param {Element} link */
function resolveHref(link) {
  const href = link.getAttribute("href");
  if (!href) {
    return null;
  }
  return new URL(href, document.baseURI).href;
}

/**
 * @param {import("./relay-fetch.js").FetchLike} fetchFn
 * @param {string} url
 */
async function fetchFavicon(fetchFn, url) {
  try {
    const response = await fetchFn(url);
    if (!response || response.status >= 400) {
      return null;
    }
    const bytes = await response.arrayBuffer();
    if (!bytes || bytes.byteLength === 0) {
      return null;
    }
    return {
      url: response.url || url,
      bytes,
      ext: extensionFor(response, url),
    };
  } catch {
    return null;
  }
}

/**
 * @param {import("./relay-fetch.js").FetchLikeResponse} response
 * @param {string} url
 */
function extensionFor(response, url) {
  const contentType = (response.headers.get("content-type") || "")
    .split(";")[0]
    .trim()
    .toLowerCase();
  if (contentType === "image/svg+xml") {
    return "svg";
  }
  if (contentType === "image/png") {
    return "png";
  }
  if (
    contentType === "image/x-icon" ||
    contentType === "image/vnd.microsoft.icon"
  ) {
    return "ico";
  }
  // Fall back to the URL's own extension when the server didn't send a
  // usable content-type (surprisingly common for /favicon.ico in
  // particular) -- still one of the three formats §3g scopes this to; if
  // it isn't, the whole result is discarded upstream by the Worker's own
  // FAVICON_EXTENSIONS validation rather than trusted blindly here.
  const pathExt = url.toLowerCase().split("?")[0].split(".").pop() ?? "";
  return ["svg", "png", "ico"].includes(pathExt) ? pathExt : "ico";
}
