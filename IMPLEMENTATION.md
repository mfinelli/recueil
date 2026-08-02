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

## Phase 6 (Dashboard)

Deferred until after Phase 7 specifically so it could be built against a more
complete backend in one go, per the original phase-ordering note; folds in Phase
8 (Manage Devices) since that screen naturally slots in once the dashboard has
basic shape. Built in the order: the Svelte project skeleton, the backend
read/write API surface every screen calls, then the screens themselves
(Setup/Login, Library, PageDetail, Collections, Devices, the reader view), and
finally the dashboard embedded into the single Go binary. See the closing
subsection below for exactly what's done and what's deliberately still open.

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

### Auth screens: session store, route guards, Setup/Login

- **`GET /api/setup-status`** (new, unauthenticated, `{"needs_setup": bool}` via
  `CountUsers`) — closes a real gap raised during planning: there was no way for
  the frontend to distinguish "show Setup" from "show Login" on first load
  without it (`POST /api/setup` only reveals "already done" via a 409 after the
  fact).
- **`src/lib/api.ts`**: the hand-rolled client (`apiFetch`/`apiJSON`/
  `ApiError`) confirmed during planning, given the current API surface size.
  `src/lib/types.ts` hand-mirrors the Go response DTOs — a manual sync point,
  flagged explicitly in both files' own comments, unlike `sqlc`'s automated
  Postgres↔Go sync.
- **`src/lib/session.svelte.ts`**: Svelte 5 runes-based session state (`user`,
  `needsSetup`). A `sessionReady` promise is kicked off at module load (not from
  a component's `onMount`) so `App.svelte` can await it once, before the
  `Router` ever mounts — no route guard needs its own "have we checked yet"
  bookkeeping. `GET /auth/me` and `GET /setup-status` run via
  `Promise.allSettled`, not `Promise.all` — a network failure on either
  shouldn't leave `sessionReady` permanently rejected and the app stuck on the
  loading screen forever.
- **`src/lib/routes.ts`**: `svelte-spa-router` guards via `wrap({conditions})`.
  Each condition (`requireSetup`/`requireGuest`/`requireAuth`) does its own
  `push()` redirect on failure directly, rather than centralizing through the
  `Router`'s `onConditionsFailed` callback plus a `userData` dictionary — judged
  simpler for only three routes.
- `Setup.svelte`/`Login.svelte` replaced with real forms; `Library.svelte`
  gained a working sign-out to prove the whole loop (Setup/Login → session →
  guarded route → logout) end to end, not just that routing itself works.

### Library + PageDetail screens

- **`Library.svelte`**: real listing/search against `GET /api/pages` (debounced
  `?q=`), `Previous`/`Next` pagination using the backend's window-function
  `total`. List and Grid view modes, persisted to `localStorage`.
- **`PageDetail.svelte`** (new): page metadata, tags, collection memberships,
  and full capture history — display-only in this pass; the write endpoints
  (tag/collection editing, the mirror-exclusion toggle, language correction) all
  already exist server-side but have no UI calling them yet. Capture rows link
  straight to `GET /api/captures/{id}/html` in a new tab rather than through the
  SPA — there's no in-app reader view built, and the browser already knows how
  to render an HTML document on its own.

### Favicon/thumbnail endpoints

Real gap noticed while building Library/PageDetail, not planned ahead of time:
`favicon_path`/`thumbnail_path` were already in the API responses with no route
that actually served the bytes.

- **`GetLatestCaptureByPage`** (new query): thumbnails aren't denormalized onto
  `pages` the way `favicon_path` is — `favicon_path` is set at `UpsertPage` time
  directly from the _ingesting_ capture, while thumbnails are written async by
  the screenshot job well after ingestion completes — so
  `GET /api/pages/{id}/thumbnail` resolves the latest capture fresh on every
  request instead of needing a schema change plus touching
  `internal/screenshot`.
- **`GET /api/pages/{id}/favicon`, `GET /api/pages/{id}/thumbnail`**, both
  through a shared `serveAsset` helper. No zstd/gzip content-negotiation dance
  like `GetCaptureHTML` — small binary images already, not worth the complexity.
  `Content-Type` inferred from the stored file extension
  (`contentTypeForAsset`), careful about a trailing `.zst` since `filepath.Ext`
  only ever returns the _last_ extension (`"favicon.svg.zst"` → `.zst`, not
  `.svg`, unless stripped first). Deliberately no `Cache-Control` — these URLs
  are page-identity-addressed, not content-addressed, so caching them long-lived
  risks serving a stale icon/thumbnail after a later re-capture changes what the
  URL resolves to.

### PageDetail's write actions, and frontend logic tests

- **`PageDetail.svelte`** now calls all four write endpoints it had been
  displaying data for read-only: tag add (`UpsertTag` + `AddPageTag`, source
  hardcoded `"manual"` server-side — the response doesn't even carry it, so the
  frontend's `TagCreated` type reflects that) and remove; collection add
  (offering only collections the page isn't already in) and remove; the
  `excluded_from_mirror` checkbox; and a per-capture language `<select>`
  populated from `GET /api/text-search-configs`, saving immediately on change.
  All four update `page` optimistically from each write's own response rather
  than refetching the whole page afterward — a normal tradeoff for a single-user
  tool, not hardened against concurrent-editor conflicts.
- **Frontend logic tests, started**: a new `"dashboard"` project in
  `vitest.config.js` (`environment: 'jsdom'`, the `svelte()` plugin so
  `.svelte.ts` compiles, and critically `resolve.conditions: ['browser']` —
  without it, `$state` resolves to Svelte's inert SSR runtime under plain Node
  rather than a live reactive signal, verified against Svelte's own testing docs
  rather than assumed). `jsdom` is now root `package.json`'s own devDependency
  too (previously only `extension/package.json`'s). `routes.ts`'s three guards
  became named exports specifically so they're directly testable, not just
  reachable through a mounted router. `session.svelte.ts`'s `sessionReady` fires
  its bootstrap fetch calls as a deliberate module-level side effect at import
  time — real for the app, awkward for tests, since a static import would race
  real network calls against a test's own mock; handled with
  `vi.resetModules()` + dynamic `import()` per test rather than changing that
  design. 25 new tests (`api.test.ts`, `session.svelte.test.ts`,
  `routes.test.ts`), full suite now 326 (was 301). Scoped to logic only, not
  component rendering (`@testing-library/svelte`) — a separate, later decision.

### Devices screen: pairing token surfaced, Manage Devices built, then admin cross-user reconsidered and removed

- **`src/routes/Devices.svelte`** (new): pairing token and paired devices on one
  screen, since they're the two halves of the same story (get a device paired,
  then see/revoke what's paired). The pairing token is shown plainly and stays
  viewable, not shown-once-then-hashed — DESIGN.md's §5 pairing-token section
  already specified this as the deliberate choice for this specific credential
  (unlike a session or bearer token, losing it would otherwise force an unwanted
  regenerate), it just hadn't been built yet. Regenerate/ revoke on the token,
  revoke per-device, copy-to-clipboard, all against endpoints that had existed
  since Phase 2 (pairing token) and earlier this phase (devices) with no UI
  calling them until now.
- **Admin cross-user device management, reconsidered and removed.** The original
  design (and this phase's first pass at `internal/httpapi`) let an admin
  list/revoke _any_ user's devices via `?user_id=`. Reasoning: cross-user access
  management isn't a session-authenticated web capability in this app, matching
  the precedent user creation itself already set (CLI-only, not a dashboard
  feature). An operator-only CLI command for the rare lost-device case is
  planned for a later phase, not built yet; `internal/devices.Client` was left
  untouched (it already takes an arbitrary `userID` per call), so nothing about
  this reversal narrows what that future command can do.

### Reader view, `go:embed` wiring, and closing out this phase

- **`GetCaptureHTML` gained a defensive
  `Content-Security-Policy: script-src 'none'`** on the archived-HTML response.
  Not the primary control — the extension's SingleFile capture already runs with
  `blockScripts: true` (see `extension/src/capture-inject/bundle-entry.js`,
  checked directly, not assumed) — but that response is served same-origin with
  the dashboard, so anything that ever did slip through would otherwise run with
  access to the logged-in session's cookies. Costs nothing to close outright.
  Covered by test assertions on both the plain and zstd-compressed response
  paths.
- **`CaptureReader.svelte`** (new, `/captures/:id`): title, capture date, AI
  summary when present, and `reader_text` rendered as plain text
  (`white-space: pre-wrap`, no `{@html}`, no guessed paragraph-splitting) —
  confirmed against `internal/readability`'s own source that `reader_text` is
  Readability.js's `textContent` field specifically, never its `content` (HTML)
  field, so there's no injection risk to design around. `PageDetail`'s capture
  rows now route here instead of opening the raw HTML directly; the raw HTML
  itself is still just a plain new-tab link (now living inside the reader view)
  — settled during planning: an iframe would mean fighting sizing/scrolling for
  a page that's a full, self-contained snapshot of someone else's layout, for
  little benefit over a new tab, which gets native zoom/find-in-page/the whole
  viewport for free.

This closes out Phase 6's core scope: Setup/Login, Library (list/grid, search,
pagination), PageDetail (full read/write loop), Collections management, Devices
(pairing token + paired-device list), the reader view, and the dashboard
embedded into the single Go binary. Two things remain open: the operator-only
CLI device-revoke command (§5, point 3 — `internal/devices.Client` is already
shaped for it), and the dashboard's actual visual design system (reconciling the
extension's neutral paper/ink surface against the marketing site's
ledger/brass/stamp accents) — both explicitly punted to a separate session.

## Phase 9 (small backend gaps: open registration, mirror resync, failed-queue retry)

Scoped as a deliberately small round of leftover implementation-phase gaps
flagged in DESIGN.md §15, rather than a new user-facing screen or capability
area — see the closing note on what's still left after this one. Built in the
order: the registration toggle, the resync CLI command, then the failed-queue
manual-retry mechanism (Worker → backend → dashboard, in that order).

### `ENABLE_OPEN_REGISTRATION`

- New `Config.EnableOpenRegistration` (`enable_open_registration`,
  `viper.SetDefault(..., false)`). Gated inline in the `Register` handler itself
  (`if !s.EnableOpenRegistration { 403 }`) rather than conditionally registering
  `POST /api/auth/register` in the router — keeps routing static, one branch to
  reason about.
- Landed default `false`, a reversal from DESIGN.md's original "open by default,
  flag for invite-only later" plan (see DESIGN.md §5, §15).
- `NewServer`'s signature grew an `enableOpenRegistration bool` parameter.
  `newTestServer`/`newTestServerWithStore` pass `true` so existing coverage,
  including `TestRegister`'s own happy-path assertions, keeps exercising the
  enabled path unchanged; a new `TestRegisterDisabledByDefault` builds its own
  one-off server with `false` to cover the real default directly.

### `recueil user resync`

- New CLI subcommand (`cmd/user.go`), CLI-only like the planned device-revoke
  command — a rare, ops-triggered action, not a dashboard click. Repair path
  DESIGN.md §14 calls for after a Postgres restore leaves the D1 pairing-token
  mirror stale.
- For each account: decrypts `pairing_token_enc` where present
  (`auth.DecryptPairingToken`), re-hashes with the same `auth.HashToken` the
  create/regenerate paths already use, and re-pushes through `mirror.PushUser` —
  the exact idempotent call already made at create/regenerate/revoke time, just
  looped across every user. Pushes `nil` for an account with a revoked (NULL)
  token, same as the revoke flow itself, so a stale non-NULL hash left over in
  D1 from before a restore gets cleared too, not just skipped. Per-account
  failures are logged and counted, not fatal to the whole run; the command exits
  non-zero only if at least one account failed.

### Failed queue items: surfaced, with manual retry

DESIGN.md §15 left this genuinely undecided (surface to the user? automatic
retry? a separate/longer expiry?) — resolved this round: surface with manual
retry, keep failed items forever otherwise (low expected volume at this
project's scale; can revisit if that stops holding).

### Failed screenshot/readability/AI jobs: same idea, extended, combined into Queue

Prompted by a direct follow-up question after the queue-items work above: these
three job tables (`screenshot_jobs`, `readability_jobs`, `ai_jobs`) all already
had automatic bounded retry with exponential backoff
(`internal/{screenshot,readability,ai}`, from Phase 7), and DESIGN.md's AI
section had originally envisioned "the dashboard surfaces failed jobs as a small
badge on the capture with a manual retry action" — never actually built. Checked
before starting: the API exposed zero job status at all (`GetCaptureByIDForUser`
was a bare `SELECT captures.*`, no join to any job table), so a
`null ai_summary` was indistinguishable between "still processing," "permanently
failed," and "will never exist" (readability failed, so `ai_jobs` was never
created — see below).

- **Combined into the Queue screen** — one place for everything currently stuck,
  not scattered across `PageDetail`. `Queue.svelte` gained a second section
  ("Failed to process") alongside the existing queue-items one ("Failed to
  capture"), rendering all three job kinds through a shared Svelte 5
  `{#snippet}` rather than tripling near-identical markup. Each row links to
  `/pages/{page_id}` (`svelte-spa-router`'s `link` action) since, unlike a queue
  item, these already have a real page to show.
- **No flag column needed here, unlike `queue_items.manual_retry`.** These three
  tables are only ever claimed by the backend's own
  `ClaimDueScreenshotJobs`/`ClaimDueReadabilityJobs`/`ClaimDueAIJobs` — no
  device races another actor for them — so a retry can reset the row directly
  and the backend's own next poll picks it up immediately, no "flag it now,
  something else claims it eventually" indirection required.
- **Your call: a manual retry does not reset `attempts`** — it spends the next
  one rather than granting a fresh budget. This required no new logic at all:
  leaving `attempts` at its already-terminal value means
  `internal/{screenshot,readability,ai}`'s existing `handleFailure` (computes
  `attempts+1 >= MaxAttempts`) naturally permanently re-fails the job after
  exactly one more attempt if it fails again, with zero special-casing for "this
  was a manual retry" anywhere in the job-processing code.
- A capture whose readability extraction permanently failed still never gets an
  `ai_jobs` row at all (unchanged, cascade already worked this way) — it
  surfaces under Readability in the Queue screen, not AI, until that's retried;
  nothing needed to change about the cascade itself for this to keep being true.

### Share-sheet PWA and iOS Shortcut (both thin remaining clients)

**Repo restructure first, prompted by how the PWA needed to deploy:**
`terraform/index.js`/`migrations/`/`tests/`/`package.json`/`tsconfig.json` moved
into a new `terraform/worker/` subdirectory, with `terraform/pwa/` as its new
sibling — a reversal of the original repo-layout plan (which explicitly kept the
Worker's source flat alongside the `.tf` files and put `pwa/` at the repo root,
arguing static PWA files weren't a Terraform/Worker concern). They are now: both
directories need to be `path.module`-relative for `cloudflare_workers_script`'s
`content_file` and `assets.directory` arguments to reach them from `main.tf`.

**Hosting decision, reversed from DESIGN.md's original plan:**
`cloudflare_pages_project` (the Terraform resource DESIGN.md originally called
for) turned out to have known, still-being-stabilized drift/source-config bugs
in the pinned `~> 5.0` provider as of recent Cloudflare changelogs, and even
when it works, Terraform only manages the project shell — actually deploying
files still needs a separate `wrangler pages deploy` step, unlike the Worker's
own single `content_file`-based `terraform apply`. Since provider v5.11,
`cloudflare_workers_script` itself accepts an `assets` block pointing at a
directory, with Terraform handling the manifest/hashing/upload the same way
Wrangler does. `main.tf`'s existing `cloudflare_workers_script "worker"`
resource now has `assets = { directory = "${path.module}/pwa" }` added directly
— one `terraform apply` for the whole Cloudflare side, no second deploy step, no
new moving part. Static files are matched by path first; every existing API
route (`/pair`, `/queue`, `/internal/*`, etc.) falls through to `index.js`'s
fetch handler untouched, since none of the PWA's own file names collide with an
API path — the provider's own default behavior, not something this module had to
configure.

**The PWA itself** (`terraform/pwa/`): `manifest.json` (Web Share Target,
`method: "GET"`, `params: {title, text, url}` — matches Android's Level 1 share
target, no service-worker request interception needed for plain URL/text
shares), `icon.svg` (a simple stamp/monogram, not a build-generated PNG set —
`"sizes": "any"` on an SVG icon is broadly supported and needs no
icon-generation pipeline), `style.css` (the exact same CSS custom properties as
`src/app.scss` and `extension/src/popup/popup.css` — deliberately reused, not
reinvented; full reconciliation across all four surfaces including the marketing
site's ledger/brass/stamp palette remains the separate, still-deferred
"dashboard visual design system" item), `index.html` (three views: pairing form,
incoming-share auto-enqueue result, and a paired "manual add + disconnect"
screen), `app.js` (vanilla JS, no dependencies, no bundler — same "no build
step" constraint `terraform/worker/index.js` already has, for the identical
reason: this deploys as a plain static file, not a build artifact), and `sw.js`
(a deliberately minimal service worker — cache-first for the app shell's own
four files only, existing purely to satisfy Android's installability requirement
for share-target registration, not as an offline-first design goal: every real
action here needs the network regardless).

One simplification worth calling out: because the PWA is served by the same
Worker it talks to, it's same-origin — there's no "Worker URL" field anywhere in
its pairing form, unlike the CLI's `recueil auth --url`. Sharing a URL extracts
it from `location.search`; Android doesn't consistently put the link in the
`url` param (some apps hand off `text` instead, sometimes with a caption), so
`app.js` falls back to scanning `text` for the first `https?://`-shaped token
before giving up.

**New device type**: `DEVICE_TYPES` (`terraform/worker/index.js`) gained
`"shortcut"` alongside the existing `"extension"`/`"pwa"`/`"cli"`, for the iOS
Shortcut client below — no D1 schema change needed (`tokens.device_type` has no
`CHECK` constraint, only a documentation comment, which was updated too). New
test in `handlePair.test.js` confirming it's accepted and stored correctly.

**iOS Shortcut**: genuinely can't be committed as source — Apple Shortcuts are
built and exported through the Shortcuts app itself, not authored as plain text,
and there's no way to test one in this environment either. The deliverable is a
documented recipe in the root `README.md`'s new "clients" section (same "drop it
in as a reminder" treatment the Docker Compose config already gets).

### CLI device-revoke command

The operator-only escape hatch DESIGN.md §5 (point 3) planned for but never
built, once the dashboard's own Manage Devices screen was reversed to strictly
self-scoped in Phase 6 (an admin can't reach into another user's devices from
the browser at all). `cmd/device.go`: `recueil device list <username>` and
`recueil device revoke <username> <device-id>`, structured exactly like
`cmd/user.go`'s existing commands — same config-load/pool-connect boilerplate,
same `queries.GetUserByUsername` + wrap-the-error pattern for resolving a
username, no special-casing `pgx.ErrNoRows` into a nicer message the way nothing
else in `cmd/` does either. Postgres is only ever touched to resolve that
username into a user id; the actual list/revoke goes through
`internal/devices.Client`, the same Worker client the dashboard's own
`ListDevices`/`RevokeDevice` handlers already use — this command doesn't add a
new path to the Worker, it's a different caller of the existing one.

`revoke` lists the user's devices first rather than revoking blind: a wrong
device id fails immediately with a clear "no device with id N for user X" before
ever making a request to the Worker (rather than surfacing as
`devices.ErrNotFound` after the fact with no context), and a successful run
reports which device it revoked by name and type, not just an id number typed
back at the operator. `list` output goes through `text/tabwriter` — no prior
convention for tabular CLI output existed anywhere in `cmd/` to match, so this
introduces one (ID/DEVICE NAME/TYPE/PAIRED/LAST USED columns, `never` for a
device that's been paired but not yet used). No tests added, consistent with
`cmd/`'s existing state: no file in this package has a test today
(`runUserCreate`/`runUserResync`/`runUserResetPassword` included), so this
doesn't introduce a gap relative to its siblings.

### README backup recipe

The one half of DESIGN.md §14's Backup & Restore section that wasn't already
covered by `recueil user resync` (that's the restore-time repair step; this is
the backup-_taking_ side itself). New "Backup" subsection in the root
`README.md`, same "starting point, not a drop-in final config" framing already
used for the Docker Compose example right above it, and explicitly pointed at
adapting it to real backup tooling (`restic`, `rclone`, a managed backup
service) rather than presenting the example script itself as the intended
production mechanism.

The recipe itself: `pg_dump -Fc` run inside the `postgres` container via
`docker compose exec -T` (avoids needing 5432 reachable from wherever the backup
script runs; `-T` specifically because the dump is binary output being
redirected to a file, not something meant to hit a TTY), plus the plain `tar` of
`./data/archive` above, both landing in one timestamped directory per invocation
so DESIGN.md's "same job/window" consistency requirement is structural rather
than something the operator has to remember to enforce themselves.

The restore half closes the loop back to the already-built resync command:
restore Postgres, untar the archive backup into a fresh volume, then run
`recueil user resync` before treating the restored instance as live.

### What's left

Not touched this round, still open from earlier phases: the dashboard's visual
design system and extension Safari packaging (explicitly punted for now).

## Phase 10 (i18n infrastructure)

### What exists now

- **`extension/_locales/en/messages.json`** (default) and
  **`extension/_locales/fr/messages.json`** — the WebExtensions i18n message
  catalogs. `fr` is a real, complete translation, not a stub, specifically so
  the pipeline gets proven against real substitution/layout behavior, not just
  file-copying.
- **`manifest.base.json`**: `name`/`description` switched to
  `__MSG_extName__`/`__MSG_extDescription__`, `default_locale: "en"` added — the
  one place the browser substitutes `__MSG_*__` placeholders outside of
  application code.
- **`popup.html`/`popup.js`**: every user-facing string (headings, field labels,
  button text/states, status messages, the empty-queue message) now goes through
  `t()`. `popup.html`'s static `<title>`/"Loading…" placeholder text stays an
  English fallback in the markup itself (general extension-page HTML has no
  `__MSG_*__` auto-substitution, unlike `manifest.json`) — `popup.js` overwrites
  both from the current locale as the very first thing it does once it runs.
- **`migrations/00010_create_user_settings.sql`**: new `user_settings` table,
  `user_id` itself as the primary key (a 1:1 extension of `users`, not a
  one-to-many table), a single nullable `language TEXT` column. No `CHECK`
  constraint, unlike every other small-fixed-enum column in this schema — see
  the migration's own comment and DESIGN.md §5d for why: the supported-language
  set is expected to grow as translations are added, unlike a genuinely fixed
  set like `role`.
- **`internal/httpapi`**: `GET`/`PATCH /api/settings`, both session-protected
  and self-scoped (no cross-user concept here at all, same as pairing-token/
  device management). `GetSettings` treats "no row" and "a row with `language`
  explicitly `NULL`" identically — both respond `{"language": null}`.
  `PatchSettings` takes a full-replacement request body (`{"language": "fr"}` to
  set, `{"language": ""}` to clear back to `NULL`) rather than `PatchPage`'s
  per-field-pointer pattern — see DESIGN.md §5d for why that's the right call
  for a single-field endpoint today, and the explicit note that it's worth
  revisiting once (not before) a second setting actually lands. Validates the
  submitted value against a shape-only regexp (`^[a-z]{2,3}(-[A-Z]{2})?$`), not
  a maintained allowlist — a `textOrNull`/`textOrNil` pair (package-local twins
  of `internal/ingest`'s and the existing `textOrNil`, respectively) handles the
  empty-string-means-NULL conversion in both directions.
- **Dashboard**: `src/lib/types.ts` gained `UserSettings`; a new
  `src/routes/Settings.svelte` (loads current settings, a `<select>` with a
  small hardcoded `LANGUAGE_OPTIONS` list — "Automatic," `en`, `fr` — that saves
  on change) wired into `src/lib/routes.ts` under the same `requireAuth` guard
  as every other authenticated screen, and linked from `AppHeader`'s main nav.
  The screen's own on-page copy says outright that changing the language doesn't
  do anything visible yet, rather than leaving that silently surprising.
- **`src/lib/locale.ts`** (new): the `custom-userSettings` client strategy.
  `getLocale()` returns a synchronous in-memory cache (client-side custom
  strategies can't be async); `setCachedLanguage()` populates it.
  `applyLanguageOverride(language: string | null)` is the actual public entry
  point `Settings.svelte` calls — deliberately not Paraglide's own exported
  `setLocale()`, which has no way to type-safely express "clear the override"
  (see the file's own comment). Not a Svelte rune — locale changes go through a
  full reload (Paraglide's own recommended default), so nothing needs to
  reactively re-render when it changes.
- **`session.svelte.ts`**: bootstrap gained a third parallel read,
  `GET /settings`, alongside the existing `/auth/me`/`/setup-status` — feeds
  `locale.ts`'s cache before `App.svelte` ever mounts the Router. A guest (401)
  is treated the same as any other bootstrap failure: the cache is just left
  unset, falling through to `preferredLanguage`/`baseLocale`.
- **`App.svelte`**: sets `document.documentElement.lang`/`dir` from Paraglide's
  resolved `getLocale()`/`getTextDirection()` once `sessionReady` (and therefore
  `locale.ts`'s cache) has settled — `index.html`'s own static `lang="en"` is
  just a pre-resolution fallback, same relationship as the extension's
  `popup.html` placeholder text to `popup.js`'s `t()` calls (§3k).
  `./node_modules/@inlang/plugin-.../dist/index.js` — deliberately not
  Paraglide's own CLI-generated `cdn.jsdelivr.net` URLs, which fetch over the
  network on every single compile.
- **Every dashboard screen now translated** (`Library`, `Devices`, `Queue`,
  `Collections`, `PageDetail`, `CaptureReader`, `Login`, `Setup`, on top of the
  `AppHeader`/`Settings` proof of concept) — `src/messages/{en,fr}.json` grew to
  cover every authored string across all eight screens, `fr` complete
  translations throughout, not stubs. A `common_*` prefix was introduced for
  strings genuinely repeated verbatim across screens (`Loading…`, `Cancel`,
  `Save`, `Username`, `Password`, `Language`, and so on) rather than duplicating
  an identical translation under N screen-prefixed keys — `Settings.svelte`'s
  own `settings_loading`/`settings_language_heading` were folded into
  `common_loading`/`common_language` as part of this, since they turned out to
  be exactly this case.
- **Two real English/French plurals** (`{count} attempt(s)` in `Queue.svelte`,
  `{count} sub-collection(s)` in `Collections.svelte`'s delete confirmation) —
  handled as two independent message keys (`_one`/`_other`) selected in plain
  JS, the same ternary the code already had, not Paraglide's ICU MessageFormat 2
  `.match`/selector syntax. Confirmed (by inspecting real compiled output, not
  assumed) that Paraglide does **not** auto-select between
  `_one`/`_other`-suffixed keys by naming convention — real MF2 plural syntax
  exists for when actual multi-language plural-rule complexity is worth the
  added authoring complexity, which two languages and two simple counts don't
  yet warrant.
- **Real localized units, not passthrough abbreviations**: `PageDetail.svelte`'s
  `formatBytes` now goes through `unit_bytes`/`unit_kilobytes`/`unit_megabytes`
  messages — French uses `o`/`Ko`/`Mo` (octet-based), not the English `B`/`KB`/
  `MB`, a real correctness difference this project's own established "authored
  strings get translated" scope already implied but hadn't been applied to yet.

## Phase 11 (tag/collection slugs and browsable detail pages)

### Schema

`tags` and `collections` both gained `slug TEXT NOT NULL` (independently unique,
not derived at read time — see §10's own updated comment) and
`created_at`/`updated_at`; `collections` also gained `description TEXT`
(nullable, tags stay lightweight and don't get one). `collections`' slug
uniqueness is scoped identically to its existing name uniqueness: two more
partial unique indexes (top-level vs. nested), not folded into a compound
`(name, slug)` pair, so a name collision and a slug collision each surface as
their own distinct conflict rather than an ambiguous "the pair wasn't unique"
error.

### `internal/slug`: generation and validation

A new small package, `Generate(name string) string` and `Valid(s string) bool`.
`Generate` is NFKD-decompose, strip Unicode combining marks (so "café" → "cafe",
not "café" verbatim), lowercase, collapse runs of non-`[a-z0-9]` characters to a
single hyphen, trim, cap at 63 chars (the conventional DNS-label length).
Intentionally **not** a transliteration/ romanization system: a name with no
Latin skeleton at all — pure CJK, Cyrillic, emoji — decomposes to nothing and
`Generate` returns `""`; callers must treat that as "couldn't auto-generate,"
not silently store an empty slug. A matching `src/lib/slugPreview.ts` mirrors
the same algorithm in JS (`String.prototype.normalize("NFKD")` + `\p{M}`
stripping) for the dashboard's live "URL will be..." preview; it's explicitly
documented as a best-effort preview, not the source of truth — the backend
always computes and validates the real slug on save, and the two implementations
are allowed to drift on edge cases (no length cap client-side, for one) since a
live preview only needs to be close, not exact.

### Conflict policy: explicit creation vs. implicit/AI creation

Two different rules, by design, not two accidentally-inconsistent ones:

- **Explicit actions with a form behind them** (the dashboard's tag rename,
  collection create/rename) get a flat `409` on any name-or-slug collision, no
  auto-suffixing. A person sees the conflict and picks something else —
  auto-appending `-2` was considered and explicitly rejected, since it would
  silently give someone a slug they never chose.
- **Implicit/background creation with no form to show a conflict in**
  (`AddPageTag`'s quick "add tag to page" flow, and the AI enrichment job)
  needed a different answer, and the two ended up different from each other too:
  `AddPageTag` still surfaces a `409` (a person did click something, even if
  there's no slug field in that particular quick-add UI yet), while the AI job
  silently skips a suggested tag whose slug collides, logs a warning, and moves
  on to the next suggestion — consistent with the pre-existing precedent that an
  AI-suggested tag colliding with an existing one _by name_ was already a silent
  no-op (`AddPageTag`'s own `ON CONFLICT DO NOTHING`).
- **`UpsertTag`'s `ON CONFLICT (user_id, name) DO UPDATE`** absorbs the ordinary
  "tag already exists" path with no error at all; the _only_ error it can return
  is the separate `(user_id, slug)` constraint firing for a genuinely new tag
  name whose candidate slug collides with some other, differently-named tag's.
  That fact is what makes the manual/AI split above safe to implement as "treat
  any error as the collision" on the manual side.

### Tag rename/delete and the pages-for-a-tag endpoint

`RenameTag` (`PATCH /api/tags/{id}`) and `DeleteTag` (`DELETE /api/tags/{id}`)
are new — tags previously had no way to be renamed or deleted at all, only
created implicitly through `AddPageTag`/the AI job. `DeleteTag` cascades to
`page_tags` via the schema's existing `ON DELETE CASCADE`, same shape as
`DeleteCollection`. `GetTagBySlug` and `ListTagPages`
(`GET /api/tags/{slug}/pages`, response `{tag, pages}`) back the new `TagDetail`
dashboard screen — keyed by slug, not id, since this is the browsable URL a
person actually sees and might bookmark; `RenameTag`/ `DeleteTag` stay id-keyed
since those are internal API calls that never appear in an address bar.

### Frontend: `PageList` extraction, `Tags`/`TagDetail`, `CollectionDetail`

- **`src/components/PageList.svelte`** (new): the list/grid rendering, view-
  mode toggle (and its `localStorage` persistence), and favicon/thumbnail
  broken-image fallback, extracted out of `Library.svelte` so `TagDetail`/
  `CollectionDetail` can reuse it instead of re-implementing the same markup.
  Its own test file (`PageList.test.ts`) picked up the view-mode/fallback tests
  that used to live in `Library.test.ts` (moved, not duplicated), plus one
  genuinely new case: resetting stale broken-image state when the `pages` prop
  changes identity — dead code as far as `Library`'s own usage goes (it fully
  remounts `PageList` on every reload), but real once
  `TagDetail`/`CollectionDetail` reuse the component without remounting on every
  navigation.
- **`src/routes/Tags.svelte`** (new): flat list (tags have no hierarchy, unlike
  collections), inline rename with a collapsed-by-default slug field — shows a
  live "URL: /tags/{preview}" button that expands into a real input on click,
  auto-following the name field until a person actually opens and edits it. No
  create form: there's no standalone tag-creation endpoint, only implicit
  creation via tagging, so this screen is rename/ delete only.
- **`src/routes/TagDetail.svelte`** (new): pages for one tag, via the shared
  `PageList`.
- **`src/routes/CollectionDetail.svelte`** (new): pages for one collection,
  routed by a wildcard path (`/collections/*`, e.g.
  `/collections/cooking/recipes`) resolved **entirely client-side** against the
  same flat `GET /collections` list `Collections.svelte` already fetches to
  build its tree — split the wildcard on `/`, walk each segment matching
  `(slug, parent_id)` one level at a time. No backend path-resolving endpoint
  exists or was needed: collections aren't slug-unique per-user (only
  per-parent), so a single-query slug lookup the way `GetTagBySlug` works for
  tags isn't available, and a server-side recursive walk was considered and
  explicitly decided against — re-fetching the full collection list client-side
  on every visit is a non-issue at this project's personal/self-hosted scale,
  the same "at this project's scale" reasoning §10 already uses to justify the
  adjacency-list schema over a closure table. Shows a collection's own direct
  pages only, never a subtree rollup — sub-collections are surfaced as their own
  links instead, a person clicks in rather than everything nesting flattening
  into one list.
- **`Collections.svelte`** picked up two changes to close the loop: its tree
  rows now link into `CollectionDetail` (previously plain text — the new screen
  would otherwise have been unreachable except by typing a URL by hand), and its
  rename form gained the same slug-field/description editing `Tags.svelte` has.
  The slug preview here accounts for nesting — it shows the _full_ path a save
  would produce (`/collections/zebra/side-dishes`), not just the leaf segment,
  by threading the accumulated parent path down through the recursive
  tree-rendering snippet alongside the existing `depth` parameter.

## Phase 12 (Dashboard visual design pass)

Picks up the item explicitly punted at the end of Phase 6: reconciling the
dashboard's placeholder `app.scss` (copy-pasted from the extension popup)
against the marketing site's ledger/brass/stamp palette into an actual design
system. Scoped screen by screen, mockup-first per screen before any real
component changes, starting with the shared foundation and `AppHeader` since
it's on every screen. See `DESIGN_SYSTEM.md` (new this phase) for the resulting
reference — colors, type roles, breakpoints, and patterns — kept separate from
this phase-history entry and from `DESIGN.md`'s architecture-level scope on
purpose, so a screen's visual work can be looked up without reading a phase
narrative first.

### What exists now

- **`src/styles/_tokens.scss`/`_typography.scss`/`_mixins.scss`** (new): the
  dashboard's color tokens (unchanged from the Phase 6 placeholder, plus one new
  `--brass` label accent from the marketing site's palette), three font roles
  (Fraunces/IBM Plex Mono/system sans, self-hosted via `@fontsource`), and
  breakpoint variables/mixins. `app.scss` now just imports these plus base
  resets, instead of carrying the placeholder values directly.
- **`AppHeader.svelte`**: active nav-link highlighting via
  `svelte-spa-router/active`, aware of each section's drill-down routes (its own
  `path` regex per link, not just an exact match — Library also matches
  `/pages/:id`/`/captures/:id`, Collections also matches `/collections/*`, Tags
  also matches `/tags/:slug`); a real mobile nav disclosure (the header had zero
  responsive handling before this phase); icon-only sign-out and a
  `Menu`/`X`-swapping nav toggle via `@lucide/svelte`.
- **`@lucide/svelte`** adopted for dashboard icons generally, not just this
  screen — see `DESIGN_SYSTEM.md`'s Icons section for the actual usage pattern.
- **Stamp motif** (the extension popup's rotated bordered badge, also on the
  marketing site's seal) was explored for reuse on the dashboard (Queue/job
  status was the obvious candidate) and explicitly dropped — extension-only for
  now, see `DESIGN_SYSTEM.md`.
- **Dark mode toggle**: discussed, not built. Leaning toward a `Settings`-screen
  preference (system/light/dark) matching the existing `language` setting's
  exact shape (`user_settings` nullable column, same upsert, same
  automatic-plus-explicit-options `<select>`) rather than a header quick-toggle
  — the header already holds nav + account, and this is a set-once preference
  rather than something reached for every session, the same profile `Settings`
  already exists for. Flagged as backend work and picked up separately when the
  `Settings` screen's own visual pass comes around; see `DESIGN_SYSTEM.md`'s
  Open Items.
- **Login/Register/Setup**: shared `PasswordInput.svelte` (show/hide toggle,
  `tabindex="-1"` on the toggle button so Tab goes straight from one password
  field to the next instead of landing on the eye icon), a register link on
  Login gated on `session.openRegistration` (already-shipped backend flag), a
  forgot-password link built and gated behind an always-false constant rather
  than commented out (password reset itself stays CLI-only, for now — see
  `DESIGN_SYSTEM.md`'s Open Items), and a format-hint placeholder
  (`rcl_bootstrap_…`) on Setup's bootstrap-token field.
- **Library**: `PageList.svelte`'s favicon/thumbnail failure tracking split into
  two independent `SvelteSet`s (a single shared one silently broke once grid
  view started rendering both images for the same page), `favicon_path` checked
  before ever requesting the image instead of relying solely on `onerror`, a
  `Globe` fallback icon for a missing/broken favicon in both list and grid, grid
  view gaining a favicon + truncated URL line to match list view's information
  density, and dotted-rule list dividers (the mixin had existed since the
  shared-foundation round but this was its first real use).
- **`Footer.svelte`**: built (brand/copyright/license, GitHub/recueil.app links,
  version/commit from the already-existing unauthenticated `GET /info`), then
  not actually wired into the app after seeing it live — landed standalone and
  unused rather than discarded, in case the decision changes.
- **`PageDetail`**: the biggest single-screen visual pass so far. Real bugs
  caught during review, not just style: the source URL was using `var(--focus)`
  (reserved for focus rings only, per `DESIGN_SYSTEM.md`'s own rule) to signal
  "this leaves the app," replaced with the standard mono/muted treatment plus an
  explicit `ExternalLink` icon. Tags/Collections each gained an independent
  edit-mode toggle (pencil ↔ checkmark) hiding the remove buttons/add-forms
  behind it — pills-only by default. Collection pills are real links,
  reconstructed client-side from the already-fetched full collection list by
  walking the `parent_id` chain (no backend change needed, since routes.ts
  wildcards `/collections/*` to the full nested path, not just one collection's
  own slug); tag pills needed an actual small backend addition instead
  (`tags.slug` added to `ListPageTags`'s `SELECT` and `pageTagResponse`, since
  `PageTag` had never exposed a slug anywhere). The mirror-exclusion checkbox
  was inverted to its positive framing ("Sync with my browser's bookmarks,"
  checked by default) — the existing wording wasn't wrong, just backend jargon
  instead of what the toggle actually does for the person looking at it.
  Recapture/Delete moved to their own row below Captures, separated from the
  sync toggle, and Recapture now disables itself during its own "Queued!"
  confirmation window (re-queuing the same URL would just be a no-op anyway).
  Capture rows drop the source label entirely for `extension` captures (the
  overwhelming default) and only call out `manual_upload` explicitly, with an
  icon — see `DESIGN_SYSTEM.md`'s new "De-emphasizing the common case" pattern.
- **`Queue`**: the last screen in this pass, and the one screen whose scope
  actually grew mid-round rather than just getting restyled — picked up full
  status visibility (pending/claimed-or-processing/failed, plus captured/done
  within the same 15-minute recency window the backend broadening round already
  established) instead of the failed-only view it shipped with. One status-badge
  vocabulary (existing tokens only, no new colors) reused across queue items and
  all three job kinds; relative timestamps via `Intl.RelativeTimeFormat` rather
  than a hand-rolled formatter, so locale-specific word order ("2 minutes ago"
  vs. "il y a 2 minutes") comes free; a summary count row above the per-section
  lists; a manual refresh button plus a light 15-minute auto-poll (same window,
  deliberately not more frequent, to stay easy on the Worker's free tier); a
  retried job now updates in place to "pending" instead of being removed from
  its list, since it's no longer true that "not failed" means "nothing to show"
  the way it did in the failed-only version. Caught a real copy collision along
  the way, not just a test artifact: the jobs section was originally headed
  "Processing," the same word an individual in-progress job's own status badge
  uses — renamed to "Enrichment jobs" (see `DESIGN_SYSTEM.md`'s own note on
  this).

### What's left

The dashboard visual design pass itself is done — every screen listed in
`DESIGN_SYSTEM.md`'s own status table has landed. Extension Safari packaging
remains open from earlier phases, unrelated to this one.

## Phase 13 (PageDetail gaps: delete, title override, manual recapture)

Three small, independently-scoped gaps flagged while working on the visual
design pass's PageDetail mockup, not new capability areas — see DESIGN.md §15's
own closing note for this round. Built together since all three touch the same
handler/route/screen; landed in the order backend → Worker → frontend for the
recapture piece specifically, since it's the only one of the three that crosses
the Worker boundary.

### `DELETE /api/pages/{id}`

- New `DeletePage` query (`:execrows`, same `WHERE id = $1 AND user_id = $2`
  scoping and `rowsAffected == 0 → 404` handling as the existing
  `DeleteCollection` — copied that shape directly rather than inventing a new
  one).
- Cascades to captures (and transitively their screenshot/readability/AI jobs),
  `page_tags`, and `page_collections` rows entirely via the schema's own
  `ON DELETE CASCADE` chain — nothing else to clean up in Postgres.
- **Deliberately doesn't touch D1 or on-disk archive files synchronously.** The
  D1 bookmark mirror self-heals via the existing periodic `Syncer`'s
  `reconcileDeletions` pass (`GetMirrorEligiblePageIDs` simply no longer
  includes the deleted id), the same mechanism already handles
  `excluded_from_mirror`. On-disk HTML/screenshot/favicon files are left
  orphaned — see DESIGN.md §15's Phase 13 entry for why a per-page delete can't
  safely reclaim them (content-hash addressing, possible sharing across
  captures/pages) and the `recueil gc` CLI command flagged there as the
  follow-up, not built this round.
- Frontend: a "Delete page" button on PageDetail, `confirm()`-gated (same
  pattern as Tags'/Collections' own deletes), navigates to `/` (the library) on
  success via `push("/")` — the first write action on this screen that leaves
  the page entirely, so its own test needed a captured (not just stubbed) `push`
  mock, same pattern as Login/Setup's own tests use.

### Title override

- `pages.title` was already denormalized from the latest capture (`UpsertPage`'s
  `SET title = $3` on every ingest) — **your call was a direct overwrite, not a
  new `title_override` column**: a later recapture clears a manual override back
  to the auto-detected title, same as it always has. New `SetPageTitle` query,
  `patchPageRequest` grew a second optional `*string` field (`title`, alongside
  the existing `excluded_from_mirror`) — at least one of the two must be
  provided; either or both can be in the same request, applied as two
  independent updates (not one combined query) since the dashboard never
  actually sends both together today (they're two separate pieces of UI).
- Frontend: an inline edit affordance on PageDetail's `<h1>` (a small pencil
  icon button, `@lucide/svelte`) swaps the heading for a text input plus
  Save/Cancel, deliberately unstyled beyond matching the screen's existing
  inline-form pattern — this screen's own visual pass is a separate,
  already-planned piece of work (DESIGN_SYSTEM.md), not something to anticipate
  here.

### Manual recapture

- **Enqueues, doesn't capture.** The backend has no rendered/authenticated
  browser session of its own (DESIGN.md §2's own reasoning for why capture only
  ever happens from a real tab) — `POST /api/pages/{id}/recapture` looks up the
  page's most recent capture's `raw_url` (not `pages.normalized_url` — the raw
  URL is what a device would actually re-fetch) and re-enqueues it through the
  exact same queue a device's own share-sheet/extension enqueue feeds, via a new
  `queueitems.Client.Enqueue` method.
- **New Worker endpoint: `POST /internal/queue-items`** (`handleServiceEnqueue`)
  — service-secret-gated, same three fields and same
  `INSERT ... ON CONFLICT(id) DO NOTHING` idempotency as the device-facing
  `POST /queue`, just called by the backend instead of a device with its own
  bearer token. `added_by_token_id` is left `NULL` (the schema already allows
  this — there's no device token to attribute a backend-initiated enqueue to).
  The queue item's own `id` is generated backend-side (`google/uuid`, same
  `uuid.NewString()` already used for `source_capture_id` in `internal/ingest`)
  since there's no client on the other end to have generated one.
- Frontend: a "Recapture" button that shows a transient "Queued!" confirmation
  on success (same pattern as Devices.svelte's copy-to-clipboard button) —
  there's nothing on the page's own state to update, since this never touches
  `page` at all, only the queue.

### `DELETE /api/captures/{id}`

- New `DeleteCapture` query — captures has no `user_id` of its own, so ownership
  is scoped via `DELETE ... USING pages` (the DELETE equivalent of the join
  `GetCaptureByIDForUser`/`SetCaptureLanguage` already use for SELECT/UPDATE),
  same reasoning, new syntax for the new statement type. Cascades to this
  capture's screenshot/readability/AI job rows via the schema's own
  `ON DELETE CASCADE` chain, same as `DeletePage`'s own reasoning — and leaves
  on-disk archive files orphaned for the same reason `DeletePage` does
  (content-hash addressing, possible sharing across captures/pages; see
  DESIGN.md §15's Phase 13 entry) — no new decision needed on either front, both
  already settled there.
- **Extends the no-empty-pages policy down one level, per your call**: deleting
  a page's _last_ remaining capture deletes the page itself too, in the same
  transaction. `internal/httpapi.DeleteCapture` reads the capture first
  (confirms ownership, gets `page_id`), opens a transaction, deletes the
  capture, calls the new `CountCapturesByPage`, and calls the already-existing
  `DeletePage` query if the count comes back zero — all committed together, so a
  page is never observably left at zero captures even for an instant.
- Frontend: a "Delete capture" button on the reader view, `confirm()`-gated
  (same pattern as PageDetail/Tags/Collections' own deletes). Always navigates
  to `/` (the library) on success, deliberately not back to the page detail —
  the response alone doesn't say whether the page survived or got deleted along
  with its last capture, and guessing wrong would mean landing on a 404; `/` is
  always valid either way.

### Regenerate summary / regenerate readability

- `POST /api/captures/{id}/regenerate-summary` and
  `POST /api/captures/{id}/regenerate-readability`: new
  `RegenerateAIJobForCapture`/ `RegenerateReadabilityJobForCapture` queries,
  each resetting the relevant job row (`ai_jobs`/`readability_jobs`) back to
  `status = 'pending'` — attempts, `error`, `claimed_at`, `completed_at` all
  reset to a clean slate too, since this is a deliberate user-requested redo,
  not error recovery (unlike the existing
  `ManualRetryAIJobForUser`/`ManualRetryReadabilityJobForUser`, both restricted
  to already-`failed` jobs from the Queue screen's own failed-item review —
  these two work from _any_ prior status, keyed by `capture_id` itself rather
  than the job's own id). The already-running `ai.Runner`/ readability job
  runner picks either up on its own normal polling schedule — no new processing
  logic anywhere.
- `regenerate-summary` 404s gracefully if readability itself never succeeded for
  this capture (no `ai_jobs` row exists yet — that row is only created once the
  readability job succeeds once). `regenerate-readability` always has a row to
  reset (`readability_jobs` is created at ingest time, unconditionally), so it
  only 404s for a genuinely bad/not-owned capture id.
- **Regenerate-readability deliberately does NOT requeue the AI job** — today
  there's no extra state (e.g. a "readability changed since this summary was
  generated" flag) to make that decision by, so a stale AI summary is left
  exactly as stale as it was before this endpoint existed; a separate, explicit
  regenerate-summary click is what actually refreshes it. If tracking that
  staleness ever becomes real work worth doing (a new column, most likely),
  automatically requeuing the AI job too becomes the natural thing to reconsider
  at the same time.
- Frontend: both are fire-and-forget from the reader view's own perspective —
  neither touches the rendered capture at all on success, so each just shows a
  transient "Queued!" confirmation (same pattern as PageDetail's own recapture
  button), not a state change to watch for.

### `GET /api/capture-config`

- Reports this running agent's currently-configured `readability_version` and
  `ai_model` — the exact same values already threaded into
  `readability.Params`/`ai.Params` at `cmd/agent.go` startup, now also threaded
  into `httpapi.Server` (`cmd/server.go`'s own call to `httpapi.NewServer` grew
  two trailing string params for it). `ai_model` is reported as unset (`null`,
  not `cfg.AIModel`'s literal value) whenever `cfg.AIBaseURL` is empty — AI
  enrichment being toggled off entirely doesn't necessarily mean `cfg.AIModel`
  itself was ever cleared, so this reads the same is-it-actually-enabled signal
  `cmd/agent.go` itself already uses, not just the model string in isolation.

### Task A: previously-untracked-in-the-API fields

- `GET /api/captures/{id}`'s response gained six fields that were already real
  columns in Postgres and simply never made it into `captureDetailResponse`:
  `readability_version`, `content_hash` (not nullable — set at ingest time, not
  by a later job), `thumbnail_size_bytes`, `thumbnail_hash`,
  `favicon_size_bytes`, `favicon_hash`. Pure DTO/mapping work — no migration, no
  new query beyond the existing `SELECT captures.*`. New `int4OrNil` helper
  (`textOrNil`'s twin for `pgtype.Int4`) for the two `*_size_bytes` fields.

### `internal/gc`: the actual sweep

- `Runner.Run(ctx, dryRun)`: reads the complete "live set" of on-disk paths
  Postgres still references (new `ListReferencedArchivePaths` query — see its
  own section below for why it's not just `captures.*_path`), then walks every
  file `archive.Store`'s root actually contains (new `Store.Walk`), removing
  whatever isn't in that live set (new `Store.Remove`) unless `dryRun` is true,
  in which case nothing is actually touched — only counted.
- Individual remove failures (permissions, a concurrent modification) are logged
  and counted (`Result.RemoveErrors`), not fatal to the run — the sweep
  continues to the next file, same "log and keep going" philosophy
  `internal/ingest`/`internal/mirror` already apply at their own per-item level.
  Only a failure reading the live-set query, or a fundamental failure walking
  the store's own root at all, aborts the whole run.
- `Result` reports files scanned/removed and bytes reclaimed either way (real,
  already-freed space when `dryRun` is false; what _would_ be freed when it's
  true) — `cmd/gc.go` prints this as a one-line summary.

### `archive.Store` gains `Walk`/`Remove`

- `Walk(fn)`: lists every regular file under the store's root, giving each one's
  path in the same root-relative shape `WriteHTML`/`WriteAsset` already return
  and `Open`/`OpenRaw` already accept, plus its size in bytes. Purely read-only
  — deciding what's still referenced is `internal/gc`'s job, not something
  `archive` should know (it has no visibility into Postgres at all).
- `Remove(relPath)`: deletes the file, then climbs back up removing each
  now-empty parent directory in turn — `CaptureDir`'s own three levels of
  sharding (`hash[0:2]/hash[2:4]/hash`) collapsed back down once nothing's left
  in them, rather than accumulating empty directory entries forever as captures
  get GC'd over an instance's lifetime. `os.Remove` refusing to remove a
  still-non-empty directory is the natural signal to stop climbing (a sibling
  capture's own directory still living under the same `hash[0:2]/hash[2:4]`
  shard prefix is the expected, common case, not a failure) — climbing never
  goes above the store's root.

### dark mode / theme preference

A `Settings`-screen preference (automatic/light/dark), the shape this project
had already sketched out (see DESIGN.md's own "Resolved this round" entry for
the full writeup, including how the flash-of-wrong-theme problem was actually
solved — the short version below).

- `src/lib/theme.ts` (new): `applyTheme(theme)` — sets/clears
  `document.documentElement.dataset.theme` and keeps a `localStorage` cache
  (`"recueil-theme"`) in sync. Much simpler than `locale.ts`'s module: nothing
  needs Paraglide's repeated-synchronous-read shape here, this is just one DOM
  mutation plus a cache write.

### active sessions

A `Devices` screen addition, in its own separate section rather than merged into
the paired-devices list.

- New `internal/auth.SessionIDFromContext`, threaded through `RequireSession`'s
  middleware alongside the existing user -- needed anywhere that has to tell
  "this specific session" apart from the user's other ones (the list's own
  `is_current` flag, and `DeleteSession`'s refusal to delete the one making the
  request).
- `GET /api/sessions` / `DELETE /api/sessions/{id}`: strictly self-scoped, same
  reasoning `ListDevices`/`RevokeDevice` already settled on. Parsing
  (`github.com/medama-io/go-useragent`), not stored as its own columns at write
  time.
- `DeleteSession` refuses (400) to delete the caller's own current session --
  checked via `SessionIDFromContext` before the query ever runs, not left to the
  database to reject. Signing out (`POST /api/auth/logout`, already existing) is
  the correct way to end that one; the dashboard's own UI doesn't even render a
  revoke control for that row, so reaching this 400 at all means a stale tab or
  a direct API call, not a normal click.
- Frontend: `Devices.svelte` gained a third section, reusing the exact same
  list/icon/`role="img"`+`aria-label` markup pattern the paired-devices list
  already established (see `DESIGN_SYSTEM.md`'s own note on this). Icons:
  `Monitor`/`Smartphone`/`Tablet` by parsed `device_class`, with a `CircleHelp`
  fallback deliberately distinct from Devices' own `Smartphone` default, for an
  empty/unrecognized `device_class`. The current session gets a left
  accent-stripe highlight and a "Current session" badge instead of a revoke
  button, not a disabled one.

### queue-items/jobs recency window (Queue screen backend broadening)

Backend/data-layer work only, explicitly scoped that way -- the Queue screen's
own UI still filters back down to `status === "failed"` after loading, keeping
today's behavior unchanged while the fuller data sits ready in the API for a
separate follow-up to actually build the "what's currently happening" view
against.

- `terraform/worker/index.js`'s `handleListFailedQueueItems` (renamed
  `handleListQueueItems`) now returns `pending`/`claimed`/`failed`
  unconditionally, plus `captured` items claimed within the last 15 minutes --
  the same window this file's own claim visibility-timeout already used
  (`handleListQueue`/`handleClaimQueueItem`), reused rather than introducing a
  second number for the same "still worth a glance" idea. The `?status=` query
  parameter is gone entirely -- there's nothing left to select between.
  `claimed_at` is now in the response too.
- `internal/queueitems.Client.ListFailed` (renamed `List`) and its `Item` type
  (`ClaimedAt *time.Time` added) updated to match -- a new
  `parseD1NativeTimestampOrNil` handles the nullable case (never-claimed items).
- `internal/httpapi`: `ListFailedQueueItems`/`ListFailedJobs` renamed
  `ListQueueItems`/`ListJobs`; `failedJob`/`failedJobsResponse` renamed
  `job`/`jobsResponse`, both gaining `Status`/`ClaimedAt`.
- The three Postgres job-listing queries
  (`ListFailedScreenshotJobsForUser`/`ListFailedReadabilityJobsForUser`/
  `ListFailedAIJobsForUser`, renamed `ListRecent...`) got the identical
  broadening: `pending`/`processing`/`failed` unconditionally, `done` only
  within the same 15-minute window (`NOW() - INTERVAL '15 minutes'`, duplicated
  across all three queries rather than centralized -- each query's own comment
  points at the other two). One consistent "recent" meaning across both the D1
  queue and Postgres jobs, not two different numbers that happen to live in
  different databases.
- Frontend types: `QueueItem.status` tightened from a bare `string` to a literal
  union (`Device.device_type`/`PageTag.source` precedent), gained `claimed_at`.
  `FailedJob`/`FailedJobsResponse` renamed `Job`/ `JobsResponse`, gained
  `status`/`claimed_at`.

## Phase 14 (Page notes / page linking)

### Schema

`pages.notes TEXT`, nullable. Page-level, not per-capture, same reasoning as
tags/collections: it's the user's own annotation about the URL, which doesn't
change with a re-archive. Deliberately **not** mirrored to D1 —
`internal/mirror/sync.go`'s payload only ever carried `title`, and notes are a
personal annotation, not bookmark structure, so it stays Postgres-only.

Also deliberately **not** folded into `SearchPages`'s full-text search over
`reader_text` — discussed and declined for now; revisit if it turns out to
matter in practice.

### Backend

- New `SetPageNotes` query (`queries/pages.sql`), right next to `SetPageTitle` —
  same `WHERE id = $1 AND user_id = $2` / `RETURNING *` shape, but unlike title,
  an empty value is meaningful here (clears the note) rather than rejected.
- `PatchPage` (`internal/httpapi/handlers.go`) gained a third optional field,
  `Notes *string`, alongside the existing `ExcludedFromMirror`/`Title` —
  extended the existing handler rather than adding a new route, since it already
  existed specifically to support "apply whichever of several optional fields
  were provided." Trim-then-nullify-if-empty: a blank/whitespace-only value
  stores `NULL`, not `""`, matching how `title`/`favicon_path` already
  distinguish "not set" from a real value.
- `pageResponse` and all three of its conversion functions
  (`pageResponseFromPage`/`FromListRow`/`FromSearchRow`) gained `Notes`. It now
  rides along on every page response — list, search, and detail alike — same as
  every other column on this struct; no separate "detail-only" field concept
  exists in this API today, so this doesn't introduce one just for notes. Worth
  revisiting if note length ever makes list/search payloads noticeably heavier
  in practice, but not a concern at today's usage.
- Six new `TestPatchPage` subtests: set, trim, clear-to-null-on-blank, and the
  standard 404-for-another-user's-page check, mirroring the existing title
  subtests' shape exactly.

### Markdown rendering (`src/lib/markdown.ts`)

Hand-rolled, not a library. The scope is intentionally three constructs (bold,
italic, simple lists), and a fixed, hand-rolled output vocabulary
(`<strong>`/`<em>`/`<ul>`/`<li>`/`<p>`/`<br>`, always wrapping HTML-escaped
text) means there's no separate sanitization step to get right or forget, unlike
adopting a general-purpose parser and then needing to sanitize _its_ output
separately. Bold is matched before italic so `**bold**`'s own asterisks are
never mistaken for italic markers. Asterisk-italic (`*x*`) can sit directly
against a word, matching common markdown convention; underscore-italic (`_x_`)
requires a non-word boundary on both sides (`(?<![\w])_(.+?)_(?![\w])`), so
identifiers like `my_variable_name` aren't mangled — the one place a naive regex
implementation usually gets underscores wrong.

Source is stored as-is in `pages.notes` and rendered client-side on read — the
same choice `reader_text`/`ai_summary` already made (see
`CaptureReader.svelte`'s own doc comment), just with actual formatting this
time.

### Frontend (`PageDetail.svelte`)

New "Notes" section, placed after Collections and before the Captures list.
Reuses the existing `edit-toggle` button class (pencil ⇄ checkmark, same as
Tags/Collections). Unlike Tags/Collections' per-item instant-save add/remove
forms, a note is one free-text field, so its edit UX matches the existing
**title-edit** pattern instead: a textarea plus explicit Save/Cancel, not
autosave-per-keystroke. Six new component tests (`PageDetail.test.ts`): empty
state, markdown actually rendering (not just displaying literal `**bold**`
text), save, clear-via-blank-save, cancel discards without calling the API, and
the API's own error message surfacing on a failed save.

`Page`/`PageDetail` (`src/lib/types.ts`) gained `notes: string | null`, which
required updating five existing test fixtures
(`PageList`/`CollectionDetail`/`Library`/`PageDetail`/`TagDetail.test.ts`) that
construct a full `Page` object literal and started failing type-checking once
the field became required.

Seven new `pagedetail_notes_*` message keys, both `en.json`/`fr.json`, kept at
full parity (251/251 keys each).

### Page links backend

- `queries/page_links.sql` (new file): `AddPageLink`/`RemovePageLink` use
  `LEAST`/`GREATEST` to canonicalize the pair, so callers never compute ordering
  themselves. Actually running these through `sqlc generate` (not just
  hand-tracing) caught two real problems before they shipped: `LEAST($1, $2)`
  alone left sqlc unable to infer a parameter type at all (an unusable
  `interface{}` field), and separately, it derived duplicate-looking generated
  struct field names (`PageIDA`/`PageIDA_2`) for `RemovePageLink`'s two
  arguments — easy to transpose by accident. Fixed with explicit `::bigint`
  casts and named `sqlc.arg()`s; both now generate clean `int64` `PageA`/`PageB`
  fields. `ListPageLinks` returns "the other page" in every link regardless of
  which side of the stored pair it's on, via a `CASE` on which id matches the
  one being looked up — this is what makes a link bidirectional _at read time_,
  with no special-casing needed anywhere else.
- `SearchPagesForLinking` (`queries/pages.sql`): plain `ILIKE` over
  `title`/`normalized_url` in one query (one `$query` parameter, matched against
  both columns with `OR`), excludes the page being linked from. Deliberately not
  a `tsvector`/trigram setup — proportionate to a personal library's scale, same
  reasoning as collections' own recursive-CTE comment elsewhere in this
  codebase. No pagination/`total_count`, unlike `ListPages`/`SearchPages`: this
  backs a live typeahead dropdown showing a handful of matches (capped at a new
  `linkCandidateLimit = 8`), not a browsable listing.
- Three new handlers: `AddPageLink`/`RemovePageLink`
  (`POST`/`DELETE /api/pages/{id}/links[/{linkPageId}]`, same
  both-sides-verified-independently pattern `AddPageToCollection` already uses,
  plus a same-page-to-itself rejection `AddPageToCollection` doesn't need an
  equivalent of) and `SearchPagesForLinking`
  (`GET /api/pages/link-candidates?q=...&exclude=...`). New shared
  `pageLinkResponse` type (id/title/normalized_url/favicon_path) covers both the
  linked-pages list and search-candidate results, since they're identical in
  shape — one type, not two. `pageDetailResponse` gained
  `Links []pageLinkResponse`, populated in `GetPage` via `ListPageLinks`.
- Router's own top-of-file route summary comment updated to describe the new
  routes, matching its existing practice of staying in sync with the routes
  actually registered below it.
- New tests: `TestPageLinks` (bidirectional visibility from both sides without a
  second POST, removal from either side, self-link rejection, cross-user 404,
  and that linking the same pair twice — even initiated from each side in turn —
  is a no-op via `AddPageLink`'s own canonicalization, not a duplicate row) and
  `TestSearchPagesForLinking` (title match, URL match, empty-query returns
  nothing rather than everything, cross-user isolation).

### Page links frontend

New "Linked pages" section, placed after Notes and before the Captures list.
Rendered as a lightweight list (favicon left, title/normalized_url on two lines
below it) rather than pills — a link carries more identifying information than a
tag/collection name does, so it reads better as a row; see this phase's own
mockup-revision note above. Favicon handling mirrors `PageList.svelte`'s own
pattern exactly, including _why_ it's a `SvelteSet` keyed by page id rather than
one shared boolean: several linked pages' images can be loading/failing
independently at once, on this screen same as that one.

### Archive stats

A small aggregate stats section on the Settings page — page/capture counts and a
disk-usage breakdown by category — rather than a dedicated profile page, which
doesn't exist yet and isn't being added just for this.

#### Backend

- `queries/stats.sql` (new file): `GetUserStats`, one query, two CTEs
  (`page_totals`, `capture_totals`) cross-joined so both always return exactly
  one row (COUNT is 0, SUM is `NULL` over an empty set, coalesced to 0) even for
  a brand new user. Hit a genuine, non-obvious Postgres quirk while writing
  this: a scalar subquery for the page count reported "column reference
  \`user_id\` is ambiguous" once combined with the joined CTE — even though each
  piece, tested in isolation, referenced only one `pages` table and had nothing
  ambiguous about it on its own. Confirmed by testing each CTE alone (fine)
  against the combined form (ambiguous) directly rather than guessing.
  Explicitly qualifying `pages.user_id` in _both_ CTEs (not just the one that
  actually needed it, based on the isolated test) resolved it; documented
  in-query so it doesn't get "simplified" back to the ambiguous bare form later.
- `GetStats` handler (`GET /api/stats`) + `statsResponse` type
  (`internal/httpapi/handlers.go`), registered as its own endpoint rather than
  folded into `GetSettings` — a computed read-only aggregate is a different
  concern than mutable user preferences, matching how the rest of this API keeps
  distinct resources separate. Router's own top-of-file summary comment updated
  to match.
- `TestGetStats`: zero-state for a brand new user, a real sum across multiple
  pages/captures (including one with a favicon and one with a thumbnail, set
  directly via `InsertCaptureIdempotent`/`SetCaptureThumbnail` rather than
  `dbtest.CreateCapture`'s own fixed defaults, to exercise those two columns
  specifically), and cross-user isolation.
- `internal/auth.RequireAdmin` existed but had never been wired to a route
  before this — `GET /api/admin/stats` is its first real caller, registered in
  its own `r.Group` (sibling to the `RequireSession` one, not nested inside it —
  nesting would run session resolution twice for no reason, since `RequireAdmin`
  already builds on `RequireSession` internally). Router's own top-of-file
  comment, which literally said "RequireAdmin exists... but isn't used here,"
  updated to match and point at DESIGN.md's reasoning.
- Two new queries (`queries/stats.sql`): `GetSystemStats` (same shape as
  `GetUserStats`, just without the per-user filter — literally every
  page/capture, instance-wide; no repeat of the ambiguity bug above, since its
  subquery and outer scope don't share a `pages` reference the way
  `GetUserStats`'s two CTEs did) and `GetTopUsersByStorage` (the 5 heaviest
  users, inner-joined throughout rather than `LEFT JOIN` — a user with zero
  pages/captures isn't a "top consumer" by definition, so excluding them
  entirely from the ranking is correct here, not a bug, unlike `GetUserStats`,
  which needs to represent "zero" for one specific user rather than omit a row).
  Deliberately coarser breakdown than the personal/system three-way split —
  compressed HTML vs. favicons-and-screenshots combined into one `other_bytes`
  figure, confirmed directly: five rows of three numbers each starts to compete
  with the one number that actually matters for a ranking.

### Frontend

New "Archive stats" section on `Settings.svelte`, loaded independently alongside
language/theme in the same mount-time `$effect` (three parallel requests now,
not two) — a stats load failure doesn't block or blank out the language/theme
toggles, and vice versa, same reasoning `PageDetail`'s own independently-loaded
sections already follow.

Laid out as two plain summary lines ("_N_ pages archived (_M_ captures)",
"Occupying _size_ total disk") followed by a details-card breakdown
(HTML/Favicons/Screenshots, each its own label+value row) — not one uniform
label/value table throughout, since the two summary lines are already complete
sentences from their own message strings, not a label paired with a separate
value the way each breakdown row genuinely is. The disk total sums compressed
HTML + favicons + screenshots; the uncompressed HTML figure is shown
parenthetically next to the compressed one for context, not added into the
total.

Admin stats: gated on `session.user?.role === "admin"`, no new plumbing needed
since the session store already carries role from its own bootstrap fetch.
Rendered as the app's **first real `<table>`** — every other ranked/itemized
list elsewhere uses a details-row card or a chip/pill list, but four independent
numeric columns per row didn't fit either shape well. Styled to match rather
than reading like a dropped-in generic table: card-surface outer shape,
dotted-rule between body rows, eyebrow-style headers, data-mono applied only to
the numeric `<td>` cells (caught and fixed a first pass that also applied it to
header words like "Captures," which read oddly — data-mono is for data, not
labels).

## Phase 15 (Storage model: per-capture directories; `gc` hardening)

A pre-release review of the storage model, done deliberately before the first
tagged release since the on-disk layout is one of the few things here that gets
genuinely hard to change once real archives exist. See DESIGN.md §4 for the
model and §15's own entry for the decision record; this covers what actually
landed.

### `internal/archive`: `NewCapture`, and directories that belong to one capture

- **`Store.NewCapture() (relDir string, err error)`** replaces `CaptureDir` (now
  unexported as `captureDir`, and taking a capture id rather than a content
  hash). Mints a UUIDv7 via `google/uuid` (already a direct dependency), shards
  three levels deep, creates the directory, returns the root-relative path.
- **The shard is the id's trailing four hex characters, not its leading ones.**
  UUIDv7 puts a millisecond timestamp in the leading bits, so leading-char
  sharding would put every capture from the same period in one bucket — the
  exact opposite of what sharding is for. The last group of a v7 id is entirely
  `rand_b`, so it distributes uniformly. This is the one non-obvious consequence
  of choosing v7 over v4, and worth stating because a future reader reaching for
  "just take the first two characters, like git does" would be quietly wrong.
- **`MkdirAll` for the shard levels, plain `os.Mkdir` for the leaf.** The shard
  directories are shared by many captures, so already-exists is the normal case
  there; the leaf is the collision signal. `EEXIST` regenerates the id (bounded
  loop, `newCaptureAttempts = 5`) rather than adopting the directory. This check
  has to be at mkdir time, not a Postgres constraint, because the disk write
  precedes the commit — a constraint alone would reject the row only after
  `writeAtomic` had already overwritten the other capture's bytes.
- **`WriteHTML(relDir, data)` /
  `WriteAsset(relDir, name, ext, data, compress)`** now take the capture
  directory rather than a hash. Filenames are role-based: `page.html.zst`,
  `favicon.{ext}[.zst]`, `thumbnail.png`. A directory holds exactly one of each,
  so nothing needs a hash to stay distinct — and a re-render (retried
  screenshot, re-extraction after a Readability.js upgrade) now overwrites in
  place through the existing atomic rename rather than writing a new file and
  orphaning its predecessor.
- **New: `Store.WalkEmptyDirs` and `Store.RemoveEmptyDir`.** `Walk` reports
  regular files only, so a capture directory created by `NewCapture` for an
  ingestion that then failed early is invisible to it. `RemoveEmptyDir`
  re-checks emptiness immediately before removing and reports "no longer
  applicable" as `(false, nil)` rather than an error — both ways that happens (a
  parent already pruned by an earlier file removal; a concurrent `NewCapture`
  claiming the directory) are ordinary, not failures a caller should have to
  recognize and discard. Doing the re-check inside the package also avoids
  exposing the Store's root, which is the only thing "relative to" actually
  means.
- **`Walk` now also reports `modTime`**, and tolerates a root that doesn't exist
  yet (zero files, which is exactly right for an instance that hasn't ingested
  anything). Parent-pruning was factored out of `Remove` into
  `pruneEmptyParents`, shared with `RemoveEmptyDir`.

### Callers

Small surface, which is most of why this was worth doing now:

- **`internal/ingest`** calls `NewCapture` before `WriteHTML`, and threads
  `relDir` into `captureFavicon` in place of the html hash.
- **`internal/screenshot`** derives the directory as
  `filepath.Dir(job.HtmlPath)`. `ClaimDueScreenshotJobs` already returned
  `html_path` alongside `content_hash`, so this needed no new query — just
  dropping the now-unused `content_hash` from the `RETURNING` list.
- **Manual upload (§3d) is not built yet**, so there was no second ingestion
  path to keep in sync.
- `content_hash`/`favicon_hash`/`thumbnail_hash` are untouched as columns and
  still in the capture DTO. §3c's retry-vs-collision disambiguation, which
  compares the returned row's `content_hash` against the freshly computed one,
  works exactly as before.

### Schema

`migrations/00004_create_captures.sql` edited in place (nothing has shipped, so
no stacked `ALTER`): `CONSTRAINT captures_html_path_key UNIQUE (html_path)`.
`html_path` is one-to-one with the capture directory, so this is the
database-side statement of the same invariant — belt-and-suspenders behind
`NewCapture`'s exclusive mkdir, and notably a constraint that would have been
_wrong_ under the previous layout, where identical HTML deliberately shared a
path.

### `internal/gc`: two safety rails and an empty-directory pass

`Run` now takes `gc.Options{DryRun, Force}` rather than a bare bool, and is
restructured **collect-then-remove** rather than removing inline during the
walk. That restructure is what lets the orphan-fraction check be a clean
all-or-nothing refusal instead of an abort partway through a deletion pass.

- **`recentThreshold` (15 minutes)** — anything modified more recently is left
  alone regardless of the live set. Reused from the D1 claim-visibility timeout
  and `internal/screenshot`'s `claimStaleTimeout` rather than inventing a third
  number for the same question. The concrete bug this fixes existed before this
  round: `archive.Store`'s `.tmp-*` files are in `Walk`'s namespace and _by
  construction_ can never be in the live set, so a sweep concurrent with an
  ingestion would unlink one mid-write and the writer's `os.Rename` would then
  fail `ENOENT`. Not silent corruption, but a spurious failure appearing only
  under concurrency — the kind that gets diagnosed slowly.
- **`maxOrphanFraction` (0.5), floored at `safetyCheckMinFiles` (100)** —
  refuses the run with `*TooManyOrphansError` if too much comes back orphaned.
  Guards a footgun rather than a known bug: the live set is stored path strings
  compared against walked path strings, and any future normalization divergence
  between the two sides produces an empty intersection and marks the whole
  archive as garbage. The floor exists because a four-file archive with three
  orphans is 75% and means nothing. `--force` overrides; a dry run reports that
  the real run would refuse (`Result.SafetyCheckTripped`) rather than silently
  proceeding, which would make `--dry-run` misleading in exactly the situation
  it matters most.
- **Empty-directory pass**, per `WalkEmptyDirs` above, age-floored the same way
  — an unreferenced-and-empty directory a few seconds old is just a capture
  between `NewCapture` and `WriteHTML`.
- `Result` gained `FilesSkippedRecent`, `EmptyDirsRemoved`,
  `SafetyCheckTripped`. `cmd/gc.go` grew `--force`, reports the new counters,
  and returns `TooManyOrphansError` unwrapped (it already explains itself in
  full, including what to do; `fmt.Errorf("running gc: %w")` would only prefix
  noise onto an actionable message).

### Tests

`internal/archive` and `internal/gc` test files rewritten. New coverage worth
naming: `NewCapture` never returning the same directory twice across 500
same-millisecond mints; identical content producing two independent files such
that removing one leaves the other intact (a test that could not have been
written under the previous layout); `.tmp-*` files surviving a sweep; empty
capture directories being pruned while recently-created ones are not; and all
three states of the orphan-fraction check (refuse, `--force`, dry-run-reports).

One honest gap: `NewCapture`'s `EEXIST` branch can't be reached from an external
test package without injecting the id generator, and adding that seam purely to
cover a branch guarding an unreachable event wasn't judged worth it. The test
there asserts the property the branch protects instead — an already-populated
capture directory is never handed back out — and says so in its own comment
rather than implying more coverage than exists.

## Phase 17 (Queue screen: awaiting-ingestion section; device attribution)

Closes the last invisible stage of a capture's life on the Queue screen. See
DESIGN.md §8's "Pending-capture claiming and cleanup" for the endpoint design.

### The gap

The screen already showed the capture queue and the enrichment jobs, but nothing
in between — so from the moment a device finished uploading until the backend's
next poll picked the capture up, there was no evidence anywhere that anything
was happening. At the default `agent_worker_poll_interval_seconds` of 1800
that's up to half an hour of apparent silence, which is exactly long enough to
make someone assume it failed.

### The move (`internal/pendingcaptures`)

The pending-captures Worker client moved out of `internal/ingest` into its own
package, taking `PendingCapture` with it. `WorkerClient` became `Client`,
`NewWorkerClient` became `NewClient`.

The reason is the dashboard call site, not tidiness: `internal/httpapi` needs to
list these rows, and having `cmd/server.go` construct an
`ingest.NewWorkerClient` would read as though `recueil server` ingests things.
It doesn't. `pendingcaptures.NewClient` sitting beside `queueitems.NewClient`
and `devices.NewClient` is obviously right; the alternative was obviously wrong.

The seam is one package per Worker _resource_, not per caller — the agent claims
/marks/sweeps, the dashboard lists, both through the same client.
`internal/queueitems` already worked this way (its `List`/`Retry` serve the
dashboard, its `Cleanup` serves the agent), so this follows an existing shape
rather than inventing one. Adding a second client for the same table, split by
caller, was the alternative and would have put the seam in the wrong place.

`parseD1Timestamp` (RFC 3339) now exists in both `internal/ingest` and
`internal/pendingcaptures`, following the duplicate-don't-share convention
`queueitems` and `devices` already record for their own
`parseD1NativeTimestamp`.

### Worker

- **`GET /internal/pending-captures?user_id=`**, alongside the existing `POST`
  claim on the same path. Read-only and user-scoped; the `POST` is cross-user
  and mutates `claimed_at`. A test asserts the `GET` doesn't claim — a dashboard
  left open must not be able to starve the ingester.
- The `GET` route added last round's test asserting a 404 on that path had to be
  rewritten. That test's rationale (a stale caller should fail loudly rather
  than silently read rows without claiming) is now moot: nothing has shipped, so
  there are no stale callers, and the routes should be whatever makes sense.
- **`GET /internal/queue-items` gained `claimed_by_device`** via a `LEFT JOIN`
  on `tokens`. Left, not inner: unclaimed items have a `NULL`
  `claimed_by_token_id`, and revocation is a row delete, so a revoked device has
  no name left. The item still lists either way, and both cases are tested.

### Timestamps: two formats on one row

`UserCapture` parses into real `time.Time` values rather than passing D1's
strings through, and this is the part most likely to be quietly broken by a
future change:

- `captured_at` is written by the **capturing device** as RFC 3339.
- `claimed_at` is written by **D1's own `CURRENT_TIMESTAMP`** as
  `YYYY-MM-DD HH:MM:SS` — no `T`, no `Z`, no offset of any kind.

Passing the latter through to the browser would have been silently wrong rather
than visibly broken: `new Date("2026-07-12 12:05:09")` is parsed as _local_ time
by every engine, so every relative timestamp in the new section would have been
off by the viewer's UTC offset — correct in UTC, wrong everywhere else. Same
reason `queueitems.Item` already does this. `internal/pendingcaptures`' own
tests cover both formats on one row explicitly.

`fetched_by_backend` maps from an integer, since SQLite has no boolean type.

### Frontend

A third `<section>` between the two existing ones, reusing the same
`StatusCategory`/badge/`.items` machinery so it's visually indistinguishable
from its neighbours. `categoryForPendingCapture` maps
`(fetched_by_backend, claimed_at)` onto pending/active/done — with no `failed`,
deliberately. Pending captures feed the summary pills alongside items and jobs,
and `loadAll` now issues three parallel requests rather than two.

Badge labels are stage-specific ("Waiting"/"Ingesting"/"Ingested") rather than
reusing the queue's "Pending"/"Claimed"/"Captured": same categories underneath,
but the words describe what's actually happening at that stage.

### The passthrough clients sit in a test-coverage gap

`claimed_by_device` was added to the Worker's query and to the dashboard's own
`QueueItem` type, but not to Go's `queueitems.Item` — so `encoding/json`
silently dropped it in the middle and the Queue screen never showed a device
name. Found by running it, not by any test.

Worth recording as a structural point rather than a one-off, because neither
suite could have caught it: the Worker's tests assert against the Worker's own
response, and the dashboard's tests stub `apiJSON` directly, so the Go client
between them is exercised by nothing but its own tests. That gap is exactly
where a hand-rolled, manually-synced API client (§13a's own disclosed tradeoff)
will fail, and it fails silently — an unknown JSON field is dropped, not an
error.

The practical rule: a field added at both ends needs an assertion in the
relevant client's own test. Added for this one;
`internal/pendingcaptures.ListForUser` was checked against `src/lib/types.ts`'s
`PendingCapture` and already matches on all five fields.

`Item.ClaimedByDevice` is `*string`, not `string`, so a JSON null (nobody has
claimed it, or the device was revoked — tokens are revoked by row delete, so the
LEFT JOIN finds nothing) stays nil rather than becoming `""`, which would render
as a dangling "by " with no name after it.

### Metrics: storage stats and agent heartbeat

Revisited `internal/metrics` post-1.0 to see what was worth adding now that the
job pipeline, queue, and storage model all actually exist. Two additions, both
Postgres-only — see `DESIGN.md`'s metrics section for why that's a hard
constraint, not an oversight (real queue depth lives only in D1, and scraping
the Worker on every Prometheus tick risks the Cloudflare free tier for no real
benefit).

**Storage stats.** `recueil_pages_total`, `recueil_captures_total`, and
`recueil_storage_bytes{kind}` reuse `GetSystemStats` — the same query the
dashboard's admin stats screen already runs. No new query needed.

**Agent heartbeat.** New `agent_heartbeats(cycle, last_success_at)` table
(migration `00012`), upserted into by `cmd/agent.go`'s `workerCycle.run` and
`runLocalCycle` after a cycle completes — but only when every step in that cycle
actually succeeded, not just because the cycle ran. Recording unconditionally
would hide exactly the failure mode this metric exists to catch: an agent
process that's alive and ticking but whose ingestion has been silently failing
every cycle. The D1 cleanup sweep (only runs every `cleanupInterval`, not every
cycle) is deliberately excluded from the gate for `cycle="worker"` — tying
heartbeat freshness to something that doesn't run every cycle would make the
metric's cadence depend on which ticks happened to also trigger a sweep.

Considered whether this holds up under multiple concurrent `agent` processes
before building it — worth recording since it's a real, deliberate scope
decision, not an oversight. `pending_captures.claimed_at` already exists
specifically because two agents can otherwise double-ingest the same row, so the
codebase already tolerates concurrent agents correctly. But a shared,
cycle-keyed heartbeat (not scoped per instance) answers "is at least one agent
still working," not "is my agent healthy" — it can't catch one of several agents
silently dying while the others keep going. Confirmed this project runs exactly
one `agent` process per deployment (aside from a redeploy's brief overlap, which
an upsert on `cycle` tolerates fine) before shipping the shared-row version
rather than the per-instance one; per-instance would need a stable identity
(hostname/ container ID aren't stable across a redeploy) and is a deliberate
future redesign, not a gap in this one.

## Phase 18 (API tokens: MCP-facing auth infrastructure)

Step one of a two-step MCP feature: this phase is the auth credential the MCP
server (a later phase) will authenticate against, built and merged on its own
first, deliberately unwired to anything MCP-specific yet — see DESIGN.md §5's
new "API tokens (machine access, e.g. MCP)" subsection for the decision record.
Nothing here changes existing behavior; it's new, currently-unused surface area.

### Schema

`migrations/00012_create_api_tokens.sql`: `api_tokens`, same hashed-opaque-
token shape as `sessions`, but no `expires_at` at all — a standing per-client
credential, not a login session. `token_hash` is uniquely constrained the same
way `session_hash` is.

### Auth primitives (`internal/auth/apitoken.go`)

- **`GenerateAPIToken`** is `GenerateSessionToken`'s twin: 32-byte CSPRNG,
  `rcl_api_...` prefix (joining the existing `rcl_sess_`/`rcl_pair_`/
  `rcl_live_`/`rcl_bootstrap_` family), hashed via the existing `HashToken` — no
  new crypto introduced for what's structurally the same credential shape.
- **`RequireAPIToken`** is structurally parallel to `RequireSession`: resolves
  `Authorization: Bearer rcl_api_...`, 401s on anything missing/malformed/
  unrecognized, and on success attaches the user via the _same_ `userContextKey`
  `RequireSession` uses — so any handler written against `auth.UserFromContext`
  works unmodified regardless of which of the two middlewares authenticated the
  request. There's deliberately no `APITokenIDFromContext` counterpart to
  `SessionIDFromContext`: nothing about an api-token-authenticated request needs
  to distinguish "this token" from the user's other ones the way session
  revocation's self-delete guard does.
- **`last_used_at` is touched synchronously**, not via the fire-and-forget
  `waitUntil` pattern D1 device tokens use — that pattern exists specifically
  for Cloudflare Workers' CPU budget, which doesn't apply to a plain Postgres
  `UPDATE` here.

### Backend endpoints (`internal/httpapi`)

`POST /api/tokens`, `GET /api/tokens`, `DELETE /api/tokens/{id}` — all in the
existing `RequireSession`-gated group, self-scoped the same way `/api/devices`
already is (`DeleteApiTokenForUser`'s `:execrows` two-column WHERE is
`DeleteSessionForUser`'s exact pattern, reused rather than reinvented).
`GET /api/tokens` never returns the token or its hash, only what's needed to
recognize/manage a row (`id`, `name`, `created_at`, `last_used_at`); the raw
token is returned exactly once, from `POST /api/tokens` alone.

### Not built in this phase

- **`RequireAPIToken` is not mounted on any route.** There's no MCP endpoint yet
  for it to guard; it exists now, tested in isolation, so the MCP phase can
  mount it without also having to get the credential mechanism right at the same
  time.
- **No dashboard UI.** Per the design decision, this will live as a second list
  on the existing Manage Devices screen rather than a new page — deferred to a
  frontend-only follow-up phase, mockup-first as usual.

### Tests

`internal/auth/apitoken_test.go`: `TestGenerateAPIToken` (prefix, hash
correctness, non-collision); `TestRequireAPIToken` split "No Database" (no
header / non-Bearer scheme / empty Bearer value — all rejected before any query
runs) and "With Database" (valid token resolves to the right user; unknown token
rejected; revoked token rejected — the last one is also the executable check on
§5's "revocation is effectively immediate" claim, since there's no D1/Worker
propagation delay to wait out).

`internal/httpapi/handlers_test.go`: `TestCreateAPIToken` (mint + blank-name
rejection + unauthenticated), `TestListAPITokens` (self-scoping, plus an
explicit assertion that the response body never contains the raw token hash —
not just that the JSON schema omits a field, but that the substring isn't
present anywhere in the payload), `TestRevokeAPIToken` (self-scoping mirrors
`TestRevokeDevice`'s "can't revoke another user's by guessing the id" case
exactly).

### API tokens: Devices screen frontend

#### `Devices.svelte`

- New "API tokens" section, placed directly after "Paired devices" and before
  "Active sessions" — per DESIGN.md §5's "second list alongside paired devices"
  call, keeping the two credential-management lists adjacent.
- **The raw token is a dismissible callout, not a list row.** Unlike the pairing
  token above it (decrypt-and-redisplay on demand, §5), an API token's raw value
  only ever exists in the `POST /api/tokens` response — there's no `GET` that
  could return it again. `revealedToken` is plain `$state`, held only in memory,
  cleared on dismiss or on creating a second token; it never touches `apiTokens`
  (the persisted list), which only ever carries the redacted shape
  (`id`/`name`/`created_at`/`last_used_at`).
- **The new row is appended from the create response directly, no follow-up
  `GET /api/tokens`.** The create response already carries everything a list row
  needs (id/name/created_at); `last_used_at: null` is filled in locally, which
  is provably correct for a token that's seconds old, not a guess.
- **`button.primary`** is a new small modifier on the shared bordered-button
  chrome — an affirmative, filled-surface treatment for "Create token"
  specifically, distinct from the bordered default every other button on this
  screen uses. Deliberately not `comp.primary-button` (the auth-screens' mixin):
  that one carries `margin-top`/padding sized for a standalone form submit,
  which doesn't fit an inline row button here.
- **`formatDateTime`, `m.devices_last_used_at`, `m.devices_never`** are reused
  as-is from the paired-devices list — identical shape (`created_at`/nullable
  `last_used_at`), no reason for a second copy. `m.devices_apitokens_created_at`
  is new (`"created {date}"` vs. devices' `"paired {date}"` — the verb genuinely
  differs, a token is created, not paired).

#### Types (`src/lib/types.ts`)

`ApiToken` (list-row shape, no `token` field), `ApiTokenListResponse`,
`ApiTokenCreateResponse` (the one shape that does carry `token`) — kept as three
distinct interfaces rather than one with an optional `token?`, so it's not
possible to type a list row as if it could ever legitimately carry a raw value.

#### Tests (`Devices.test.ts`)

`mockLoad`'s dispatcher gained a fourth branch (`/tokens`) — required, not
optional: the mount-time `$effect` now fires four parallel loads instead of
three, so every existing test would have hit `mockLoad`'s "unexpected apiJSON
call" throw without it. All 26 pre-existing tests still pass unchanged.

New `describe("api tokens", ...)` block (12 tests), structured like the existing
`sessions` block: load/empty/error states, create (asserts the exact `{name}`
body sent, the reveal callout appearing, the new row landing in the list, and
the name input clearing), blank-name client-side rejection, copy, dismiss
(asserts the revealed value disappears but the list row doesn't), revoke
confirm/decline/error — the confirm-and-error cases mirror
`TestRevokeDevice`-equivalent coverage on the Go side.

## Phase 19 (MCP Server: read-only tools)

Step two of the MCP feature, on top of Phase 18's auth. Full design record in
DESIGN.md §16; this entry covers what actually landed and a few things
discovered along the way that weren't settled at design time.

### New dependency

While researching the SDK, found and factored in
[GO-2026-5771](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/jsonschema)
(DNS rebinding via the SDK's HTTP transport, patched in v1.4.0 -- long since
covered by pinning v1.7.0) and the more recent removal of the SDK's default
cross-origin protection as of v1.6.0. `internal/mcpapi.NewHandler` explicitly
configures
`StreamableHTTPOptions.CrossOriginProtection = http.NewCrossOriginProtection()`
rather than relying on the SDK's now-off default or its deprecated
`enableoriginverification` compatibility flag. See DESIGN.md §16 for the full
reasoning, including why bearer-token auth already changes the threat model here
regardless.

### `internal/mcpapi` (new package)

Sibling to `internal/httpapi`, not a subpackage -- `mcpapi.go` (server/handler
construction, `Stateless: true`, the cross-origin config above, `clampLimit`,
local `textOrEmpty`/`timestamptzOrEmpty` copies of `internal/httpapi`'s own
unexported helpers) and `tools.go` (all seven tools).

### Tools

`search_archive`, `list_recent`, `list_tags`, `list_pages_by_tag`,
`list_collections`, `list_pages_by_collection`, `get_page` -- all read-only, all
scoped via `auth.UserFromContext`. `list_pages_by_tag`/
`list_pages_by_collection` resolve-then-list (`GetTagBySlug`/
`GetCollectionByID` first, for the ownership check neither `ListTagPages` nor
`ListCollectionPages` does on its own), then clamp to `limit` in Go since
neither underlying query takes one.

`get_page` checks an explicit `capture_id` against the resolved page's own
`ListCapturesByPage` results before returning its content -- catches a caller
naming a `page_id` it owns and a `capture_id` belonging to a _different_ one of
its own pages, which `GetCaptureByIDForUser` alone wouldn't catch (it only
scopes by `user_id`, not by the specific page passed alongside it). Covered by
`TestGetPage/a_capture_id_belonging_to_a_different_page_of_the_same_user_is_rejected`.

Tool-level failures ("no page with that id", a foreign `capture_id`, etc.)
return `*mcp.CallToolResult{IsError: true, ...}`, not a Go `error` -- the SDK
surfaces a Go `error` return as a JSON-RPC protocol-level error, which isn't the
right shape for "this call succeeded, the answer is just a normal failure."
Every test asserting one of these checks `result.IsError`, not `err`.

### Routing (`internal/httpapi/router.go`)

`POST /mcp` mounted as a top-level route -- sibling to `/api`, not nested under
it, since it's a different auth mechanism (bearer `api_tokens`, not the session
cookie every `/api` route uses) and a different request framing (JSON-RPC over
Streamable HTTP). Guarded by `auth.RequireAPIToken`, unused until now since
Phase 18 built it with no caller yet. `cmd/server.go` needed no changes --
`NewRouter` already receives the `*db.Queries` `mcpapi.NewHandler` needs.

### `internal/auth`: one small addition

`auth.NewContextForTesting(ctx, user)` -- exported specifically because
`internal/mcpapi`'s tool methods are deliberately unexported (only
`registerTools` should call them), which means testing them means an internal
(`package mcpapi`) test file, same reasoning as
`internal/auth/session_test.go`'s own package choice. But `internal/mcpapi`
can't reach `internal/auth`'s unexported `userContextKey` even from its own
internal test package -- different package entirely -- so building a context
`auth.UserFromContext` will actually recognize needed one small exported hook.
Not usable to forge auth in production code; `userContextKey` itself stays
unexported.

### Tests (`internal/mcpapi/tools_test.go`)

One top-level test function per tool, `t.Run` subtests, `package mcpapi`
(internal, per the above). Every tool has an explicit "never returns/touches
another user's data" case, not just a happy path -- `TestSearchArchive`,
`TestListTags`, `TestListPagesByTag`, `TestListCollections`,
`TestListPagesByCollection`, and `TestGetPage` each check this either via a
second `dbtest.CreateUser` whose data must not appear, or via a direct attempt
to reach the other user's row by id/slug (expecting `IsError`, not a Go error,
per above). `TestGetPage` additionally covers: default-to-latest capture, an
explicit `capture_id` selecting older content and listing the newer one under
`other_captures`, and the cross-page `capture_id` guard. `TestClampLimit` is a
direct unit test of the shared clamp, since several other tests only exercise it
implicitly.
