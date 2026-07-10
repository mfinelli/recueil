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
      ],
    },
  };
});
