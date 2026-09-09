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

import { describe, it, expect, vi, afterEach } from "vitest";
import { apiFetch, apiJSON, uploadManualCapture, ApiError } from "./api";

// Real Response objects (Node's built-in global, not hand-rolled fakes) so
// .ok/.status/.json() behave exactly like a real fetch would -- the same
// reasoning this project's Go side avoids mocking DB-touching code:
// prefer a real implementation of whatever's standing in for the thing
// under test.
function mockFetchOnce(response: Response) {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(response));
}

describe("apiFetch", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("sends credentials: include and the recueil API base path", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await apiFetch("/auth/me");

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/auth/me",
      expect.objectContaining({ method: "GET", credentials: "include" }),
    );
  });

  it("JSON-encodes a body and sets Content-Type when one is given", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await apiFetch("/auth/login", {
      method: "POST",
      body: { username: "alice", password: "hunter2" },
    });

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.method).toBe("POST");
    expect(init.body).toBe(
      JSON.stringify({ username: "alice", password: "hunter2" }),
    );
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe(
      "application/json",
    );
  });

  it("does not set a body/Content-Type when none is given (e.g. a plain GET)", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await apiFetch("/auth/me");

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.body).toBeUndefined();
    expect(init.headers).toBeUndefined();
  });
});

describe("apiJSON", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("decodes a successful JSON response", async () => {
    mockFetchOnce(
      new Response(JSON.stringify({ id: 1, username: "alice" }), {
        status: 200,
      }),
    );

    const result = await apiJSON<{ id: number; username: string }>("/auth/me");
    expect(result).toEqual({ id: 1, username: "alice" });
  });

  it("returns undefined for a 204 No Content, without attempting to parse a body", async () => {
    mockFetchOnce(new Response(null, { status: 204 }));
    const result = await apiJSON("/devices/1");
    expect(result).toBeUndefined();
  });

  it("throws ApiError with the backend's own error message on a non-2xx response", async () => {
    mockFetchOnce(
      new Response(JSON.stringify({ error: "invalid username or password" }), {
        status: 401,
      }),
    );

    await expect(
      apiJSON("/auth/login", { method: "POST", body: {} }),
    ).rejects.toMatchObject({
      name: "ApiError",
      status: 401,
      message: "invalid username or password",
    });
  });

  it("is a real instanceof ApiError, not just an object shaped like one", async () => {
    mockFetchOnce(
      new Response(JSON.stringify({ error: "not found" }), { status: 404 }),
    );

    try {
      await apiJSON("/pages/999999");
      expect.fail("expected apiJSON to throw");
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError);
    }
  });

  it("falls back to statusText when the error body isn't valid JSON", async () => {
    mockFetchOnce(
      new Response("<html>not json</html>", {
        status: 502,
        statusText: "Bad Gateway",
      }),
    );

    await expect(apiJSON("/pages")).rejects.toMatchObject({
      status: 502,
      message: "Bad Gateway",
    });
  });
});

describe("uploadManualCapture", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("posts multipart/form-data with url/html/favicon fields, no explicit Content-Type", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(
      new Response(JSON.stringify({ page_id: 1, capture_id: 2 }), {
        status: 201,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const htmlFile = new File(["<html></html>"], "capture.html", {
      type: "text/html",
    });
    const faviconFile = new File(["<svg></svg>"], "favicon.svg", {
      type: "image/svg+xml",
    });

    await uploadManualCapture(
      "https://example.com/article",
      htmlFile,
      faviconFile,
    );

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [calledURL, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(calledURL).toBe("/api/manual-upload");
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("include");
    // No Content-Type set by hand because the browser fills in the multipart
    // boundary itself.
    expect(init.headers).toBeUndefined();

    const body = init.body as FormData;
    expect(body.get("url")).toBe("https://example.com/article");
    expect(body.get("html")).toBe(htmlFile);
    expect(body.get("favicon")).toBe(faviconFile);
  });

  it("omits the favicon field entirely when none is given", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(
      new Response(JSON.stringify({ page_id: 1, capture_id: 2 }), {
        status: 201,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const htmlFile = new File(["<html></html>"], "capture.html", {
      type: "text/html",
    });

    await uploadManualCapture("https://example.com/article", htmlFile, null);

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const body = init.body as FormData;
    expect(body.get("favicon")).toBeNull();
  });

  it("decodes a successful response", async () => {
    mockFetchOnce(
      new Response(JSON.stringify({ page_id: 5, capture_id: 9 }), {
        status: 201,
      }),
    );

    const htmlFile = new File(["<html></html>"], "capture.html", {
      type: "text/html",
    });
    const result = await uploadManualCapture<{
      page_id: number;
      capture_id: number;
    }>("https://example.com/article", htmlFile, null);

    expect(result).toEqual({ page_id: 5, capture_id: 9 });
  });

  it("throws ApiError with the backend's own error message on a non-2xx response", async () => {
    mockFetchOnce(
      new Response(JSON.stringify({ error: "url is required" }), {
        status: 400,
      }),
    );

    const htmlFile = new File(["<html></html>"], "capture.html", {
      type: "text/html",
    });

    await expect(uploadManualCapture("", htmlFile, null)).rejects.toMatchObject(
      {
        name: "ApiError",
        status: 400,
        message: "url is required",
      },
    );
  });

  it("falls back to statusText when the error body isn't valid JSON", async () => {
    mockFetchOnce(
      new Response("<html>not json</html>", {
        status: 413,
        statusText: "Request Entity Too Large",
      }),
    );

    const htmlFile = new File(["<html></html>"], "capture.html", {
      type: "text/html",
    });

    await expect(
      uploadManualCapture("https://example.com", htmlFile, null),
    ).rejects.toMatchObject({
      status: 413,
      message: "Request Entity Too Large",
    });
  });
});
