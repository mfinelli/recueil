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

import { defineConfig } from "vitest/config";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import {
  cloudflareTest,
  readD1Migrations,
} from "@cloudflare/vitest-pool-workers";

export default defineConfig(async () => {
  const migrations = await readD1Migrations("./terraform/worker/migrations");

  return {
    test: {
      projects: [
        {
          plugins: [
            cloudflareTest({
              main: "terraform/worker/index.js",
              miniflare: {
                // NOTE: keep in sync with version defined in terraform
                compatibilityDate: "2026-07-08",
                d1Databases: { DB: "test-db" },
                bindings: {
                  SERVICE_SECRET: "test-service-secret",
                  TEST_MIGRATIONS: migrations,
                  // Fake R2 S3 API credentials -- only exercised for
                  // presigned-URL *construction* in tests (see
                  // handleGetUploadUrls tests), never a real upload, so
                  // any well-formed values work.
                  R2_ACCOUNT_ID: "test-account-id",
                  R2_BUCKET_NAME: "test-bucket",
                  R2_ACCESS_KEY_ID: "test-access-key-id",
                  R2_ACCESS_KEY_SECRET: "test-access-key-secret",
                },
              },
            }),
          ],
          test: {
            name: "worker",
            include: ["terraform/worker/tests/**/*.test.js"],
            setupFiles: ["terraform/worker/tests/apply-migrations.js"],
          },
        },
        {
          test: {
            name: "extension",
            environment: "jsdom",
            include: ["extension/tests/**/*.test.js"],
          },
        },
        {
          // The dashboard's own logic tests (src/lib/*.test.ts) -- NOT
          // component-rendering tests yet, just the plain TS/runes logic
          // underneath the screens: the API client, session/auth state,
          // and route guards.
          plugins: [svelte()],
          resolve: {
            // Without this, Svelte resolves to its server/SSR runtime
            // under plain Node, where $state is an inert one-shot value
            // container, not a live reactive signal -- session.svelte.ts's
            // tests would then silently test something that isn't the
            // real reactive behavior at all, not fail loudly.
            conditions: ["browser"],
          },
          test: {
            name: "dashboard",
            environment: "jsdom",
            include: ["src/**/*.test.ts"],
          },
        },
      ],
    },
  };
});
