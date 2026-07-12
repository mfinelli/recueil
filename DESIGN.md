# Recueil — Design Document

_Updated after Phase 1 implementation, then several more times after the tooling
work that followed it. Round one concentrated in §5 (bootstrap token redesign,
dashboard session auth resolved), the new §5b, §10 (schema for
`users`/`sessions`/`schema_migrations` resolved), the new §13a, and §15. Round
two updated §13 (repository tree) and §13a (CLI/config via cobra+viper, `chi`
replacing stdlib routing, goose moved to its `Provider` API, the new Postgres
test harness) and resolved the goose-as-library item §15 had left open. Round
three updated §13/§13a for `cmd/server.go` (the actual `recueil server`
subcommand, and `main.go`'s real role embedding both migration directories),
`Execute()` owning a single signal-aware context threaded via `cmd.Context()`,
and the health-check endpoints mounted on `internal/httpapi`'s router. Round
four added §13a's Metrics and HTTP middleware entries (Prometheus, `httplog`,
the chi middleware stack) and recorded OpenTelemetry as considered and
deliberately deferred, with the condition under which it'd be worth revisiting.
This round adds the new §3d (manual upload, bypassing the queue/R2/D1/Worker
entirely) and the `captures.source` column it introduces (§10). Round five,
prompted by discovering that the D1 password-hash mirror would require verifying
a slow hash (bcrypt) inside a CPU-limited Cloudflare Worker — infeasible at any
cost factor on the free tier, and not fixable by swapping in a faster
Worker-native primitive without still mirroring password-derived material into
D1. Replaces the D1 password-hash mirror entirely with a separate,
single-purpose per-user **pairing token**: D1 no longer stores anything
password-derived, in any form. Updates §2's architecture diagram, §5's
device-authentication design, §10's Postgres and D1 `users` tables, and the
D1-mirror security discussion accordingly. This is a retrofit of already-built
Phase 1 code (the `/internal/users/mirror` route and D1 `users` schema), not
purely new Phase 2 scope — noted in §15. Round six documents Phase 2 as actually
built: the `/pair`, `/queue` (enqueue/read/claim), and `/internal/tokens`
(list/revoke) Worker endpoints, plus a `/internal/queue-items/cleanup` endpoint
added once implementation surfaced that successfully-processed queue items had
no retention/cleanup story. Updates §5's `tokens` schema (`STRICT`, an index),
§8 (the claim endpoint's actual 410/409/404 semantics, and the new queue-item
cleanup design), §10's D1 schema (both tables' actual implemented shape), and
resolves the corresponding item in §15._

## 1. Overview

Recueil is a self-hosted personal web archiving tool. It replaces a Frankenstein
setup of ArchiveBox + Linkwarden + Karakeep (plus custom sync scripts) with a
single purpose-built system.

### Motivating problems with the current setup

- Headless-browser archivers (ArchiveBox-style) fail on sites with CAPTCHAs,
  paywalls, or content behind interaction (click-to-expand, infinite scroll,
  login walls).
- Multiple tools store multiple redundant formats (WARC, PDF, screenshots,
  MHTML, etc.), most of which are never used.
- Keeping three self-hosted tools in sync requires custom glue scripts.

### Core design principle

**Capture happens in a real, already-authenticated, already-rendered browser tab
— not a headless fetch.** This is the actual fix for the CAPTCHA/paywall
problem, not a workaround. Because of this, the system deliberately does **not**
add any server-side "fetch and archive a URL" fallback — doing so would
reintroduce the exact failure mode being solved.

Note that this principle applies to the _initial capture_ only. Once a page has
been captured as a fully inlined HTML file, deriving further artifacts from that
already-captured file (e.g. a thumbnail — see §6) is a different, safe
operation: it's rendering static, already-authenticated content offline, not
re-fetching a live page.

### Format decision

Store exactly one artifact format per capture: a fully inlined single HTML file
(SingleFile-style — CSS, images, fonts inlined as data URIs), plus a plain-text
Readability extraction, plus a thumbnail image. No WARC, no PDF, no MHTML.

---

## 2. High-Level Architecture

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│  iOS Shortcut    │     │  Share-sheet PWA │     │       CLI       │
│  (enqueue only)  │     │  (Cloudflare      │     │  (enqueue only) │
│                  │     │   Pages, public)  │     │                 │
└────────┬─────────┘     └────────┬──────────┘     └────────┬────────┘
         │                        │                          │
         └────────────────────────┼──────────────────────────┘
                                  ▼
                        ┌───────────────────┐
                        │  Cloudflare Worker │  (dumb relay + auth)
                        │  - device auth       │
                        │  - enqueue URL        │
                        │  - presigned R2 URLs  │
                        │  - D1 read/write      │
                        │  - service-secret-     │
                        │    gated backend API   │
                        └─────────┬─────────┘
                                  │
                    ┌─────────────┼─────────────┐
                    ▼                           ▼
             ┌─────────────┐             ┌─────────────┐
             │     D1      │             │     R2      │
             │ (queue,     │             │ (temp blob  │
             │  device      │◄────┐       │  storage)   │
             │  tokens,     │     │       │             │
             │  bookmark    │     │       │             │
             │  mirror)     │     │       │             │
             └──────┬──────┘     │       └──────┬──────┘
                    │            │              │
                    │  poll      │              │  pull
                    ▼            │              ▼
        ┌──────────────────────────────────────────┐
        │         Desktop Browser Extension          │
        │  - reads queue from D1 (via Worker)         │
        │  - user selects item → loads URL             │
        │  - captures: HTML (via vendored SingleFile     │
        │    library), Readability text                   │
        │  - uploads to R2 via presigned URL              │
        │  (no longer captures a screenshot — see §6)      │
        └──────────────────────────────────────────┘
                    │
                    │ (async, outbound-only polling)
                    ▼
        ┌──────────────────────────────────────────┐
        │      Backend (Go + Postgres, Docker)       │
        │  - polls Worker/D1 for pending captures      │
        │  - pulls blobs from R2, then deletes from R2   │
        │  - zstd-compresses HTML, stores locally          │
        │  - enqueues async screenshot job (§6)             │
        │  - runs optional AI enrichment (summary/tags)      │
        │  - pushes bookmark-list mirror row to D1 ───────────┘
        │    (via Worker, after each capture is processed)
        │  - pushes pairing-token-hash mirror to D1 ───────────┘
        │    (via Worker, on account creation/regeneration/revocation)
        │  - authenticates the dashboard directly (session      │
        │    auth against its own Postgres `users` table —       │
        │    no token/D1 involvement)                              │
        │  - serves dashboard API (reachable on LAN/VPN/etc.,      │
        │    reachability is the operator's responsibility)         │
        └──────────────────────────────────────────┘
                    │                           │
                    ▼                           ▼
        ┌──────────────────────┐   ┌──────────────────────────┐
        │  Screenshot service    │   │   Svelte Dashboard         │
        │  (chromedp +            │   │  - library browsing, search  │
        │  headless-shell         │   │  - version history per page   │
        │  container, driven       │   │  - tags (manual + AI),          │
        │  by the backend)          │   │    nested collections             │
        └──────────────────────┘   └──────────────────────────┘
```

### Key architectural property: capture path never touches the backend

The desktop extension, the share-sheet PWA, and the CLI depend **only** on the
Worker and R2 — both public, both authenticated via bearer token. None of them
ever need the backend to be network-reachable. The backend's only required
connectivity is **outbound**: polling the Worker/D1 API, pulling objects from
R2, making occasional authenticated calls to the Worker for device-token
revocation (§5), and — the one exception to "only ever talks to the Worker" — a
direct, narrowly-scoped call to Cloudflare's D1 query API to run schema
migrations at startup (§5b). It can run with zero inbound firewall rules and the
entire archiving loop still works end to end.

Backend network reachability is a concern **only** for the optional dashboard
(browsing your library, search, login). How that's exposed (LAN only, reverse
proxy, VPN, tunnel, etc.) is entirely up to the deployer and is intentionally
out of scope for this project — the repo should document the requirement ("must
be reachable by whatever device you want the dashboard on") without assuming any
specific networking solution.

---

## 3. Capture Flow

1. User adds a URL to the queue, either:
   - Directly in the desktop extension while browsing, or
   - Remotely via the share-sheet PWA (Android) or iOS Shortcut (phone) or CLI —
     these only enqueue, they never capture.
2. Enqueueing hits the Worker, which writes a row to `queue_items` in D1.
3. The desktop extension polls D1 (via the Worker) for pending queue items, on
   an infrequent schedule (see §7), and can notify the user something needs
   archiving. The extension also exposes a manual "check now" action in its
   popup for on-demand polling.
4. User selects a queued item (or a page they're currently on, for direct/
   unqueued capture) and triggers capture.
5. Extension captures:
   - Full inlined single-page HTML, via SingleFile's own capture code **vendored
     directly into the extension as a library** (see §3a) — not by messaging a
     separately installed SingleFile extension.
   - Readability.js-extracted plain text (run **in the extension**, against the
     live DOM, before any re-archival loses render-time state)
6. Extension requests a presigned R2 upload URL from the Worker, uploads both
   artifacts (HTML + reader text) directly to R2 (bypassing Worker body-size
   limits; presigned R2 PUT supports objects far larger than any archived page
   will ever be).
7. Extension notifies the Worker that the upload is complete → Worker writes a
   `pending_captures` row to D1, using a **client-generated UUID** as the row's
   id (and marks the `queue_items` row, if any, as `captured`).
8. Backend, on its own polling schedule, discovers the new `pending_captures`
   row, pulls the blobs from R2, zstd-compresses the HTML, stores it on local
   disk, computes content hashes (see §3b), deletes the R2 objects, writes rows
   to Postgres (idempotently — see §3c), and finally pushes a lightweight mirror
   row back to D1 for the bookmark-list feature (see §8).
9. Backend enqueues a **screenshot job** (async, decoupled — see §6).
10. (Optional, async) Backend enqueues an AI job to summarize/tag the capture
    using the Readability text (see §7).
11. Backup of the resulting Postgres data and local archive directory is the
    operator's own responsibility (see §14) — not part of this pipeline.

### 3a. SingleFile integration

SingleFile is not invoked as a separate, independently installed browser
extension via cross-extension messaging — that path isn't well-supported
(SingleFile is designed to be user-triggered via its own toolbar button, and
there's no first-class API for a third-party extension to invoke it and get the
result back programmatically).

Instead, SingleFile publishes its own capture logic as embeddable script files
intended for exactly this kind of reuse. The Recueil extension vendors these
files directly (e.g. `single-file-background.js`, plus a WebExtension polyfill)
and calls `extension.getPageData(...)` from its own content script to get back
`{ content, title, filename }`. This is "use SingleFile as a library within our
own extension," not "talk to a second installed extension" — it avoids any
dependency on a stable cross-browser extension ID, `externally_connectable`
support, or requiring the user to separately install SingleFile at all.

The extension's own `package.json`/bundler setup (already needed to pull in
Readability.js) is the natural place to also vendor SingleFile's capture code.

### 3b. Content hashing

Each capture stores **two** hashes:

- `content_hash` — over the full inlined HTML. Useful for exact byte-for-byte
  dedup detection.
- `reader_text_hash` — over the Readability-extracted plain text. This is the
  hash that drives the dashboard's "unchanged since last capture" flag.

The full-HTML hash is a poor signal for "did the visible content change" — most
real pages embed per-load-unique content (CSRF tokens, cache-busted asset URLs,
session IDs, timestamps) even when nothing meaningful changed, so it will almost
never repeat in practice. The reader-text hash is a much more reliable (though
still imperfect — Readability output can shift for reasons unrelated to the main
content) signal for that specific UI feature.

### 3c. Capture idempotency (crash recovery)

The `pending_captures.id` (a client-generated UUID, already required for
retry-safety on the upload-complete notification — see §8) doubles as an
idempotency key for backend ingestion:

```sql
ALTER TABLE captures ADD COLUMN source_capture_id TEXT UNIQUE;
```

Ingestion becomes: write the blob to disk, then
`INSERT ... ON CONFLICT (source_capture_id) DO NOTHING` into `captures`. If the
row already exists (a retry after a crash), skip straight to R2 cleanup and the
D1 `fetched_by_backend` flag update. Ordering the steps this way — disk write,
then DB commit, then R2 delete, then D1 flag — means a crash at any point either
leaves the R2 object in place for a safe retry, or leaves only harmless orphaned
cleanup state; nothing can double-insert a capture.

### Re-archiving the same URL

Re-archiving a previously captured URL is **not** an update — it's a new version
under the same logical page. The backend groups captures by `normalized_url`
(see §9, URL normalization) into a `pages` row, and each individual capture
becomes a new `captures` row linked to that page. The dashboard shows all
historical versions of a page with their capture timestamps.

### 3d. Manual upload (bypassing the queue)

For the case where a page was captured somewhere Recueil's own extension wasn't
installed — received as an email attachment, saved from a device without the
extension, handed over by someone else — the dashboard supports directly
uploading an already-captured, fully inlined SingleFile-style HTML file plus the
URL it came from. This is a genuinely different pathway from §3's queue-based
flow, not a variant of it:

- **Bypasses R2, D1, and the Worker entirely.** The dashboard already talks
  directly to the backend (§2, §11 — that's the one thing dashboard reachability
  is for), so this is a single authenticated `POST` straight into the backend,
  gated by the same `RequireSession` middleware as any other dashboard endpoint,
  scoped to the authenticated user the same way any other capture is
  (`pages.user_id`).
- **Reader text is extracted client-side, in the dashboard's own browser,
  before/during upload** — the dashboard runs Readability.js against the
  uploaded HTML and sends the extracted text alongside the raw HTML, mirroring
  exactly how the extension does it today (§3a: extraction happens in a real
  browser, never server-side). This keeps the principle consistent across both
  capture paths rather than introducing a Go-side Readability port for this one
  pathway. The page title is read the same way, from the uploaded HTML's
  `<title>` tag, rather than requiring a separate manual field.
- **No idempotency-by-UUID machinery, unlike §3c.** §3c's `source_capture_id`
  scheme exists to protect an unattended, multi-step background process
  (extension → R2 → Worker → backend poll) against double-insertion on
  crash-recovery retry. A manual upload is a single synchronous, foreground,
  user-initiated request with no equivalent multi-step window to protect — so
  every submission is simply treated as a new intentional capture, no
  deduplication against prior identical uploads. `captures.source_capture_id`
  stays `NULL` for these rows (already nullable, so no schema change was needed
  to allow this).
- **Everything downstream of ingestion is unchanged**: content and reader-text
  hashing (§3b), URL normalization (§9), grouping into `pages` by
  `normalized_url` — a manual upload of an already-captured URL is just another
  new version under the same page, identical in kind to any other re-archive
  above. The async screenshot job (§6) and AI enrichment (§7) both apply
  unmodified, since §6 already explicitly operates on "already-captured, fully
  inlined SingleFile HTML" — which is exactly the shape of a manually uploaded
  file.
- **One real, concrete conflict with existing infrastructure, worth flagging
  rather than discovering later**: SingleFile archives with inlined images/fonts
  routinely run tens of megabytes, while the global
  `middleware.RequestSize(1 << 20)` (§13a) caps every request body at 1MB. This
  upload endpoint needs its own, much larger `RequestSize` scoped to just that
  route — the same "scope it, don't rely on the global default" pattern already
  used for `AllowContentType` on `/api` (§13a).
- **Schema addition**: `captures.source TEXT` (`'extension'` |
  `'manual_upload'`), mirroring the existing `page_tags.source` (`'manual'` |
  `'ai'`) pattern — lets the dashboard show capture origin directly rather than
  inferring it from whether `source_capture_id` happens to be `NULL`. See §10.

---

## 4. Storage Strategy

- **R2 is temporary only.** It exists purely to get large payloads from the
  extension (which may not have a stable public endpoint to push to) to the
  backend (which may not be reachable to receive a push). Once the backend has
  pulled and locally stored a capture's blobs, they are deleted from R2.
- **Local disk is canonical.** The backend stores the zstd-compressed HTML (HTML
  compresses extremely well with zstd, commonly 80-90% size reduction) on local
  disk, referenced by path from the `captures` table. Thumbnails (see §6) are
  also stored on local disk, never in R2.
- **Backup is entirely the operator's responsibility** — see §14. The
  application itself performs no automated backup.
- **Database choice: Postgres, not SQLite**, despite this being a personal
  archive. Real user accounts (family members using one deployment, and a
  potential future multi-tenant hosted version) tip this in Postgres's favor:
  SQLite's single-writer lock becomes a real constraint with concurrent family
  members archiving/querying at once, and multi-tenant isolation / hosted-DB
  migration paths are native to Postgres. Docker Compose makes the extra
  container a non-issue operationally.
- **Bind mounts, not named Docker volumes**, for both the Postgres data
  directory and the local archive directory (see §14) — this makes it
  straightforward for whatever external backup tool the operator chooses to
  snapshot the directories directly from the host filesystem.

---

## 5. Authentication

### Requirements driving the design

- The backend must never need to be publicly reachable for the core archiving
  flow to work.
- Multiple devices (desktop extension, phone shortcut, CLI, PWA) need
  independent, individually revocable credentials.
- The **dashboard** is a separate case: it's only ever accessed over whatever
  network the operator has chosen to expose it on (LAN/VPN/tunnel), so it
  doesn't need to satisfy the "backend stays fully private" constraint the
  device-capture path does.
- Real user accounts are wanted (to support family members on one shared
  deployment, and to keep the door open for a future hosted/paid version).

### Two separate authentication mechanisms

**Devices (extension, PWA, CLI) → opaque bearer tokens, backed by a per-user
pairing token, D1-owned.**

An earlier revision of this design had the backend mirror the account's **bcrypt
password hash** into D1, verified by the Worker at pairing time against a
submitted username/password. That approach turned out to be infeasible on its
own terms, independent of the security question: bcrypt is designed to cost on
the order of 100-300ms even in native code, and Cloudflare Workers on the free
tier are capped at 10ms of CPU time per request. There is no cost factor that
gets bcrypt (or an equivalent memory-hard hash) under that ceiling without
weakening it past the point of doing its job — and there's no native bcrypt in
the `workerd` runtime anyway, so a pure-JS implementation would only be slower
still. A Worker-native fast primitive (PBKDF2 via `crypto.subtle.deriveBits`,
which _would_ fit the CPU budget) was considered as a fix and rejected: it still
means mirroring password-derived material into D1, just under a different
algorithm, and doesn't address the underlying exposure.

Instead, each account gets a separate, single-purpose credential — a **pairing
token** — used only to authenticate a device once in exchange for a bearer
token. It is never used to log into the dashboard, and the dashboard password is
never used to pair a device:

- Generated automatically at account creation: 32-byte CSPRNG value,
  base64url-encoded, `rcl_pair_...` prefix. One per user, valid indefinitely
  until regenerated or revoked (not single-use, not scoped per-device).
- **Postgres stores it reversibly** — `users.pairing_token_enc`, AES-256-GCM
  (`crypto/aes`/`crypto/cipher`, stdlib) — a deliberate departure from how every
  other credential in this system is stored. This is justified specifically
  because a pairing token isn't a user-chosen secret carrying the same stakes as
  a password; it's closer in kind to an API key, and the dashboard needs to
  redisplay it on demand (see below) rather than forcing a regenerate-to-view
  flow the way a show-once bearer/session token does. The AES key
  (`PAIRING_TOKEN_KEY`, 32 random bytes, base64-encoded) is operator-generated
  and lives in the backend's `.env` alongside the Worker service secret (§5a)
  and D1 migration credential (§5b) — it isn't Cloudflare/Terraform-managed,
  since it never needs to leave the backend's own trust boundary.
- **D1 stores only `SHA-256(pairing_token)`** — the same shape and reasoning as
  the existing device-token/session-token hashing: the token already carries
  ~256 bits of entropy, so a leaked hash alone doesn't yield a usable
  credential. Unlike the password-hash mirror it replaces, a full D1 compromise
  now exposes nothing password-derived at all — only a credential whose sole
  purpose is pairing new devices, independently revocable from the account's
  actual login credential.
- **Device pairing is single-credential.** A device submits only the pairing
  token to the Worker — no username. The Worker hashes it, looks up the owning
  `user_id` directly (a pairing token hashes to exactly one account), and issues
  an opaque bearer token exactly as originally designed: 32-byte CSPRNG,
  `rcl_live_...` prefix, hashed at rest (`SHA-256`) in D1's `tokens` table,
  revoked by row deletion. Nothing about bearer-token issuance, storage, or
  revocation changes from the original design — only what's submitted to obtain
  one. A JWT was considered here too (for the same reasons as the original
  design) and rejected for the same reason: a DB lookup already happens on every
  request for revocation, so a JWT's main benefit doesn't apply, and it adds
  signing/claims-schema surface for no payoff at this scale. Implemented as
  `POST /pair` (request: `pairing_token`, `device_name`, `device_type`;
  response: the raw bearer token, shown exactly once). `tokens.last_used_at` is
  touched on every subsequent authenticated device request (`POST /queue`,
  `GET /queue`, `POST /queue/:id/claim`) via a fire-and-forget write
  (`ExecutionContext.waitUntil`), so it never adds latency to the request it's
  authenticating.
- **Pairing-token management** — new session-gated backend endpoints
  (dashboard-facing, not Worker-facing):
  - `GET /api/pairing-token` — decrypts and returns the current token, so it's
    always viewable on the dashboard. (Show-once-then-hash-only, the pattern
    used for bearer/session tokens, was considered and rejected specifically for
    this credential: losing it would otherwise force a regenerate, which is a
    worse default for something a person may not immediately save to a password
    manager, unlike a login password or session token.)
  - `POST /api/pairing-token/regenerate` — issues a new token, overwrites both
    the Postgres (encrypted) and D1 (hashed) copies.
  - `DELETE /api/pairing-token` — revokes without reissuing, blocking further
    device pairing until a regenerate.
  - All three are built alongside Phase 2's device-auth work even though the
    dashboard UI to call them doesn't exist until much later — this avoids a
    second pass through `internal/auth` solely for the dashboard's sake once
    it's built.
- **D1's `users` table (§10) is no longer a credential mirror in the login
  sense.** It exists purely to hold `pairing_token_hash` and give
  `queue_items`/`tokens`/etc. a `user_id` foreign key target — nothing else
  about an account needs to live there.

```sql
-- D1
CREATE TABLE tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash TEXT NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id),
  device_name TEXT NOT NULL,
  device_type TEXT NOT NULL,       -- 'extension' | 'pwa' | 'cli'
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TEXT
) STRICT;

CREATE INDEX idx_tokens_user_id ON tokens(user_id);
```

**Dashboard → direct session auth against Postgres, DB-backed sessions.** The
dashboard is a normal web app: it authenticates by checking
`username`/`password_hash` directly in Postgres, with no involvement of D1 or
the Worker at all. Sessions are **DB-backed** (a `sessions` table in Postgres),
using the same hashed-opaque-token shape as device tokens above — a 32-byte
CSPRNG value with a recognizable prefix (`rcl_sess_...`), stored as its SHA-256
hash, with the raw value held only in an `HttpOnly`, `SameSite=Lax` cookie. This
was a deliberate choice over a stateless signed cookie: it keeps sessions
revocable the same way device tokens are (delete the row), at the cost of a DB
lookup per authenticated request — an acceptable cost at this project's request
volume, consistent with the reasoning already applied to device-token revocation
and D1 polling elsewhere in this document.

```sql
-- Postgres
CREATE TABLE sessions (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  session_hash TEXT NOT NULL,
  user_id BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  CONSTRAINT sessions_pkey PRIMARY KEY (id),
  CONSTRAINT sessions_session_hash_key UNIQUE (session_hash),
  CONSTRAINT sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);
```

Sessions have a 30-day absolute TTL (`expires_at`) and no idle-timeout expiry —
`last_seen_at` is updated on every authenticated request but isn't currently
read by any feature; it's kept for a plausible future "your active sessions"
dashboard view rather than actively driving expiry logic today. Logout deletes
the row. This is simpler than reusing the device-token mechanism and avoids
needing a `tokens` table in Postgres — the earlier design's ambiguity about
"does the backend keep its own copy of tokens" is resolved by not needing one;
`sessions` and D1's `tokens` are two distinct, independently-revocable
credential systems for two distinct kinds of client.

### Manage Devices dashboard screen

Because D1 is the sole owner of device tokens, this isn't purely a UI-only
addition — the data (`tokens.device_name`, `device_type`, `created_at`,
`last_used_at`) already exists in D1, but the dashboard/backend has no existing
path to read or mutate it. Three pieces are needed:

1. **Two new Worker endpoints**, gated by the backend↔Worker service secret
   (§5a): `GET /internal/tokens?user_id=` (list a user's device tokens) and
   `DELETE /internal/tokens/:id?user_id=` (revoke one). Both simple,
   single-operation endpoints, consistent with the "dumb Worker" principle.
   **Built as part of Phase 2.** The revoke endpoint's `user_id` query parameter
   is not just for listing — it's also required on the delete call and checked
   against the token's actual owning user before the row is removed; a mismatch
   deletes nothing rather than someone else's device. This is a deliberate
   belt-and-suspenders addition beyond the original design: the Worker still
   doesn't know about roles (see point 3 below, unchanged), but this catches a
   backend-side bug that passes the wrong `user_id`/token `id` pair, at no real
   cost.
2. **A backend API passthrough**: the dashboard never talks to the Worker
   directly (it has no bearer token or service secret of its own); it calls the
   backend, which makes the outbound authenticated call to the Worker and
   returns the result. This keeps the backend the single place that holds the
   service secret. **Not yet built** — depends on the dashboard existing.
3. **Authorization scope, decided as:** a member can list/revoke only their own
   devices; an admin can list/revoke _any_ user's devices (useful for responding
   to a compromised account without waiting on that user). The Worker endpoints
   themselves don't need to know about roles — the backend enforces this scoping
   before making the outbound call, based on the dashboard session's role.

One behavior worth documenting rather than treating as a bug: revocation is
**not** a live push to the device. A revoked extension/PWA/CLI will keep working
until its next request to the Worker, at which point the token lookup fails and
it gets a 401 — at that point it needs to be re-paired. There's no mechanism
(and none is planned) to immediately invalidate an in-flight session on the
device side.

### 5a. Backend ↔ Worker service authentication

The backend itself is a distinct, higher-privilege actor from any single user's
device — it polls for pending captures and pushes mirror rows across _all_ users
in a deployment, and (per above) needs to issue revoke calls. This needs its own
credential, separate from the per-device token system.

**Decision: a static shared secret.**

- Generated via Terraform's `random_password` resource at `terraform apply`
  time, output with `sensitive = true` so it doesn't leak into plaintext
  state/CI output.
- Injected into the Worker as an environment binding/secret.
- The operator copies it from `terraform output` into the backend's `.env` after
  apply.
- Checked by the Worker as a header (e.g. `X-Service-Key`) on the small set of
  backend-only endpoints (poll `pending_captures`, push credential/ bookmark
  mirror rows, revoke a device token).
- Rotation = regenerate + redeploy, which is acceptable at this operational
  scale (single backend per deployment, infrequent rotation).

Alternatives considered and rejected:

- Reusing the `tokens` table with a "service" row — doesn't fit, since
  `tokens.user_id` is scoped to one user and the backend needs cross-user
  access.
- mTLS or Cloudflare Access service tokens — real options, but add meaningfully
  more operational complexity (cert management, or an additional Cloudflare
  product dependency) for no real benefit at this scale.

### 5b. Backend ↔ Cloudflare D1 migrations

The backend applies D1 schema migrations itself, at startup, rather than
requiring the operator to install and run `wrangler d1 migrations apply` —
consistent with the same "no external tool needed to run the binary" goal that
keeps Postgres migrations tool-managed only by current-implementation choice,
not architectural necessity (see §15 for that side's open status). This means
the backend needs to reach Cloudflare's D1 query API directly — the one place in
the system where the backend talks to Cloudflare directly rather than
exclusively through the Worker. This doesn't weaken the "backend stays fully
non-public" property elsewhere in this document (§2, §11): that property is
about _inbound_ reachability, which is unaffected; this is a new _outbound_ path
only, initiated by the backend, never received by it.

- **Migrations live at `terraform/worker/migrations/*.sql`** — the same files
  that define the D1 schema conceptually, embedded into the Go binary via
  `go:embed` at build time (not fetched at runtime). Applied migrations are
  tracked in a `schema_migrations` table (§10) that the backend creates and owns
  itself.
- Deliberately **not** wrangler's `d1_migrations` table/convention — wrangler is
  not part of this project's toolchain anywhere (see §15), and reusing that name
  would risk two independent, uncoordinated bookkeeping systems touching the
  same table if an operator ever pointed `wrangler` at the database directly out
  of habit.
- **Credential: a Cloudflare API token scoped to `D1:Edit` on this one
  database** — provisioned via Terraform's `cloudflare_api_token` resource,
  output as `sensitive`, copied into the backend's `.env` alongside the Worker
  service secret from §5a. This is a materially different kind of credential
  from the service secret: an actual Cloudflare account-level token, not an
  application-level shared secret, and narrower in scope than a full-account
  token.
- Runs once at startup, alongside the bootstrap-admin check below — a no-op once
  nothing's pending. Safe to call on every restart.

This was a deliberate tradeoff, not an oversight: the alternative (a Worker
endpoint that runs migrations, gated by the existing service secret, keeping the
Worker as the sole thing that ever touches D1) was considered and rejected,
because it would mean a schema change requires a Worker redeploy (a
`terraform apply`) even when nothing about the Worker's own code changed — worse
operator friction than holding one additional narrowly-scoped credential.

### Account creation and roles

- **Open registration.** Anyone who can reach the dashboard can create a
  `member` account via a signup form — no invite step. This is deliberately
  consistent with the dashboard's threat model: reachability is already gated by
  whatever network the operator chose (LAN/VPN/tunnel), so anyone who can reach
  the signup form is presumed already trusted at the network level, the same way
  anyone on a home LAN can usually reach a router's admin page. This also lines
  up naturally with a future hosted/SaaS mode, where open signup is the default
  expectation anyway. A config flag (e.g. `ENABLE_OPEN_REGISTRATION=false`) is
  worth offering later for operators who want invite-only instead, but isn't
  required for the initial version.
- **Bootstrap token for the first admin, held in memory, not persisted.** On
  startup, if `users` is empty, the backend generates a random bootstrap token
  (32-byte CSPRNG, base64url-encoded, `rcl_bootstrap_...` prefix), prints it to
  the backend's logs, and holds it — token value, a 1-hour expiry, and a
  consumed flag — entirely in a process-local value, never written to Postgres.
  A restart before the token is used simply generates a new one; the old one is
  gone. This is a correction from an earlier revision of this design, which
  specified a persisted `bootstrap_token` table: that approach had a real bug —
  a restart before use would silently leave the _previous_ token valid until its
  own expiry, alongside a newly generated one. The in-memory approach can't have
  that failure mode, since there's nothing left to be stale after a restart.

  The dashboard's "create first admin" screen requires this token as well as a
  username/password. The token is only marked consumed after the admin account
  is actually created **successfully** — not merely validated — so a request
  that fails after validation (a username race, a transient DB error) can be
  retried with the same token rather than requiring a full restart to get a
  fresh one. This closes the narrow race where the dashboard is briefly
  reachable on the network before the operator has locked it down — without the
  token, reaching the setup screen isn't enough to claim admin.

  This design assumes exactly one backend process. That assumption was already
  implicit elsewhere (§5a's service-secret rotation reasoning assumes "single
  backend per deployment"), but an in-memory, unshared bootstrap token makes it
  a hard constraint for this one flow specifically: a second replica would hold
  its own independent token, invisible to the first, until whichever one
  processes the setup request wins.

- **Roles:** `admin` and `member`. Add to the `users` schema:
  ```sql
  ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'member';
  -- values: 'admin' | 'member'
  ```
  Admins can create/manage other users; members manage only their own
  bookmarks/tags/collections. Role is purely a backend/dashboard authorization
  concern and is **not** included in the D1 mirror — D1 only needs enough to
  authenticate a device and identify its owning `user_id`, never authorization
  decisions.

### Security note: D1 as a mirror target

D1 isn't directly internet-addressable on its own, but "not publicly accessible"
doesn't mean zero risk:

- The Worker itself is public, so a bug in its auth-check logic is a path to the
  D1-mirrored credentials. The Worker is kept intentionally minimal to limit
  this surface, but it isn't literally zero.
- Cloudflare, as the D1 host, has access to the data at rest — using any managed
  cloud service extends the trust boundary to that provider, which is a standard
  tradeoff of this architecture and not unique to Recueil.
- The practical residual risk is low, and lower than the design's original
  revision: every credential D1 now holds — bearer-token hashes, the
  pairing-token hash — is `SHA-256` of a CSPRNG-generated, ~256-bit-entropy
  value, not anything human-chosen. There is no longer a password-derived value
  of any kind in D1's mirror, so the earlier "password hashes need a proper
  slow/salted hash, not SHA-256" caveat no longer applies to anything D1 stores.
  The corresponding new risk lives entirely on the Postgres side instead:
  `users.pairing_token_enc` is reversible by design (see §5), so a compromise of
  both a Postgres backup and the `PAIRING_TOKEN_KEY` would expose usable pairing
  tokens — notably, still not the account password itself, and a pairing token
  alone only grants the ability to pair a new device, not dashboard access.
- The backend's D1 migration credential (§5b) is a second, narrower extension of
  this trust boundary — scoped to `D1:Edit` on one database, used only at
  startup, and distinct from the Worker service secret. It doesn't change the
  "Worker stays public, backend stays private" shape (it's outbound-only from
  the backend, same as everything else in §2), but it is a second real
  Cloudflare credential the backend now holds, worth naming plainly alongside
  the rest of this section's tradeoffs.

This tradeoff is accepted as part of the design and should be stated plainly in
the repo's security documentation rather than left implicit.

---

## 6. Screenshot / Thumbnail Generation

**Moved from the extension to the backend.** The extension no longer captures a
screenshot at all — it uploads only HTML and reader text (see §3). Thumbnail
generation now happens as an async backend job, after a capture's HTML has
already been pulled from R2 and stored locally.

### Why this is safe, unlike a general "fetch and archive" fallback

The core design principle in §1 forbids the backend from ever fetching a _live_
URL — that's the CAPTCHA/paywall/auth problem the whole system exists to avoid.
Rendering the **already-captured, fully inlined SingleFile HTML** is a different
operation: no network requests, no live authentication state, no CAPTCHA, and
(since SingleFile strips scripts) no live JS execution. It's an offline,
sandboxed render of a static document that's already been through the "real
browser tab" capture path.

### Design

- **`chromedp`** (Go, drives Chrome/Chromium over the DevTools Protocol) — fits
  the existing Go backend without adding a Node dependency.
- Runs as a **separate sidecar container** in Docker Compose, using the
  `chromedp/headless-shell` image — a small, purpose-built headless Chrome build
  maintained specifically for this use case. Kept as its own service (not
  bundled into the backend image) so Chromium's dependency footprint and
  per-instance memory cost don't bloat the core backend image, and so it can be
  updated/pinned independently.
- The backend keeps a **long-running connection** to the headless-shell instance
  and opens a new tab per screenshot job, rather than cold-starting a browser
  process per capture (which adds ~1-3s of avoidable latency each time).
- **Bounded concurrency** — a small worker pool (e.g. 2-3 concurrent tabs),
  appropriate for modest self-hosted hardware.
- The HTML is served to the headless browser via `file://` or a brief ephemeral
  local HTTP server; since SingleFile inlines all resources as data URIs, there
  are no external resource loads to worry about either way.
- **Fully async and non-blocking**, matching the `ai_jobs` pattern (see §7): a
  capture is fully valid and browsable with no thumbnail, and a failed/slow
  screenshot never invalidates the capture. `captures.thumbnail_path` remains
  nullable. Bounded retry with backoff, same shape as §7.

### Consequence for the schema

Because the screenshot is no longer produced client-side and never touches R2:

- `r2_key_thumbnail` is **removed** from the D1 `pending_captures` table.
- The extension only needs to request presigned URLs for two objects (HTML,
  reader text), not three.

### Tradeoff, stated explicitly

This adds a real piece of self-hosting weight — an extra container, its memory
overhead, and one more moving part — compared to the originally proposed
`chrome.tabs.captureVisibleTab` approach in the extension. In exchange it
removes the extension's dependency on the user's current scroll/viewport state
entirely and produces consistent, full-page-quality thumbnails server-side.
Given Compose already orchestrates Postgres, this was judged worth the added
weight.

A cross-browser (Firefox) equivalent was considered and rejected: Firefox
doesn't speak the Chrome DevTools Protocol, so there's no chromedp-equivalent
for it; the closest option (Playwright for Go, which supports Firefox) is a
heavier, unofficially-maintained dependency with its own binary-download step.
Since the content being rendered is a static, already-inlined document with no
live JS, rendering-engine differences that would normally motivate multi-browser
coverage don't apply here — Chrome/Chromium via chromedp is sufficient.

---

## 7. AI Enrichment (Optional)

- Entirely optional and asynchronous — never blocks capture or ingestion. A
  capture is fully valid, searchable, and browsable with zero AI fields
  populated.
- Runs against the Readability-extracted plain text, not the raw HTML — cheaper
  and produces better summaries than trying to parse rendered HTML.
- Supports **two backend types**, chosen by the user in configuration:
  Ollama-compatible (local) or OpenAI-compatible (hosted). A single small
  interface (`Summarize(text) (summary, tags, error)`) covers both.
- Tracked in its own `ai_jobs` table, one row per capture, decoupled from the
  `captures` table so enrichment status/failure never affects the capture's core
  validity.
- AI-generated tags are written to the same `page_tags` table as manual tags,
  distinguished by a `source` column (see §9). The dashboard visually
  distinguishes them from manual tags (e.g. a small badge/icon or muted styling)
  rather than rendering them identically — the `source` column exists
  specifically to support this.

### Retry and failure handling

```sql
ALTER TABLE ai_jobs ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ai_jobs ADD COLUMN next_attempt_at TIMESTAMPTZ;
```

On failure: increment `attempts`; if under a small max (e.g. 3), set `status`
back to `pending` with `next_attempt_at` pushed out (simple exponential
backoff); once attempts are exhausted, mark `status = 'failed'` permanently with
`error` preserved. The dashboard surfaces failed jobs as a small badge on the
capture with a manual retry action — no dead-letter queue is needed given this
is optional and low-stakes; the failed row itself serves that purpose.

The same `attempts`/`next_attempt_at`/bounded-retry shape is reused for the
screenshot job in §6.

---

## 8. Cross-Device Queue and Bookmark List

### Queue (phone → desktop archiving)

- Adding a URL from a phone (via Shortcut, PWA, or CLI) only **enqueues** it —
  it does not attempt to archive anything server-side. The intended workflow is
  deliberately: queue remotely, archive later from the desktop extension, where
  a real rendered/authenticated browser session exists.
- The desktop extension polls the queue via the Worker/D1 (see §7 polling
  cadence in the original numbering — now consolidated below) and can notify the
  user that items are waiting.
- Claiming is done with a conditional update (`WHERE status = 'pending'`) to
  prevent two devices from grabbing the same item simultaneously; a claimed item
  records which device claimed it and when.
- Implemented as three bearer-token-authenticated Worker endpoints, all
  operating purely between a device and D1 — the backend never touches
  `queue_items` at all:
  - `POST /queue` — enqueue. `id` is client-generated (idempotent retry via
    `INSERT ... ON CONFLICT(id) DO NOTHING` — see §3c's identical reasoning for
    `pending_captures`).
  - `GET /queue` — lists this user's pending items, plus any claimed item whose
    claim has gone stale (see visibility timeout below). Listing never claims.
  - `POST /queue/:id/claim` — the actual atomic claim, via a conditional
    `UPDATE ... WHERE ... RETURNING`. This, not the listing endpoint, is where
    the two-devices-race-for-the-same-item risk actually lives, and where the
    phase-2 brief's instruction to "build the idempotency and visibility-timeout
    logic in at this point, not later" was aimed.

**Claim failure is not a single status code.** A failed claim distinguishes
three cases, decided during Phase 2 rather than left as a uniform `409`:

- `404` — the item doesn't exist, or belongs to a different user. These two
  cases are deliberately collapsed together rather than distinguished, so a
  claim attempt never leaks cross-user existence.
- `410` — the item is in a terminal state (`captured` or `failed`): it used to
  be claimable and permanently isn't anymore. This is more precise than a bare
  404 (which conventionally means "wrong ID") for "this happened, but it's
  over."
- `409` — the item is actively claimed by another device and the claim hasn't
  gone stale yet: a genuine, temporary conflict worth retrying later.

Distinguishing these costs one extra `SELECT`, but only on the failure path — a
successful claim is still a single `UPDATE ... RETURNING` with no additional
round trip.

### Queue visibility timeout

A claimed item can get stuck if the claiming device dies mid-capture or the tab
is closed. Rather than a separate scheduled sweep job, this is handled as **lazy
reclaim folded into the existing claim query**:

```sql
WHERE status = 'pending'
   OR (status = 'claimed' AND claimed_at < now() - interval '15 minutes')
```

15 minutes is comfortably more than enough time to pull 2-3 blobs from R2 and
write a DB record, and this avoids needing a Cron Trigger or any additional
scheduled infrastructure, consistent with the "dumb Worker" philosophy.

### Queue item cleanup

Nothing above ever removes a terminal-state `queue_items` row on its own — a
`captured` or `failed` item exists purely to support the 410 semantics above,
and would otherwise accumulate forever. Surfaced during Phase 2 implementation,
not anticipated in the original design:

- **`POST /internal/queue-items/cleanup`**, service-secret gated, called on the
  backend's own schedule (once or twice a day is plenty) — deliberately **not**
  a Cloudflare Cron Trigger, for exactly the same "keep the Worker dumb, let the
  backend own scheduling" reasoning already applied to the visibility-timeout
  reclaim above. Not scoped to a single user — this is a maintenance sweep
  across the whole deployment, not a per-device operation, so it takes no
  `user_id` parameter the way the device-facing endpoints do.
- **Deletes only `captured` items**, and only once older than a 72-hour
  retention window (long enough to be useful for auditability/debugging shortly
  after the fact, short enough not to accumulate indefinitely). `failed` items
  are deliberately **not** touched — they're kept indefinitely for now. What to
  do about them long-term (surface them to the user, retry them, expire them on
  a separate/longer schedule) is an open question, not decided here — see §15.
- **The retention clock is `claimed_at`, not `created_at`.** An item can sit
  `pending` for a long time before being claimed; it's time since actual
  completion that should drive retention, not time since the original enqueue.
  There is no dedicated "when did this finish" timestamp on `queue_items` today
  — `claimed_at` is used as a pragmatic proxy, reasonable at this project's
  scale since the gap between a successful claim and the capture actually
  completing is seconds to minutes, not enough to matter for a 72-hour window.
  If a future phase's `complete`/`fail` endpoint adds a dedicated completion
  timestamp, this is a one-line filter change, not a design change.

### Bookmark-list mirror (extension as a browsable list)

- Separately from the queue, the extension can act as a lightweight bookmark
  list of everything already archived — similar to a browser's native bookmarks
  UI: just title + URL, no thumbnails.
- This is a **one-way, backend → D1 push**: after the backend finishes
  processing a capture, it upserts a row into a D1 `archived_pages` table — the
  mirror-image of the credential mirror (backend → D1, rather than D1 →
  backend), keeping the same principle: the extension only ever needs to talk to
  the Worker/D1, never the backend.
- The extension does **not** live-sync this list. It caches the list locally and
  refreshes on a coarse schedule (see §7 polling cadence below) or on explicit
  user request, using an incremental "give me changes since X" query against
  `archived_pages.updated_at`.
- Because this list is just title + URL, no thumbnail storage is needed in R2 or
  D1 for this feature.

```sql
-- D1
CREATE TABLE archived_pages (
  page_id INTEGER PRIMARY KEY,      -- matches Postgres pages.id
  user_id INTEGER NOT NULL REFERENCES users(id),
  raw_url TEXT NOT NULL,
  title TEXT,
  latest_capture_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_archived_pages_user ON archived_pages(user_id);
```

### Polling cadence

Settled as **infrequent background polling with on-demand override**, rather
than tight polling or any push mechanism:

- Extension → queue (D1 via Worker): every 5-15 minutes in the background, plus
  a manual "check now" button in the extension popup for on-demand polling.
- Extension → bookmark-list mirror refresh: coarse (e.g. once per day) or on
  explicit user request.
- Backend → `pending_captures` (D1 via Worker): every few minutes. No on-demand
  path is needed here since nothing is synchronously waiting on it.

No WebSocket/push infrastructure (e.g. a Durable Object) is used — that would be
real added infrastructure for a problem infrequent polling plus a manual refresh
button already solves adequately at this scale. (The "once a minute" figure used
in the original §13 cost analysis was illustrative headroom math, not a spec.)

---

## 9. URL Normalization

Two URL fields are stored for every capture, never conflated:

- **`raw_url`** — exactly what was captured, byte-for-byte, never rewritten.
- **`normalized_url`** — a computed, canonical form used purely as the
  dedup/grouping key that determines which `pages` row a capture belongs to.

Normalization strategy:

- Adopt the **ClearURLs** community-maintained ruleset (regex-based rules per
  site/provider, actively maintained, MIT licensed) to strip known tracking
  parameters (`utm_*`, `fbclid`, `gclid`, `igshid`, etc.) without touching
  functionally meaningful query parameters. Do not hand-roll a tracking
  parameter list.
- Additional canonicalization beyond tracking-param stripping:
  - Lowercase the host.
  - Strip default ports (`:443`, `:80`).
  - Drop the URL fragment, unless the site is a known SPA that encodes
    meaningful route state in the fragment.
  - Sort remaining query parameters alphabetically for a stable key.
  - Strip trailing slash.

---

## 10. Data Model

### Postgres (backend-owned — canonical archive)

`BIGINT GENERATED ALWAYS AS IDENTITY` primary keys are used throughout (rather
than UUIDs) for smaller indexes and better insert/join performance at this
project's scale.

All constraints (primary keys, unique constraints, checks, and foreign keys) are
explicitly named (`<table>_pkey`, `<table>_<column>_key`,
`<table>_<column>_check`, `<table>_<column>_fkey`) rather than left to
Postgres's auto-generated names — this makes later
`ALTER TABLE ... DROP CONSTRAINT` migrations (e.g. changing the set of allowed
`role` values) referenceable by a name stated in the migration file, rather than
needing to look up whatever Postgres happened to call them. Applied below to
`users` and `sessions`, the two tables actually implemented so far; the rest of
this section's tables will pick up the same convention as they're implemented.

```sql
CREATE TABLE users (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  username TEXT NOT NULL,
  password_hash TEXT NOT NULL,       -- bcrypt; verified backend-side only,
                                      -- never mirrored anywhere (see §5)
  pairing_token_enc TEXT NOT NULL,   -- AES-256-GCM, reversible; source for
                                      -- the D1 pairing_token_hash mirror and
                                      -- for dashboard redisplay (§5)
  role TEXT NOT NULL DEFAULT 'member',   -- 'admin' | 'member'
  display_name TEXT,                 -- nullable; UI falls back to username
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT users_pkey PRIMARY KEY (id),
  CONSTRAINT users_username_key UNIQUE (username),
  CONSTRAINT users_role_check CHECK (role IN ('admin', 'member'))
);

-- Dashboard sessions (§5) — DB-backed, hashed opaque tokens, same shape as
-- D1 device tokens. Revocation is a row delete (logout); no idle timeout,
-- only the absolute expires_at.
CREATE TABLE sessions (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  session_hash TEXT NOT NULL,
  user_id BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  CONSTRAINT sessions_pkey PRIMARY KEY (id),
  CONSTRAINT sessions_session_hash_key UNIQUE (session_hash),
  CONSTRAINT sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);

-- One row per distinct URL ever archived, grouped by normalized_url
CREATE TABLE pages (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id),
  normalized_url TEXT NOT NULL,
  title TEXT,                        -- denormalized from latest capture
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, normalized_url)
);

-- One row per capture event: the version history
CREATE TABLE captures (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  page_id BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  source_capture_id TEXT UNIQUE,     -- pending_captures.id (D1), for
                                      -- ingestion idempotency (see §3c);
                                      -- NULL for manual uploads (§3d), which
                                      -- have no equivalent crash-recovery
                                      -- window to protect
  source TEXT NOT NULL DEFAULT 'extension',  -- 'extension' | 'manual_upload'
                                      -- (§3d) — mirrors page_tags.source
  raw_url TEXT NOT NULL,
  title TEXT,
  html_path TEXT NOT NULL,           -- local disk path, zstd-compressed
  html_size_bytes INTEGER NOT NULL,
  thumbnail_path TEXT,               -- populated async by the screenshot
                                      -- service (§6); null until then
  reader_text TEXT,                  -- Readability plain-text extraction
  content_hash TEXT NOT NULL,        -- full-HTML hash (exact dedup)
  reader_text_hash TEXT NOT NULL,    -- powers "unchanged since last capture"
  captured_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_captures_page_id ON captures(page_id);

ALTER TABLE captures ADD COLUMN reader_text_tsv tsvector
  GENERATED ALWAYS AS (to_tsvector('english', coalesce(reader_text, ''))) STORED;
CREATE INDEX idx_captures_fts ON captures USING GIN (reader_text_tsv);

CREATE TABLE tags (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id),
  name TEXT NOT NULL,
  UNIQUE (user_id, name)
);

-- Tags live on pages, not captures: tags describe the subject matter of
-- the URL, which doesn't change per-version. Both manual and AI-applied
-- tags coexist here, distinguished by `source`.
CREATE TABLE page_tags (
  page_id BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  tag_id BIGINT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  source TEXT NOT NULL DEFAULT 'manual',  -- 'manual' | 'ai'
  PRIMARY KEY (page_id, tag_id)
);

-- Nested collections. Adjacency list (parent_id self-reference) rather
-- than a closure table: simpler writes, and at this project's scale a
-- recursive CTE for "this collection and all descendants" is fast enough
-- that a closure table's extra write-complexity isn't justified.
CREATE TABLE collections (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id),
  parent_id BIGINT REFERENCES collections(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, parent_id, name)
);
CREATE INDEX idx_collections_parent ON collections(parent_id);

-- A page may be in zero, one, or many collections. Deleting a collection
-- cascades to delete child collections (the subtree), but only removes
-- *membership* rows here. There is no dedicated "Unsorted" collection row;
-- absence of membership rows IS the Unsorted state.
CREATE TABLE page_collections (
  page_id BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  collection_id BIGINT NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (page_id, collection_id)
);

-- Decoupled from captures so a capture remains fully valid/browsable
-- with zero AI enrichment ever having run.
CREATE TABLE ai_jobs (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  capture_id BIGINT NOT NULL REFERENCES captures(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | done | failed
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ,
  summary TEXT,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);
```

There is **no `tokens` table in Postgres** — device tokens are owned entirely by
D1 (see §5), and the dashboard uses its own DB-backed `sessions` table above, so
no bearer-token table is needed on the backend side at all.

### D1 (Worker-owned — auth, queue, bookmark mirror only)

D1 tables use `STRICT` (enforcing declared column types, since SQLite is
dynamically typed by default) and, where a table's primary key is non-integer
and only ever looked up by that key, `WITHOUT ROWID` (avoiding an unnecessary
hidden-rowid indirection) — applied below to the tables actually implemented so
far; the rest of this section's tables will pick up the same convention as
they're implemented.

`queue_items` and `pending_captures` use client-generated UUIDs rather than
server-generated identity columns, for idempotency on retry (see §3c) and
because the extension generates the ID before the row exists server-side.

```sql
-- Bookkeeping for the backend's own D1 migration runner (§5b) — not
-- wrangler's `d1_migrations` table; wrangler is not used anywhere in this
-- project's toolchain (see §15).
CREATE TABLE schema_migrations (
  id TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT, WITHOUT ROWID;

-- Mirrors Postgres users.id for device pairing without ever exposing the
-- backend. Holds only pairing_token_hash — no password-derived value of
-- any kind (see §5's redesign away from a password-hash mirror). Does NOT
-- include `role` — authorization is a backend/dashboard concern only. id
-- is never D1-generated: it's always supplied explicitly from the
-- Postgres-side value on every mirror-push INSERT, so plain
-- `INTEGER PRIMARY KEY` (rowid alias, not AUTOINCREMENT) is correct here —
-- D1 only assigns its own value if a row is inserted with id omitted or
-- NULL, which never happens on this path. `username` is dropped entirely:
-- pairing is single-credential (submit the pairing token, no username), so
-- the Worker never needs to look a user up by name.
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  pairing_token_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT;

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

-- URLs waiting to be archived by the desktop extension. Enqueued and claimed
-- entirely by devices via their own bearer tokens (§8) -- the backend never
-- touches this table directly. WITHOUT ROWID for the same client-generated-
-- UUID-primary-key reason as pending_captures below; the composite index is
-- not a bare user_id index because every poll/claim query filters on both
-- user_id and status together (§8).
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

-- Completed captures awaiting backend pickup from R2.
-- Note: r2_key_thumbnail has been removed — screenshots are generated
-- backend-side from the already-pulled HTML (see §6), never uploaded by
-- the extension.
CREATE TABLE pending_captures (
  id TEXT PRIMARY KEY,              -- client-generated UUID
  user_id INTEGER NOT NULL REFERENCES users(id),
  queue_item_id TEXT REFERENCES queue_items(id),  -- null for direct captures
  url TEXT NOT NULL,
  r2_key_html TEXT NOT NULL,
  r2_key_readable TEXT,
  captured_at TIMESTAMP NOT NULL,
  fetched_by_backend BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Bookmark-list mirror, pushed by the backend after each capture is
-- processed. Pulled by the extension on its own coarse/on-demand schedule.
CREATE TABLE archived_pages (
  page_id INTEGER PRIMARY KEY,      -- matches Postgres pages.id
  user_id INTEGER NOT NULL REFERENCES users(id),
  raw_url TEXT NOT NULL,
  title TEXT,
  latest_capture_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_archived_pages_user ON archived_pages(user_id);
```

`pending_captures.queue_item_id` is nullable specifically to support **direct
captures** — archiving a page the user is already on, which was never queued in
the first place.

`queue_items` rows are not permanent once `captured` — see §8's queue-item
cleanup subsection for the retention/deletion policy (`failed` rows are kept
indefinitely for now; only `captured` rows are ever swept).

Backend↔Worker service calls (polling, mirror pushes, token revocation, queue-
item cleanup) are authenticated via the shared service secret (§5a), not a row
in `tokens`. The backend's D1 migration runner (§5b) uses a separate, narrower
Cloudflare API token, not the service secret, and is the only thing that ever
writes to `schema_migrations`.

---

## 11. Components Summary

| Component                 | Tech                                                                                                    | Reachability required                                              | Responsibility                                                                                                                                                 |
| ------------------------- | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Desktop browser extension | WebExtensions (Chrome/Firefox compatible)                                                               | Worker + R2 only                                                   | Poll queue, capture (HTML via vendored SingleFile, reader text), upload to R2                                                                                  |
| Share-sheet PWA           | Static site, Cloudflare Pages                                                                           | Worker only                                                        | Android share-target: enqueue a URL, nothing else                                                                                                              |
| iOS Shortcut              | Apple Shortcuts                                                                                         | Worker only                                                        | Enqueue a URL from iOS share sheet                                                                                                                             |
| CLI                       | Small script/binary                                                                                     | Worker only                                                        | Enqueue URLs, scriptable                                                                                                                                       |
| Cloudflare Worker         | Plain JS (ES modules), no build step — `@ts-check` + JSDoc for static type-checking, ESLint for linting | Public                                                             | Device auth (checks D1 credential mirror), issues bearer tokens, presigned R2 URLs, D1 read/write, service-secret-gated backend endpoints                      |
| D1                        | Cloudflare D1 (SQLite)                                                                                  | N/A (accessed via Worker only, except backend migrations — §5b)    | Device tokens, queue, bookmark-list mirror, schema-migration bookkeeping                                                                                       |
| R2                        | Cloudflare R2                                                                                           | N/A (accessed via presigned URLs)                                  | Temporary blob storage between capture and backend pickup                                                                                                      |
| Backend                   | Go + Postgres, Docker Compose                                                                           | Outbound-only for archiving; inbound optional (dashboard, LAN/VPN) | Pull from R2, compress, store, version, search, tags, collections, AI enrichment, dashboard session auth, dashboard API, Postgres + D1 schema migrations (§5b) |
| Screenshot service        | chromedp + `chromedp/headless-shell`, Docker                                                            | Backend-internal only (no inbound, no outbound)                    | Renders already-captured inlined HTML offline, produces thumbnails                                                                                             |
| Dashboard                 | Svelte                                                                                                  | Same as backend                                                    | Library browsing, search, reader view, version history, tags, collections, user/session management                                                             |

---

## 12. Deployment

- **Backend**: Docker Compose, bundling the Go backend, Postgres, and the new
  headless-Chrome screenshot sidecar as services. Postgres's data directory and
  the local archive directory both use **bind mounts** (not named volumes) so an
  external backup tool can snapshot them directly from the host (see §14).
- **Cloudflare side**: Terraform/OpenTofu module in the public repo,
  provisioning D1, R2, the Worker (and its routes/bindings), the Cloudflare
  Pages project for the share-sheet PWA, a `random_password` resource for the
  backend↔Worker service secret (§5a), and a `cloudflare_api_token` resource
  scoped to `D1:Edit` on the D1 database for the backend's migration runner
  (§5b) — both output as `sensitive`, to be copied into the backend's `.env`
  after `terraform apply`.
- **Networking**: the repo takes no position on how the backend/dashboard is
  exposed beyond the local machine — that's a deployment-time decision left to
  the operator (LAN-only, reverse proxy, VPN, tunnel, etc.). The core archiving
  flow (extension/PWA/CLI → Worker → R2 → backend polling) works identically
  regardless of that choice, since it never depends on backend reachability.

---

## 13. Repository Layout

Monorepo, structured flat by "what a thing is" rather than by architectural
layer. Components only get their own directory when they genuinely need
isolation (their own build tooling, dependency manifest, or — in the
Worker/PWA's case — a hard requirement of having **no** build step at all). The
screenshot service does not add a new top-level directory: it's driven by Go
code in the existing backend module (via `chromedp`, connecting to the sidecar
container over the network) plus a new service definition in
`docker-compose.yml`.

```
recueil/
├── main.go                  # embeds Postgres migrations/ and D1's
│                               # terraform/worker/migrations/ (embed
│                               # directives can't reach either from cmd/,
│                               # one directory below both — see cmd/server.go),
│                               # assigns them to exported cmd package vars,
│                               # then os.Exit(cmd.Execute())
├── cmd/
│   ├── root.go              # cobra root command; owns the one signal-aware
│   │                           # context (SIGINT/SIGTERM), threaded to
│   │                           # subcommands via cmd.Context() rather than
│   │                           # each subcommand creating its own (§13a)
│   ├── server.go             # `recueil server` — the actual backend startup:
│   │                            # config, both migration runs (via fs.Sub on
│   │                            # the embedded FS's from main.go), the
│   │                            # bootstrap holder, httpapi wiring, graceful
│   │                            # shutdown on cmd.Context().Done()
│   └── cli/                 # recueil-cli, shares the same go.mod
├── internal/
│   ├── config/               # viper-based config: --config TOML file, env
│   │                            # vars, defaults set in this package's own
│   │                            # init() (§13a)
│   ├── auth/                  # password hashing, session tokens, bootstrap flow
│   ├── db/                     # sqlc-generated query code (renamed from
│   │                             # an earlier `dbgen` during Phase 1)
│   ├── pgmigrate/              # applies migrations/*.sql via goose's Provider
│   │                             # API against an already-open pool (§13a)
│   ├── dbtest/                 # Postgres integration-test harness: connects
│   │                             # to docker-compose.test.yml, applies
│   │                             # migrations via internal/pgmigrate, t.Cleanup
│   │                             # fixture factories (§13a)
│   ├── d1migrate/              # applies D1 migrations via the Cloudflare
│   │                             # API (§5b) against an fs.FS the caller
│   │                             # supplies — main.go embeds
│   │                             # terraform/worker/migrations/*.sql and
│   │                             # passes it in, same pattern as pgmigrate
│   ├── mirror/                 # pushes the credential mirror to the Worker
│   └── httpapi/                # dashboard-facing HTTP handlers + chi router;
│                                  # also mounts /info, /ping, /health
│                                  # (unauthenticated — §13a) on the same router
├── migrations/                 # Postgres migrations — plain .sql files, no
│                                  # embed.go: main.go embeds these directly
│                                  # (a sibling directory, no `..` needed) and
│                                  # passes the fs.FS into pgmigrate.Run; the
│                                  # test harness instead reads this same
│                                  # directory straight off disk (os.DirFS),
│                                  # since tests always run with the full repo
│                                  # present and don't need go:embed's
│                                  # binary-self-containment property (§13a)
├── queries/                    # sqlc source .sql query files
├── sqlc.yaml
├── src/                     # Svelte dashboard source
├── Dockerfile
├── go.mod
├── package.json             # root: dashboard's Vite/Svelte deps, plus
│                               # Worker test/lint devDependencies (vitest,
│                               # @cloudflare/vitest-pool-workers, eslint) —
│                               # the Worker directory has no package.json
│                               # of its own; see §13a
├── vite.config.js
├── vitest.config.js          # root-level; covers Worker tests now, expected
│                               # to grow a Svelte-scoped project (§13a)
├── eslint.config.js           # root-level; same per-directory-scoping plan
├── Makefile                   # test-db-up/test-db-down/test — the same
│                                 # commands drive docker-compose.test.yml
│                                 # locally and in CI (§13a)
├── docker-compose.test.yml    # dedicated ephemeral test Postgres (§13a) —
│                                 # distinct port + tmpfs, separate from the
│                                 # dev database below
│
├── extension/                # WebExtension, own package.json (needs bundling
│   ├── src/                    # to pull in Readability.js and vendored
│   ├── manifest.json            # SingleFile capture code as dependencies)
│   └── package.json
│
├── terraform/                  # OpenTofu module
│   ├── main.tf                   # includes random_password for the
│   │                                # backend↔Worker service secret (§5a) and
│   │                                # a cloudflare_api_token scoped to D1:Edit
│   │                                # for the backend's migration runner (§5b)
│   ├── variables.tf
│   ├── outputs.tf
│   ├── worker/                   # plain JS, no build step for deployment —
│   │   ├── index.js                # local test/lint tooling doesn't change that
│   │   ├── migrations/            # D1 schema — applied by the backend (§5b),
│   │   │   │                        # not by wrangler
│   │   │   ├── 0000_schema_migrations.sql
│   │   │   └── 0001_users.sql
│   │   ├── tests/                 # @cloudflare/vitest-pool-workers — real
│   │   │   │                        # simulated D1 via Miniflare, not mocks
│   │   │   ├── apply-migrations.js
│   │   │   ├── fetch.test.js
│   │   │   └── handleUserMirror.test.js
│   │   └── tsconfig.json          # @ts-check/JSDoc type-checking, index.js only
│   └── pwa/                      # static share-target PWA, no build step
│       ├── index.html
│       └── manifest.json
│
├── website/                      # Zola site — self-contained, own layout
│   ├── config.toml
│   ├── content/
│   ├── templates/
│   └── static/
│
├── pnpm-workspace.yaml
├── docker-compose.yml            # backend + postgres + screenshot sidecar
│                                    # (dev database — see docker-compose.test.yml
│                                    # above for the separate test one)
├── README.md                      # includes backup guidance, see §14
└── LICENSE
```

Note: this tree reflects the Go package layout, Worker tooling, and the Postgres
testing/migration setup as actually implemented, plus the root-level
`vitest.config.js`/`eslint.config.js` placement agreed on during that work
(§13a). The `cmd/cli/` entry is carried over unchanged from the previous
revision and hasn't been revisited since the `main.go`/`cobra` restructure —
worth confirming it still shares `go.mod` cleanly once the CLI is actually
built.

### Notes on specific decisions

(Unchanged from v1 — see original rationale for Go-at-root, CLI sharing the
server's Go module, the dashboard's build output being embedded via `go:embed`,
the Worker/PWA's no-build-step requirement, the extension's own
directory/bundler, and `website/`'s self-containment.)

### 13a. Implementation Stack & Tooling

Concrete tooling choices made during implementation, kept here rather than split
into a separate document — per this section's own placement, this is meant to be
read before implementing the next piece, not discovered after the fact in a
README that can drift out of sync with the architecture decisions around it.

**Backend (Go):**

- **Postgres access:** `pgx/v5` as the driver; `sqlc` for codegen from
  `queries/*.sql` against the `migrations/` schema (`sql_package: pgx/v5`) — the
  hand-written query files are the source of truth, the generated code under
  `internal/db/` is regenerable, not hand-maintained.
- **Postgres migrations:** `goose`, as a library — not the external CLI. Uses
  goose's `Provider` API (`goose.NewProvider`/`WithStore`/ `WithSessionLocker`)
  rather than its older package-level `SetBaseFS`/ `SetDialect` functions: those
  mutate shared package-global state, which is a genuine data race if ever
  called concurrently within one process (confirmed with `-race` — two
  goroutines calling them simultaneously race immediately, even setting
  identical values); `Provider` scopes all config to the call and is documented
  safe for concurrent use (confirmed: 8 concurrent calls against the same pool,
  zero race warnings). Bookkeeping lives in a table named `schema_migrations`
  (via `WithStore`), matching D1's migration bookkeeping table name, not goose's
  default `goose_db_version`. Also takes a Postgres session (advisory) lock for
  the duration of a migration run (`WithSessionLocker`), so two processes racing
  to migrate the same database — a rolling deploy briefly overlapping two
  backend instances, a stray manual invocation — serialize rather than
  interleave. Takes an already-open `*pgxpool.Pool` rather than a database URL,
  so a caller that already has a pool (production startup, the test harness
  below) doesn't open a second connection just to migrate. This resolves the
  goose-as-library question raised and deliberately deferred earlier (see §15) —
  and ends up going a step further than D1's migration runner by adding the
  session lock, which D1's Cloudflare-API-based approach has no equivalent for.
- **Postgres test harness:** `internal/dbtest` — connects to a dedicated,
  ephemeral test Postgres container (`docker-compose.test.yml`; a distinct port
  from the dev database so both can run at once; `tmpfs` data directory so every
  start is genuinely clean, unlike dev's bind-mounted durability), applies
  migrations via the same `internal/pgmigrate` code path production startup uses
  — not a separate test-only migration runner — and provides
  `t.Cleanup`-registering fixture factories (`CreateUser`, `CreateSession`).
  Fails the test hard (`t.Fatalf`) if the database isn't reachable, never skips:
  a missing test database should be loud everywhere it happens, not quietly
  hidden behind a passing (skipped) run. `Reset` (for tests needing a
  guaranteed-clean starting state) truncates every table in the schema
  discovered dynamically via `pg_tables`, not a hardcoded list — so it doesn't
  need updating every time a migration adds a table, and correctly clears tables
  with no foreign-key path back to `users` at all, which a hardcoded
  `TRUNCATE users CASCADE` would silently miss. `testcontainers-go` was
  considered for container provisioning and rejected: its dependency tree (a
  full Docker API client, containerd, OpenTelemetry, `gopsutil`) is heavier than
  anything else in this project, including Viper.
  `make test-db-up`/`test-db-down`/`test` drive the same
  `docker-compose.test.yml` locally and in CI, rather than a separately
  maintained GitHub Actions `services:` block.
- **D1 migrations:** run by the backend itself at startup — embedded
  (`go:embed`) SQL files applied via a direct call to Cloudflare's D1 query API
  using the official `cloudflare-go` SDK. See §5b for the credential and
  rationale; tracked in D1's `schema_migrations` table (§10), not wrangler's.
- **CLI / config:** `cobra` for command structure — `main.go` embeds both
  migration directories and hands them to the `cmd` package (see the repo tree
  above), then `os.Exit(cmd.Execute())`; the actual backend startup lives in
  `cmd/server.go` as a `recueil server` subcommand, not in `main.go` itself.
  `Execute()` owns a single signal-aware context (`signal.NotifyContext` on
  `SIGINT`/`SIGTERM`) passed to `rootCmd` via `ExecuteContext`; subcommands read
  it back via `cmd.Context()` rather than each creating its own — confirmed for
  real that this context reaches a subcommand's `RunE` correctly and that its
  cancellation is what `cmd/server.go` waits on to shut the HTTP server down
  gracefully (a real behavior this gained over the phase-1 `main.go`, which
  built a cancellable context but never actually used it). `viper` for
  configuration — an explicit `--config` TOML file (shell completion restricted
  to `.toml` via `MarkPersistentFlagFilename`, no automatic search of `$HOME` or
  the working directory the way cobra-cli's default scaffold does), environment
  variables, and in-package defaults. Defaults are set in `internal/config`'s
  own `init()`, not in `cmd/root.go` — they need to apply regardless of which
  binary or test calls `config.Load()`, not only when `cmd`'s `init()` has
  already run. Viper pulls in a notably heavier dependency tree than most
  choices in this project — parsers for formats never used (YAML, HCL, Java
  properties, INI, dotenv) alongside the one actually used (TOML) — accepted for
  the CLI-ecosystem integration cobra and viper provide together, on the
  reasoning that Go's own dead-code elimination strips the unused format parsers
  from the final binary regardless of how large the source dependency is.
- **Health checks:** `go.finelli.dev/healthchecks` (module
  `github.com/mfinelli/go-healthchecks`), mounted directly on the same chi
  router as the dashboard API (`internal/httpapi`) rather than a second port —
  `/info` (build metadata), `/ping` (machine-consumable status code, for a
  Docker `HEALTHCHECK` or uptime monitor), `/health` (always `200`,
  human-readable JSON detail on failure). Deliberately unauthenticated and
  registered outside the `RequireSession` group. Two things confirmed against
  the real library rather than assumed from its docs: it declares
  `package healthcheck` (singular) despite the plural import path, and its
  handlers are returned as the library's own unexported function type, not
  `http.HandlerFunc` — chi's `Get` requires the latter specifically, so mounting
  them needs an explicit `http.HandlerFunc(hc.Health())` conversion, not a
  direct pass. The `Check` function itself calls a small `Ping` method added to
  `internal/db.Queries` (`SELECT 1` through the existing `DBTX` interface)
  rather than threading the raw `*pgxpool.Pool` into `httpapi`.
- **Metrics:** `/metrics`, Prometheus exposition format, mounted on the same chi
  router (`internal/metrics`). Standard Go runtime and process collectors
  (`collectors.NewGoCollector`/`NewProcessCollector`) plus a custom
  `recueil_users_total` gauge — a `prometheus.Collector` that queries
  `CountUsers` fresh on every scrape rather than maintaining cached state,
  expected to grow (pages, captures, collections) as those features exist.
  Deliberately built on its own `prometheus.NewRegistry()`, not the global
  `prometheus.DefaultRegisterer` — same reasoning as choosing goose's `Provider`
  API over its package-level `SetBaseFS`/`SetDialect`: avoids hidden shared
  mutable state that could collide across multiple instantiations (confirmed via
  test: two independently-built registries never collide, which they would under
  the global default). A failed collection (e.g. the DB unreachable) is logged
  and simply omits that one metric rather than failing the whole scrape —
  confirmed for real, both the success and failure paths. **OpenTelemetry
  (distributed tracing) was considered and deliberately deferred, not rejected
  outright.** The core API/SDK (`go.opentelemetry.io/otel`) is actually light on
  its own (just `go-logr`), but any real exporter — confirmed even the
  OTLP-over-HTTP variant, not just gRPC — pulls in `google.golang.org/grpc`'s
  full tree, comparable in weight to `testcontainers-go` (§13a, rejected earlier
  for the same reason). More fundamentally, tracing's value scales with a
  request's hop count across services, and this project's current call graph is
  shallow (one backend process, Postgres, occasional Worker calls) — the
  architectural case isn't there yet, and self-hosted personal-scale operators
  are unlikely to be running a trace backend to send spans to regardless. Worth
  revisiting once the screenshot service (§6) and AI enrichment (§7) exist as a
  genuine async multi-stage pipeline — that's the shape (multiple hops,
  independent failure points, a second real process boundary in the chromedp
  sidecar) where tracing's value proposition actually applies here.
- **Password hashing:** `bcrypt` (`golang.org/x/crypto/bcrypt`).
- **HTTP routing:** `chi` (`github.com/go-chi/chi/v5`) — confirmed zero
  transitive dependencies, and its middleware signature
  (`func(http.Handler) http.Handler`) is identical to stdlib's own convention,
  so `internal/auth`'s `RequireSession`/`RequireAdmin` needed no changes to work
  as ordinary chi middleware. This supersedes the earlier phase-1 choice of
  stdlib `net/http`'s own pattern routing with no router library at all —
  reasonable for three routes, less so once route grouping (stating an auth
  requirement once for a whole group, e.g. future admin-scoped routes under §5's
  Manage Devices screen) and middleware composition became the actual friction,
  rather than routing itself.
- **HTTP middleware:** `github.com/go-chi/httplog/v2` for structured request
  logging, plus a handful of chi's own middlewares — chosen and ordered based on
  what actually held up under testing, not just chi's defaults.
  `httplog.RequestLogger` already wraps chi's own `RequestID` and `Recoverer`
  internally (confirmed via source and by deliberately panicking a handler:
  clean `500`, full stacktrace logged, server kept running) — neither needed
  adding separately. `CleanPath` is kept; `RedirectSlashes` deliberately is not,
  because `CleanPath`'s `path.Clean()` silently strips a trailing slash into
  chi's internal `RoutePath` before any redirect-based slash-handling middleware
  would ever see one — confirmed for real that a `POST` to a trailing-slash
  route variant hits the handler directly with no visible redirect, same method,
  making `RedirectSlashes` inert given this ordering (and a silent internal
  normalization is the safer behavior for a JSON API regardless — no HTTP
  redirect method-preservation question ever arises). `RequestSize` (1MB cap)
  and `Timeout` (30s, returning `504`) are route-agnostic hardening applied
  globally; `AllowContentType("application/json")` is scoped to the `/api`
  sub-router specifically, since it's enforcing the JSON API's data contract,
  not a general protection every current or future route should inherit
  (confirmed harmless on bodyless requests either way — it skips the check when
  `r.ContentLength == 0` — but scoped for what it communicates, not because
  scoping changes behavior today). `RealIP` was considered and deliberately not
  added: genuinely useful behind a trusted reverse proxy, but this project
  treats network exposure (LAN-only, VPN, tunnel, reverse proxy) as entirely the
  operator's choice (§2, §12) — blindly trusting a client-supplied header
  without knowing a proxy is actually in front would let anyone reachable spoof
  their IP in logs. `pprof` (`middleware.Profiler`) was also considered and
  deliberately not added: useful for an operator diagnosing their own instance,
  but exposes sensitive runtime info and its own CPU-cost surface, not something
  to mount on the same unauthenticated router as health checks without a
  separate, deliberate decision about how it's gated.
- **Testing:** `testify`, with table-driven cases (`t.Run` subtests, or
  `[]struct{...}` tables) where that reduces duplication rather than as a
  blanket rule. For code that calls an external HTTP API, tests run against a
  real `httptest.Server` plus that library's own base-URL override where one
  exists (e.g. `option.WithBaseURL` for `cloudflare-go`), rather than a
  hand-rolled interface mock — closer to the real request/response shape for the
  same effort. Handler-level tests (`internal/httpapi`) are written as external
  `_test` packages deliberately — exercising only the package's exported
  constructors, the same way a real caller would, rather than reaching into
  unexported internals.

**Cloudflare Worker:**

- **No build step, ever.** Plain JS (ES modules), not TypeScript; deployed via
  Terraform's Cloudflare provider directly, never `wrangler deploy`.
- **Static type-checking without a build step:** `@ts-check` + JSDoc annotations
  in `index.js`, checked via `tsc --noEmit` against a `tsconfig.json` scoped to
  the deployed script only. Test files are deliberately out of that scope — they
  import the `cloudflare:test` virtual module, which only exists inside the
  Vitest pool's runtime and which plain `tsc` has no way to resolve.
- **Linting:** ESLint (flat config), root-level (`eslint.config.js`), scoped
  per-directory via each config object's `files` glob rather than a separate
  config file per component. The Worker's own `index.js` needs
  `globals.serviceworker` (for `Request`/`Response`/`URL`/`fetch`/`crypto`, none
  of which are standard Node or browser globals as far as ESLint's built-in
  knowledge goes); its test files additionally need `globals.vitest`. The same
  file is expected to grow a Svelte-dashboard-scoped block later rather than
  needing a file of its own.
- **Testing:** `@cloudflare/vitest-pool-workers` — runs test files inside the
  real `workerd` runtime (not a Node-side approximation of it), with Miniflare
  providing a real local D1 database. The same `migrations/*.sql` files that
  back §5b's runtime migrations are applied to that local database via
  `readD1Migrations`/`applyD1Migrations`, so there's one schema source of truth
  rather than a separate test fixture schema to keep in sync. Root-level
  `vitest.config.js`, using Vitest's `projects` array (not the older, now
  superseded `vitest.workspace.ts` mechanism) so the same file and the same
  `vitest run` invocation will also cover Svelte dashboard tests once those
  exist — each project scoped to its own runtime/environment (`workerd` for the
  Worker, presumably `jsdom` or similar for Svelte component tests), never mixed
  within one project.
- Both the ESLint and Vitest devDependencies live in the root `package.json` —
  the Worker's own directory has no `package.json` of its own and isn't a pnpm
  workspace member. The "no build step" constraint is about what ships to
  Cloudflare on deploy; it was never a constraint on what local tooling is
  allowed to exist for development and CI.

This section is expected to keep growing as the extension, dashboard, and CLI
are built out.

---

## 14. Backup & Restore

**The application performs no automated backup.** This is a deliberate choice:
baking `pg_dump` (or equivalent) into the backend's own image or shelling out to
it from the Go binary is an awkward dependency for an application binary to
carry, and commits the project to tracking Postgres version compatibility
indefinitely. Instead, backup is documented as the operator's responsibility.

### What must be backed up

Two things, together, on the same schedule:

1. **The Postgres database** — via `pg_dump` or equivalent. This is the
   irreplaceable half: page groupings, tags, collections, version history,
   accounts. Note that copying Postgres's raw data directory while the container
   is running is **not** safe without WAL-aware tooling — a proper dump (or a
   backup tool that understands Postgres's on-disk format) is required, not a
   raw file copy.
2. **The local archive directory** (zstd-compressed HTML + thumbnails) — a plain
   directory copy/sync is fine here, since these are static files once written.

Because both bind-mount to the host filesystem (§4, §12), any external backup
tool (rsync, restic, a `pg_dump | rclone` pipeline, etc.) can operate on them
directly. The README should include one example recipe as a starting point,
without the application running it itself.

**Consistency matters**: if the two are backed up on different schedules or by
different mechanisms, a restore can leave a `captures` row pointing at an
`html_path` that wasn't captured in the same backup window, or vice versa. Both
should be backed up in the same job/window.

### Restore

- The archive directory must be restored to the **same mount path** it was
  originally running at — `captures.html_path` values are not stored relative to
  a configurable root, so a different path layout on restore will break lookups.
- After restoring Postgres from a backup, the **D1 credential mirror can be
  stale** relative to the restored state (e.g. password changes or account
  creations made after the backup was taken won't be reflected, or deleted/
  changed accounts may still have valid mirrored credentials). A manual **resync
  command** (CLI or admin dashboard action) re-runs the existing
  credential-mirror-push logic as an idempotent bulk operation across all users,
  rather than only firing on the create/password-change event hook. This should
  be run after any Postgres restore.

---

## 15. Open Questions / Future Decisions

All items from the previous revision have been resolved (AI tag styling,
manage-devices design, bootstrap hardening, and registration model — see §5,
§7). Phase 0 (Cloudflare scaffolding — D1, R2, and a bare Worker, provisioned
via a public Terraform module) is also complete and resolved the module layout
question below in favor of a reusable module in `terraform/`, consumed via
source = `"..."` (pinned to a tag once releases exist) from the operator's own
root config.

Phase 1 (backend + Postgres + bootstrap admin, §5) is also complete: session
auth, the bootstrap-admin flow (§5, now in-memory as described there), and the
Worker's first real route (`/internal/users/mirror`, §5, §5b) are built and
tested. **`wrangler` is deliberately absent from this project's toolchain
entirely** — the Worker deploys via Terraform's Cloudflare provider directly
(not `wrangler deploy`), and D1 schema migrations run via a direct,
backend-embedded call to Cloudflare's D1 query API (§5b) rather than
`wrangler d1 migrations apply`. This is worth stating plainly since it's a real,
deliberate absence rather than something that just hasn't come up yet.

Since that revision, Postgres migrations were also moved off the external
`goose` CLI and onto goose-as-a-library (§13a), resolving the item this section
previously left open — Postgres now mirrors D1's "the binary applies its own
migrations" shape, and in one respect goes further (a session advisory lock
around the migration run, which D1's Cloudflare-API-based approach has no
equivalent for). `chi` also replaced stdlib `net/http`'s own routing (§13a) once
route grouping and middleware composition became the actual friction as the API
surface grew past a handful of routes; `cobra` and `viper` were adopted for CLI
structure and configuration. None of these supersede an architectural decision
recorded elsewhere in this document — they're implementation-phase tooling
choices, tracked in §13a rather than here.

What remains open is purely implementation-phase, not architectural:

- Whether `ENABLE_OPEN_REGISTRATION` (mentioned in §5 as a future invite-only
  toggle) is worth building in the initial version or deferred until someone
  actually asks for it.
- The pairing-token redesign (round five) retrofitted already-built Phase 1
  code: the `/internal/users/mirror` Worker route and D1 `users` schema both
  changed from password-hash mirroring to pairing-token-hash mirroring before
  Phase 2's device-pairing endpoint could be built against them. This is now
  complete.
- **Resolved this round:** the Worker API endpoint surface flagged as "the
  logical next design step" in the previous revision — device pairing
  (`POST /pair`), the queue (`POST /queue`, `GET /queue`,
  `POST /queue/:id/claim`), device-token management
  (`GET`/`DELETE /internal/tokens`), and queue-item cleanup
  (`POST /internal/queue-items/cleanup`) — is now built and tested (§5, §8,
  §10). What's _not_ built yet, and remains the next logical step: presigned R2
  upload URLs, and the `complete`/`fail` endpoints that transition a claimed
  queue item to `captured`/`failed` and write the corresponding
  `pending_captures` row — deliberately deferred out of Phase 2, since that work
  is entangled with the capture-upload pipeline's shape rather than the
  queue/auth mechanics Phase 2 was scoped to.
- **New this round:** what to do with `failed` queue items long-term. §8's
  cleanup endpoint deliberately never removes them (only `captured` items are
  swept), but "keep forever, do nothing else" isn't a real long-term answer —
  surfacing them to the user, a retry mechanism, or a separate (probably much
  longer, or manually-triggered) expiry are all plausible and none has been
  decided. Blocked on the `complete`/`fail` endpoints existing at all, since
  there's no way to mark an item `failed` yet.
