# recueil — Cloudflare infrastructure

Three things that deploy together but are worked on independently: an
OpenTofu/Terraform module, a Cloudflare Worker (plain JS), and a share-sheet PWA
(plain static files, served by that same Worker). Looking to actually deploy an
instance? See
[Deploying recueil](https://recueil.app/docs/operators/deploying-recueil/) —
this README is for working on the code itself.

See the [root README](../README.md) for repo-wide setup.

## The module

- `main.tf` — the actual resources: a zone lookup, a `random_password` for the
  backend↔Worker service secret, the D1 database, the R2 bucket, the Worker
  script (bound to all three, plus the PWA's static assets), and the custom
  domain route.
- `variables.tf` / `outputs.tf` — inputs and outputs.
- `waf.tf` — the optional Browser Integrity Check bypass rule.
- `versions.tf` — provider constraints (`cloudflare ~> 5.0`, `random ~> 3.0`).

This directory is a module, not a standalone root config — no `provider` or
`backend` block, not meant to be `apply`'d directly from here.

**Validating a change**: `tofu validate` checks syntax and internal consistency
without needing real credentials; `tofu fmt -recursive` (already part of
`just fmt`) covers formatting. Actually planning or applying needs a real
`cloudflare` provider against a real zone — there's no way around that for a
genuine dry run, so testing a change for real means pointing a throwaway
zone/account at it, not just this module in isolation.

**Versioning**: consumers pin `source = "...//terraform?ref=vX.Y.Z"` (see
Deploying recueil). Tag a release whenever a change here is something a consumer
would actually need to pull in.

## The Worker

`worker/index.js` — plain JS, no build step and no bundler. `main.tf` deploys it
via `content_file`, so editing this file directly _is_ the deploy; the next
`tofu apply` (or a consumer's module ref bump) picks up whatever's currently in
it, as-is.

Tests live at `worker/tests/**`, run as the `worker` named project in the root
`vitest.config.js` (real Miniflare D1 via `@cloudflare/vitest-pool-workers`) —
not a separate suite:

```sh
pnpm run test                          # everything, worker included
pnpm exec vitest run --project worker  # just this one
```

`pnpm run --filter=@recueil/terraform types` runs this package's JSDoc-driven
`tsc` type-check. (The package name is historical — `@recueil/terraform` refers
to this Worker code specifically, not the OpenTofu module above, which isn't an
npm package at all.)

## The PWA

`pwa/` — also plain static files (HTML/JS/CSS) without a build step. Deployed as
a Workers static-asset binding on the same script (`assets` in `main.tf`), so it
rides along with the Worker's deploy rather than needing one of its own — and
doesn't exist at all if a consumer sets `enable_pwa = false`.
