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
password-derived value of any kind. Explicitly _not_ `d1_migrations` (wrangler's
own convention) — wrangler is absent from this project's toolchain entirely; the
Worker deploys via Terraform's Cloudflare provider directly, and D1 migrations
run via a direct backend → Cloudflare API call, never
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
  (exercise only exported constructors, same as a real caller would);
  `internal/auth`'s tests are internal (`package auth`), since they need real
  access to unexported internals (`cookieName`, `userContextKey`, the bootstrap
  holder's private fields) to prove the mutex and consume-only-on-success logic
  actually hold.
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
- **`RealIP` and `pprof` middleware were both considered and not added.**
  `RealIP` is a spoofing risk without a trusted reverse proxy in front, and this
  project treats network exposure as entirely the operator's choice. `pprof`
  leaks sensitive runtime info and needs its own gating decision, not a default
  mount alongside health checks.
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

## Phase 2 (Worker Device Auth + Queue)

### What exists now

The Worker (`terraform/index.js`) now has a real, tested endpoint surface beyond
the Phase 1 credential mirror: device pairing, the enqueue/read/claim queue
endpoints, device-token management, and a queue-item cleanup sweep. All of it
operates purely between a device (or the backend, for the service-secret-gated
endpoints) and D1 — **the backend still never touches `queue_items` directly**,
consistent with DESIGN.md §2's "capture path never touches the backend"
property. This is worth stating plainly since it's an easy thing to assume
backwards: it's the _desktop extension_ (or whatever device polls) that claims
queue items using its own bearer token, not the backend using the service
secret.

| Endpoint                               | Auth                         | Notes                                                                                           |
| -------------------------------------- | ---------------------------- | ----------------------------------------------------------------------------------------------- |
| `POST /pair`                           | none (pairing token in body) | Exchanges a pairing token for a device bearer token. Single-credential — no username submitted. |
| `POST /queue`                          | device bearer token          | Enqueue. `id` is client-generated; idempotent retry via `ON CONFLICT(id) DO NOTHING`.           |
| `GET /queue`                           | device bearer token          | Lists this user's pending + stale-claimed items. Never claims.                                  |
| `POST /queue/:id/claim`                | device bearer token          | Atomic conditional `UPDATE ... RETURNING`. Where the actual claim race is resolved.             |
| `GET /internal/tokens?user_id=`        | service secret               | List a user's paired devices.                                                                   |
| `DELETE /internal/tokens/:id?user_id=` | service secret               | Revoke one device. Scoped by `user_id` as well as `id` — see below.                             |
| `POST /internal/queue-items/cleanup`   | service secret               | Deletes old `captured` queue items. Not scoped to a user (maintenance sweep).                   |

### Claim failure semantics

A failed claim (`POST /queue/:id/claim` matching zero rows) distinguishes three
cases rather than a uniform `409`, decided during this phase:

- **`404`** — wrong id, or the item belongs to a different user (collapsed
  together so that a claim attempt never leaks cross-user existence).
- **`410`** — the item is `captured` or `failed`: a terminal state, permanently
  no longer claimable. More precise than a bare 404 for "this happened, and it's
  over."
- **`409`** — actively claimed by another device, claim not yet stale: a
  genuine, temporary conflict worth retrying.

Distinguishing these costs one extra `SELECT`, but only on the failure path — a
successful claim is still a single round trip.

### Queue item cleanup

Nothing in the original design removed a terminal-state `queue_items` row —
surfaced only once real implementation made it obvious the table would otherwise
grow unboundedly. `POST /internal/queue-items/cleanup`:

- Deletes only `captured` rows, older than a 72-hour retention window.
- Never touches `failed` rows, at any age — kept indefinitely for now. What to
  do about them long-term (surface to the user, retry, a separate/longer expiry)
  is an open question, tracked in DESIGN.md §15, not decided here.
- Uses `claimed_at`, not `created_at`, as the retention clock — a pragmatic
  proxy for "when did this actually finish," since there's no dedicated
  completion timestamp on `queue_items` yet. Good enough at this project's scale
  (claim-to-capture is seconds to minutes); a one-line filter change if a future
  phase adds a real `completed_at`.
- Called on the backend's own schedule (once or twice a day), not a Cloudflare
  Cron Trigger — same "keep the Worker dumb, let the backend own scheduling"
  reasoning as the visibility-timeout reclaim (§8).

### D1 schema additions this phase

```sql
-- terraform/migrations/0002_create_tokens.sql
CREATE TABLE tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash TEXT NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id),
  device_name TEXT NOT NULL,
  device_type TEXT NOT NULL,        -- 'extension' | 'pwa' | 'cli'
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TEXT
) STRICT;
CREATE INDEX idx_tokens_user_id ON tokens(user_id);

-- terraform/migrations/0003_create_queue_items.sql
CREATE TABLE queue_items (
  id TEXT PRIMARY KEY,              -- client-generated UUID
  user_id INTEGER NOT NULL REFERENCES users(id),
  url TEXT NOT NULL,
  added_by_token_id INTEGER REFERENCES tokens(id),
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | claimed | captured | failed
  claimed_by_token_id INTEGER REFERENCES tokens(id),
  claimed_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT, WITHOUT ROWID;
CREATE INDEX idx_queue_items_user_status ON queue_items(user_id, status);
CREATE INDEX idx_queue_items_added_by_token_id ON queue_items(added_by_token_id);
CREATE INDEX idx_queue_items_claimed_by_token_id ON queue_items(claimed_by_token_id);
```

`STRICT`/`WITHOUT ROWID`/index conventions match Phase 1's tables, applied here
as the doc's own stated intent ("the rest of this section's tables will pick up
the same convention as they're implemented").

### Testing

- `@cloudflare/vitest-pool-workers` against real Miniflare D1 throughout, same
  as Phase 1's Worker tests — no mocks. New files: `handlePair.test.js`,
  `queue.test.js` (enqueue/list/claim/cleanup — one file, since they share the
  same table and seed helpers), `internal-tokens.test.js`, plus a shared
  `test-helpers.js` (a test-only `sha256Hex`, since `index.js`'s own version is
  unexported and there's no reason to export an internal just for tests to reach
  it).
- `fetch.test.js` gained routing-level tests through the real dispatcher
  (`SELF.fetch`), not just direct handler calls — the unit tests call handlers
  directly and bypass the regex-based path matching entirely, so the actual
  `:id` extraction in `/queue/:id/claim` and `/internal/tokens/:id` needed its
  own coverage (malformed paths, missing segments, extra trailing segments).
- **Confirmed empirically, not assumed**: D1 supports `RETURNING` on both
  `INSERT` (`POST /pair`'s token creation) and `UPDATE` (the claim endpoint) —
  this wasn't certain going in and is now verified against real Miniflare D1,
  not just Cloudflare's docs. Also confirmed for real: the actual claim race
  (two tokens racing for the same item — first wins, second gets `409`), the
  stale-claim reclaim, and that a wrong-user claim attempt genuinely never
  touches the other user's row (not just that it returns the right status code).

### Decisions worth remembering for later phases

- **The backend never touches `queue_items`.** Worth restating since it's easy
  to assume otherwise: enqueue/read/claim are entirely device ↔ Worker, using
  the device's own bearer token. The backend's only queue-adjacent
  responsibility is the service-secret-gated cleanup sweep, which doesn't read
  or claim anything — it only deletes terminal rows past their retention window.
- **`DELETE /internal/tokens/:id` requires `user_id` as a safety net, beyond
  what the original design called for.** The Worker still doesn't know about
  roles (the backend enforces admin-vs-self scoping before ever calling this
  endpoint), but requiring `user_id` to match the token's actual owner means a
  backend-side bug that passes the wrong id/user_id pair deletes nothing rather
  than someone else's device.
- **`complete`/`fail` are not built yet.** The brief for this phase was device
  auth + queue read/write; the endpoints that would actually transition a
  claimed item to `captured`/`failed` and write a `pending_captures` row are
  entangled with the capture-upload pipeline's shape (presigned R2 URLs, the
  upload-complete notification) rather than the queue/auth mechanics this phase
  covered — deferred to the phase that builds that pipeline.
- **What to do with `failed` queue items long-term is unresolved.** The cleanup
  endpoint only ever sweeps `captured` rows; `failed` rows accumulate
  indefinitely until some future decision (surface to the user? retry? a
  separate, longer expiry?) — tracked as open in DESIGN.md §15, not decided
  here.

## Phase 3 (Capture Upload Pipeline + Backend Ingestion)

Phase 3's original brief was three pieces: a CLI (enqueue-only), a throwaway
fake-extension script proving the R2/D1/Postgres pipeline end-to-end, and
whatever Worker/backend plumbing those two needed to actually work against. The
fake extension was explicitly carved back out during closeout, not just deferred
as leftover phase work — with everything else in this phase actually built and
tested, a throwaway script whose only job is proving already-tested plumbing
works felt like lower value than moving on, and it's genuinely its own scope
(the real extension is a substantial piece of work in its own right, per
DESIGN.md's original phased plan). It becomes its own dedicated future phase
instead. Everything else landed: the presigned upload endpoints, a real tested
backend ingestion pipeline, the D1 bookmark-list mirror, the `recueil agent`
trigger mechanism, and the CLI (`auth`/`enqueue`).

### What exists now

**Worker (`terraform/index.js`)**, beyond Phase 2's queue/device-auth surface:

| Endpoint                                      | Auth                | Notes                                                                                                       |
| --------------------------------------------- | ------------------- | ----------------------------------------------------------------------------------------------------------- |
| `POST /captures/upload-urls`                  | device bearer token | Presigned R2 PUT URL for a capture's single HTML object, keyed by `pending/{userId}/{captureId}/page.html`. |
| `POST /queue/:id/complete`                    | device bearer token | Writes the `pending_captures` row, marks the queue item `captured`. Content-hash-checksum-bound (below).    |
| `POST /queue/:id/fail`                        | device bearer token | Marks a claimed queue item `failed`. Same 404/410/409 semantics as claim.                                   |
| `GET /internal/pending-captures?limit=`       | service secret      | Backend's ingestion poll: unfetched captures, oldest first, bounded (default 50, max 200).                  |
| `POST /internal/pending-captures/:id/fetched` | service secret      | Marks a `pending_captures` row as pulled/ingested.                                                          |

Presigned uploads use hand-rolled SigV4 (`crypto.subtle`, zero dependencies —
the Worker's own no-build-step/no-dependency constraint still applies) living
**inline in `index.js`, not a separate module**: `cloudflare_workers_script`
turned out to have no multi-module upload support at all, so a separate
`r2-presign.js` file would simply never have been deployed. Verified against
AWS's own published SigV4 test vector, and separately cross-validated against
the official `@smithy/signature-v4` signer (a real, pinned devDependency, never
shipped) for the actual R2-shaped request — both checks are permanent parts of
the test suite now, not one-off scratch verification.

Every upload is also bound to a `x-amz-checksum-sha256` value (R2's "Flexible
Checksums" feature) computed from the exact bytes about to be uploaded — every
capture path always has the content in hand before requesting a presigned URL,
so there's no legitimate case for skipping this. Worth being precise about which
SigV4 mechanism does what: `x-amz-content-sha256` (the payload-hash _signing_
input) stays the literal `UNSIGNED-PAYLOAD`, matching R2's own documented
convention — it was never the right lever for content integrity.
`x-amz-checksum-sha256` is the actual, separate mechanism R2 verifies the real
uploaded bytes against.

**`internal/urlnorm`** — a `Pipeline` of composable `Step`s (string in, string
out), not a single hardcoded function, since ClearURLs is meant to be the first
entry, not the only one:

- `ClearURLs` — a Go port of the real ClearURLs extension's own algorithm
  (`pureCleaning`/`_cleaning`/`removeFieldsFormURL`), verified line-by-line
  against the actual upstream JS source, not inferred from the ruleset's own
  documentation. Uses `github.com/dlclark/regexp2` (stdlib RE2 can't compile
  some patterns the real ruleset relies on). The ruleset (`data.min.json`) is
  vendored as a git submodule at `internal/urlnorm/clearurls-rules` — inside the
  package that uses it, embedded directly as `[]byte` via `go:embed`
  (`//go:embed clearurls-rules/data.min.json`), not threaded through
  `main.go`/`cmd` the way the Postgres/D1 migration directories are, since
  nothing outside this package ever needs it. `completeProvider` and
  `forceRedirection` are not ported at all — see DESIGN.md §9 for why neither
  applies to normalizing an already-known URL string.
- `Canonicalize` — host/scheme lowercasing, default-port stripping (including
  correct IPv6 bracket handling), fragment dropping, query-param sorting,
  trailing-slash stripping.

**`internal/r2`** — the backend's own R2 client (distinct from the Worker's
presign-only access): `aws-sdk-go-v2`'s real S3 client, `UsePathStyle: true` set
explicitly rather than trusting the SDK's virtual-host-by-default resolution
(R2's actual addressing puts the bucket in the path, confirmed while building
the Worker's own presigner). Reuses the same manually- provisioned R2 API token
as the Worker — not a second credential.

**`internal/archive`** — the local, canonical, zstd-compressed disk store.
Sharded paths (`{key[0:2]}/{key[2:4]}/{key}.html.zst`, git's own object-store
scheme), atomic temp-file-then-rename writes. **Keyed by `content_hash`, not
capture ID** — a real bug caught mid-session: two captures colliding on a
capture ID would also collide on an ID-keyed disk path, and the atomic rename
silently overwrites whatever's already there, which would have corrupted an
unrelated, already-successfully-stored capture's file. Keying by content hash
means a "collision" can only happen for genuinely byte-identical content, where
overwriting with identical bytes is a harmless no-op.

**`internal/ingest`** — the actual orchestration:

- `WorkerClient` — the two service-secret-gated polling endpoints above.
- `Ingester.RunOnce(ctx) error` — processes one bounded batch. **No scheduler
  wired up yet** — deferred (see Open items below); this is a fully callable,
  tested unit with nothing calling it in production yet.
- Per-capture flow: pull from R2 → hash → zstd-compress to local disk → detect
  language → normalize URL (via `internal/urlnorm`) → one Postgres transaction
  (upsert page, insert capture, enqueue `screenshot_jobs`/ `readability_jobs`
  rows if genuinely new) → delete the R2 object → mark fetched in D1 → clear the
  transient `source_capture_id`.
- Language detection: parses the captured HTML's own `<html lang="...">`
  attribute, maps the primary subtag (ISO 639-1) to a candidate Postgres text
  search config name via a small hardcoded table, then validates that candidate
  against **this specific instance's live `pg_ts_config` catalog** rather than
  trusting the Go-side table as authoritative (which configs exist depends on
  the running Postgres version). Falls back to `simple` whenever there's no tag,
  no mapping, or the candidate doesn't actually exist.
- Title: parsed server-side from the raw HTML's `<title>` tag, uniformly for
  every capture. Worth noting plainly — this is a real gap between an earlier
  design assumption and what actually got built: SingleFile's own
  `getPageData()` return includes a title (DESIGN.md §3a), but nothing in the
  built `POST /queue/:id/complete` request body ever carries it through to the
  Worker/D1. Parsing it at ingestion is the one real source of truth today, not
  a fallback.

**New Postgres migrations** (`00003`–`00006`): `pages`, `captures`,
`screenshot_jobs`, `readability_jobs` — see DESIGN.md §10 for the schema itself;
nothing here that isn't already documented there.

### `source_capture_id`: three revisions before landing correctly

Worth its own writeup since it went through real back-and-forth this session and
the final shape isn't obvious from a first read of the code:

1. **First cut**: `NULL` for manual uploads (no client ID to use), a real value
   otherwise, `UNIQUE` but nullable.
2. **Second cut**: made `NOT NULL` — reasoning at the time was "every capture
   should have a real, uniform identity." This briefly shipped and was wrong: it
   didn't account for what a _conflict_ on that column actually means.
3. **Final, current shape**: nullable again, but now genuinely _transient_ —
   populated while a capture is actively in flight, **cleared back to `NULL`
   once ingestion is fully done** (R2 object deleted _and_ D1's
   `fetched_by_backend` flag confirmed set). Two problems, previously conflated,
   are now handled explicitly and separately:
   - **A retry must not fail forever re-fetching an already-deleted R2 object.**
     Fixed by a fallback check (query Postgres for an already-committed row)
     that runs — critically — only _after_ the full pipeline attempt fails,
     never as an upfront gate. An upfront pre-check-before-R2-Get was tried
     first and rejected: it bypasses the content-hash comparison below entirely,
     silently discarding a genuine collision's data instead of catching it. This
     was self-caught by tracing the collision test against the new code before
     it was ever presented, not caught externally.
   - **A conflict on insert must not be assumed to mean "retry."** It could be a
     genuine collision — two different captures sharing an ID (astronomically
     unlikely for a random UUID, not impossible). Resolved via `content_hash`
     comparison: matching hash means legitimate retry (no-op); mismatched hash
     means real collision, and a fresh UUID is generated and the insert retried
     (bounded, `github.com/google/uuid`).
   - Manual upload (not yet built) needs no separate insert logic to fit this —
     same content-hash-based handling, just starting with a backend-generated
     candidate ID instead of a client-supplied one.

### Testing

- `sqlc.yaml` needed an explicit type override (`db_type: "regconfig"` →
  `go_type: "string"`) — without it, sqlc falls back to `interface{}` for a
  Postgres type it has no built-in mapping for.
- `sqlc`'s own schema analysis only understands tables defined in our
  migrations, not Postgres system catalogs — a query against `pg_ts_config` was
  flatly rejected ("relation does not exist"). The live language-config
  validation is therefore a small hand-written query against the raw pool, not a
  generated one.

### Closeout dispositions

Phase 3 is closed with the items below explicitly triaged, not left as an
undifferentiated pile of "todo" — each has a real disposition, decided
deliberately rather than by default.

**Carved out into its own future phase, not deferred as Phase 3 leftover:**

- **The fake extension script** (pair → claim → presigned upload → complete) —
  see the closure note above. Nothing has exercised the R2/D1/Postgres pipeline
  end-to-end against a real deployed Worker yet; everything that exists today is
  proven via tests against fakes/`dbtest` only. The real browser extension is a
  substantial piece of work in its own right and deserves a dedicated phase, not
  a rushed throwaway script squeezed into this one's closeout.

**Explicitly deferred — will resolve or revisit in a later phase:**

- **`docker-compose.yml` still doesn't exist** for any service. Not built yet:
  local development currently uses a personal `compose.yaml` and the binary run
  directly, and the real, end-user-facing compose file will get built alongside
  end-user documentation, so the two stay consistent with each other rather than
  needing to be reconciled later.
- **`failed` queue items' long-term retention** — unresolved since Phase 2; the
  cleanup endpoint only ever sweeps `captured` rows. Still open, not forgotten.
- **Fragment-aware URL canonicalization for known SPAs** —
  `urlnorm.Canonicalize` drops every URL fragment unconditionally; the "unless
  it's a known SPA with meaningful route state" exception from DESIGN.md §9 has
  no implementation and no site list to check against yet.

**Explicitly won't-do — reconsider only if it becomes a real, felt problem:**

- **A `--url` override flag on `recueil enqueue`.** There's no supporting
  machinery on the `auth` side (no multi-profile concept, nothing to override
  _to_), so the flag would just be confusing rather than useful — see DESIGN.md
  §3f. If real multi-server support is ever wanted, it's a clean, additive
  change later (rename the credentials file, add a `--profile` flag), not
  something worth a half-measure now.
- **Postgres `LISTEN`/`NOTIFY`** for faster job pickup, layered on top of
  `recueil agent`'s poll loop. Discussed during the agent's design (DESIGN.md
  §3e) and explicitly set aside: plain polling is entirely sufficient at this
  project's personal-archive scale, and there's no felt latency problem this
  would actually be solving right now.

### `recueil agent` — the trigger mechanism, resolved

What triggers `Ingester.RunOnce`/`Syncer.SyncOnce` was the one genuinely open
design question left over from the ingestion and mirror-sync work above — see
DESIGN.md §3e for the full reasoning (a dedicated process over a goroutine or
cron, Postgres over RabbitMQ/Redis as the coordination layer). Landed as
`cmd/agent.go`: a new `recueil agent` subcommand, ticker-driven
(`agent_poll_interval_seconds`, default 120), running both `RunOnce` and
`SyncOnce` sequentially each tick, deployed as a separate process/container from
`server` sharing the same image and config.

Also fixed while wiring this up, unrelated to the agent itself but a real gap:
several config keys added earlier this phase (`pairing_token_key`,
`archive_dir`, all four `r2_*` keys) were never added to `cmd/root.go`'s
`bindEnvOrPanic` list. `internal/config`'s own tests never would have caught
this — they exercise `Load()` via `viper.Set()` directly, which works regardless
of binding, so the gap was invisible to every test that existed until something
needed to actually read these from a real environment variable in production.

### `recueil auth` / `recueil enqueue` — the CLI, resolved

See DESIGN.md §3f for the full design reasoning. Landed as flat files in `cmd/`
(`auth.go`, `enqueue.go`), matching `server.go`/`agent.go`'s existing convention
rather than the stale `cmd/cli/` subdirectory an earlier revision of DESIGN.md's
repo tree assumed.

Two new packages: `internal/clicreds` (XDG-located credentials file,
atomic-write, storing `worker_url` alongside the token as one unit since a token
is only ever meaningful for the Worker that issued it) and `internal/deviceapi`
(`Pair` as a standalone unauthenticated function, `Client.Enqueue` as the
authenticated counterpart — kept separate rather than one unified type, since
pairing is how a device obtains the credential `Client` needs in the first
place). Nothing needed adding to the Worker/D1 schema at all:
`tokens.device_name`/`device_type` (already including `'cli'`) and
`POST /pair`'s handling of both already existed from Phase 2 — this phase's
actual gap was entirely CLI-side.

`server`/`agent`'s existing config behavior (explicit `--config`/env only, no
automatic discovery) is completely untouched — `auth`/`enqueue` don't use
`internal/config`/Viper at all, reading everything from the `internal/clicreds`
file instead.

### Post-closeout addition: per-page mirror exclusion

Landed after Phase 3's initial closeout, ahead of Phase 3½'s favicon work
proper. `pages.excluded_from_mirror BOOLEAN NOT NULL DEFAULT FALSE` — lets a
page be opted out of the D1 bookmark-list mirror (§8). The existing migration
(`00003_create_pages.sql`) was modified in place rather than adding a new one,
since nothing has shipped yet.

No D1 schema change and no changes to `internal/mirror/sync.go`'s actual logic
were needed — exclusion falls out entirely from two query-level filters:
`GetPagesUpdatedSince` (incremental upsert) now excludes these pages outright,
and the renamed `GetMirrorEligiblePageIDs` (formerly `GetAllPageIDs`; the old
name would have been misleading once it stopped returning literally all page
IDs) redefines deletion reconciliation's Postgres-side "desired set" to also
exclude them — so a page excluded after already being synced is
indistinguishable, from that pass's point of view, from one that was deleted
outright. Both are just "no longer in the desired set," handled by the same
existing diff-and-delete code.

No toggle endpoint yet — the column and query-level filtering exist now, but
setting the flag has no caller until the dashboard (Phase 5) exists to expose
it.

---

## Phase 3½

Backend/Worker-side groundwork for favicon capture, built and tested the same
way the rest of Phase 3 was — against real Postgres (`dbtest`) and real
Miniflare D1, with fakes standing in for R2/the extension, since the real
extension still doesn't exist yet (that's next). See DESIGN.md §3g for the full
design writeup; this is the "what actually landed" companion to it.

### `internal/archive`: restructured around per-capture directories

`Store.Write` (one method, HTML-only, keyed by `content_hash`) split into two:

- `WriteHTML(htmlHash, data)` — same content-hash keying as before, but the
  sharded directory (`CaptureDir(htmlHash)`, now exported) holds a fixed
  filename (`page.html.zst`) rather than baking the hash into the filename
  itself, since the directory already encodes it.
- `WriteAsset(htmlHash, assetHash, ext, data, compress)` — everything else
  belonging to a capture (favicon today, a screenshot later) lives in that same
  directory, but named by _its own_ content hash, not `htmlHash`. This is
  load-bearing, not a style choice: two captures can share byte-identical HTML
  while carrying different favicons (a static page recaptured after the site's
  icon changed), so keying a secondary asset by the html hash would silently
  reintroduce the exact same-key-different-content overwrite bug
  `content_hash`-keying exists to avoid in the first place — just one level
  removed. `compress` is per-call: SVG gets zstd'd, PNG/ICO (already compressed
  binary formats) are stored raw.
- `Open` now infers compression from a `.zst` path suffix instead of always
  assuming it, since not everything in the store is compressed anymore.

### Schema

- Postgres: `captures.favicon_path TEXT` (nullable) — populated _synchronously_
  at ingestion (unlike `thumbnail_path`, which is async), and never cleaned up
  or mutated afterward, since captures are immutable history.
  `pages.favicon_path TEXT` (nullable) — denormalized from the latest capture
  exactly the way `pages.title` already is, including overwriting back to `NULL`
  if the latest capture didn't find one. `UpsertPage` and
  `InsertCaptureIdempotent` both updated accordingly; both existing migrations
  (`00003`, `00004`) modified in place, same as the mirror-exclusion change
  above — nothing's shipped yet.
- D1: `pending_captures.r2_key_favicon TEXT` (nullable), existing migration
  (`0004_create_pending_captures.sql`) modified in place. The real file
  extension lives in the key itself (`.../favicon.svg` vs `.../favicon.png`)
  rather than a separate mime/type column — the backend recovers it by reading
  the key back (`filepath.Ext`) at ingestion, the same way `page.html`'s
  extension was always implicit.

### Worker (`terraform/index.js`)

- `POST /captures/upload-urls` accepts an optional
  `(favicon_ext, content_sha256_favicon)` pair — both present or both absent, a
  half-specified request is rejected outright, not silently treated as "no
  favicon." When present, issues a second presigned PUT at a deterministic key
  (`faviconObjectKey`, mirroring `captureObjectKey`) and returns
  `upload_url_favicon`/`r2_key_favicon`/`required_headers_favicon` alongside the
  existing HTML fields. `favicon_ext` is validated against a fixed set
  (`svg | png | ico`, `FAVICON_EXTENSIONS`) matching DESIGN.md §3g's selection
  scheme.
- `POST /queue/:id/complete` accepts an optional `favicon_ext`; the Worker
  recomputes the deterministic favicon key itself (never trusts a
  client-supplied key, same posture `r2_key_html` already has) and writes it
  into the new `pending_captures` column.
- `GET /internal/pending-captures` includes `r2_key_favicon` in its `SELECT` —
  no other change needed, it was already a raw passthrough of the row.

### `internal/ingest`

- `PendingCapture.R2KeyFavicon *string` — nil whenever the extension didn't
  upload one.
- New `Ingester.captureFavicon`: pulls the favicon object from R2 (if any),
  hashes it, derives its extension from the R2 key, and writes it via
  `Store.WriteAsset` alongside the HTML in the same capture directory.
  **Deliberately never returns an error** — a fetch, read, or disk-write failure
  here is logged and the capture proceeds with `favicon_path` left empty, since
  a favicon is cosmetic and an unreachable/malformed one is never a reason to
  lose an otherwise-good HTML capture.
- `processOne`'s R2 cleanup pass deletes the favicon object alongside the HTML
  one when present, best-effort (a delete failure there is logged, not
  propagated — the object is already durably stored locally or wasn't, either
  way R2's copy was never canonical).
- `favicon_path` threaded through `writeInput` into both `UpsertPage` and
  `InsertCaptureIdempotent` via the same `textOrNull` helper `title` already
  uses (empty string → `NULL`).

### `recueil user` — operator account management (post-closeout addition)

- New `cmd/user.go`: `recueil user create <username> [--role admin|member]` and
  `recueil user reset-password <username>`, both direct-to-Postgres CLI commands
  for operators — motivated by needing a way to create a real test account for
  the extension work before there's a dashboard to do it through. See DESIGN.md
  §5 "Account creation and roles" for the full rationale.
- Both reuse existing pieces rather than duplicating handler logic:
  `config.Load()` for the same config `recueil server` reads, `pgmigrate.Run`
  (idempotent, so the command works even before `server` has ever started
  against a fresh database), and the same `auth`/`db`/`mirror` calls
  `Setup`/`Register`/`RegeneratePairingToken` already use.
- `user create` pushes the pairing token to D1 via `mirror.PushUser` so it's
  immediately usable for device pairing, not just dashboard login — a push
  failure is logged as a warning, not a hard error, matching the existing
  posture in `RegeneratePairingToken`.
- `user reset-password` calls `DeleteSessionsForUser` after updating the
  password hash — the first real caller of that query, which existed in
  `queries/sessions.sql` unused until now.
- Password entry (`readNewPassword`, shared by both commands): masked, confirmed
  twice on a real TTY; falls back to a single unconfirmed line from stdin
  otherwise, so both commands stay scriptable.
- Username is a positional arg (not a flag) on both commands; `--role` remains a
  flag on `create`, defaulting to `member`.

### Browser Integrity Check bypass (post-closeout addition)

Carried over from the Python glue script this project's CLI replaced, which had
hit the same problem against a different zone: Cloudflare's Browser Integrity
Check (BIC) tends to flag non-browser Go HTTP clients and drop their requests
before they reach the Worker. See DESIGN.md §5c for the full writeup; landed
this round:

- `internal/deviceapi`, `internal/mirror`, and `internal/ingest.WorkerClient`
  each set `User-Agent: recueil/1.0` on every outbound request (one
  package-local `const userAgent`, not a shared package — only a handful of call
  sites, so a new package wasn't worth it). The browser extension is untouched;
  its requests already carry a real browser's User-Agent and TLS fingerprint.
- New `terraform/waf.tf`: a `cloudflare_ruleset`
  (`browser_integrity_check_bypass`) that skips BIC for that User-Agent, gated
  by `var.enable_browser_integrity_check_bypass` (default `true`).
- The User-Agent string is a fixed `1.0` protocol constant, not the CLI/
  backend's real release version — intentionally not threaded through from
  `cmd`'s ldflags-injected `Version`, to avoid coupling every app release to a
  coordinated `terraform apply` for an exact-match WAF expression.

---

## Phase 5 (the real extension) — in progress

### What actually works end to end

Pairing, direct capture (including embedded-iframe inlining — see DESIGN.md
§3h), favicon capture, upload to R2, and backend ingestion via `recueil agent`
have all been confirmed working together against a real deployed Worker and a
real Postgres instance — not just unit-tested in isolation. A captured page
shows up as a real row in `captures` with a real `favicon_path`, the same as if
it had come through any other path.

Concretely, what's built in `extension/`:

- **Scaffolding**: `manifest.base.json` + per-browser overlays, `build.js`
  (esbuild, three independent bundles — background/capture-inject/popup, see
  DESIGN.md §3h for why not one), `package.js` (produces real `.xpi`/`.crx`
  files via `web-ext`/`crx3` — neither installs _durably_ without further steps,
  see `extension/README.md`).
- **Auth** (`background/auth.js`): pairing against `POST /pair`'s real contract,
  `storage.local` (never `storage.sync`) for the device token, `getAuthState()`
  never returns the token itself to a caller.
- **Capture** (`background/capture.js`, `capture-inject/`): the two-step
  injection pattern, `single-file-core` wired with the direct-fetch-first relay
  (`relay-fetch.js`, see DESIGN.md §3h), embedded-iframe inlining
  (`allFrames: true` injection + `background/frame-tree-relay.js`, see DESIGN.md
  §3h), favicon selection (`favicon.js`), all glued together in
  `captureTab`/`captureActiveTab`.
- **Upload orchestration**: `POST /captures/upload-urls` → R2 PUT(s) →
  `POST /captures/complete` — the same direct-capture endpoint added earlier
  this phase, now with a real caller.
- **Popup UI** (`popup/`): pairing form (with draft-state persistence across the
  popup's own forced teardown on blur, and a computed `defaultDeviceName`
  placeholder) and a "Save this page" button — deliberately unstyled, a second
  pass once the UI's actual shape has stopped moving.
- **Extension test suite**: a new `"extension"` vitest project (jsdom
  environment), 80 tests across `favicon.js`, `hash.js`, `relay-fetch.js`,
  `storage.js`, `api-client.js`, `auth.js`, `device-name.js`, and
  `frame-tree-relay.js`. `device-name.js` caught a real bug (iOS user agents
  misdetected as macOS, since iOS UAs always include "like Mac OS X" as a
  compatibility string and the OS check order didn't account for that);
  `frame-tree-relay.js` pins the relay's forwarding, its `Promise.resolve`
  response, and that it leaves non-frame-tree messages for the other background
  listeners.
- **Type checking**: `extension/tsconfig.json` (JSDoc-based, same pattern as the
  Worker's), including a hand-written ambient declaration for `single-file-core`
  (which ships no types at all) covering only the two functions actually called.

### Real bugs caught along the way, worth remembering

- **The permission requested at pairing time was scoped too narrowly.** Initial
  version requested only the Worker's own origin; captures need `<all_urls>`
  (the manifest's own declared ceiling for exactly this reason) to reach both
  R2's presigned upload URLs (a different origin entirely) and whatever
  third-party resources a captured page references. Pairing succeeded either way
  — only the first real capture attempt surfaced it.
- **None of the raw `fetch()` calls were wrapped**, so a network-level failure
  anywhere surfaced as the browser's bare generic error message ("NetworkError
  when attempting to fetch resource.") with no indication of which of several
  fetch calls across the pipeline had actually failed. Fixed by wrapping each
  one with context and a proper `.cause` chain.
- **Multi-frame capture** (now fixed): see DESIGN.md §3h for the full account.
  The symptom — `getPageData()` hanging plus Firefox's "Receiving end does not
  exist." the moment `removeFrames` flipped to `false` — was a missing
  **background frame-tree relay**, not anything on the content side. On Firefox
  (native `globalThis.browser`), `content-frame-tree.js` hands each frame's DOM
  to the top frame via `browser.runtime.sendMessage` and expects the background
  to forward it to `frameId: 0`; with no such listener the send both rejected
  and never delivered, so collection never completed. Two prior source-reading
  theories (notably a missing `globalThis.singlefile` assignment) pointed at the
  content side and didn't fix it — instructive because the leg they addressed is
  one the code only sometimes takes, falling through to the
  `runtime.sendMessage` transport that actually had no receiver. Chrome was
  never affected (`globalThis.browser` is `undefined` there, so the collector
  coordinates in-page via `postMessage`). Fixed with
  `background/frame-tree-relay.js`, modeled on `SingleFile-MV3`'s own
  `frame-tree/bg/frame-tree.js`, and confirmed in a real capture.

### Popup visual design pass

Done, in its own follow-up session rather than as a quick pass tacked onto Phase
5's functional work. No logo yet, and none was needed in the popup itself since
the popup is opened _from_ the toolbar icon, so repeating the logo inside it
would be redundant.

Grounded in what "recueil" actually means (a collection) rather than generic
extension-popup chrome: warm paper/ink palette with an oxblood accent, a serif
heading against monospace for URLs/data and system sans for everything else,
hairline/dotted rules instead of boxed cards, and one signature element —
pending/success status render as an ink stamp (rotated, double-outlined; success
gets a slam-down entrance animation, pending stays static since a wait isn't an
event). Errors don't get the stamp treatment — a stamp reads as "done," which is
wrong for e.g. a queue claim that already expired — and stay a plain
accent-colored alert line instead. Follows `prefers-color-scheme` for
light/dark; no in-popup theme toggle. Iterated via a standalone static HTML
mockup (both modes, every state) before touching real extension files, which is
where the animation timing, spacing around the capture button,
ellipsis-truncated queue entries, and several hover/cursor fixes (toggle switch,
refresh control, non-interactive empty- queue row) all got settled before the
CSS/JS wiring pass.

### Still ahead

Safari packaging, whenever that becomes a priority — mechanical (Xcode-wrapped,
same source), not attempted yet, and not a priority right now. Moving settings
(bookmark sync's toggle, so far the only one) into a dedicated extension options
page was considered and explicitly decided against for now, after actually using
the popup during testing — everything stays in the popup unless/until there are
enough settings that it stops making sense there.

With those two exceptions, every piece from the original five-step plan
(pairing, capture, upload, queue-driven capture, bookmark sync) is built and
confirmed working end to end, and the popup now has a real visual identity
rather than functional-only styling.

### Queue-driven capture

Built as two isolated steps, tested separately, per ours own preference for
incremental delivery: the list-refresh-and-badge half first, then the actual
claim flow and completion-routing change on top. Ended up simpler on the backend
side than expected — `POST /queue/:id/claim` and its 404/409/410 distinctions
already existed from Phase 2 (atomic claim + 15-minute stale-reclaim), so this
phase needed zero new Worker endpoints, only the extension-side pieces:

- `background/queue.js`: `refreshQueueList()` (cache + badge, see DESIGN.md §3i
  for the four refresh triggers and why none of them fire on every
  service-worker wake) and `claimQueueItem()` (the real, live lock check, opens
  a focused tab on success, tracks `tabId -> queueItemId`).
- `background/capture.js`: `captureActiveTab()` checks that tracked map and
  routes completion to `POST /queue/:id/complete` instead of the default
  `POST /captures/complete` when it's set; also where the tab-auto-close logic
  lives (queue-driven only, never direct — see DESIGN.md §3i for the reasoning).
- `popup/popup.js`: a clickable queue list, a manual refresh button, and a
  status area for claim errors (already fully human-readable by the time they
  reach the popup — see DESIGN.md §3i on why that translation has to happen in
  the background, before crossing the messaging boundary).

The core design pivot worth remembering: the original plan (background tab,
unsupervised, timeout-based failure detection) doesn't work, for a reason that
only became clear from testing rather than reasoning about it in advance — a
CAPTCHA or paywall page captures _successfully_, no error at all, just wrong
content. Nothing about a background tab's load state distinguishes that from a
real page loading correctly. The fix wasn't a better detection heuristic (there
isn't one), it was putting a human in the loop by default, for every queue item,
and reusing the already-proven direct-capture pipeline as the actual completion
mechanism instead of building a separate automated one.

Two small follow-up fixes landed once real use surfaced them: refreshing the
queue immediately after a successful pairing (otherwise the popup shows "nothing
in the queue" until whichever of the alarm/startup triggers happens to fire
next, even with real pending items already on the instance), and confirming
(against Chrome's own documentation, consistent with Firefox's own bug history)
that a periodic alarm missed across several ticks — a laptop suspended for 24
hours against a 6-hour period, say — fires exactly once on resume rather than
once per missed tick.

11 new tests (`queue.test.js`'s `claimQueueItem`, `storage.test.js`'s
claimed-tabs round-trips) — `captureActiveTab`'s tab-closing behavior itself was
deliberately left to manual/console verification rather than given a dedicated
test, since exercising it meaningfully would mean mocking the entire
tab/scripting/fetch pipeline just to check one boolean call at the end, the same
coverage-theater tradeoff already ruled out for `capture.js`'s other
tab-touching functions.

### Bookmark sync

The original plan (§8, §15 in earlier revisions) was a custom in-popup bookmark
list, mirroring D1's `archived_pages`. That plan changed mid-phase, prompted by
a direct question about whether the browser's own native bookmarks could be used
instead — and the answer turned out to be yes, with a real, non-obvious
reconciliation approach rather than a compromise. See DESIGN.md §3j for the full
design writeup; concretely, what got built:

- **One new Worker endpoint**, unlike queue-driven capture's zero:
  `GET /archived-pages` (`terraform/index.js`'s `handleListArchivedPages`),
  device-bearer-token authed, a plain full-list read of the caller's own
  `archived_pages` rows — no pagination, no `since` parameter, simpler than the
  backend's own Postgres → D1 sync, which needs that complexity at a scale a
  single browser's bookmark tree never will.
- **`background/bookmarks.js`**: `syncBookmarks()` (the full-list diff --
  create/adopt/update/remove), `ensureFolder()` (create-or-adopt exactly one
  "recueil" folder, never a duplicate — see DESIGN.md §3j for the probe-bookmark
  technique this needed), `enableBookmarkSync()` / `disableBookmarkSync()` (the
  latter also relinquishes the `bookmarks` permission itself), and
  `registerBookmarkSyncAlarm()` (same 6-hour cadence as the queue).
- **`popup/popup.js`**: a checkbox toggle (reflects actual on/off state, unlike
  the queue's one-shot buttons) that requests the `bookmarks` optional
  permission synchronously in its own change handler — same user-gesture
  reasoning as pairing's `<all_urls>` request — before sending the enable
  message.
- **`background/index.js`**: `unpair()` now runs `disableBookmarkSync()` first,
  specifically _before_ its own `storage.local.clear()` — ordering that matters,
  since the teardown needs to read the tracked folder id before that wipe would
  otherwise have already erased it. Wrapped in its own `.catch(() => {})` at the
  dispatch layer, on top of `disableBookmarkSync`'s own internal safeguards:
  unpairing itself must never be blocked by a bookmarks-API hiccup.

**The real design pivot, found via a direct question rather than discovered the
hard way**: an initial version tracked a `page_id -> bookmark id` map in
`storage.local`, reconciling by id the same way the queue's claimed-tabs map
works. That turned out to be solving a harder problem than existed —
`GET /archived-pages`'s `raw_url` field is actually sourced from
`pages.normalized_url` (`internal/mirror/sync.go`'s `RawURL: p.NormalizedUrl`),
the exact column `pages`' own `UNIQUE (user_id, normalized_url)` constraint is
built on, so it's already a stable, unique identity key with no separate
tracking needed at all. Diffing the archived-pages list directly against
`browser.bookmarks.getChildren(folderId)` by URL is simpler than _and_ more
correct than the id-map version: the browser's own bookmark tree already is the
persisted state to compare against, and the cross-device-sync case (a bookmark
that arrived via Firefox Sync from another device before this device's own next
sync tick runs) falls out for free, needing no special "adopt" branch at all —
it just looks like "a URL that's already there," identical to one created
locally. The same reasoning was then applied one level up, in a second real fix:
the dedicated folder itself needed the same create-or-adopt treatment (a probe
bookmark discovers the real default container id, searched before falling back
to creating a fresh folder), after the first version would have blindly created
a duplicate "recueil" folder if one had already arrived via sync before this
device's own first sync ran.

One documentation bug caught and fixed along the way, worth remembering as a
category: an earlier comment framed "a bookmark manually placed inside recueil's
own folder gets swept away" as a risk specific to disabling sync or unpairing.
That was never accurate — `syncBookmarks` already removes any unrecognized
folder child on _every_ ordinary sync, not just at teardown. The behavior was
already correct; only the comment describing it was misleading, in a way that
could have led someone to believe manual bookmark management inside the folder
was safer than it actually is.

23 new tests across `bookmarks.test.js` (19) and `storage.test.js`'s
bookmark-sync keys (4) — including the folder-adoption and per-bookmark-adoption
cases, the `enableBookmarkSync`/`disableBookmarkSync` orchestration, and a real
test-setup bug caught in the process (a mock's default `vi.fn()` returned
`undefined` instead of a resolved promise, breaking a `.catch()` chained onto it
— fixed by giving the mock a proper default resolved value, not by changing the
production code).

### `POST /captures/complete`: direct-capture completion

`pending_captures.queue_item_id` was made nullable back in Phase 3 "to support
direct captures... not used by anything built so far" (its own migration
comment). That gap got hit for real once extension work reached the actual
upload flow: `POST /queue/:id/complete` requires an existing queue item, which a
direct capture (archiving a page the user is already on, never enqueued) doesn't
have.

`handleCompleteDirectCapture` mirrors `handleCompleteQueueItem` closely — same
client-generated-`capture_id` idempotency, same server-recomputed
`r2_key_html`/`r2_key_favicon` (never trusting a client-supplied key), same
`favicon_ext` validation against `FAVICON_EXTENSIONS`. The real differences
follow directly from there being no queue item: the caller supplies `url`
directly instead of it being read off a `queue_items` row, and there's no queue
item status to transition since none exists. `POST /captures/upload-urls` needed
no changes at all — its own doc comment already noted it was "deliberately not
scoped to a queue item."

Full test coverage added (`captures.test.js`, plus a routing test in
`fetch.test.js`), run against real Miniflare D1 the same way the rest of this
suite is — all 177 tests across the Worker suite pass.

## Phase 7 (Screenshot Job)

Phase 6 (the dashboard) is being skipped for now, on the reasoning that Phase
7's work makes it a more complete dashboard once it does get built. Phase 7
itself is three pieces — screenshot job, readability job, AI job — built in that
order; only the first is done so far.

### What exists now

- **`queries/screenshot_jobs.sql`**: `ClaimDueScreenshotJobs`,
  `GetScreenshotJobByCaptureID`, `SetCaptureThumbnail`, `MarkScreenshotJobDone`,
  `RetryScreenshotJob`, `FailScreenshotJob` — alongside the
  `CreateScreenshotJob` insert that already existed from Phase 3½'s ingestion
  work.
- **`internal/screenshot`**: the `Runner` — a long-lived `chromedp`
  `RemoteAllocator` connection to the shared sidecar, plus a long-lived
  ephemeral HTTP server (see DESIGN.md §6's "Implementation (Phase 7)" for why
  that exists instead of `file://`). `RunOnce` claims a bounded batch of due
  jobs and processes them with a `screenshot_worker_concurrency`-bounded worker
  pool; each job opens the capture's already-decompressed HTML at a fresh
  random-token URL, takes a full-page PNG screenshot, hashes and stores it via
  `archive.Store.WriteAsset` (keyed by the _screenshot's_ own hash, not the
  capture's — same reasoning archive.go's package doc already gives for
  favicons), and commits `captures.thumbnail_path` + the job's `done` status in
  one transaction. Failure hands off to exponential backoff
  (`30s * 2^(attempts-1)`, capped at 30 minutes) up to `screenshot_max_attempts`
  (default 3) before marking the job `failed` permanently.
- **`internal/config`**: four new fields, all with defaults, explicitly _not_
  added to the required-config list — an unreachable or unconfigured sidecar
  degrades to "no thumbnail, retried later," never a startup failure, matching
  this whole feature's optional-by-design status.
  `sidecar_url`/`sidecar_render_host` are two different directions of the same
  connection (agent→sidecar vs. sidecar→agent) and need genuinely different
  values depending on deployment shape; see DESIGN.md §6 for the concrete
  local-dev-vs-all-docker cases this covers.
- **`compose.yaml`**: a new `chromedp` service (`chromedp/headless-shell`,
  `shm_size: 2gb`) in both the `local` and `test` profiles. Its host port stays
  published (`127.0.0.1:9222:9222`) — a real difference from what the eventual
  operator-facing deployment docs should show, since we run the agent binary
  directly during development rather than as a container on the same compose
  network, and needs to reach the sidecar from outside it.
  `extra_hosts: host.docker.internal:host-gateway` is what lets the sidecar
  container reach back out to that host-side agent process in turn (needed on
  Linux; a no-op, not a conflict, on Docker Desktop).
- **`cmd/agent.go`**: `screenshot.Runner` constructed alongside the existing
  `Ingester`/`Syncer`, with its own `defer Close()` (it's the first of these
  three to hold live OS resources — the sidecar connection and the render
  server's listener — that need explicit teardown, unlike `Ingester`/`Syncer`
  themselves). `runAgentCycle` now runs all three passes sequentially on the
  same shared tick.

### Testing

`internal/screenshot`'s tests run against real Postgres (`internal/dbtest`)
_and_ the real `chromedp` sidecar (compose's new `test`-profile service) — no
mocked CDP client, extending this project's existing "no mocks for DB-touching
code" convention to the sidecar for the same reason: a hand-rolled fake would
just re-test this package's own assumptions about chromedp, not chromedp itself.
Coverage so far: a happy-path render (asserted all the way down to the stored
file's actual PNG magic bytes, not just "no error"), one failure not blocking
the rest of a batch, permanent failure once `max_attempts` is exhausted, and a
no-due-jobs no-op. `backoff` itself (pure, unexported) gets its own table-driven
unit test in an internal (`package screenshot`) test file — the one exception to
this project's external-test-package default, per its own stated testing
convention.

### Round 2: review feedback and the resulting changes

A first review pass (before any of this had run against real Postgres/Chrome)
caught several things worth fixing before, not after, first real use:

- Fixed viewport (`chromedp.CaptureScreenshot` + `EmulateViewport(1280, 800)`)
  replaced `chromedp.FullScreenshot` — full-page screenshots directly
  contradicted the "uniform thumbnails for the dashboard" reason this job exists
  on the backend at all.
- `Runner.New` now pings the sidecar's `/json/version` once at startup and fails
  loudly if it's unreachable, so a restart-until-healthy orchestrator policy
  actually has something to act on.
- `ListDueScreenshotJobs` became `ClaimDueScreenshotJobs`: a real atomic
  `FOR UPDATE SKIP LOCKED` claim (new migration `00007` adds a `'processing'`
  status + `claimed_at` to both `screenshot_jobs` and `readability_jobs`, plus a
  15-minute stale-reclaim, matching the D1 queue's own number), ahead of
  actually needing multi-process safety — future-proofing for a
  horizontally-scaled or hosted deployment, not solving a problem that exists
  today.
- `cmd/agent.go` now runs two independent tickers instead of one:
  `agent_worker_poll_interval_seconds` (default 300s, everything touching the
  Cloudflare Worker/D1) and `agent_local_poll_interval_seconds` (default 30s,
  Postgres-only jobs) — the old single `agent_poll_interval_seconds` is gone.
  This is what lets the Worker stay comfortably inside Cloudflare's free tier
  without also slowing down how quickly a freshly-ingested capture gets its
  screenshot.
- New migration `00008` adds `captures.thumbnail_size_bytes` and
  `captures.favicon_size_bytes` (both nullable); `archive.Store.WriteAsset` now
  returns the actual on-disk size instead of discarding it, threaded through
  both this job and `internal/ingest`'s favicon handling.

### Round 3: asset hashes and a non-root sidecar

- **`favicon_hash`/`thumbnail_hash`** (new migration `00009`): both already
  keyed by their own sha256 hash as their on-disk filename (see
  `internal/archive`'s package doc), but a filename is an implementation detail
  — recording the hash as its own column is what a future integrity-check
  command needs (hash everything actually on disk, compare against what was
  recorded at write time, independent of whatever naming scheme is current).
  Sha256 throughout, not a faster algorithm for these smaller assets: the
  performance difference is irrelevant at this scale (small files, hashed once,
  async), and a second algorithm would only cost a future verify command the
  need to track which column uses which. `archive.Store.WriteAsset`'s
  already-computed hash is now threaded through `internal/ingest`'s favicon
  handling and `internal/screenshot`'s thumbnail handling into
  `InsertCaptureIdempotent`/`SetCaptureThumbnail` rather than only ever living
  in the returned path string.

### Round 4: first real run against Docker — two genuine bugs found

The first attempt at `just compose test` failed outright, surfacing two
unrelated problems, both fixed once actually run for real rather than only
reasoned through:

- **Chromium's DevTools port turned out to be permanently loopback-only, full
  stop.** Since Chromium M113/M114, an all-zeros
  `--remote-debugging-address=0.0.0.0` is silently forced back to `127.0.0.1` in
  Chromium's own source — a non-configurable security decision (unrestricted
  network access to the DevTools protocol is a full remote-control vector), not
  a bug and not something any flag works around. This meant `sidecar_url` was
  never actually reachable — not from the host via the published port, and,
  worse, not from another container on the same compose network either, meaning
  this would have equally broken a fully-dockerized production deployment, not
  just local dev. **Fix**: `compose.yaml`'s `chromedp` service now runs
  Chromium's real listener on an internal-only port (9223); a new
  `chromedp-proxy` service (`alpine/socat`, sharing `chromedp`'s network
  namespace via `network_mode: "service:chromedp"`) bridges the
  externally-reachable 9222 to it with a plain TCP forward. Nothing in Go or
  `internal/config` changed — `sidecar_url` still targets port 9222 either way,
  now transparently proxied rather than hitting Chromium directly.

## Phase 7 continued: the readability job, and `internal/sidecar`

Built next, per Phase 7's stated order (screenshot, then readability, then AI
enrichment). Two decisions made before writing any code, both implemented as
decided:

1. **Extract the plumbing `internal/screenshot` and `internal/readability` both
   need (the `chromedp` allocator connection, the ephemeral render server) into
   a shared `internal/sidecar` package**, refactoring the already-working
   `internal/screenshot` to use it, rather than duplicating that infrastructure
   a second time. What stays duplicated instead: the retry/backoff/claim
   bookkeeping, since `screenshot_jobs` and `readability_jobs` are separate
   sqlc-generated Go types and the actual duplication is small (a few dozen
   lines each) — see DESIGN.md §6a's own "Implementation" section for the fuller
   reasoning either way.
2. **Readability.js itself is threaded through `main.go`, not vendored into
   `internal/readability` via a Makefile copy step.** `main.go` embeds
   `node_modules/@mozilla/readability/Readability.js` directly (already within
   `go:embed`'s reach from the repo root) and assigns it to `cmd.ReadabilityJS`,
   mirroring exactly how `PostgresMigrationsFS`/ `Commit`/`Date`/`Version`
   already flow from `main.go` into `cmd`. No new vendoring pattern, no new
   `.gitignore` entry, no generated file to keep in sync.

### What exists now

- **`internal/sidecar`** (new package): `Sidecar.New` pings the sidecar at
  startup (same fail-loudly reasoning as the original `screenshot.Runner`),
  starts the ephemeral render server, dials the `RemoteAllocator`.
  `Sidecar.NewTab(htmlData, timeout)` registers HTML and hands back a ready tab
  context + URL — never calls `chromedp.Run` itself, since a shared package
  can't know whether a caller needs e.g. a fixed viewport applied before
  `Navigate`. `Sidecar.Close()` is the only close call site `cmd/agent.go` needs
  now, for both jobs.
- **`internal/screenshot`** refactored: `Runner` no longer holds a listener,
  server, or allocator context directly, no longer has a `Close()`, and `Params`
  takes a `*sidecar.Sidecar` instead of `SidecarURL`/`RenderHost`. Behavior
  otherwise unchanged; its existing tests were updated for the new construction
  shape, not rewritten.
- **`internal/readability`** (new package): same `RunOnce`/`processOne`/
  `commitDone`/`handleFailure`/`backoff` shape as `internal/screenshot`.
  `render` does two `chromedp.Evaluate` calls against a `sidecar.NewTab`-
  provided tab: inject `Source` (defining the global `Readability` constructor),
  then run `new Readability(document.cloneNode(true)).parse()` and bind the JSON
  result into a `*article` (a pointer, specifically so a JSON `null` —
  Readability's own signal that a page isn't extractable — correctly leaves it
  `nil` rather than silently unmarshaling into a zero-value struct). Only
  `textContent` is persisted (`reader_text`); the rest of `parse()`'s output
  (`title`, `byline`, `excerpt`, etc.) is decoded but not yet used anywhere.
- **`queries/readability_jobs.sql`**: `ClaimDueReadabilityJobs`,
  `GetReadabilityJobByCaptureID`, `SetCaptureReadability`,
  `MarkReadabilityJobDone`, `RetryReadabilityJob`, `FailReadabilityJob` —
  alongside the `CreateReadabilityJob` insert that already existed.
  `SetCaptureReadability` overwrites `reader_text`/`reader_text_hash`/
  `readability_version` in place (no history kept, per DESIGN.md §6a);
  `captures.reader_text_tsv` (a generated column) recomputes automatically as
  part of that same `UPDATE`.
- **`main.go`**: new
  `//go:embed node_modules/@mozilla/readability/ Readability.js` and a
  `readabilityVersion` var (ldflags-injected), both assigned into the new
  `cmd.ReadabilityJS`/`cmd.ReadabilityVersion` vars (declared alongside the
  existing `Commit`/`Date`/`Version`/migrations-FS ones in `cmd/server.go`).
- **`internal/config`**: `screenshot_sidecar_url`/`screenshot_render_host`
  renamed to `sidecar_url`/`sidecar_render_host` (no longer screenshot-only, now
  that the connection is shared) — a real breaking rename, judged worth it over
  keeping a screenshot-specific name that would actively mislead once a second
  job depends on the same config. New
  `readability_worker_concurrency`/`readability_max_attempts`, mirroring the
  screenshot job's own pair exactly.
- **`cmd/agent.go`**: constructs one `*sidecar.Sidecar`, then both
  `screenshot.Runner` and `readability.Runner` on top of it; `runLocalCycle` now
  runs both `RunOnce` calls on the shared local ticker.

### Testing

`internal/sidecar` gets its own tests against the real sidecar (a startup
reachability failure, and a `NewTab` round-trip confirming served HTML is
actually fetchable and that `cleanup` actually tears things down).
`internal/readability`'s tests are the same shape as `internal/screenshot`'s
(real Postgres, real chromedp, real vendored `Readability.js` read directly off
disk via a relative path — skipped with a clear message if `node_modules` isn't
present rather than failing confusingly), plus one specific to this package:
confirming an empty `Version` is stored as `NULL`, not an empty string. The test
HTML is intentionally more than a one-liner — Readability's own heuristics judge
very short pages "not extractable" and return `null`, which would make every
test fail at the extraction step itself rather than testing anything this
package's own logic is responsible for.

## Phase 7 continued: the AI job, and the tagging schema

Two decisions made before writing code, both implemented as decided:

1. **A single OpenAI-compatible backend**, not separate Ollama/OpenAI code paths
   — Ollama, llama.cpp's own server, and effectively every hosted provider
   besides Anthropic all speak the same `/v1/chat/completions` shape.
   `BaseURL`/`APIKey`/`Model` config covers all of them.
2. **`tags`/`page_tags` built now**, not deferred to the dashboard work they
   were originally meant to arrive with — they existed only as prose in
   DESIGN.md §10 before this, never an actual migration. Building them now
   avoids retrofitting the AI job around a schema change later.

### What exists now

- **`migrations/00007_create_tags.sql`**: `tags` (unique per `(user_id, name)`)
  and `page_tags` (`(page_id, tag_id)` primary key, `source` distinguishing
  `'manual'`/`'ai'`).
- **`migrations/00008_create_ai_jobs.sql`**: `captures.ai_summary`/
  `captures.ai_model` (both nullable, mirroring `reader_text`'s own precedent
  now that TOAST makes the original "keep this decoupled for storage reasons"
  concern moot — no `ai_summary_hash`, since this data never touches disk and
  LLM output isn't deterministic enough for a hash to answer anything useful);
  `ai_jobs` itself, with `'processing'`/ `claimed_at` from day one.
- **`queries/tags.sql`**: `UpsertTag` — get-or-create by `(user_id, name)`,
  using the `ON CONFLICT ... DO UPDATE SET name = EXCLUDED.name RETURNING *`
  idiom rather than `DO NOTHING`, since the latter returns zero rows on the
  conflict path instead of the existing row.
- **`queries/page_tags.sql`**: `AddPageTag`
  (`ON CONFLICT (page_id, tag_id) DO NOTHING` — an AI tag colliding with an
  existing manual one, or vice versa, is a no-op, never an error),
  `ListPageTags`.
- **`queries/ai_jobs.sql`**: `CreateAIJob`, `GetAIJobByCaptureID`,
  `ClaimDueAIJobs` (no readiness join needed — a row's mere existence already
  implies `reader_text` is set, joins `pages` too since tags need
  `page_id`/`user_id`, not just `capture_id`), `SetCaptureAI`, `MarkAIJobDone`,
  `RetryAIJob`, `FailAIJob`.
- **`internal/readability`**: `commitDone` now also calls `CreateAIJob` in the
  same transaction as marking itself done — the one and only place an `ai_jobs`
  row ever gets created, expressing the readability→AI dependency as "when does
  the row get created" rather than a claim-time join.
- **`internal/ai`** (new package): `Runner` — same `RunOnce`/`processOne`/
  `commitDone`/`handleFailure`/`backoff` shape as `internal/screenshot`/
  `internal/readability`, but never touches `internal/sidecar` at all (a plain
  HTTP client, no browser). Uses the official `openai-go` SDK
  (`option.WithBaseURL` supports pointing it at any compatible server cleanly;
  matches this backend's existing precedent of official SDKs — `aws-sdk-go-v2`
  for R2 — over the Worker/JS side's deliberate zero-dependency approach). Two
  separate chat completion calls per capture (summarize, then generate tags)
  rather than one combined prompt — simpler prompts, no dependency on a model
  reliably producing one specific combined structure; a failure in either
  discards an already-successful result from the same attempt rather than
  partially committing (accepted, low-stakes waste, not an oversight — see the
  package's own `processOne` doc). Tag parsing is a lenient comma-separated-list
  split, not JSON or any structured-output feature, since support for those
  varies significantly across compatible servers. `reader_text` is truncated to
  `ai_max_input_chars` per call (default 24,000, ~6k tokens by the common
  ~4-chars-per-token rule of thumb -- raised from an initial, too-conservative
  12,000 after review, and made configurable rather than staying a single fixed
  number, since the right value genuinely differs between a large-context hosted
  model and a constrained local one).
- **`internal/config`**: `ai_base_url`/`ai_api_key`/`ai_model`/
  `ai_worker_concurrency` (default 2, more conservative than
  screenshot/readability's default 3 — hosted APIs often rate-limit, and many
  local single-GPU model servers can't meaningfully parallelize inference
  against one loaded model anyway)/`ai_max_attempts`/
  `ai_request_timeout_seconds` (default 300 — much longer than the sidecar jobs'
  fixed 60s, per the tolerance for slow local-model completions). No
  `ai_enabled` boolean: an empty `ai_base_url` is what disables AI enrichment
  entirely.
- **`cmd/agent.go`**: constructs `*ai.Runner` only if `cfg.AIBaseURL != ""`;
  `runLocalCycle` takes it as a possibly-nil parameter and simply skips it
  otherwise.

### Testing

`internal/ai`'s tests run against real Postgres, but a **fake**
OpenAI-compatible HTTP server (`net/http/httptest`), not a real LLM — a
departure from `internal/screenshot`/`internal/readability`'s "no mocks, real
backing service" convention. Reasoning: a real local model would make these
tests slow, heavy (an actual model download/load), and non-deterministic enough
that they could only ever assert "got some non-empty text back" — far weaker
than what's actually worth testing here. What this package owns and could have
bugs in is request/response handling, retry bookkeeping, and tag parsing, all of
which a fake server exercises precisely without depending on any model's actual
output quality. The fake server distinguishes the summarize vs. tag-generation
call by checking which system prompt came through, and simulates a failure
deterministically via a sentinel string in the user content rather than needing
a second server or base URL. Coverage: a full enrichment (summary + tags + job
done), a tag colliding with a pre-existing manual one being a silent no-op,
stale-job reclaim, one failure not blocking the batch, permanent failure after
max attempts, and a no-op with nothing due — plus pure unit tests for `backoff`
and `parseTags`.

## Phase 7 continued: job metrics

Prompted by a simple question worth asking of any new async job: is it worth
surfacing completed/failed counts to Prometheus? Yes, and it turned out to fit
the existing `internal/metrics` collector cleanly — `/metrics` is served by the
web server process (`internal/httpapi/router.go`), not the agent, but since job
status lives in Postgres regardless of which process is actually running
`RunOnce` cycles, the existing "query fresh on every scrape" pattern already
used for `recueil_users_total` extends to job status with no new architecture
needed.

Added two gauges: `recueil_jobs_total{job,status}` (current count per job
type/status combination — `screenshot`/`readability`/`ai` × `pending`/
`processing`/`done`/`failed`, 12 combinations total, all emitted explicitly
every scrape including zeros — a metric that appears and disappears as data
comes and goes makes `rate()`/`sum()` behave far less predictably than one
continuously present at 0) and `recueil_job_oldest_pending_age_seconds{job}`
(age of the oldest still-pending job of that type — a more actionable backlog
signal than a raw pending count, since some pending jobs at any given moment is
normal; a _growing_ age is what actually indicates something stuck).
Deliberately absent, not zero, for a job type with nothing currently pending —
asserted directly in `internal/metrics/metrics_test.go`.

## Phase 6 (Dashboard) — in progress

Deferred until after Phase 7 specifically so it could be built against a more
complete backend in one go, per the original phase-ordering note; folds in Phase
8 (Manage Devices) since that screen naturally slots in once the dashboard has
basic shape. Two halves so far: the Svelte project skeleton itself, and the
backend read/write API surface every dashboard screen will call. No actual
dashboard screens exist yet beyond two placeholder routes proving the skeleton
works end to end — that's the next piece of this phase.

### Svelte project skeleton

- **Plain Svelte 5 (runes) + Vite + TypeScript + SCSS**, not SvelteKit — the
  session model is already a same-origin cookie (§5), so there's no SSR/
  server-loader need SvelteKit's extra layer would earn its keep for. Routing
  via `svelte-spa-router`, a small client-side router rather than file-based
  conventions. `svelte.config.js` exports `vitePreprocess()` (from
  `@sveltejs/vite-plugin-svelte` itself, not the separate `svelte-preprocess`
  package) so `svelte-check` understands SCSS the same way Vite does.
- **Root `package.json` is now the dashboard's own package** — `src/` lives
  directly at the repo root. This meant reorganizing the pnpm workspace:
  `terraform/package.json` is new, holding the Worker's own devDependencies
  (`wrangler`, `@cloudflare/*`, `@aws-crypto/*`, `@smithy/*`) and making
  `terraform/` a pnpm workspace member, mirroring `extension/`'s existing
  isolation pattern. `eslint.config.js`/`vitest.config.js` stay root-level
  regardless — shared orchestration across every package was already the
  documented plan (DESIGN.md §13a) and didn't need to change, only which
  `package.json` owns which dependencies. `jsdom` moved from root to
  `extension/package.json` (its actual owner; was only in root incidentally).
- **Dev workflow:** `vite.config.ts` proxies `/api` to `http://localhost:8080`
  (matching `listen_addr`'s default) so `pnpm dev` doesn't need a Go rebuild per
  change.
- Skeleton content is intentionally minimal: `src/App.svelte` wires
  `svelte-spa-router` to two placeholder routes (`Login`, `Library`), each using
  `<style lang="scss">` to prove the TS+SCSS pipeline actually works, not just
  that it's configured. `src/app.scss`'s token set is explicitly copy-pasted
  from the extension popup's own CSS as a placeholder — reconciling it against
  the marketing site's ledger/brass/stamp palette into a real dashboard design
  system is a separate, not-yet-started pass.
- Verified: `pnpm build`, `svelte-check`,
  `pnpm run --filter=@recueil/terraform types`, `eslint` (extended with
  `typescript-eslint`/`eslint-plugin-svelte` for `.ts`/`.svelte`), and the full
  pre-existing 301-test Worker/extension suite all still pass after the reorg.

### Collections: migration + queries

`collections`/`page_collections` were fully specified in DESIGN.md §10 but had
no migration — unlike `tags`/`page_tags`, which landed early during Phase 7.
Built this phase (`migrations/00009_create_collections.sql`,
`queries/collections.sql`, `queries/page_collections.sql`):

- `CreateCollection` is a plain `INSERT`, not an upsert like `UpsertTag`/
  `UpsertPage` — collections are created by explicit user action through the
  dashboard, not derived from ingestion, so a duplicate name should surface as a
  real conflict for the caller to turn into a 409, not silently merge.
- `RenameCollection`/`DeleteCollection` check both `id` and `user_id` in their
  `WHERE` clause (same belt-and-suspenders pattern as the D1 token-revoke
  cross-check) — a caller bug passing the wrong id can't touch another user's
  collection.
- `ListCollectionsByUser` returns a flat list; the dashboard reconstructs the
  tree client-side from `(id, parent_id)`, no recursive CTE needed for a
  full-user listing.

### Manage Devices backend (`internal/devices`)

New package, not folded into `internal/mirror` or `internal/deviceapi` — see
DESIGN.md's updated Manage Devices section and the package's own doc comment for
the full reasoning (it authenticates as the backend itself via the service
secret, same credential tier as `mirror`/`ingest.WorkerClient`, a different
actor from `deviceapi`'s paired-device bearer token).

- `Client.ListTokens`/`RevokeToken` against the Worker's existing
  `GET`/`DELETE /internal/tokens` endpoints (built back in Phase 2, per
  DESIGN.md — this phase only needed the backend-side client and passthrough).
  `ErrNotFound` sentinel on the Worker's 404.
- **`parseD1NativeTimestamp`**: `tokens.created_at`/`last_used_at` are written
  by the Worker's own raw SQL (`CURRENT_TIMESTAMP`), which is SQLite-native
  format (`"2006-01-02 15:04:05"`, always UTC, no `T`/offset) — not RFC 3339
  like `internal/ingest`'s own `parseD1Timestamp` (which parses timestamps a
  _device_ generates client-side). A different source, a different format;
  reusing the wrong helper would have silently failed to parse or misread the
  time.
- `internal/httpapi`: `GET /api/devices`, `DELETE /api/devices/{id}`.
  `resolveTargetUserID` implements DESIGN.md's member-vs-admin scoping (member:
  self only; admin: any `?user_id=`, defaulting to self) as a per-request check
  inside the handlers, not a route-level `RequireAdmin` gate — both roles hit
  the identical routes with different allowed parameter values, which doesn't
  fit an all-or-nothing gate.
- `cmd/server.go` constructs
  `devices.NewClient(cfg.WorkerURL, cfg.WorkerServiceSecret)` alongside the
  existing `mirror.NewClient` call, same config values, both pointed at the one
  real Worker deployment.

### Pages/captures: library browsing, search, detail, HTML, language correction

- **`ListPages`/`SearchPages`** (`queries/pages.sql`) both carry a
  `COUNT(*) OVER()` window column so the dashboard gets a pagination total
  without a second round-trip — Postgres computes window functions before
  `LIMIT`/`OFFSET` slicing, so it's the full matching-set count, not just the
  returned page. `SearchPages` matches if _any_ capture of a page matches
  (`DISTINCT ON (pages.id)`, ranked by `ts_rank`), not just the latest — version
  history means the remembered content might only live in an older capture.
  `plainto_tsquery` uses the `'simple'` config (query terms are the user's
  input, not document content). Pagination is plain `LIMIT`/`OFFSET`, not keyset
  — simpler params, and drift-under-concurrent- insert isn't a real concern at
  this project's scale.
- **`GetPageByIDForUser`/`GetCaptureByIDForUser`** are new, user-scoped
  counterparts to the existing unscoped `GetPageByID`/`GetCaptureByID` — those
  two are left alone rather than changed in place, since their one existing
  caller (`internal/ai`'s tests) uses them specifically to discover a row's
  _own_ `user_id` in the first place, which a required `user_id` parameter would
  make circular. `GetCaptureByIDForUser` joins through `pages` for the ownership
  check, since `captures` has no `user_id` column of its own.
- **`GetPage` (page detail)** returns the page, its full capture history
  (`ListCapturesByPage`, most recent first, summarized — not the full row;
  `reader_text`/`ai_summary` are large and belong to capture detail instead),
  its tags (`ListPageTags`), and its collection memberships
  (`ListPageCollections`), all flattened into one JSON object via an embedded
  `pageResponse` struct rather than a nested envelope.
- **`GetCapture` (capture detail)** returns the full row including
  `reader_text`/`ai_summary`.
- **`GetCaptureHTML`**: the archived HTML is already zstd-compressed on disk. If
  the client's `Accept-Encoding` includes `zstd`, streams those bytes completely
  unmodified via a new `archive.Store.OpenRaw` (no decompression) —
  `Content-Encoding: zstd` set directly. Otherwise streams the decompressed HTML
  and lets the router's own `middleware.Compress` (now includes `text/html` in
  its allowed types) gzip it if the client asked for gzip instead. Verified
  directly against chi's real `compress.go` source (not just its docs) that
  `WriteHeader` steps aside the moment `Content-Encoding` is already set on the
  response, so the zstd path can't get double-compressed by the same middleware
  handling the gzip fallback.
- **`PatchCaptureLanguage`**: manual correction (DESIGN.md §10).
  `reader_text_tsv` recomputes automatically as part of the same `UPDATE` —
  already an established, documented fact in this codebase
  (`SetCaptureReadability`'s own comment), not something newly assumed here. An
  invalid `regconfig` value surfaces as a real Postgres error from the `UPDATE`
  itself (the cast performs a `pg_ts_config` catalog lookup), so no separate
  pre-validation query is needed.
- **`ListTextSearchConfigs`** (`GET /api/text-search-configs`), backing the
  correction dropdown: a plain query against the raw pool, not sqlc-generated —
  confirmed directly (not assumed) that adding a `pg_ts_config` query to
  `queries/*.sql` and running `sqlc generate` against it fails with
  `relation "pg_ts_config" does not exist`, since sqlc's schema analysis only
  knows our own migrations, not Postgres's built-in system catalogs. Same
  reasoning `internal/ingest`'s own `languageConfigExists` already documents for
  itself; `Server` picked up a `Pool *pgxpool.Pool` field for this one handler's
  sake.
- New `dbtest` fixtures: `CreatePage`, `CreateCapture` (via the real
  `InsertCaptureIdempotent`/`UpsertPage` paths, not bespoke inserts),
  `SetCaptureReaderText`, `CreateCaptureWithHTML` (writes real content through a
  caller-supplied `archive.Store` for tests needing actual on-disk HTML, e.g.
  `GetCaptureHTML`'s zstd/gzip streaming). `newTestServer`'s signature wasn't
  changed to expose its internal `archive.Store` (it has ~40 other call sites
  that don't care); a separate `newTestServerWithStore` helper covers the
  handful of tests that do.

### Tags/collections routes

Mostly wiring — the collections queries already existed from earlier this phase,
and only two tag queries were missing (`ListTags`, `RemovePageTag`;
`queries/tags.sql` previously only had `UpsertTag`, `page_tags.sql` only
`AddPageTag`/`ListPageTags`).

- `GET /api/tags`, `POST`/`DELETE /api/pages/{id}/tags[/{tagId}]` — adding a tag
  upserts by name (`UpsertTag`) then links it with `source: "manual"`, matching
  the source value a person applying a tag through the dashboard should carry
  (distinct from the AI enrichment job's own tags).
- Full collections CRUD under `/api/collections`, plus
  `GET /api/collections/{id}/pages` and page↔collection membership under
  `/api/pages/{id}/collections`. `CreateCollection`'s optional `parent_id` is
  verified to belong to the calling user before use — the FK itself has no
  `user_id` check, so without this a request could nest a new collection under
  another user's collection id.
- `GetPage`'s response was extended to include `tags`/`collections` as part of
  this work, since both queries already existed and page detail was otherwise
  missing them — a stale comment on that handler (still saying reader_text/
  ai_summary belonged to a "future" capture detail endpoint) was also fixed;
  that future arrived the round before this one.

### Still ahead

The actual Svelte screens against this now-complete route table: library
browsing/search UI, page/capture detail + reader view, tag/collection management
UI, the Manage Devices screen, and the login/setup flow the skeleton's
placeholder routes stand in for. Then `go:embed` wiring once there's something
real to embed, and the dashboard's actual visual design system (reconciling the
extension's neutral paper/ink surface against the marketing site's
ledger/brass/stamp accents — flagged during planning but deferred for now).
