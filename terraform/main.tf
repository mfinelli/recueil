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
  content_file   = "${path.module}/index.js"
  main_module    = "index.js"
  content_sha256 = filesha256("${path.module}/index.js")

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
  ]
}

resource "cloudflare_workers_custom_domain" "worker_domain" {
  account_id = var.account_id
  zone_id    = data.cloudflare_zones.zone.result[0].id
  hostname   = "${var.worker_subdomain}.${var.zone_name}"
  service    = cloudflare_workers_script.worker.script_name
}
