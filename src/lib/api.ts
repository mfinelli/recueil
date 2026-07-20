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

// Hand-rolled client, not generated -- the Go side has no OpenAPI spec, so
// the response types below are manually kept in sync with internal/httpapi's
// response DTOs. Worth being aware of as a manual sync point (unlike sqlc's
// automated Postgres <-> Go sync), but reasonable while the API surface
// stays this size.

const API_BASE = "/api";

/** Matches internal/httpapi's writeError: {"error": "..."} */
export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}

interface RequestOptions {
  method?: string;
  body?: unknown;
}

// apiFetch is the low-level primitive -- same-origin, cookie-based session
// auth (credentials: "include"), so the dashboard never handles a bearer
// token itself. Doesn't throw on a non-2xx response; callers that just need
// the raw Response (e.g. session bootstrap, which treats 401 as a normal,
// expected outcome rather than an error) use this directly.
export async function apiFetch(
  path: string,
  options: RequestOptions = {},
): Promise<Response> {
  const init: RequestInit = {
    method: options.method ?? "GET",
    credentials: "include",
  };
  if (options.body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(options.body);
  }
  return fetch(API_BASE + path, init);
}

// apiJSON is the common case: decode JSON on success, throw ApiError
// (carrying the backend's own {"error": "..."} message when present) on
// anything else. A 204 No Content decodes to undefined rather than
// attempting to parse an empty body as JSON.
export async function apiJSON<T>(
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const res = await apiFetch(path, options);
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body: unknown = await res.json();
      if (
        body &&
        typeof body === "object" &&
        "error" in body &&
        typeof body.error === "string"
      ) {
        message = body.error;
      }
    } catch {
      // Non-JSON error body (e.g. a proxy's own HTML error page) -- fall
      // back to statusText rather than let the parse failure mask the
      // real HTTP error.
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
}
