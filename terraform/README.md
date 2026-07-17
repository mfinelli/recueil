# Recueil — Cloudflare module

Provisions the Cloudflare side of Recueil: a D1 database, an R2 bucket, and a
Worker (bound to both, plus a generated backend↔Worker service secret) served on
a custom domain. This directory is a Terraform **module**, not a standalone root
config — it has no `provider` or `backend` block. Reference it from your own
root module, which supplies a configured `cloudflare` provider and owns state
storage.

## Usage

```hcl
module "recueil" {
  # TODO: pin to a tag once releases exist, e.g. ?ref=v0.1.0
  source = "github.com/mfinelli/recueil//terraform"

  account_id       = var.cloudflare_account_id
  name_prefix      = "mario"              # must be globally unique (R2)
  zone_name        = "mydomain.com"       # must already exist in your account
  worker_subdomain = "recueil"

  # See "Manual setup: R2 API credentials" below before running apply.
  r2_access_key_id     = var.recueil_r2_access_key_id
  r2_secret_access_key = var.recueil_r2_secret_access_key
}

output "service_secret" {
  value     = module.recueil.service_secret
  sensitive = true
}
```

## Requirements

- A Cloudflare account with the target zone already added.
- A `cloudflare` provider configured in your root module (API token with D1, R2,
  Workers, and DNS/zone edit permissions for the target zone).
- An R2 API token (Access Key ID + Secret Access Key) — see below. This is the
  one credential in this module that can't be Terraform-provisioned; it must
  exist before your first `apply`.

## Manual setup: R2 API credentials

The Worker issues presigned upload URLs directly against R2's S3-compatible API
(design doc §3/§6), which requires an Access Key ID + Secret Access Key pair — a
different credential type from anything else Terraform manages here (the
D1/R2/Workers resources above are all provisioned through Cloudflare's own
control-plane API token, not the S3-compatible one). There is currently no
Terraform resource that creates this pair, so it's a one-time manual step:

1. Cloudflare dashboard → **R2** → **Manage R2 API Tokens** → **Create API
   Token**.
2. Scope it to **Object Read & Write**, restricted to the bucket this module
   creates (`${name_prefix}-recueil`) if you'd rather not grant account-wide R2
   access.
3. Copy the resulting **Access Key ID** and **Secret Access Key** — Cloudflare
   only shows the secret once — into `r2_access_key_id` / `r2_secret_access_key`
   (e.g. via your root module's own `.tfvars` or a secrets manager; mark them
   `sensitive` the same way this module already does internally).

Rotating this credential means generating a new R2 API token and running `apply`
again with the new values; the old token can then be revoked from the same R2
API Tokens screen.

## Outputs

- `worker_url` — the Worker's public HTTPS entrypoint (the custom domain; the
  `workers.dev` subdomain is deliberately left disabled).
- `d1_database_id`
- `r2_bucket_name`
- `service_secret` (sensitive) — copy into the backend's `.env` after `apply`.
