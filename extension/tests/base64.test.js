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

import { describe, expect, it } from "vitest";
import { bytesToBase64 } from "../src/common/base64.js";

describe("bytesToBase64", () => {
  it("encodes bytes as standard base64", () => {
    expect(bytesToBase64(new TextEncoder().encode("hello"))).toBe("aGVsbG8=");
  });

  it("is empty for no bytes", () => {
    expect(bytesToBase64(new Uint8Array())).toBe("");
  });

  it("round-trips arbitrary binary, including every byte value", () => {
    const bytes = Uint8Array.from({ length: 256 }, (_, i) => i);
    const decoded = atob(bytesToBase64(bytes));
    expect(Array.from(decoded, (c) => c.charCodeAt(0))).toEqual(
      Array.from(bytes),
    );
  });

  it("handles input larger than one chunk, past what String.fromCharCode(...) can take at once", () => {
    const bytes = new Uint8Array(300_000).fill(0xab);
    const encoded = bytesToBase64(bytes);
    expect(atob(encoded)).toHaveLength(300_000);
    expect(encoded.length).toBe(Math.ceil(300_000 / 3) * 4);
  });

  it("matches the +/ alphabet and = padding the Worker's decoder expects", () => {
    expect(bytesToBase64(Uint8Array.of(0xfb, 0xff))).toBe("+/8=");
  });
});
