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

output "worker_url" {
  description = <<-EOT
    Public HTTPS entrypoint of the Recueil Worker (the custom domain,
    not a workers.dev subdomain — that subdomain is deliberately left
    disabled).
  EOT
  value       = "https://${var.worker_subdomain}.${var.zone_name}"
}

output "d1_database_id" {
  description = <<-EOT
    ID of the D1 database backing the queue, device tokens, and
    bookmark/credential mirrors.
  EOT
  value       = cloudflare_d1_database.worker_db.id
}

output "r2_bucket_name" {
  description = <<-EOT
    Name of the R2 bucket used as the temporary capture blob buffer.
  EOT
  value       = cloudflare_r2_bucket.capture_buffer.name
}

output "service_secret" {
  description = <<-EOT
    Shared secret for backend<->Worker service authentication (design doc §5a).
    Copy into the backend's .env after apply; not stored anywhere else.
  EOT
  value       = random_password.service_secret.result
  sensitive   = true
}
