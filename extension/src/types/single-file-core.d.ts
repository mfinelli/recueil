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

// single-file-core ships no types at all  -- this declares only the two entry
// points bundle-entry.js actually calls, not the library's whole surface.
// Shapes are drawn from reading core/index.js and core/util.js directly, not
// from any published documentation (there is none).
declare module "single-file-core/single-file.js" {
  /**
   * Sets fetch/frameFetch overrides (see relay-fetch.js) and other
   * process-wide options core/util.js's getInstance() reads back later.
   * Real signature is far broader; only what we actually pass is declared.
   */
  export function init(options?: {
    fetch?: (
      url: string,
      init?: { headers?: HeadersInit; referrer?: string },
    ) => Promise<{
      status: number;
      statusText: string;
      url: string;
      headers: { get(name: string): string | null };
      arrayBuffer(): Promise<ArrayBuffer>;
    }>;
  }): void;

  /**
   * Real signature accepts further (initOptions, doc, win) parameters we
   * never pass -- getPageData() defaults doc/win to globalThis.document/
   * globalThis itself when omitted, which is exactly the content-script
   * page context bundle-entry.js runs in.
   */
  export function getPageData(options?: {
    removeFrames?: boolean;
    compressHTML?: boolean;
    removeHiddenElements?: boolean;
    removeUnusedStyles?: boolean;
    removeUnusedFonts?: boolean;
    removeImports?: boolean;
    blockScripts?: boolean;
    blockAudios?: boolean;
    blockVideos?: boolean;
    removeAlternativeFonts?: boolean;
    removeAlternativeMedias?: boolean;
    removeAlternativeImages?: boolean;
  }): Promise<{
    title: string;
    filename: string;
    mimeType: string;
    content: string;
    comment?: string;
  }>;
}
