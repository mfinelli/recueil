import js from "@eslint/js";
import globals from "globals";

export default [
  js.configs.recommended,
  {
    files: ["terraform/index.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.serviceworker },
    },
  },
  {
    files: ["terraform/tests/**/*.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.serviceworker, ...globals.vitest },
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
      "extension/src/popup/**/*.js",
      "extension/src/common/**/*.js",
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
      "internal/urlnorm/clearurls-rules/**/*",
      "extension/dist/**/*",
      "extension/node_modules/**/*",
    ],
  },
];
