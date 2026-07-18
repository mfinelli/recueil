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

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  apiRequest,
  ApiAuthError,
  ApiError,
} from "../src/common/api-client.js";

const config = {
  workerBaseURL: "https://recueil.example.com",
  token: "rcl_live_test-token",
  deviceId: 1,
  deviceName: "test device",
  deviceType: "extension",
};

function fakeResponse({
  status = 200,
  statusText = "OK",
  headers = {},
  jsonBody,
  textBody = "",
} = {}) {
  const lowerHeaders = Object.fromEntries(
    Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v]),
  );
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText,
    headers: { get: (name) => lowerHeaders[name.toLowerCase()] ?? null },
    json: async () => jsonBody,
    text: async () => textBody,
  };
}

let fetchMock;
beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("apiRequest -- request construction", () => {
  it("builds the URL from workerBaseURL + path and defaults to GET", async () => {
    fetchMock.mockResolvedValue(fakeResponse({ status: 204 }));
    await apiRequest(config, "/queue");
    expect(fetchMock).toHaveBeenCalledWith(
      "https://recueil.example.com/queue",
      expect.objectContaining({ method: "GET" }),
    );
  });

  it("always includes the Authorization header", async () => {
    fetchMock.mockResolvedValue(fakeResponse({ status: 204 }));
    await apiRequest(config, "/queue");
    const [, init] = fetchMock.mock.calls[0];
    expect(init.headers.Authorization).toBe("Bearer rcl_live_test-token");
  });

  it("adds Content-Type and a JSON-stringified body only when a body is given", async () => {
    fetchMock.mockResolvedValue(fakeResponse({ status: 204 }));
    await apiRequest(config, "/captures/complete", {
      method: "POST",
      body: { capture_id: "abc" },
    });
    const [, init] = fetchMock.mock.calls[0];
    expect(init.headers["Content-Type"]).toBe("application/json");
    expect(init.body).toBe(JSON.stringify({ capture_id: "abc" }));
  });

  it("sends no Content-Type or body for a GET with no body", async () => {
    fetchMock.mockResolvedValue(fakeResponse({ status: 204 }));
    await apiRequest(config, "/queue");
    const [, init] = fetchMock.mock.calls[0];
    expect(init.headers["Content-Type"]).toBeUndefined();
    expect(init.body).toBeUndefined();
  });
});

describe("apiRequest -- response handling", () => {
  it("returns null for a 204", async () => {
    fetchMock.mockResolvedValue(fakeResponse({ status: 204 }));
    expect(await apiRequest(config, "/x")).toBeNull();
  });

  it("returns the parsed body for a JSON response", async () => {
    fetchMock.mockResolvedValue(
      fakeResponse({
        headers: { "content-type": "application/json" },
        jsonBody: { id: "abc" },
      }),
    );
    expect(await apiRequest(config, "/x")).toEqual({ id: "abc" });
  });

  it("returns null for a non-JSON, non-204 ok response", async () => {
    fetchMock.mockResolvedValue(
      fakeResponse({ headers: { "content-type": "text/plain" } }),
    );
    expect(await apiRequest(config, "/x")).toBeNull();
  });

  it("throws ApiAuthError specifically on 401, not a generic ApiError", async () => {
    fetchMock.mockResolvedValue(fakeResponse({ status: 401 }));
    await expect(apiRequest(config, "/x")).rejects.toBeInstanceOf(ApiAuthError);
  });

  it("throws ApiError with the real status for other non-ok responses", async () => {
    fetchMock.mockResolvedValue(
      fakeResponse({ status: 500, textBody: "internal error" }),
    );
    const error = await apiRequest(config, "/x").catch((e) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect(error.status).toBe(500);
    expect(error.message).toContain("internal error");
  });

  it("wraps a raw fetch() failure with method/path context and preserves it as .cause", async () => {
    const originalError = new TypeError(
      "NetworkError when attempting to fetch resource.",
    );
    fetchMock.mockRejectedValue(originalError);

    const error = await apiRequest(config, "/captures/complete", {
      method: "POST",
    }).catch((e) => e);

    expect(error.message).toContain("POST");
    expect(error.message).toContain("/captures/complete");
    expect(error.message).toContain("NetworkError");
    expect(error.cause).toBe(originalError);
  });
});
