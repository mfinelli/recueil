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
  together deliberately, so a claim attempt never leaks cross-user existence).
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
- **`complete`/`fail` are deliberately not built yet.** The brief for this phase
  was device auth + queue read/write; the endpoints that would actually
  transition a claimed item to `captured`/`failed` and write a
  `pending_captures` row are entangled with the capture-upload pipeline's shape
  (presigned R2 URLs, the upload-complete notification) rather than the
  queue/auth mechanics this phase covered — deferred to the phase that builds
  that pipeline.
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
  `forceRedirection` are deliberately not ported at all — see DESIGN.md §9 for
  why neither applies to normalizing an already-known URL string.
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
  wired up yet** — deliberately deferred (see Open items below); this is a fully
  callable, tested unit with nothing calling it in production yet.
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

- **`docker-compose.yml` still doesn't exist** for any service. Deliberately not
  built yet: local development currently uses a personal `compose.yaml` and the
  binary run directly, and the real, end-user-facing compose file will get built
  alongside end-user documentation, so the two stay consistent with each other
  rather than needing to be reconciled later.
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
  deliberately never returning the token itself to a caller.
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

### Still ahead

Queue-driven capture (alarm-driven polling, opening a tab nobody has open,
waiting for it to load, running the same capture pipeline, closing it,
`fail`-ing on timeout) and the bookmark-list menu (pulling the D1 mirror for
popup display) — no forced ordering between them. Safari packaging, whenever
that becomes a priority — mechanical (Xcode-wrapped, same source), not attempted
yet.

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
