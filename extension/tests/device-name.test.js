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

import { afterEach, describe, expect, it, vi } from "vitest";
import { defaultDeviceName } from "../src/common/device-name.js";

// Real UA strings (trimmed of version noise that doesn't matter here),
// one per browser/OS combination defaultDeviceName actually branches on.
const UA_FIREFOX_LINUX =
  "Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0";
const UA_FIREFOX_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0";
const UA_FIREFOX_MAC =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 14.6; rv:132.0) Gecko/20100101 Firefox/132.0";
const UA_CHROME_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
const UA_CHROME_LINUX =
  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
const UA_EDGE_WINDOWS =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0";
const UA_SAFARI_MAC =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_6_1) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15";
const UA_CHROME_ANDROID =
  "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36";
const UA_SAFARI_IOS =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Mobile/15E148 Safari/604.1";

function withUserAgent(ua) {
  vi.stubGlobal("navigator", { userAgent: ua });
}

describe("defaultDeviceName", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it.each([
    ["Firefox on Linux", UA_FIREFOX_LINUX, "Firefox on Linux"],
    ["Firefox on Windows", UA_FIREFOX_WINDOWS, "Firefox on Windows"],
    ["Firefox on macOS", UA_FIREFOX_MAC, "Firefox on macOS"],
    ["Chrome on Windows", UA_CHROME_WINDOWS, "Chrome on Windows"],
    ["Chrome on Linux", UA_CHROME_LINUX, "Chrome on Linux"],
    ["Safari on macOS", UA_SAFARI_MAC, "Safari on macOS"],
    ["Chrome on Android", UA_CHROME_ANDROID, "Chrome on Android"],
    ["Safari on iOS", UA_SAFARI_IOS, "Safari on iOS"],
  ])("%s", (_name, ua, expected) => {
    withUserAgent(ua);
    expect(defaultDeviceName()).toBe(expected);
  });

  it("prefers Edge over Chrome when both tokens are present in the UA", () => {
    // Edge's own UA still contains "Chrome/" (Chromium-based) --
    // isSVGCandidate-style token order matters here the same way it did
    // for favicon type sniffing, this is exactly the kind of thing that's
    // easy to get backwards.
    withUserAgent(UA_EDGE_WINDOWS);
    expect(defaultDeviceName()).toBe("Edge on Windows");
  });

  it("falls back to generic labels for an unrecognized UA", () => {
    withUserAgent("SomeObscureBrowser/1.0 (SomeObscureOS)");
    expect(defaultDeviceName()).toBe("Browser on Unknown OS");
  });
});
