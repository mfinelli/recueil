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

// Deliberately minimal: this app has no offline-first ambitions (every
// real action here, /pair and /queue, needs the network regardless), so
// the only job of this service worker is satisfying installability --
// Android's "add to home screen" / share-target registration wants one
// registered with a real fetch handler, not just a manifest. Cache-first
// for the app shell's own four static files is a reasonable freebie on
// top of that, not a design goal in itself.

const CACHE_NAME = "recueil-pwa-v1";
const SHELL_FILES = [
  "/",
  "/style.css",
  "/app.js",
  "/manifest.json",
  "/icon.svg",
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then((cache) => cache.addAll(SHELL_FILES))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((key) => key !== CACHE_NAME)
            .map((key) => caches.delete(key)),
        ),
      )
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);

  // Only ever serve the app shell from cache -- every API call (/pair,
  // /queue) and the share-target GET (which carries query params this
  // fetch handler must not intercept and serve a stale cached page for)
  // always goes to the network untouched.
  if (event.request.method !== "GET" || !SHELL_FILES.includes(url.pathname)) {
    return;
  }
  if (url.search) {
    return;
  }

  event.respondWith(
    caches
      .match(event.request)
      .then((cached) => cached || fetch(event.request)),
  );
});
