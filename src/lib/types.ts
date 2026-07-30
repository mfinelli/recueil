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
  notes: string | null;
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

// GET /api/captures/{id} -- the full row, including reader_text/ai_summary
// that CaptureSummary deliberately omits (see internal/httpapi's own
// GetPage doc comment: those belong to capture detail, not the page
// detail's capture-history list). reader_text is plain extracted text
// (Readability.js's textContent, not its HTML content field), so it's always
// safe to render directly, no HTML-injection risk from third-party page
// content.
export interface CaptureDetail {
  id: number;
  page_id: number;
  source: string;
  raw_url: string;
  title: string | null;
  thumbnail_path: string | null;
  thumbnail_size_bytes: number | null;
  thumbnail_hash: string | null;
  favicon_path: string | null;
  favicon_size_bytes: number | null;
  favicon_hash: string | null;
  reader_text: string | null;
  readability_version: string | null;
  content_hash: string;
  ai_summary: string | null;
  ai_model: string | null;
  language: string;
  html_compressed_size_bytes: number;
  html_uncompressed_size_bytes: number;
  captured_at: string;
  created_at: string;
  updated_at: string;
}

export interface PageTag {
  id: number;
  name: string;
  slug: string;
  source: "manual" | "ai";
}

export interface PageCollection {
  id: number;
  name: string;
  parent_id: number | null;
}

// A lightweight page reference -- both a link on PageDetail and a
// link-candidates search result are shaped identically (see
// internal/httpapi's own pageLinkResponse), so one type covers both.
export interface PageLink {
  id: number;
  title: string | null;
  normalized_url: string;
  favicon_path: string | null;
}

// PageDetail extends Page: internal/httpapi's pageDetailResponse embeds its
// own pageResponse, flattening the same fields into one JSON object rather
// than a nested envelope -- this mirrors that shape.
export interface PageDetail extends Page {
  captures: CaptureSummary[];
  tags: PageTag[];
  collections: PageCollection[];
  links: PageLink[];
}

// GET /api/tags' item shape -- also what the rename/delete-adjacent
// create/rename tag endpoints return. Slugs are a real, independently
// unique column not derived client-side.
export interface Tag {
  id: number;
  name: string;
  slug: string;
}

export interface TagListResponse {
  tags: Tag[];
}

// GET /api/tags/{id}/pages' response -- includes the tag itself, not just
// its pages, so TagDetail can render a heading without a second request.
export interface TagPagesResponse {
  tag: Tag;
  pages: Page[];
}

// POST /api/pages/{id}/tags' response -- same shape as Tag; kept as its
// own alias since the two describe conceptually different actions (the
// full tag vocabulary vs. "the tag I just applied to this page"), same
// as the PageCollection/Collection split below.
export type TagCreated = Tag;

// GET /api/collections' own item shape -- structurally close to
// PageCollection but with created_at, since that's a full collection
// row, not the lighter per-page membership view.
export interface Collection {
  id: number;
  parent_id: number | null;
  name: string;
  slug: string;
  description: string | null;
  created_at: string;
  updated_at: string;
}

export interface CollectionListResponse {
  collections: Collection[];
}

// GET /api/collections/{id}/pages' response -- direct membership only,
// not the collection's subtree (CollectionDetail shows sub-collections
// as their own links instead, same decision as tags: no recursive
// rollup). Unlike TagPagesResponse, there's no collection object nested
// in here -- CollectionDetail already has the full collection (and its
// ancestors, for the breadcrumb) from resolving the URL's path against
// GET /api/collections, so it would be redundant here.
export interface CollectionPagesResponse {
  pages: Page[];
}

export interface TextSearchConfigsResponse {
  languages: string[];
}

// GET /api/devices' item shape. last_used_at is null for a device that's
// never made an authenticated request yet (paired but not yet used).
export interface Device {
  id: number;
  device_name: string;
  device_type: "extension" | "pwa" | "cli" | "shortcut";
  created_at: string;
  last_used_at: string | null;
}

export interface DeviceListResponse {
  devices: Device[];
}

export interface Session {
  id: number;
  browser: string;
  browser_version: string;
  os: string;
  device_class: "desktop" | "mobile" | "tablet" | "tv" | "bot" | "";
  created_at: string;
  last_seen_at: string;
  is_current: boolean;
}

export interface SessionListResponse {
  sessions: Session[];
}

export interface PairingTokenResponse {
  pairing_token: string;
}

// GET/PATCH /api/settings' response shape. language is null both for "no
// row yet" and "explicitly cleared".
export interface UserSettings {
  language: string | null;
  theme: string | null;
}

// GET /api/stats' shape -- Settings page's stats section. A plain sum
// across every capture row.
export interface Stats {
  page_count: number;
  capture_count: number;
  html_compressed_bytes: number;
  html_uncompressed_bytes: number;
  favicon_bytes: number;
  screenshot_bytes: number;
}

// GET /api/queue-items' item shape -- pending/claimed/failed
// unconditionally, plus 'captured' items from the last few minutes. id is a
// client-generated UUID (queue_items.id is TEXT), not a number.
export interface QueueItem {
  id: string;
  url: string;
  status: "pending" | "claimed" | "captured" | "failed";
  manual_retry: boolean;
  claimed_at: string | null;
  created_at: string;
}

export interface QueueItemListResponse {
  items: QueueItem[];
}

// GET /api/jobs' item shape -- one combined shape for all three job
// kinds (screenshot/readability/AI), same as internal/httpapi's own job
// DTO. pending/processing/failed unconditionally, 'done' only within the
// same recency window QueueItem's own 'captured' status uses. id is a
// plain job-table integer PK, unlike QueueItem's client-generated UUID.
export interface Job {
  id: number;
  page_id: number;
  url: string;
  title: string | null;
  status: "pending" | "processing" | "done" | "failed";
  attempts: number;
  error: string | null;
  claimed_at: string | null;
  completed_at: string | null;
}

export interface JobsResponse {
  screenshot_jobs: Job[];
  readability_jobs: Job[];
  ai_jobs: Job[];
}

// GET /info -- unauthenticated, unprefixed (not under /api, served
// directly by internal/httpapi/router.go alongside /ping/health/metrics
// via go.finelli.dev/healthchecks), so this doesn't go through
// api.ts's apiJSON/apiFetch (both hardcode the /api prefix). commit is
// already a short SHA (e.g. "acff9fd"), not a full 40-character hash --
// nothing to truncate on the frontend.
export interface InfoResponse {
  version: string;
  commit: string;
  date: string;
}

// GET /api/capture-config -- this running agent's currently-configured
// readability_version/ai_model, i.e. what a regenerate would actually
// produce right now. Compared against a capture's own already-stored
// readability_version/ai_model to decide whether to show a regenerate
// button (equal means regenerating would just reproduce what's already
// there). Both nullable independently of the capture's own fields --
// null here means "not configured" (or AI disabled entirely), not "same
// as the capture."
export interface CaptureConfig {
  readability_version: string | null;
  ai_model: string | null;
}
