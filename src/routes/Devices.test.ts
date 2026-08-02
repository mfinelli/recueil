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
import type {
  ApiToken,
  Device,
  PairingTokenResponse,
  Session,
} from "../lib/types";
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

const currentSession: Session = {
  id: 10,
  browser: "Chrome",
  browser_version: "118",
  os: "Windows",
  device_class: "desktop",
  created_at: "2026-05-01T12:00:00Z",
  last_seen_at: "2026-06-01T09:30:00Z",
  is_current: true,
};

const otherSession: Session = {
  id: 11,
  browser: "Safari",
  browser_version: "17",
  os: "iOS",
  device_class: "mobile",
  created_at: "2026-04-01T12:00:00Z",
  last_seen_at: "2026-05-20T09:30:00Z",
  is_current: false,
};

const claudeDesktopToken: ApiToken = {
  id: 5,
  name: "Claude Desktop",
  created_at: "2026-06-01T12:00:00Z",
  last_used_at: "2026-07-19T08:30:15Z",
};

type LoadOptions = {
  pairingToken?: PairingTokenResponse | null;
  pairingTokenError?: unknown;
  devices?: Device[];
  devicesError?: unknown;
  apiTokens?: ApiToken[];
  apiTokensError?: unknown;
  sessions?: Session[];
  sessionsError?: unknown;
};

// Only handles the four GET-on-mount calls -- action tests layer their own
// mockImplementationOnce for the specific write endpoint they exercise on
// top of this (mockImplementationOnce takes priority over the base
// mockImplementation below it).
function mockLoad({
  pairingToken = { pairing_token: "the-pairing-token" },
  pairingTokenError,
  devices = [],
  devicesError,
  apiTokens = [],
  apiTokensError,
  sessions = [],
  sessionsError,
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
    if (path === "/tokens") {
      if (apiTokensError) return Promise.reject(apiTokensError);
      return Promise.resolve({ tokens: apiTokens });
    }
    if (path === "/sessions") {
      if (sessionsError) return Promise.reject(sessionsError);
      return Promise.resolve({ sessions });
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
    expect(
      screen.getByRole("button", { name: "Copy pairing token" }),
    ).toBeTruthy();

    expect(screen.getByText("Alice's phone")).toBeTruthy();
    // Device type is conveyed by the icon (role="img", aria-label) next
    // to the name now, not a text prefix on the meta line -- see the
    // dedicated device-type-icon tests below.
    expect(screen.getByRole("img", { name: "Browser extension" })).toBeTruthy();
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

    const copyButton = await screen.findByRole("button", {
      name: "Copy pairing token",
    });
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
    await fireEvent.click(
      await screen.findByRole("button", { name: "Revoke Alice's phone" }),
    );

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
    await fireEvent.click(
      await screen.findByRole("button", { name: "Revoke Alice's phone" }),
    );

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

  describe("device-type icon", () => {
    it.each([
      ["extension", "Browser extension"],
      ["cli", "Command-line interface"],
      ["shortcut", "iOS Shortcut"],
      ["pwa", "Progressive web app"],
    ] as const)(
      "labels a %s device as %s, exposed to screen readers via role=img",
      async (type, label) => {
        mockLoad({ devices: [{ ...aliceDevice, device_type: type }] });
        render(Devices);

        expect(await screen.findByRole("img", { name: label })).toBeTruthy();
      },
    );
  });

  describe("sessions", () => {
    it("shows a session's browser/OS and signed-in/active dates", async () => {
      mockLoad({ sessions: [otherSession] });
      render(Devices);

      expect(await screen.findByText("Safari · iOS")).toBeTruthy();
      expect(screen.getByText(/signed in.*active/)).toBeTruthy();
    });

    it("highlights the current session with a badge and no revoke control", async () => {
      mockLoad({ sessions: [currentSession, otherSession] });
      render(Devices);

      await screen.findByText("Chrome · Windows");
      expect(screen.getByText("Current session")).toBeTruthy();
      expect(
        screen.queryByRole("button", { name: "Sign out Chrome · Windows" }),
      ).toBeNull();
      expect(
        screen.getByRole("button", { name: "Sign out Safari · iOS" }),
      ).toBeTruthy();
    });

    it("revokes a non-current session after confirming, removing it from the list", async () => {
      mockLoad({ sessions: [currentSession, otherSession] });
      confirmMock.mockReturnValue(true);
      render(Devices);

      const revokeButton = await screen.findByRole("button", {
        name: "Sign out Safari · iOS",
      });
      apiJSONMock.mockResolvedValueOnce(undefined);
      await fireEvent.click(revokeButton);

      expect(confirmMock).toHaveBeenCalled();
      expect(apiJSONMock).toHaveBeenCalledWith("/sessions/11", {
        method: "DELETE",
      });
      expect(screen.queryByText("Safari · iOS")).toBeNull();
      expect(screen.getByText("Chrome · Windows")).toBeTruthy();
    });

    it("doesn't revoke a session when the confirmation is declined", async () => {
      mockLoad({ sessions: [otherSession] });
      confirmMock.mockReturnValue(false);
      render(Devices);

      const revokeButton = await screen.findByRole("button", {
        name: "Sign out Safari · iOS",
      });
      await fireEvent.click(revokeButton);

      expect(apiJSONMock).not.toHaveBeenCalledWith(
        "/sessions/11",
        expect.objectContaining({ method: "DELETE" }),
      );
      expect(screen.getByText("Safari · iOS")).toBeTruthy();
    });

    it("shows the API's own error message when loading sessions fails", async () => {
      mockLoad({ sessionsError: new ApiError(500, "sessions unavailable") });
      render(Devices);

      expect(await screen.findByText("sessions unavailable")).toBeTruthy();
    });

    it("falls back to a generic error for a non-ApiError sessions load failure", async () => {
      mockLoad({ sessionsError: new Error("network error") });
      render(Devices);

      expect(await screen.findByText("failed to load sessions")).toBeTruthy();
    });

    it("shows the API's own error message when revoking a session fails", async () => {
      mockLoad({ sessions: [otherSession] });
      confirmMock.mockReturnValue(true);
      render(Devices);

      const revokeButton = await screen.findByRole("button", {
        name: "Sign out Safari · iOS",
      });
      apiJSONMock.mockRejectedValueOnce(new ApiError(400, "cannot revoke"));
      await fireEvent.click(revokeButton);

      expect(await screen.findByText("cannot revoke")).toBeTruthy();
      // The session must still be listed -- the revoke failed.
      expect(screen.getByText("Safari · iOS")).toBeTruthy();
    });

    describe("device-class icon", () => {
      it.each(["desktop", "mobile", "tablet"] as const)(
        "renders a %s session's icon as an accessible role=img labeled with its browser/OS",
        async (deviceClass) => {
          mockLoad({
            sessions: [{ ...otherSession, device_class: deviceClass }],
          });
          render(Devices);

          expect(
            await screen.findByRole("img", { name: "Safari · iOS" }),
          ).toBeTruthy();
        },
      );

      it("falls back to a generic icon, not Devices' own Smartphone default, for an unrecognized device_class", async () => {
        // "" is exactly what the backend sends for a session whose
        // user_agent go-useragent couldn't parse at all -- this is that
        // case reaching the dashboard, not a test-only value.
        mockLoad({
          sessions: [
            { ...otherSession, browser: "", os: "", device_class: "" },
          ],
        });
        render(Devices);

        expect(
          await screen.findByRole("img", { name: "Unknown device" }),
        ).toBeTruthy();
      });
    });
  });

  describe("api tokens", () => {
    it("shows a token's name and created/last-used dates", async () => {
      mockLoad({ apiTokens: [claudeDesktopToken] });
      render(Devices);

      expect(await screen.findByText("Claude Desktop")).toBeTruthy();
      expect(screen.getByText(/created.*last used/)).toBeTruthy();
    });

    it("shows a placeholder when no api tokens exist", async () => {
      mockLoad({ apiTokens: [] });
      render(Devices);

      expect(await screen.findByText("No API tokens yet.")).toBeTruthy();
    });

    it("shows the API's own error message when loading tokens fails", async () => {
      mockLoad({ apiTokensError: new ApiError(500, "tokens unavailable") });
      render(Devices);

      expect(await screen.findByText("tokens unavailable")).toBeTruthy();
    });

    it("falls back to a generic error for a non-ApiError tokens load failure", async () => {
      mockLoad({ apiTokensError: new Error("network error") });
      render(Devices);

      expect(await screen.findByText("failed to load API tokens")).toBeTruthy();
    });

    it("creates a token, reveals it once, and adds it to the list", async () => {
      mockLoad({ apiTokens: [] });
      render(Devices);

      const nameInput = await screen.findByPlaceholderText(
        'Name this token, e.g. "Claude Desktop"',
      );
      await fireEvent.input(nameInput, { target: { value: "My Script" } });

      apiJSONMock.mockResolvedValueOnce({
        id: 9,
        name: "My Script",
        token: "rcl_api_abc123",
        created_at: "2026-08-01T00:00:00Z",
      });
      await fireEvent.click(
        screen.getByRole("button", { name: "Create token" }),
      );

      expect(apiJSONMock).toHaveBeenCalledWith("/tokens", {
        method: "POST",
        body: { name: "My Script" },
      });
      expect(await screen.findByText("rcl_api_abc123")).toBeTruthy();
      expect(screen.getByText("Token created")).toBeTruthy();
      // The new row is already in the list underneath the reveal, from the
      // same response, without a second /tokens fetch.
      expect(screen.getAllByText("My Script").length).toBeGreaterThan(0);
      // The input clears, ready for the next token.
      expect((nameInput as HTMLInputElement).value).toBe("");
    });

    it("rejects creating a token with a blank name, without calling the API", async () => {
      mockLoad({ apiTokens: [] });
      render(Devices);

      const before = apiJSONMock.mock.calls.length;
      await fireEvent.click(
        await screen.findByRole("button", { name: "Create token" }),
      );

      expect(apiJSONMock.mock.calls.length).toBe(before);
      expect(await screen.findByText("name is required")).toBeTruthy();
    });

    it("copies the revealed token to the clipboard", async () => {
      mockLoad({ apiTokens: [] });
      render(Devices);

      const nameInput = await screen.findByPlaceholderText(
        'Name this token, e.g. "Claude Desktop"',
      );
      await fireEvent.input(nameInput, { target: { value: "My Script" } });
      apiJSONMock.mockResolvedValueOnce({
        id: 9,
        name: "My Script",
        token: "rcl_api_abc123",
        created_at: "2026-08-01T00:00:00Z",
      });
      await fireEvent.click(
        screen.getByRole("button", { name: "Create token" }),
      );
      await screen.findByText("rcl_api_abc123");

      await fireEvent.click(screen.getByRole("button", { name: "Copy token" }));

      expect(writeTextMock).toHaveBeenCalledWith("rcl_api_abc123");
      expect(
        await screen.findByRole("button", { name: "Copied!" }),
      ).toBeTruthy();
    });

    it("dismisses the reveal callout without affecting the token list", async () => {
      mockLoad({ apiTokens: [] });
      render(Devices);

      const nameInput = await screen.findByPlaceholderText(
        'Name this token, e.g. "Claude Desktop"',
      );
      await fireEvent.input(nameInput, { target: { value: "My Script" } });
      apiJSONMock.mockResolvedValueOnce({
        id: 9,
        name: "My Script",
        token: "rcl_api_abc123",
        created_at: "2026-08-01T00:00:00Z",
      });
      await fireEvent.click(
        screen.getByRole("button", { name: "Create token" }),
      );
      await screen.findByText("rcl_api_abc123");

      await fireEvent.click(screen.getByRole("button", { name: "Done" }));

      expect(screen.queryByText("rcl_api_abc123")).toBeNull();
      expect(screen.getByText("My Script")).toBeTruthy();
    });

    it("revokes a token after confirming, removing it from the list", async () => {
      mockLoad({ apiTokens: [claudeDesktopToken] });
      confirmMock.mockReturnValue(true);
      render(Devices);

      apiJSONMock.mockResolvedValueOnce(undefined);
      await fireEvent.click(
        await screen.findByRole("button", { name: "Revoke Claude Desktop" }),
      );

      expect(confirmMock).toHaveBeenCalledWith(
        'Revoke "Claude Desktop"? Any program using this token will lose access immediately.',
      );
      expect(apiJSONMock).toHaveBeenCalledWith("/tokens/5", {
        method: "DELETE",
      });
      expect(await screen.findByText("No API tokens yet.")).toBeTruthy();
    });

    it("doesn't revoke a token when the confirmation is declined", async () => {
      mockLoad({ apiTokens: [claudeDesktopToken] });
      confirmMock.mockReturnValue(false);
      render(Devices);

      const before = apiJSONMock.mock.calls.length;
      await fireEvent.click(
        await screen.findByRole("button", { name: "Revoke Claude Desktop" }),
      );

      expect(apiJSONMock.mock.calls.length).toBe(before);
      expect(screen.getByText("Claude Desktop")).toBeTruthy();
    });

    it("shows the API's own error message when revoking a token fails", async () => {
      mockLoad({ apiTokens: [claudeDesktopToken] });
      confirmMock.mockReturnValue(true);
      render(Devices);

      apiJSONMock.mockRejectedValueOnce(
        new ApiError(500, "cannot revoke token"),
      );
      await fireEvent.click(
        await screen.findByRole("button", { name: "Revoke Claude Desktop" }),
      );

      expect(await screen.findByText("cannot revoke token")).toBeTruthy();
      expect(screen.getByText("Claude Desktop")).toBeTruthy();
    });
  });
});
