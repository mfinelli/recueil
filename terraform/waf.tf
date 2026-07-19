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

# Browser Integrity Check (BIC) bypass for recueil's own non-browser Go
# clients (the CLI's internal/deviceapi, and the backend's internal/mirror /
# internal/ingest.WorkerClient) -- Cloudflare's BIC heuristics otherwise tend
# to flag exactly this shape of traffic (no browser TLS/JA3 fingerprint, no
# normal navigation headers) and drop it with an error before it ever reaches
# the Worker.
#
# Keyed on User-Agent alone, not on the presence of a bearer token or
# service-key header: the CLI's POST /pair call carries neither (pairing is
# unauthenticated by design), so requiring one here would leave pairing
# exposed to the exact problem this bypass exists to fix. BIC is a low-stakes
# anti-scraping heuristic, not a real security boundary, so identifying "this
# is our own client" by User-Agent alone is sufficient for this specific,
# narrow purpose.
#
# This is *only* the BIC bypass. It intentionally does not enforce auth
# presence or block anything -- the Worker's own handlers already do that
# (bearer token or X-Service-Key, per route). A separate, stricter zone-level
# rule for that is a distinct piece of work, deferred for now.
resource "cloudflare_ruleset" "browser_integrity_check_bypass" {
  count = var.enable_browser_integrity_check_bypass ? 1 : 0

  zone_id = data.cloudflare_zones.zone.result[0].id
  name    = "Browser Integrity Check bypass for recueil's own Go clients"
  kind    = "zone"
  phase   = "http_request_firewall_custom"

  rules = [{
    description = "Bypass Browser Integrity Check for recueil's CLI/backend User-Agent"
    expression  = "(http.user_agent eq \"recueil/1.0\")"
    action      = "skip"

    action_parameters = {
      products = ["bic"] # bic = Browser Integrity Check
    }

    logging = {
      enabled = true
    }
  }]
}
