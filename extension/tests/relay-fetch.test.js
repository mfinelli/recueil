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

import { beforeEach, describe, expect, it, vi } from "vitest";

const sendMessageMock = vi.fn();
vi.mock("webextension-polyfill", () => ({
  default: { runtime: { sendMessage: (...args) => sendMessageMock(...args) } },
}));

// Imported after the mock is registered, matching how vi.mock hoisting
// requires this to be set up before the module under test is loaded.
const { relayFetch } = await import("../src/capture-inject/relay-fetch.js");

beforeEach(() => {
  sendMessageMock.mockReset();
});

describe("relayFetch -- header normalization", () => {
  it("passes undefined through untouched when no headers are given", async () => {
    sendMessageMock.mockResolvedValue({ ok: true, status: 200, headers: {} });
    await relayFetch("https://example.com/x");
    expect(sendMessageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        init: expect.objectContaining({ headers: undefined }),
      }),
    );
  });

  it("normalizes a plain object unchanged", async () => {
    sendMessageMock.mockResolvedValue({ ok: true, status: 200, headers: {} });
    await relayFetch("https://example.com/x", {
      headers: { "X-Test": "value" },
    });
    expect(sendMessageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        init: expect.objectContaining({ headers: { "X-Test": "value" } }),
      }),
    );
  });

  it("normalizes a real Headers instance into a plain object", async () => {
    sendMessageMock.mockResolvedValue({ ok: true, status: 200, headers: {} });
    await relayFetch("https://example.com/x", {
      headers: new Headers({ "X-Test": "value" }),
    });
    const call = sendMessageMock.mock.calls[0][0];
    // Headers lowercases names on construction -- confirmed here rather
    // than assumed, since it's what makes the receiving side's
    // case-insensitive lookup actually correct.
    expect(call.init.headers).toEqual({ "x-test": "value" });
  });

  it("normalizes an array of [name, value] tuples into a plain object", async () => {
    sendMessageMock.mockResolvedValue({ ok: true, status: 200, headers: {} });
    await relayFetch("https://example.com/x", {
      headers: [
        ["X-Test", "value"],
        ["X-Other", "value2"],
      ],
    });
    const call = sendMessageMock.mock.calls[0][0];
    expect(call.init.headers).toEqual({
      "X-Test": "value",
      "X-Other": "value2",
    });
  });
});

describe("relayFetch -- response shaping", () => {
  it("returns a fetch-like object on success", async () => {
    const body = new TextEncoder().encode("hello").buffer;
    sendMessageMock.mockResolvedValue({
      ok: true,
      status: 200,
      statusText: "OK",
      url: "https://example.com/final",
      headers: { "content-type": "text/plain" },
      body,
    });

    const response = await relayFetch("https://example.com/x");

    expect(response.status).toBe(200);
    expect(response.statusText).toBe("OK");
    expect(response.url).toBe("https://example.com/final");
    expect(await response.arrayBuffer()).toBe(body);
  });

  it("headers.get() does a case-insensitive lookup", async () => {
    sendMessageMock.mockResolvedValue({
      ok: true,
      status: 200,
      headers: { "content-type": "image/png" },
    });

    const response = await relayFetch("https://example.com/x");

    expect(response.headers.get("Content-Type")).toBe("image/png");
    expect(response.headers.get("content-type")).toBe("image/png");
    expect(response.headers.get("missing-header")).toBeNull();
  });

  it("throws, including the url and the relayed error, when the relay reports failure", async () => {
    sendMessageMock.mockResolvedValue({ ok: false, error: "boom" });

    await expect(relayFetch("https://example.com/x")).rejects.toThrow(
      /https:\/\/example\.com\/x.*boom/,
    );
  });

  it("throws when the background sends back nothing at all", async () => {
    sendMessageMock.mockResolvedValue(undefined);
    await expect(relayFetch("https://example.com/x")).rejects.toThrow();
  });
});
