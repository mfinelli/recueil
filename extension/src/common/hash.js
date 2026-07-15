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

// POST /captures/upload-urls (see terraform/index.js's HEX_SHA256_PATTERN)
// wants content_sha256_html/content_sha256_favicon as lowercase hex --
// this is the only hash we compute client-side; the base64 form the
// actual R2 PUT's x-amz-checksum-sha256 header needs is handed back to us
// directly in the upload-urls response's required_headers_html/
// required_headers_favicon, not something we derive ourselves. Two
// different encodings of "the same fact," computed in two different
// places, deliberately not duplicated.

/**
 * @param {ArrayBuffer|Uint8Array} data
 * @returns {Promise<string>} lowercase hex-encoded SHA-256 digest
 */
export async function sha256Hex(data) {
  const digest = await crypto.subtle.digest("SHA-256", data);
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("");
}
