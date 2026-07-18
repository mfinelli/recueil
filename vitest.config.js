import { defineConfig } from "vitest/config";
import {
  cloudflareTest,
  readD1Migrations,
} from "@cloudflare/vitest-pool-workers";

export default defineConfig(async () => {
  const migrations = await readD1Migrations("./terraform/migrations");

  return {
    test: {
      projects: [
        {
          plugins: [
            cloudflareTest({
              main: "terraform/index.js",
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
            include: ["terraform/tests/**/*.test.js"],
            setupFiles: ["terraform/tests/apply-migrations.js"],
          },
        },
        {
          test: {
            name: "extension",
            environment: "jsdom",
            include: ["extension/tests/**/*.test.js"],
          },
        },
      ],
    },
  };
});
