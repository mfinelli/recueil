# Recueil — Implementation Summary

## Phase 0 (Cloudflare Scaffolding)

### What exists now

A public Terraform module at `terraform/` in the Recueil repo, consumed via

```hcl
module "recueil" {
  source = "github.com/mfinelli/recueil//terraform"
  # TODO: pin to a tag once releases exist, e.g. ?ref=v0.1.0

  account_id       = var.cloudflare_account_id
  name_prefix      = "test"
  zone_name        = "example.com"
  worker_subdomain = "recueil"
}
```

from personal, local IaC. It's a **child module** — no `provider` or `backend`
block; state and provider config live entirely in the consumer.

### Resources provisioned

| Local name                                       | Resource       | Notes                                                                                                                                                                                                                                                   |
| ------------------------------------------------ | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `cloudflare_d1_database.worker_db`               | D1 database    | Empty, no tables yet. Requires explicit `read_replication = { mode = "disabled" }` — the provider sends `null` otherwise and the Cloudflare API 400s ([provider issue #6309](https://github.com/cloudflare/terraform-provider-cloudflare/issues/6309)). |
| `cloudflare_r2_bucket.capture_buffer`            | R2 bucket      | Temporary blob buffer only. No lifecycle config yet — nothing writes to it yet.                                                                                                                                                                         |
| `cloudflare_workers_script.worker`               | Worker script  | Deployed from a single flattened `index.js` (currently a 501 stub). Requires `content_sha256 = filesha256(...)` alongside `content_file`.                                                                                                               |
| `cloudflare_workers_custom_domain.worker_domain` | Custom domain  | Binds the script to `${var.worker_subdomain}.${var.zone_name}`, via a `data "cloudflare_zones"` lookup. Confirmed working end-to-end — didn't need the `cloudflare_workers_route` fallback.                                                             |
| `random_password.service_secret`                 | Service secret | 48 chars, alphanumeric only (`special = false`). Charset restriction is for safe handling in `.env` files and HTTP headers, not entropy — 48 alphanumeric chars is already ~285 bits.                                                                   |

`workers.dev` is deliberately left disabled — the custom domain is the only
entrypoint.

### Bindings wired into the Worker

Decided and wired now, ahead of any real Worker logic, so future phases can
build directly against them without another `apply`:

| Binding name     | Points to                    |
| ---------------- | ---------------------------- |
| `DB`             | the D1 database              |
| `BUCKET`         | the R2 bucket                |
| `SERVICE_SECRET` | the generated service secret |

### Module interface

**Variables:**

- `account_id`
- `name_prefix` — must be globally unique (R2 bucket names are global)
- `zone_name`
- `worker_subdomain` — combined with `zone_name` into the full hostname rather
  than accepting an independent full hostname, so the two can't structurally
  disagree

**Outputs:**

- `worker_url`
- `d1_database_id`
- `r2_bucket_name`
- `service_secret` (sensitive)

**Version constraints:**

- `required_version >= 1.5.0` — works for both Terraform and OpenTofu (no
  split-handling needed; OpenTofu's version numbering continues Terraform's own
  sequence from the fork point)
- `cloudflare` provider `~> 5.0`

### Decisions worth remembering for later phases

- **Single Worker script, single subdomain** — the Worker is one component per
  the design doc, not split by function.
- **D1 schema is still empty.** Tables (`users`, `tokens`, `queue_items`,
  `pending_captures`, `archived_pages` from design doc §10) haven't been created
  yet — likely next, alongside real Worker route logic.
- **Worker script is still a 501 stub** — no auth, no routes, no D1/R2 logic
  implemented against the bindings yet.
- **Module versioning is currently unpinned** (tracks the default branch), with
  a `# TODO: pin to a tag once releases exist` comment left in the local IaC's
  `source` line as a reminder.
- **Known provider rough edges to watch for:**
  - the `read_replication` null bug (worked around, see above)
  - `cloudflare_workers_custom_domain`'s `environment` field has a documented
    404 risk against certain Worker deployment paths — not hit here, but worth
    knowing about if a future redeploy approach changes (e.g. a move to the
    versioned `cloudflare_worker`/`-version`/ `-deployment` resources instead of
    `cloudflare_workers_script`)
