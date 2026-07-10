# Recueil — Cloudflare module

Provisions the Cloudflare side of Recueil: a D1 database, an R2 bucket, and
a Worker (bound to both, plus a generated backend↔Worker service secret)
served on a custom domain. This directory is a Terraform **module**, not a
standalone root config — it has no `provider` or `backend` block. Reference
it from your own root module, which supplies a configured `cloudflare`
provider and owns state storage.

This is phase 0 only: the D1 database is created with no schema/tables yet,
and the Worker script is a stub with no real logic. Its bindings are wired
up now so later phases can build directly on top without another apply.

## Usage

```hcl
module "recueil" {
  # TODO: pin to a tag once releases exist, e.g. ?ref=v0.1.0
  source = "github.com/mfinelli/recueil//terraform"

  account_id       = var.cloudflare_account_id
  name_prefix      = "mario"              # must be globally unique (R2)
  zone_name        = "mydomain.com"       # must already exist in your account
  worker_subdomain = "recueil"
}
```

## Requirements

- A Cloudflare account with the target zone already added.
- A `cloudflare` provider configured in your root module (API token with
  D1, R2, Workers, and DNS/zone edit permissions for the target zone).

## Outputs

- `worker_url` — the Worker's public HTTPS entrypoint (the custom domain;
  the `workers.dev` subdomain is deliberately left disabled).
- `d1_database_id`
- `r2_bucket_name`
- `service_secret` (sensitive) — copy into the backend's `.env` after
  `apply`.
