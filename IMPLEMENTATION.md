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

## Phase 1 (Backend + Postgres + Bootstrap Admin — and the tooling that grew around it)

### What exists now

A working Go backend binary (`recueil server`, via cobra) that: loads config
(TOML file + env vars via viper), connects to Postgres, applies its own Postgres
and D1 migrations at startup (no external migration tool needed for either),
serves a session-cookie-authenticated dashboard API (chi router), and exposes
`/health`, `/ping`, `/info`, and `/metrics` on the same router. A full Postgres
integration-test harness (dedicated Docker Compose container, fixture factories,
real-database tests throughout) backs the whole thing. Scope grew substantially
past the original "backend + Postgres + bootstrap admin" framing — cobra/viper,
chi, health checks, and Prometheus metrics were all added along the way, each
recorded below and in the design doc (§13a).

Device authentication is **not** based on mirroring any password-derived value
into D1. Each account has a separate, single-purpose **pairing token**,
generated automatically at account creation, that exists only to pair a device
against the Worker in exchange for a bearer token — the dashboard password is
never used for this, and D1 never stores anything password-derived. See
DESIGN.md §5 for the full rationale (in short: a CPU-limited Cloudflare Worker
cannot feasibly verify a slow hash like bcrypt, and mirroring a faster
Worker-native hash of the password would still mean exposing password-derived
material to D1 — a separate credential avoids the problem at the source rather
than picking a faster algorithm to mirror).

The design doc has been kept in sync throughout (five revision rounds so far)
and is the authoritative reference for _why_ each decision below was made — this
document is the "what exists, what to watch for" companion to it, matching the
Phase 0 doc's role.

### Repository structure added this phase

```
recueil/
├── main.go                    # embeds migrations/ and terraform/worker/migrations/,
│                                 assigns to exported cmd package vars, os.Exit(cmd.Execute())
├── cmd/
│   ├── root.go                # cobra root command; owns the one signal-aware
│   │                             context (SIGINT/SIGTERM), threaded to
│   │                             subcommands via cmd.Context()
│   ├── server.go               # `recueil server` — actual startup: config,
│   │                             both migration runs, bootstrap holder,
│   │                             pairing-token key parsing, httpapi wiring,
│   │                             graceful shutdown
│   └── cli/                   # carried over unchanged; NOT reconfirmed
│                                 compatible with the new structure yet
├── internal/
│   ├── config/                 # viper: --config TOML file, env vars, defaults
│   │                             in this package's own init()
│   ├── auth/                    # password hashing, session tokens, bootstrap
│   │                             flow, pairing-token generation + reversible
│   │                             AES-256-GCM encrypt/decrypt
│   ├── db/                      # sqlc-generated query code (renamed from `dbgen`)
│   ├── pgmigrate/                # Postgres migrations via goose's Provider API
│   ├── dbtest/                   # Postgres integration-test harness
│   ├── d1migrate/                 # D1 migrations via direct Cloudflare API call
│   ├── mirror/                    # pushes the pairing-token-hash mirror to the Worker
│   ├── metrics/                    # Prometheus registry + custom collectors
│   └── httpapi/                    # chi router, handlers, health checks, middleware
├── migrations/                  # Postgres migrations — plain .sql, no embed.go
├── queries/                     # sqlc source .sql files
├── sqlc.yaml
├── docker-compose.test.yml       # dedicated ephemeral test Postgres
├── Makefile                      # test-db-up / test-db-down / test
├── vitest.config.js               # root-level; covers Worker tests, will grow
│                                    # a Svelte-scoped project later
├── eslint.config.js                # root-level; same per-directory scoping plan
└── terraform/worker/
    ├── index.js                     # plain JS (@ts-check + JSDoc), one real
    │                                  route: /internal/users/mirror
    ├── migrations/                   # D1 schema (schema_migrations, users)
    ├── tests/                        # @cloudflare/vitest-pool-workers, real
    │                                  # simulated D1 via Miniflare
    └── tsconfig.json                  # tsc --noEmit, index.js only
```

### Packages and responsibilities

| Package              | Responsibility                                                                          | Notes                                                                                                                                                                               |
| -------------------- | --------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/config`    | Loads `Config` via viper                                                                | Defaults live in this package's own `init()`, not `cmd/root.go` — they must apply regardless of which binary/test calls `Load()`.                                                   |
| `internal/auth`      | `bcrypt` hashing, session tokens, bootstrap holder, pairing-token generation/encryption | Bootstrap token is in-memory, not persisted (see below). Pairing token is AES-256-GCM encrypted for Postgres storage, reversibly — see below.                                       |
| `internal/db`        | sqlc-generated Postgres queries                                                         | `timestamptz` columns map to `pgtype.Timestamptz`, not `time.Time` (sqlc's `pgx/v5` default — kept, not overridden).                                                                |
| `internal/pgmigrate` | Applies `migrations/*.sql` against an already-open `*pgxpool.Pool`                      | Uses goose's `Provider` API, not its package-level functions — see rough edges below.                                                                                               |
| `internal/dbtest`    | Postgres integration-test harness                                                       | `Setup()` fails hard (never skips) if the test DB is unreachable; `Reset()` truncates every table dynamically, not a hardcoded list.                                                |
| `internal/d1migrate` | Applies D1 migrations via a direct Cloudflare API call                                  | Takes an `fs.FS` parameter; `main.go` does the actual `go:embed`.                                                                                                                   |
| `internal/mirror`    | Pushes the pairing-token mirror (`id`/`pairing_token_hash`) to the Worker               | Hand-rolled `net/http` client — this is _our own_ Worker, not Cloudflare's control-plane API, so the official SDK doesn't apply here. Holds no password-derived value at any point. |
| `internal/metrics`   | Builds the Prometheus registry served at `/metrics`                                     | Own `prometheus.NewRegistry()`, never the global `DefaultRegisterer`.                                                                                                               |
| `internal/httpapi`   | Dashboard-facing HTTP API: chi router, handlers, health checks, middleware              | See below for the middleware stack specifically.                                                                                                                                    |

### Configuration & CLI

- `cobra` for command structure; `viper` for config — an explicit `--config`
  TOML file (shell completion restricted to `.toml`, no automatic search of
  `$HOME` or the working directory the way cobra-cli's default scaffold does),
  environment variables, and package-level defaults.
- `Execute()` (`cmd/root.go`) owns a single `signal.NotifyContext`
  (`SIGINT`/`SIGTERM`), passed to `rootCmd` via `ExecuteContext`; subcommands
  read it back via `cmd.Context()` rather than each creating their own.
  `cmd/server.go`'s graceful shutdown depends on this — confirmed the context
  reaches a subcommand's `RunE` correctly and that cancellation is what triggers
  `httpServer.Shutdown()`.
- Both Postgres and D1 migrations are applied by the binary itself at startup —
  no external migration CLI needed for either store.
- A new required config value, `pairing_token_key` (`PAIRING_TOKEN_KEY` as an
  env var) — a base64-encoded 32-byte AES-256 key, operator-generated (e.g.
  `openssl rand -base64 32`), used to reversibly encrypt/decrypt each account's
  pairing token for Postgres storage. Not Cloudflare/Terraform-managed, since it
  never leaves the backend's own trust boundary. `config.Load()` fails fast if
  it's missing, same as the other required secrets.

### Database

**Postgres** — `users` and `sessions` tables exist, both using a project-wide
convention adopted this phase: every constraint (PK, unique, check, FK) is
explicitly named (`users_pkey`, `users_role_check`, etc.) rather than left to
Postgres's auto-generated names, so a later `DROP CONSTRAINT` migration can
reference it directly. `sessions` is DB-backed (not stateless signed cookies) —
hashed opaque tokens, same shape as D1's device tokens, 30-day absolute TTL, no
idle-timeout expiry. `users` additionally holds `pairing_token_enc` (nullable
`TEXT`) — the AES-256-GCM-encrypted pairing token, reversible so the dashboard
can redisplay it on demand; `NULL` means no pairing token currently exists
(post-revoke, pre-regenerate).

**D1** — `schema_migrations` (bookkeeping for the backend's own migration
runner) and `users` exist, both `STRICT`; `schema_migrations` is additionally
`WITHOUT ROWID`. D1's `users` table holds only `id` and `pairing_token_hash`
(nullable, `SHA-256` of the pairing token) — no `username`, since pairing is
single-credential (a device submits only the token, never a username), and no
password-derived value of any kind. Named deliberately _not_ `d1_migrations`
(wrangler's own convention) — wrangler is absent from this project's toolchain
entirely; the Worker deploys via Terraform's Cloudflare provider directly, and
D1 migrations run via a direct backend → Cloudflare API call, never
`wrangler d1 migrations apply`.

**Migrations, both stores** — embedded into the binary (`main.go` does the
`go:embed`, since `cmd/server.go` is one directory below both `migrations/` and
`terraform/worker/migrations/` and can't reach either directly).
`internal/pgmigrate` uses goose's `Provider` API specifically (not
`SetBaseFS`/`SetDialect`) — see rough edges. Postgres migrations also take a
Postgres session (advisory) lock for the duration of the run, so two processes
racing to migrate the same database serialize rather than interleave.

### HTTP layer

**Routing** — `chi`, replacing the original stdlib `net/http` pattern routing
once route grouping and middleware composition became real friction. Confirmed
zero transitive dependencies. Routes are nested under an `/api` sub-router
(`r.Route("/api", ...)`), with a session-protected group nested inside that
under `RequireSession`: `/api/auth/me`, and pairing-token management
(`GET`/`POST /regenerate`/`DELETE` on `/api/pairing-token`) — view, regenerate,
and revoke, each of which round-trips through `internal/mirror` to keep D1 in
sync. The dashboard UI for these doesn't exist yet (that's a much later phase),
but the endpoints were built now, alongside the rest of this phase's auth work,
rather than requiring a second pass through `internal/auth`/`internal/httpapi`
later solely for the dashboard's sake.

**Middleware stack** (in order): `httplog.RequestLogger` (structured logging —
already wraps chi's own `RequestID` and `Recoverer` internally, confirmed via
source and by deliberately panicking a handler), `CleanPath`,
`RequestSize(1MB)`, `Timeout(30s)`,
`Compress(5, "application/json", "text/plain")`, `GetHead` — all
global/route-agnostic. `AllowContentType ("application/json")` is scoped to just
the `/api` sub-router, since it's enforcing the JSON API's data contract
specifically, not a protection every route should inherit.

**Health checks** — `/info`, `/ping`, `/health` (module
`github.com/mfinelli/go-healthchecks`, imported as
`go.finelli.dev/healthchecks`), unauthenticated, mounted alongside the API.
`Check` calls a small `Ping` method added to `internal/db.Queries` (a bare
`SELECT 1`), rather than threading the raw pool into `httpapi`.

**Metrics** — `/metrics`, standard Go/process collectors plus a custom
`recueil_users_total` gauge that queries fresh on every scrape (not cached). A
failed collection is logged and simply omitted from that scrape rather than
failing the whole thing.

**Bootstrap-admin flow** — `Setup`'s "already completed" check (`count > 0`)
runs _before_ bootstrap-token validation, so once any admin exists, every
further `/api/setup` call gets `409` regardless of token validity — this is
deliberate (never confirms/denies a submitted token's validity once setup is
done), not a bug, but worth knowing since it means the token-reuse-specific
`401` path is unreachable via a real sequential flow once an admin exists. The
first-admin account created here gets a pairing token generated and mirrored the
same way any other account does (see below) — nothing about the bootstrap path
is a special case for pairing-token purposes.

**Pairing-token lifecycle** — generated automatically whenever an account is
created (bootstrap `/api/setup` and open registration `/api/auth/register` both
go through the same path): a 32-byte CSPRNG value (`GeneratePairingToken`),
AES-256-GCM-encrypted for the Postgres row (`EncryptPairingToken`), and its
`SHA-256` hash pushed to the D1 mirror via `internal/mirror.PushUser`.
`GET /api/pairing-token` decrypts and returns the current value (redisplay, not
show-once, since losing this credential shouldn't force a regenerate the way
losing a session token would). `POST /api/pairing-token/regenerate` issues a new
one, overwriting both copies. `DELETE /api/pairing-token` clears the Postgres
value to `NULL` and pushes a JSON `null` to the mirror endpoint, which the
Worker treats as "clear the mirrored hash" — blocking further pairing until a
regenerate, without affecting any bearer tokens a device already obtained.

### Testing

- `testify` throughout, table-driven where it reduces duplication.
- DB-touching code is tested against a **real** Postgres instance via
  `internal/dbtest`, never mocks — this is a stated project philosophy, not a
  per-package choice.
- Code that calls an external HTTP API is tested against a real
  `httptest.Server` plus that library's own base-URL override where one exists,
  rather than a hand-rolled interface mock.
- `internal/httpapi` and `internal/metrics` tests are external `_test` packages
  deliberately (exercise only exported constructors, same as a real caller
  would); `internal/auth`'s tests are internal (`package auth`) deliberately,
  since they need real access to unexported internals (`cookieName`,
  `userContextKey`, the bootstrap holder's private fields) to prove the mutex
  and consume-only-on-success logic actually hold.
- `internal/httpapi`'s pairing-token tests register a real account through the
  actual HTTP flow (rather than `dbtest.CreateUser`'s placeholder fixture) and
  verify that the token the dashboard decrypts actually hashes to what was
  pushed to a mock Worker — end-to-end consistency between the Postgres and D1
  copies, not just that each side independently does something plausible.
- `testcontainers-go` was evaluated for the Postgres test harness and
  **rejected** — its dependency tree (full Docker API client, containerd,
  OpenTelemetry, `gopsutil`) is heavier than anything else in this project,
  including Viper. Went with a dedicated `docker-compose.test.yml` instead.

### Decisions worth remembering for later phases

- **Bootstrap token is in-memory, never persisted to Postgres.** This replaced
  an earlier persisted-table design that had a real bug (a restart before use
  left the _previous_ token silently valid). Assumes exactly one backend process
  — already implicit elsewhere (§5a's service-secret rotation reasoning), but
  this makes it a hard constraint for this specific flow.
- **Pairing-token encryption key rotation is a real operational hazard, not just
  a config value to set once.** Rotating `PAIRING_TOKEN_KEY` makes every
  already-encrypted `pairing_token_enc` value permanently undecryptable —
  equivalent to simultaneously revoking every account's pairing token. Not
  currently guarded against in code; worth a startup sanity check or at least
  loud documentation before this bites someone.
- **`internal/dbtest`'s migration path is anchored via `runtime.Caller(0)`**,
  not a caller-relative path like `"../../migrations"` — confirmed correct from
  a test package three directories deeper than any real caller. Anything that
  copies this pattern should keep that anchoring, not revert to a relative path
  that happens to work today.
- **OpenTelemetry (distributed tracing) was considered and deliberately
  deferred, not rejected.** The core SDK is light, but any real exporter —
  confirmed even OTLP-over-HTTP, not just gRPC — drags in `grpc-go`'s full tree.
  The bigger reason: this project's call graph is still too shallow (one backend
  process, Postgres, occasional Worker calls) for distributed tracing's value
  proposition to apply. Revisit once the screenshot service and AI enrichment
  (§6, §7) form a genuine async multi-stage pipeline — that's the shape where
  it'll actually pay off.
- **`RealIP` and `pprof` middleware were both considered and deliberately not
  added.** `RealIP` is a spoofing risk without a trusted reverse proxy in front,
  and this project treats network exposure as entirely the operator's choice.
  `pprof` leaks sensitive runtime info and needs its own deliberate gating
  decision, not a default mount alongside health checks.
- **A new capture pathway — manual upload — is designed but not yet
  implemented** (design doc §3d): dashboard-direct upload of an already-captured
  SingleFile HTML file, bypassing R2/D1/Worker entirely. Needs its own, much
  larger `RequestSize` override scoped to that one route — the global 1MB cap
  would break it immediately, since SingleFile archives with inlined images
  routinely run tens of megabytes. Adds `captures.source` (`'extension'` |
  `'manual_upload'`) to the schema.

### Known rough edges / bugs found and fixed this phase

- **Viper defaults registered in the wrong package.** `SetDefault` calls living
  in `cmd/root.go`'s `init()` don't apply when `config.Load()` is called by a
  test or a different binary that never imports `cmd`. Fixed by moving the
  `SetDefault` calls into `internal/config`'s own `init()`.
- **goose's package-level `SetBaseFS`/`SetDialect` genuinely race under
  concurrent calls** — confirmed with `-race`: two goroutines calling them
  simultaneously race immediately, even when setting identical values. Motivated
  the switch to goose's `Provider` API, which is documented safe for concurrent
  use (confirmed: 8 concurrent `Run()` calls against the same pool, zero race
  warnings).
- **`CleanPath` placed before `RedirectSlashes` makes `RedirectSlashes` inert.**
  `CleanPath`'s `path.Clean()` silently strips a trailing slash into chi's
  internal `RoutePath` before `RedirectSlashes` ever sees one — confirmed via a
  real HTTP test (a `POST` to a trailing-slash route variant hits the handler
  directly, no visible redirect, same method). Resolved by dropping
  `RedirectSlashes` entirely rather than keeping inert middleware around — a
  silent internal normalization is the safer behavior for a JSON API regardless.
- **An earlier iteration of device authentication mirrored the account's bcrypt
  password hash into D1** for the Worker to verify at pairing time. Abandoned
  before it saw any real traffic: bcrypt costs 100-300ms even natively,
  Cloudflare Workers cap free-tier CPU time at 10ms per request, and there's no
  native bcrypt in the `workerd` runtime regardless. Replaced with the per-user
  pairing token described throughout this document — see DESIGN.md §5 for the
  full comparison against the Worker-native-fast-hash alternative that was also
  considered and rejected.
