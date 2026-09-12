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

// Collects license text for the extension's workspace production
// dependencies and writes one combined THIRD_PARTY_LICENSES file per
// browser target. It's scoped via `pnpm list --filter @recueil/extension
// --prod` instead of a flat license-checker --production scan of the hoisted
// node_modules: the local package.json is the source of truth.

import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { readFile, writeFile, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const EXTENSION_DIR = fileURLToPath(new URL(".", import.meta.url));
// nodeLinker: hoisted means node_modules only actually exists at the repo
// root, not inside extension/ and license-checker-rseidelsohn scans
// node_modules relative to its cwd, so it must run from here or it finds
// nothing.
const REPO_ROOT = fileURLToPath(new URL("..", import.meta.url));
const BROWSERS = ["chrome", "firefox"];

function flattenProdDependencies(workspaceListing) {
  const packages = new Set();

  function walk(deps) {
    if (!deps) return;
    for (const [name, info] of Object.entries(deps)) {
      if (info.version) packages.add(`${name}@${info.version}`);
      walk(info.dependencies);
    }
  }

  for (const workspace of workspaceListing) walk(workspace.dependencies);
  return [...packages].sort();
}

function collectProdDependencies() {
  const raw = execFileSync(
    "pnpm",
    [
      "list",
      "--filter",
      "@recueil/extension",
      "--prod",
      "--depth",
      "Infinity",
      "--json",
    ],
    { cwd: EXTENSION_DIR, encoding: "utf8" },
  );
  return flattenProdDependencies(JSON.parse(raw));
}

function collectLicenseData(packages, filesDir) {
  // --includePackages restricts license-checker-rseidelsohn's node_modules
  // scan to exactly the set `pnpm list` says is actually bundled; --files
  // copies the license text alongside it instead of just the name.
  const raw = execFileSync(
    "pnpm",
    [
      "dlx",
      "license-checker-rseidelsohn",
      "--includePackages",
      packages.join(";"),
      "--files",
      filesDir,
      "--json",
    ],
    { cwd: REPO_ROOT, encoding: "utf8" },
  );
  return JSON.parse(raw);
}

async function buildDisclaimer(licenseData) {
  const entries = Object.entries(licenseData).sort(([a], [b]) =>
    a.localeCompare(b),
  );

  const sections = await Promise.all(
    entries.map(async ([nameAndVersion, info]) => {
      const text = info.licenseFile
        ? await readFile(info.licenseFile, "utf8")
        : "(no license file found)\n";
      const heading = `${nameAndVersion} (${info.licenses ?? "Unknown"})`;
      return `${heading}\n${"-".repeat(heading.length)}\n\n${text.trim()}\n`;
    }),
  );

  return (
    "THIRD-PARTY SOFTWARE NOTICES AND INFORMATION\n" +
    "This file lists the dependencies bundled into the recueil browser " +
    "extension and their licenses.\n\n" +
    sections.join("\n\n")
  );
}

async function main() {
  const packages = collectProdDependencies();
  const tmpDir = await mkdtemp(join(tmpdir(), "recueil-ext-licenses-"));

  try {
    const licenseData = collectLicenseData(packages, tmpDir);
    const disclaimer = await buildDisclaimer(licenseData);

    for (const browser of BROWSERS) {
      await writeFile(
        new URL(`./dist/${browser}/THIRD_PARTY_LICENSES`, import.meta.url),
        disclaimer,
      );
      console.log(`wrote extension/dist/${browser}/THIRD_PARTY_LICENSES`);
    }
  } finally {
    await rm(tmpDir, { recursive: true, force: true });
  }
}

await main();
