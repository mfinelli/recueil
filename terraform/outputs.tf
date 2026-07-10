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
