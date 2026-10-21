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

import { describe, it, expect } from "vitest";
import { fileExtension, readFileAsBase64 } from "./files";

describe("readFileAsBase64", () => {
  it("returns the file's bytes as base64, without a data: prefix", async () => {
    const file = new File(["hello"], "hello.txt", { type: "text/plain" });
    expect(await readFileAsBase64(file)).toBe(btoa("hello"));
  });

  it("handles binary content", async () => {
    const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0, 255]);
    const file = new File([bytes], "icon.png", { type: "image/png" });
    const decoded = atob(await readFileAsBase64(file));
    expect(Array.from(decoded, (c) => c.charCodeAt(0))).toEqual(
      Array.from(bytes),
    );
  });

  it("is an empty string for an empty file", async () => {
    expect(await readFileAsBase64(new File([], "empty.ico"))).toBe("");
  });
});

describe("fileExtension", () => {
  it("is the lower-cased extension without the dot", () => {
    expect(fileExtension("favicon.PNG")).toBe("png");
    expect(fileExtension("a.b.svg")).toBe("svg");
  });

  it("is empty for a name with no extension", () => {
    expect(fileExtension("favicon")).toBe("");
  });
});
