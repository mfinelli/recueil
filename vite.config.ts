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

import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { paraglideVitePlugin } from "@inlang/paraglide-js";

// Build output goes to dist/, embedded into the Go binary via go:embed.
//
// Dev workflow: `pnpm dev` runs Vite's own dev server; the proxy below
// forwards /api (and its cookies) to the Go backend so `recueil server`
// doesn't need rebuilding on every frontend change. Adjust the target if
// your local backend listens somewhere other than the default.
//
// paraglideVitePlugin compiles messages/{locale}.json into typed,
// tree-shakeable message functions under src/paraglide/.
//
// NOTE: Paraglide's own generated/documented default for the `modules`
// array in project.inlang/settings.json points at cdn.jsdelivr.net,
// fetching its message-format/matcher plugins over the network on every
// single compile.  project.inlang/settings.json points at
// ./node_modules/@inlang/plugin-{message-format,m-function-matcher}
// instead following the advice in
// https://github.com/opral/paraglide-js/issues/498#issuecomment-2830728989
// for how to use real npm dependencies (pinned and lockfile-verified like
// everything else).
export default defineConfig({
  plugins: [
    svelte(),
    paraglideVitePlugin({
      project: "./src/project.inlang",
      outdir: "./src/paraglide",
      strategy: ["custom-userSettings", "preferredLanguage", "baseLocale"],
      emitTsDeclarations: true,
    }),
  ],
  server: {
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
});
