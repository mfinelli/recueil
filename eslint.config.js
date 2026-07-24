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

import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";
import svelte from "eslint-plugin-svelte";
import svelteParser from "svelte-eslint-parser";

export default [
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...svelte.configs.recommended,
  {
    files: ["src/**/*.ts"],
    languageOptions: {
      globals: { ...globals.browser },
    },
  },
  {
    files: ["src/**/*.svelte"],
    languageOptions: {
      parser: svelteParser,
      parserOptions: {
        parser: tseslint.parser,
      },
      globals: { ...globals.browser },
    },
  },
  {
    // eslint-plugin-svelte's own base config (svelte:base:setup-for-svelte-script)
    // already routes *.svelte.ts through svelte-eslint-parser -- necessary for
    // Svelte 5 runes in a plain module -- but doesn't tell it which parser to
    // use for the TS content itself, unlike its .svelte handling above. Without
    // this, TS syntax in a .svelte.ts file fails to parse entirely.
    files: ["src/**/*.svelte.ts"],
    languageOptions: {
      parser: svelteParser,
      parserOptions: {
        parser: tseslint.parser,
      },
      globals: { ...globals.browser },
    },
  },
  {
    files: ["vite.config.ts", "svelte.config.js"],
    languageOptions: {
      globals: { ...globals.node },
    },
  },
  {
    files: ["terraform/worker/index.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.serviceworker },
    },
  },
  {
    files: ["terraform/worker/tests/**/*.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.serviceworker, ...globals.vitest },
    },
  },
  {
    files: ["terraform/pwa/app.js", "terraform/pwa/token.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.browser },
    },
  },
  {
    files: ["terraform/pwa/sw.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.serviceworker },
    },
  },
  {
    files: ["extension/tests/**/*.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.browser, ...globals.vitest },
    },
  },
  {
    files: ["extension/src/background/**/*.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.serviceworker, ...globals.browser },
    },
  },
  {
    files: [
      "extension/src/capture-inject/**/*.js",
      "extension/src/common/**/*.js",
      "extension/src/popup/**/*.js",
    ],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.browser },
    },
  },
  {
    files: ["extension/build.js", "extension/package.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.node },
    },
  },
  {
    ignores: [
      "dist/**/*",
      "extension/dist/**/*",
      "extension/node_modules/**/*",
      "internal/urlnorm/clearurls-rules/**/*",
      "src/paraglide/**/*",
    ],
  },
];
