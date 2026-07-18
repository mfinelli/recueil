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
import { sha256Hex } from "../src/common/hash.js";

describe("sha256Hex", () => {
  it("matches the well-known SHA-256 digest of the empty string", async () => {
    const digest = await sha256Hex(new TextEncoder().encode(""));
    expect(digest).toBe(
      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    );
  });

  it('matches the well-known NIST test vector for "abc"', async () => {
    const digest = await sha256Hex(new TextEncoder().encode("abc"));
    expect(digest).toBe(
      "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
    );
  });

  it("accepts a raw ArrayBuffer, not just a Uint8Array", async () => {
    const buffer = new TextEncoder().encode("abc").buffer;
    const digest = await sha256Hex(buffer);
    expect(digest).toBe(
      "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
    );
  });

  it("is deterministic -- the same input always produces the same digest", async () => {
    const data = new TextEncoder().encode("recueil");
    const first = await sha256Hex(data);
    const second = await sha256Hex(data);
    expect(first).toBe(second);
  });

  it("produces different digests for different input", async () => {
    const a = await sha256Hex(new TextEncoder().encode("recueil"));
    const b = await sha256Hex(new TextEncoder().encode("Recueil"));
    expect(a).not.toBe(b);
  });

  it("always returns a 64-character lowercase hex string", async () => {
    const digest = await sha256Hex(new TextEncoder().encode("anything"));
    expect(digest).toMatch(/^[0-9a-f]{64}$/);
  });
});
