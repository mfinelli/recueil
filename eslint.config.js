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
    ignores: ["internal/urlnorm/clearurls-rules/**/*"],
  },
];
