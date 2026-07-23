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

import { describe, expect, it, vi } from "vitest";

// A minimal fake of browser.i18n.getMessage -- just enough to prove t()'s
// own contract (delegates key + substitutions through, throws loudly on a
// missing key) without needing a real browser or a real _locales file
// loaded, which jsdom has no notion of at all.
let messages;
vi.mock("webextension-polyfill", () => ({
  default: {
    i18n: {
      getMessage(key, substitutions) {
        const template = messages[key];
        if (!template) return "";
        if (!substitutions) return template;
        const subs = Array.isArray(substitutions)
          ? substitutions
          : [substitutions];
        return template.replace(/\$(\d)/g, (_, n) => subs[Number(n) - 1]);
      },
    },
  },
}));

const { t } = await import("../src/common/i18n.js");

describe("t", () => {
  it("returns the looked-up message", () => {
    messages = { greeting: "hello" };
    expect(t("greeting")).toBe("hello");
  });

  it("passes substitutions through to getMessage", () => {
    messages = { greeting: "hello $1" };
    expect(t("greeting", "world")).toBe("hello world");
  });

  it("throws on a missing key rather than returning an empty string", () => {
    messages = {};
    expect(() => t("nonexistent")).toThrow(/nonexistent/);
  });

  it("throws on a key whose message is an empty string", () => {
    messages = { blank: "" };
    expect(() => t("blank")).toThrow(/blank/);
  });
});
