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

// Same in-memory fake as storage.test.js -- auth.js's getAuthState/unpair
// go through the real storage.js, so this is what backs it during these
// tests, not a mock of storage.js itself.
let store;
vi.mock("webextension-polyfill", () => ({
  default: {
    storage: {
      local: {
        async get(key) {
          return key in store ? { [key]: store[key] } : {};
        },
        async set(obj) {
          Object.assign(store, obj);
        },
        async remove(key) {
          delete store[key];
        },
      },
    },
  },
}));

const { pair, getAuthState, unpair } =
  await import("../src/background/auth.js");
const { getConfig } = await import("../src/common/storage.js");

let fetchMock;
beforeEach(() => {
  store = {};
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  vi.unstubAllGlobals();
});

function fakePairResponse(body, { status = 201 } = {}) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => JSON.stringify(body),
  };
}

describe("pair", () => {
  it("posts to /pair with the expected body and persists the returned config", async () => {
    fetchMock.mockResolvedValue(
      fakePairResponse({
        token: "rcl_live_new-token",
        device_id: 42,
        device_name: "My Laptop",
        device_type: "extension",
      }),
    );

    const config = await pair({
      workerBaseURL: "https://recueil.example.com",
      pairingToken: "rcl_pair_abc",
      deviceName: "My Laptop",
    });

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("https://recueil.example.com/pair");
    expect(JSON.parse(init.body)).toEqual({
      pairing_token: "rcl_pair_abc",
      device_name: "My Laptop",
      device_type: "extension",
    });

    expect(config).toEqual({
      workerBaseURL: "https://recueil.example.com",
      token: "rcl_live_new-token",
      deviceId: 42,
      deviceName: "My Laptop",
      deviceType: "extension",
    });
    // Actually persisted, not just returned.
    expect(await getConfig()).toEqual(config);
  });

  it("strips a trailing slash from workerBaseURL before storing or requesting", async () => {
    fetchMock.mockResolvedValue(
      fakePairResponse({
        token: "t",
        device_id: 1,
        device_name: "d",
        device_type: "extension",
      }),
    );

    const config = await pair({
      workerBaseURL: "https://recueil.example.com/",
      pairingToken: "x",
      deviceName: "d",
    });

    expect(config.workerBaseURL).toBe("https://recueil.example.com");
    expect(fetchMock.mock.calls[0][0]).toBe("https://recueil.example.com/pair");
  });

  it("throws with the response status/text when the Worker rejects pairing", async () => {
    fetchMock.mockResolvedValue(
      fakePairResponse({ error: "invalid pairing token" }, { status: 400 }),
    );

    await expect(
      pair({
        workerBaseURL: "https://recueil.example.com",
        pairingToken: "bad",
        deviceName: "d",
      }),
    ).rejects.toThrow(/400/);
  });

  it("wraps a raw fetch() failure with context and preserves it as .cause", async () => {
    const originalError = new TypeError(
      "NetworkError when attempting to fetch resource.",
    );
    fetchMock.mockRejectedValue(originalError);

    const error = await pair({
      workerBaseURL: "https://recueil.example.com",
      pairingToken: "x",
      deviceName: "d",
    }).catch((e) => e);

    expect(error.message).toContain("recueil.example.com");
    expect(error.cause).toBe(originalError);
  });
});

describe("getAuthState", () => {
  it("reports unpaired when nothing is stored", async () => {
    expect(await getAuthState()).toEqual({ paired: false });
  });

  it("reports paired with instance/device info once configured", async () => {
    fetchMock.mockResolvedValue(
      fakePairResponse({
        token: "rcl_live_secret",
        device_id: 1,
        device_name: "My Device",
        device_type: "extension",
      }),
    );
    await pair({
      workerBaseURL: "https://recueil.example.com",
      pairingToken: "x",
      deviceName: "My Device",
    });

    const state = await getAuthState();
    expect(state).toEqual({
      paired: true,
      workerBaseURL: "https://recueil.example.com",
      deviceName: "My Device",
    });
  });

  it("never includes the bearer token, even though it's in storage", async () => {
    fetchMock.mockResolvedValue(
      fakePairResponse({
        token: "rcl_live_should-never-leak",
        device_id: 1,
        device_name: "d",
        device_type: "extension",
      }),
    );
    await pair({
      workerBaseURL: "https://recueil.example.com",
      pairingToken: "x",
      deviceName: "d",
    });

    const state = await getAuthState();
    expect(state.token).toBeUndefined();
    expect(JSON.stringify(state)).not.toContain("rcl_live_should-never-leak");
  });
});

describe("unpair", () => {
  it("clears the stored config without making any network call", async () => {
    fetchMock.mockResolvedValue(
      fakePairResponse({
        token: "t",
        device_id: 1,
        device_name: "d",
        device_type: "extension",
      }),
    );
    await pair({
      workerBaseURL: "https://recueil.example.com",
      pairingToken: "x",
      deviceName: "d",
    });
    fetchMock.mockClear();

    await unpair();

    expect(await getAuthState()).toEqual({ paired: false });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("is a harmless no-op when nothing was ever paired", async () => {
    await expect(unpair()).resolves.toBeUndefined();
  });
});
