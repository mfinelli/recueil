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

// Produces actual package files (not just an unpacked dist/{chrome,firefox}
// directory) from a fresh build -- see the README for why neither one
// installs *durably* on its own: Chrome's sideload hardening and Firefox's
// signing requirement both still apply regardless of this script. What
// this gets you is the real artifact each of those durable paths actually
// wants next (an .xpi to submit for AMO self-distribution signing, a .crx
// as what Developer-mode/Enterprise-policy installation actually expects)
// instead of a directory.

import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { mkdir, rename } from "node:fs/promises";
import crx3 from "crx3";

const EXTENSION_DIR = fileURLToPath(new URL(".", import.meta.url));
const PACKAGES_DIR = fileURLToPath(
  new URL("./dist/packages/", import.meta.url),
);

async function main() {
  // A stale dist/ would silently package last build's code -- rebuilding
  // here first means `pnpm package` is always correct on its own, not
  // dependent on remembering to `pnpm build` immediately beforehand.
  execFileSync("node", ["build.js"], { cwd: EXTENSION_DIR, stdio: "inherit" });
  await mkdir(PACKAGES_DIR, { recursive: true });

  await packageFirefox();
  await packageChrome();
}

async function packageFirefox() {
  execFileSync(
    "npx",
    [
      "web-ext",
      "build",
      "--source-dir=dist/firefox",
      "--artifacts-dir=dist/packages",
      "--filename=recueil-firefox.zip",
      "--overwrite-dest",
      "--no-config-discovery",
    ],
    { cwd: EXTENSION_DIR, stdio: "inherit" },
  );

  // web-ext always names its output .zip -- an .xpi is just a zip with a
  // different extension, same underlying format, so renaming is all
  // that's needed. Still UNSIGNED: release Firefox will refuse to install
  // this permanently -- see README for the AMO self-distribution signing
  // step that turns this into something durably installable.
  await rename(
    fileURLToPath(
      new URL("./dist/packages/recueil-firefox.zip", import.meta.url),
    ),
    fileURLToPath(
      new URL("./dist/packages/recueil-firefox.xpi", import.meta.url),
    ),
  );
  console.log("wrote extension/dist/packages/recueil-firefox.xpi (unsigned)");
}

async function packageChrome() {
  // crx3 reuses dist/packages/recueil-chrome.pem across runs if it's
  // already there rather than generating a new one every time -- and that
  // matters beyond just convenience: Chrome derives the extension's ID
  // from the public key, so a fresh key on every build would mean a
  // different extension ID every time you package, breaking anything
  // that ever referenced the previous one (an enterprise force-install
  // policy, in particular, pins by ID). Since dist/ is entirely
  // gitignored, though, that reuse only holds *within one checkout* --
  // wiping dist/ or building fresh on another machine generates a new
  // key/ID pair. If a stable ID ever actually matters (e.g. once an
  // enterprise force-install policy exists), move this .pem somewhere
  // persisted and treated like the secret it is, not left inside dist/.
  await crx3(
    [fileURLToPath(new URL("./dist/chrome/manifest.json", import.meta.url))],
    {
      crxPath: fileURLToPath(
        new URL("./dist/packages/recueil-chrome.crx", import.meta.url),
      ),
      keyPath: fileURLToPath(
        new URL("./dist/packages/recueil-chrome.pem", import.meta.url),
      ),
    },
  );
  console.log(
    "wrote extension/dist/packages/recueil-chrome.crx (only installs with Developer mode enabled -- see README)",
  );
}

await main();
