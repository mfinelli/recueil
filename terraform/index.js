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

// Recueil Worker — phase 0 stub.
//
// No real logic yet. This exists so Terraform has a script to deploy and
// bind D1 / R2 / the service secret to, so later phases can build directly
// against env.DB, env.BUCKET, and env.SERVICE_SECRET without a second
// infrastructure change. See the design doc §2/§11 for what this Worker
// will eventually do: device auth, queue enqueue, presigned R2 URLs, D1
// read/write, and the service-secret-gated backend endpoints.

export default {
  async fetch(request, env, ctx) {
    return new Response("Recueil Worker: not yet implemented", {
      status: 501,
    });
  },
};
