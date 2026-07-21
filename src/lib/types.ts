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

// Mirrors internal/httpapi's response DTOs by hand -- see lib/api.ts's own
// note on why (no OpenAPI spec/codegen from the Go side yet).

export interface Page {
  id: number;
  normalized_url: string;
  title: string | null;
  latest_capture_at: string;
  excluded_from_mirror: boolean;
  favicon_path: string | null;
  created_at: string;
  updated_at: string;
}

export interface PageListResponse {
  pages: Page[];
  total: number;
}

export interface CaptureSummary {
  id: number;
  source: string;
  raw_url: string;
  title: string | null;
  thumbnail_path: string | null;
  language: string;
  html_compressed_size_bytes: number;
  html_uncompressed_size_bytes: number;
  captured_at: string;
}

export interface PageTag {
  id: number;
  name: string;
  source: "manual" | "ai";
}

export interface PageCollection {
  id: number;
  name: string;
  parent_id: number | null;
}

// PageDetail extends Page: internal/httpapi's pageDetailResponse embeds its
// own pageResponse, flattening the same fields into one JSON object rather
// than a nested envelope -- this mirrors that shape.
export interface PageDetail extends Page {
  captures: CaptureSummary[];
  tags: PageTag[];
  collections: PageCollection[];
}

// POST /api/pages/{id}/tags' response -- lighter than PageTag: the backend
// hardcodes source: "manual" for anything added through this endpoint
// (see internal/httpapi's AddPageTag), so it isn't part of the response at
// all; the caller already knows what it just set.
export interface TagCreated {
  id: number;
  name: string;
}

// GET /api/collections' own item shape -- structurally close to
// PageCollection but with created_at, since that's a full collection
// row, not the lighter per-page membership view.
export interface Collection {
  id: number;
  parent_id: number | null;
  name: string;
  created_at: string;
}

export interface CollectionListResponse {
  collections: Collection[];
}

export interface TextSearchConfigsResponse {
  languages: string[];
}

// GET /api/devices' item shape. last_used_at is null for a device that's
// never made an authenticated request yet (paired but not yet used).
export interface Device {
  id: number;
  device_name: string;
  device_type: string;
  created_at: string;
  last_used_at: string | null;
}

export interface DeviceListResponse {
  devices: Device[];
}

export interface PairingTokenResponse {
  pairing_token: string;
}
