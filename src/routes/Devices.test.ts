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

// Same AppHeader/apiJSON mocking approach as Settings.test.ts and
// CaptureReader.test.ts. Two things new to this screen: it calls
// window.confirm() before every destructive action (regenerate/revoke
// token, revoke device) and navigator.clipboard.writeText() to copy the
// token -- jsdom implements neither by default, so both are stubbed.
// apiJSON is mocked with a single path/method-dispatching implementation
// (rather than a mockResolvedValueOnce chain) since loadPairingToken()
// and loadDevices() fire in parallel from one $effect, so there's no
// guaranteed call order between the two to chain against.
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/svelte";

vi.mock("svelte-spa-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("svelte-spa-router")>();
  return { ...actual, push: vi.fn() };
});

vi.stubGlobal(
  "fetch",
  vi.fn().mockResolvedValue(new Response("{}", { status: 200 })),
);

vi.mock("../lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/api")>();
  return { ...actual, apiJSON: vi.fn() };
});

import { apiJSON, ApiError } from "../lib/api";
import type { Device, PairingTokenResponse } from "../lib/types";
import Devices from "./Devices.svelte";

const apiJSONMock = vi.mocked(apiJSON);
const confirmMock = vi.fn();
const writeTextMock = vi.fn().mockResolvedValue(undefined);

vi.stubGlobal("confirm", confirmMock);
Object.defineProperty(navigator, "clipboard", {
  value: { writeText: writeTextMock },
  configurable: true,
});

afterEach(() => {
  cleanup();
  apiJSONMock.mockReset();
  confirmMock.mockReset();
  writeTextMock.mockClear();
});

const aliceDevice: Device = {
  id: 1,
  device_name: "Alice's phone",
  device_type: "extension",
  created_at: "2026-05-01T12:00:00Z",
  last_used_at: "2026-06-01T09:30:00Z",
};

type LoadOptions = {
  pairingToken?: PairingTokenResponse | null;
  pairingTokenError?: unknown;
  devices?: Device[];
  devicesError?: unknown;
};

// Only handles the two GET-on-mount calls -- action tests layer their own
// mockImplementationOnce for the specific write endpoint they exercise on
// top of this (mockImplementationOnce takes priority over the base
// mockImplementation below it).
function mockLoad({
  pairingToken = { pairing_token: "the-pairing-token" },
  pairingTokenError,
  devices = [],
  devicesError,
}: LoadOptions = {}) {
  apiJSONMock.mockImplementation((path: string) => {
    if (path === "/pairing-token") {
      if (pairingTokenError) return Promise.reject(pairingTokenError);
      if (pairingToken === null) {
        return Promise.reject(new ApiError(404, "no token"));
      }
      return Promise.resolve(pairingToken);
    }
    if (path === "/devices") {
      if (devicesError) return Promise.reject(devicesError);
      return Promise.resolve({ devices });
    }
    throw new Error(`unexpected apiJSON call: ${path}`);
  });
}

describe("Devices", () => {
  it("shows loading states, then the pairing token and paired devices", async () => {
    mockLoad({ devices: [aliceDevice] });
    render(Devices);

    expect(screen.getAllByText("Loading…").length).toBeGreaterThan(0);

    expect(await screen.findByText("the-pairing-token")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Regenerate" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();

    expect(screen.getByText("Alice's phone")).toBeTruthy();
    expect(screen.getByText(/extension ·/)).toBeTruthy();
  });

  it("shows a Generate button and no token when there isn't one yet", async () => {
    mockLoad({ pairingToken: null });
    render(Devices);

    expect(await screen.findByText("No pairing token yet.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Generate" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Revoke" })).toBeNull();
  });

  it("shows the API's own error message when loading the token fails", async () => {
    mockLoad({
      pairingTokenError: new ApiError(500, "token store unavailable"),
    });
    render(Devices);

    expect(await screen.findByText("token store unavailable")).toBeTruthy();
  });

  it("falls back to a generic error for a non-ApiError devices load failure", async () => {
    mockLoad({ devicesError: new Error("network error") });
    render(Devices);

    expect(await screen.findByText("failed to load devices")).toBeTruthy();
  });

  it("shows a placeholder when no devices are paired", async () => {
    mockLoad({ devices: [] });
    render(Devices);

    expect(await screen.findByText("No devices paired yet.")).toBeTruthy();
  });

  it("copies the pairing token to the clipboard", async () => {
    mockLoad();
    render(Devices);

    const copyButton = await screen.findByRole("button", { name: "Copy" });
    await fireEvent.click(copyButton);

    expect(writeTextMock).toHaveBeenCalledWith("the-pairing-token");
    expect(await screen.findByRole("button", { name: "Copied!" })).toBeTruthy();
  });

  it("regenerates the pairing token after confirming", async () => {
    mockLoad();
    confirmMock.mockReturnValue(true);
    render(Devices);

    apiJSONMock.mockResolvedValueOnce({ pairing_token: "new-token" });
    await fireEvent.click(
      await screen.findByRole("button", { name: "Regenerate" }),
    );

    expect(confirmMock).toHaveBeenCalled();
    expect(apiJSONMock).toHaveBeenCalledWith("/pairing-token/regenerate", {
      method: "POST",
    });
    expect(await screen.findByText("new-token")).toBeTruthy();
  });

  it("doesn't regenerate the pairing token when the confirmation is declined", async () => {
    mockLoad();
    confirmMock.mockReturnValue(false);
    render(Devices);

    const before = apiJSONMock.mock.calls.length;
    await fireEvent.click(
      await screen.findByRole("button", { name: "Regenerate" }),
    );

    expect(confirmMock).toHaveBeenCalled();
    expect(apiJSONMock.mock.calls.length).toBe(before);
    expect(screen.getByText("the-pairing-token")).toBeTruthy();
  });

  it("revokes the pairing token after confirming", async () => {
    mockLoad();
    confirmMock.mockReturnValue(true);
    render(Devices);

    apiJSONMock.mockResolvedValueOnce(undefined);
    await fireEvent.click(
      await screen.findByRole("button", { name: "Revoke" }),
    );

    expect(apiJSONMock).toHaveBeenCalledWith("/pairing-token", {
      method: "DELETE",
    });
    expect(await screen.findByText("No pairing token yet.")).toBeTruthy();
  });

  it("revokes a device after confirming, removing it from the list", async () => {
    mockLoad({ devices: [aliceDevice] });
    confirmMock.mockReturnValue(true);
    render(Devices);

    apiJSONMock.mockResolvedValueOnce(undefined);
    const revokeButtons = await screen.findAllByRole("button", {
      name: "Revoke",
    });
    // Two "Revoke" buttons exist once a token and a device both render --
    // the token's own is first in document order, the device row's second.
    await fireEvent.click(revokeButtons[1]);

    expect(confirmMock).toHaveBeenCalledWith(
      'Revoke "Alice\'s phone"? It will need to be paired again to archive pages.',
    );
    expect(apiJSONMock).toHaveBeenCalledWith("/devices/1", {
      method: "DELETE",
    });
    expect(screen.queryByText("Alice's phone")).toBeNull();
    expect(await screen.findByText("No devices paired yet.")).toBeTruthy();
  });

  it("doesn't revoke a device when the confirmation is declined", async () => {
    mockLoad({ devices: [aliceDevice] });
    confirmMock.mockReturnValue(false);
    render(Devices);

    const before = apiJSONMock.mock.calls.length;
    const revokeButtons = await screen.findAllByRole("button", {
      name: "Revoke",
    });
    await fireEvent.click(revokeButtons[1]);

    expect(apiJSONMock.mock.calls.length).toBe(before);
    expect(screen.getByText("Alice's phone")).toBeTruthy();
  });

  it("shows the API's own error message when an action fails", async () => {
    mockLoad();
    confirmMock.mockReturnValue(true);
    render(Devices);

    apiJSONMock.mockRejectedValueOnce(
      new ApiError(500, "regeneration failed server-side"),
    );
    await fireEvent.click(
      await screen.findByRole("button", { name: "Regenerate" }),
    );

    expect(
      await screen.findByText("regeneration failed server-side"),
    ).toBeTruthy();
  });
});
