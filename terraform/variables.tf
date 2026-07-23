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

variable "account_id" {
  description = <<-EOT
    Cloudflare account ID that owns the D1 database, R2 bucket, and Worker.
  EOT
  type        = string
}

variable "name_prefix" {
  description = <<-EOT
    Prefix applied to all globally-namespaced resources (R2 bucket name, D1
    database name, Worker script name) to avoid collisions with other
    deployments of this module, e.g. "mario". R2 bucket names in particular
    are globally unique across all of Cloudflare, so this must not be left
    at a shared default.
  EOT
  type        = string
}

variable "zone_name" {
  description = <<-EOT
    The Cloudflare zone (domain) that already exists in the target account
    and under which the Worker's custom domain will be created, e.g.
    "mydomain.com".
  EOT
  type        = string
}

variable "worker_subdomain" {
  description = <<-EOT
    Subdomain to bind under var.zone_name for the Worker's public entrypoint,
    e.g. "recueil" for "recueil.mydomain.com".
  EOT
  type        = string
}

variable "r2_access_key_id" {
  description = <<-EOT
    Access Key ID for R2's S3-compatible API, used by the Worker to build
    presigned upload URLs (design doc §3/§6). There is no Terraform resource
    that provisions this credential -- unlike the D1 database, R2 bucket, or
    the Worker's own service secret, R2 API tokens (Access Key ID + Secret
    Access Key) must be created once, manually, via the Cloudflare dashboard
    (Account Home > R2 > Manage R2 API Tokens > Create API Token, with
    read+write permission scoped to this bucket) or the R2 API directly, then
    supplied here. See terraform/README.md for the exact steps.
  EOT
  type        = string
  sensitive   = true
}

variable "r2_secret_access_key" {
  description = <<-EOT
    Secret Access Key paired with var.r2_access_key_id. See that variable's
    description for why this can't be Terraform-provisioned directly.
  EOT
  type        = string
  sensitive   = true
}

variable "enable_browser_integrity_check_bypass" {
  description = <<-EOT
    Whether to provision a zone-level ruleset that skips Cloudflare's
    Browser Integrity Check (BIC) for requests carrying recueil's own
    Go-client User-Agent (see internal/deviceapi, internal/mirror,
    internal/ingest). Only relevant if BIC is actually enabled on the zone
    -- Cloudflare's BIC heuristics otherwise tend to flag the CLI's and
    backend's non-browser HTTP clients. The browser extension is unaffected
    since it's a real browser making the request. Defaults to true since
    this is a narrow, additive bypass with no real downside for a zone that
    doesn't have BIC on at all.
  EOT
  type        = bool
  default     = true
}

variable "enable_pwa" {
  description = <<-EOT
    Whether to deploy the share-sheet PWA's static assets alongside the Worker.
  EOT
  type        = bool
  default     = true
}
