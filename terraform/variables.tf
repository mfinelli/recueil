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
