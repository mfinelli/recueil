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

// Builds two independent extension trees, one per browser target, from one
// source tree -- see DESIGN.md §3g/architecture discussion for why a
// single MV3 codebase covers both rather than forking like upstream
// SingleFile did. Each target gets its own manifest (manifest.base.json
// merged with manifest.<browser>.json -- a shallow, top-level merge: a key
// present in the browser overlay entirely replaces the base's value for
// that key, it doesn't deep-merge) and its own esbuild bundle of the same
// source.
//
// background.js and capture-inject.js are bundled as separate, independent
// IIFEs, not one bundle -- they run in genuinely different contexts
// (service worker vs. a page's content-script world) and are loaded at
// different times (background.js at extension startup, capture-inject.js
// only when a capture is actually triggered, via
// scripting.executeScript({files: [...]})  -- see background/index.js).
// Bundling them together would mean capture-inject's code (and its
// single-file-core dependency, the largest thing in this build) loads and
// parses on every service-worker wake, for no benefit.

import { build, context } from "esbuild";
import { readFile, writeFile, mkdir, copyFile } from "node:fs/promises";

const BROWSERS = ["chrome", "firefox"];
const watch = process.argv.includes("--watch");

async function readJSON(relPath) {
  return JSON.parse(await readFile(new URL(relPath, import.meta.url), "utf8"));
}

async function buildManifest(browser) {
  const base = await readJSON("./manifest.base.json");
  const overlay = await readJSON(`./manifest.${browser}.json`);
  // Shallow merge is deliberate -- see file doc comment. The two overlays
  // today only ever replace whole top-level keys (background,
  // browser_specific_settings), never partially patch into a base object,
  // so there's nothing a deep merge would buy us yet.
  const merged = { ...base, ...overlay };

  const outDir = new URL(`./dist/${browser}/`, import.meta.url);
  await mkdir(outDir, { recursive: true });
  await writeFile(
    new URL("manifest.json", outDir),
    JSON.stringify(merged, null, 2) + "\n",
  );
}

async function buildBundle(browser, { entry, outfile }) {
  const options = {
    entryPoints: [new URL(entry, import.meta.url).pathname],
    outfile: new URL(`./dist/${browser}/${outfile}`, import.meta.url).pathname,
    bundle: true,
    format: "iife",
    platform: "browser",
    target: "es2022",
    sourcemap: true,
    minify: !watch,
  };

  // esbuild's build({watch: true}) option was removed years ago (as of
  // 0.17) in favor of a separate context()/ctx.watch() API -- confirmed
  // against the actual installed version (0.25.12) rather than assumed,
  // since this is exactly the kind of thing that's easy to get wrong from
  // stale memory of an older esbuild release.
  if (watch) {
    const ctx = await context(options);
    await ctx.watch();
    return ctx;
  }
  return build(options);
}

async function copyStatic(browser, filename) {
  await copyFile(
    new URL(`./src/popup/${filename}`, import.meta.url),
    new URL(`./dist/${browser}/${filename}`, import.meta.url),
  );
}

async function buildAll() {
  for (const browser of BROWSERS) {
    await buildManifest(browser);
    await buildBundle(browser, {
      entry: "./src/background/index.js",
      outfile: "background.js",
    });
    await buildBundle(browser, {
      entry: "./src/capture-inject/bundle-entry.js",
      outfile: "capture-inject.js",
    });
    await buildBundle(browser, {
      entry: "./src/popup/popup.js",
      outfile: "popup.js",
    });
    // popup.html/popup.css are plain static files -- no bundling needed,
    // just copied alongside the JS bundle popup.html's <script>/<link>
    // tags reference by the same relative filename.
    await copyStatic(browser, "popup.html");
    await copyStatic(browser, "popup.css");
    console.log(`built extension/dist/${browser}`);
  }
}

await buildAll();
