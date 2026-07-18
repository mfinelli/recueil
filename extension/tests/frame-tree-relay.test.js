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

const addListenerMock = vi.fn();
const tabsSendMessageMock = vi.fn();
vi.mock("webextension-polyfill", () => ({
  default: {
    runtime: {
      onMessage: { addListener: (...args) => addListenerMock(...args) },
    },
    tabs: { sendMessage: (...args) => tabsSendMessageMock(...args) },
  },
}));

// Imported after the mock is registered, matching how vi.mock hoisting
// requires this to be set up before the module under test is loaded.
const { registerFrameTreeRelay } =
  await import("../src/background/frame-tree-relay.js");

const INIT_RESPONSE = "singlefile.frameTree.initResponse";
const ACK_INIT_REQUEST = "singlefile.frameTree.ackInitRequest";

// The relay registers a single runtime.onMessage listener; every test
// exercises that listener, so pull it back out of the mock once here.
/** @returns {(message: any, sender: any) => unknown} */
function getListener() {
  registerFrameTreeRelay();
  return addListenerMock.mock.calls.at(-1)[0];
}

beforeEach(() => {
  addListenerMock.mockReset();
  tabsSendMessageMock.mockReset();
  tabsSendMessageMock.mockResolvedValue({});
});

describe("registerFrameTreeRelay", () => {
  it("registers exactly one runtime.onMessage listener", () => {
    registerFrameTreeRelay();
    expect(addListenerMock).toHaveBeenCalledTimes(1);
    expect(addListenerMock.mock.calls[0][0]).toBeTypeOf("function");
  });

  it("forwards an initResponse to the top frame of the sender's tab", () => {
    const listener = getListener();
    const message = { method: INIT_RESPONSE, frames: [], sessionId: "s1" };

    const result = listener(message, { tab: { id: 42 } });

    expect(tabsSendMessageMock).toHaveBeenCalledWith(42, message, {
      frameId: 0,
    });
    // Resolving (not undefined) is what keeps the sender's
    // runtime.sendMessage from rejecting with "Receiving end does not
    // exist." -- the whole reason this relay exists.
    expect(result).toBeInstanceOf(Promise);
  });

  it("forwards an ackInitRequest the same way", () => {
    const listener = getListener();
    const message = {
      method: ACK_INIT_REQUEST,
      windowId: "0.1",
      sessionId: "s1",
    };

    listener(message, { tab: { id: 7 } });

    expect(tabsSendMessageMock).toHaveBeenCalledWith(7, message, {
      frameId: 0,
    });
  });

  it("resolves to an object so the sender's sendMessage settles", async () => {
    const listener = getListener();
    const result = await listener(
      { method: INIT_RESPONSE, sessionId: "s1" },
      { tab: { id: 1 } },
    );
    expect(result).toEqual({});
  });

  it("ignores messages that aren't frame-tree messages, leaving them for other listeners", () => {
    const listener = getListener();

    // recueil's own relay-fetch message rides on .type, not .method, so it
    // must fall through to undefined here rather than being swallowed.
    expect(listener({ type: "recueil:relay-fetch" }, { tab: { id: 1 } })).toBe(
      undefined,
    );
    expect(
      listener({ method: "singlefile.something.else" }, { tab: { id: 1 } }),
    ).toBe(undefined);
    expect(listener(undefined, { tab: { id: 1 } })).toBe(undefined);
    expect(tabsSendMessageMock).not.toHaveBeenCalled();
  });

  it("does not throw or forward when the sender has no tab", () => {
    const listener = getListener();

    // Still resolve rather than reject -- a frame-tree message with no
    // routable tab can't be forwarded, but leaving the sender hanging is
    // worse than dropping it.
    const result = listener({ method: INIT_RESPONSE, sessionId: "s1" }, {});

    expect(tabsSendMessageMock).not.toHaveBeenCalled();
    expect(result).toBeInstanceOf(Promise);
  });

  it("swallows a rejected tabs.sendMessage so it doesn't surface as an unhandled rejection", async () => {
    tabsSendMessageMock.mockRejectedValue(
      new Error(
        "Could not establish connection. Receiving end does not exist.",
      ),
    );
    const listener = getListener();

    // The listener's own resolution must not depend on the forward
    // succeeding; awaiting it must not reject.
    await expect(
      listener({ method: INIT_RESPONSE, sessionId: "s1" }, { tab: { id: 3 } }),
    ).resolves.toEqual({});
  });
});
