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

// Vendors self-hosted assets into static/ ahead of `zola build`. Zola has no
// bundler of its own, so this stands in for what Vite's asset pipeline does
// for the dashboard: pull specific files out of node_modules and place them
// somewhere the site can reference directly.
//
// Two independent jobs:
//
// 1. Fonts -- copies the exact @fontsource weight CSS files the dashboard
//    imports in src/main.ts (see DESIGN_SYSTEM.md's Typography section), plus
//    whatever files those CSS files actually reference, into
//    static/fonts/<package>/. Deliberately doesn't vendor the whole
//    @fontsource files/ directory -- each package ships every weight of the
//    family, and we only want the handful this project actually uses.
//
// 2. Icons -- copies specific SVGs out of simple-icons into static/icons/.

import { readFile, writeFile, mkdir, copyFile } from "node:fs/promises";

const STATIC = new URL("./static/", import.meta.url);

const FONT_PACKAGES = {
  fraunces: {
    module: "@fontsource/fraunces",
    weights: ["500", "600", "500-italic", "600-italic"],
  },
  "ibm-plex-mono": {
    module: "@fontsource/ibm-plex-mono",
    weights: ["400", "500"],
  },
};

const ICONS = {
  github: "simple-icons/icons/github.svg",
};

async function vendorFonts() {
  for (const [slug, { module, weights }] of Object.entries(FONT_PACKAGES)) {
    const pkgDir = new URL(`../node_modules/${module}/`, import.meta.url);
    const outDir = new URL(`fonts/${slug}/`, STATIC);
    await mkdir(outDir, { recursive: true });
    await mkdir(new URL("files/", outDir), { recursive: true });

    for (const weight of weights) {
      const cssUrl = new URL(`${weight}.css`, pkgDir);
      const css = await readFile(cssUrl, "utf8");

      // Pull every ./files/whatever.woff2|woff reference out of the CSS
      // rather than assuming a fixed subset list -- @fontsource's own subset
      // split (latin/latin-ext/vietnamese/etc.) is the source of truth here,
      // not something worth hardcoding and letting drift.
      const referenced = new Set(
        [...css.matchAll(/url\(\.\/files\/([^)]+)\)/g)].map((m) => m[1]),
      );
      for (const file of referenced) {
        await copyFile(
          new URL(`files/${file}`, pkgDir),
          new URL(`files/${file}`, outDir),
        );
      }

      await writeFile(new URL(`${weight}.css`, outDir), css);
    }
    console.log(`vendored ${weights.length} weight(s) for ${slug}`);
  }
}

async function vendorIcons() {
  const outDir = new URL("icons/", STATIC);
  await mkdir(outDir, { recursive: true });

  for (const [name, specifier] of Object.entries(ICONS)) {
    const src = new URL(`../node_modules/${specifier}`, import.meta.url);
    await copyFile(src, new URL(`${name}.svg`, outDir));
  }
  console.log(`vendored ${Object.keys(ICONS).length} icon(s)`);
}

await mkdir(STATIC, { recursive: true });
await vendorFonts();
await vendorIcons();
