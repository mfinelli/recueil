# Phase 0 — Cloudflare scaffolding.
#
# Stands up D1 (empty schema, no tables yet — see the design doc's
# migrations phase), R2, and a Worker whose script is currently a stub with
# no real logic, but whose bindings are wired up now so later phases can
# write Worker code against env.DB / env.BUCKET / env.SERVICE_SECRET
# without a second infrastructure change.

data "cloudflare_zones" "this" {
  name = var.zone_name
}

resource "random_password" "service_secret" {
  length  = 48
  special = false
}

resource "cloudflare_d1_database" "this" {
  account_id = var.account_id
  name       = "${var.name_prefix}-recueil"
}

resource "cloudflare_r2_bucket" "this" {
  account_id = var.account_id
  name       = "${var.name_prefix}-recueil"
}

resource "cloudflare_workers_script" "this" {
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
      id   = cloudflare_d1_database.this.id
    },
    {
      type        = "r2_bucket"
      name        = "BUCKET"
      bucket_name = cloudflare_r2_bucket.this.name
    },
    {
      type = "secret_text"
      name = "SERVICE_SECRET"
      text = random_password.service_secret.result
    },
  ]
}

resource "cloudflare_workers_custom_domain" "this" {
  account_id  = var.account_id
  zone_id     = data.cloudflare_zones.this.result[0].id
  hostname    = var.worker_hostname
  service     = cloudflare_workers_script.this.script_name
  environment = "production"
}
