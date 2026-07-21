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

data "cloudflare_zones" "zone" {
  name = var.zone_name
}

resource "random_password" "service_secret" {
  length  = 48
  special = false
}

resource "cloudflare_d1_database" "worker_db" {
  account_id = var.account_id
  name       = "${var.name_prefix}-recueil"

  read_replication = {
    mode = "disabled"
  }
}

resource "cloudflare_r2_bucket" "capture_buffer" {
  account_id = var.account_id
  name       = "${var.name_prefix}-recueil"
}

resource "cloudflare_workers_script" "worker" {
  account_id     = var.account_id
  script_name    = "${var.name_prefix}-recueil"
  content_file   = "${path.module}/worker/index.js"
  main_module    = "index.js"
  content_sha256 = filesha256("${path.module}/worker/index.js")

  # Static assets (the share-sheet PWA -- pwa/) served directly by this
  # same Worker/script, rather than a separate Cloudflare Pages project:
  # one `terraform apply` for the whole Cloudflare side, no second deploy
  # step (Pages still requires a `wrangler pages deploy` even once the
  # project itself exists), and no new moving part to keep in sync with
  # this module. Static files are matched first by path; anything that
  # doesn't match a real file under pwa/ (every API route this Worker
  # handles: /pair, /queue, /internal/*, etc.) falls through to the
  # fetch handler in index.js untouched -- this is the provider's own
  # default (`run_worker_first` unset), not something this module
  # overrides, since the PWA's own file names never collide with any
  # API path.
  assets = {
    directory = "${path.module}/pwa"
  }

  # Bump periodically; pinned rather than computed dynamically since
  # Terraform has no "today" primitive worth adding a data source for.
  compatibility_date = "2026-07-10"

  bindings = [
    {
      type = "d1"
      name = "DB"
      id   = cloudflare_d1_database.worker_db.id
    },
    {
      type        = "r2_bucket"
      name        = "BUCKET"
      bucket_name = cloudflare_r2_bucket.capture_buffer.name
    },
    {
      type = "secret_text"
      name = "SERVICE_SECRET"
      text = random_password.service_secret.result
    },
    {
      # Not secret -- the account ID is not sensitive on its own (it's
      # visible in every Cloudflare dashboard URL and API response for this
      # account) -- but it must match whatever account var.r2_access_key_id/
      # var.r2_secret_access_key were issued against, so it's derived from
      # the same var.account_id already passed into this module rather than
      # a separately-typed value that could drift out of sync with it.
      type = "plain_text"
      name = "R2_ACCOUNT_ID"
      text = var.account_id
    },
    {
      type = "plain_text"
      name = "R2_BUCKET_NAME"
      text = cloudflare_r2_bucket.capture_buffer.name
    },
    {
      # R2 S3 API credentials for presigned upload URLs (design doc §3/§6).
      # Manually provisioned -- see variables.tf's r2_access_key_id for why
      # Terraform can't create this resource itself.
      type = "secret_text"
      name = "R2_ACCESS_KEY_ID"
      text = var.r2_access_key_id
    },
    {
      type = "secret_text"
      name = "R2_ACCESS_KEY_SECRET"
      text = var.r2_secret_access_key
    },
  ]
}

resource "cloudflare_workers_custom_domain" "worker_domain" {
  account_id = var.account_id
  zone_id    = data.cloudflare_zones.zone.result[0].id
  hostname   = "${var.worker_subdomain}.${var.zone_name}"
  service    = cloudflare_workers_script.worker.script_name
}
