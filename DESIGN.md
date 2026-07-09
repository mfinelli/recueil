# Recueil — Design Document

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

**Capture happens in a real, already-authenticated, already-rendered browser
tab — not a headless fetch.** This is the actual fix for the CAPTCHA/paywall
problem, not a workaround. Because of this, the system deliberately does
**not** add any server-side "fetch and archive a URL" fallback — doing so
would reintroduce the exact failure mode being solved.

### Format decision

Store exactly one artifact format per capture: a fully inlined single HTML
file (SingleFile-style — CSS, images, fonts inlined as data URIs), plus a
plain-text Readability extraction, plus a thumbnail image. No WARC, no PDF, no
MHTML.

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
                        │  - device auth      │
                        │  - enqueue URL       │
                        │  - presigned R2 URLs │
                        │  - D1 read/write     │
                        └─────────┬─────────┘
                                  │
                    ┌─────────────┼─────────────┐
                    ▼                           ▼
             ┌─────────────┐             ┌─────────────┐
             │     D1      │             │     R2      │
             │ (queue,     │             │ (temp blob  │
             │  tokens,    │◄────┐       │  storage)   │
             │  credential │     │       │             │
             │  mirror,    │     │       │             │
             │  bookmark   │     │       │             │
             │  mirror)    │     │       │             │
             └──────┬──────┘     │       └──────┬──────┘
                    │            │              │
                    │  poll      │              │  pull
                    ▼            │              ▼
        ┌──────────────────────────────────────────┐
        │         Desktop Browser Extension          │
        │  - reads queue from D1 (via Worker)         │
        │  - user selects item → loads URL             │
        │  - captures: HTML, Readability text,          │
        │    screenshot thumbnail                        │
        │  - uploads to R2 via presigned URL              │
        └──────────────────────────────────────────┘
                    │
                    │ (async, outbound-only polling)
                    ▼
        ┌──────────────────────────────────────────┐
        │      Backend (Go + Postgres, Docker)       │
        │  - polls Worker/D1 for pending captures      │
        │  - pulls blobs from R2, then deletes from R2   │
        │  - zstd-compresses HTML, stores locally          │
        │  - runs optional AI enrichment (summary/tags)      │
        │  - pushes bookmark-list mirror row to D1 ───────────┘
        │    (via Worker, after each capture is processed)
        │  - pushes password-hash mirror to D1 ────────────────┘
        │    (via Worker, on account creation/password change)
        │  - serves dashboard API (optional, network-agnostic)   │
        │  - runs separate scheduled backup job to S3-compatible   │
        │    storage                                                 │
        └──────────────────────────────────────────┘
                    │
                    ▼
        ┌──────────────────────────────────────────┐
        │        Svelte Dashboard (optional)          │
        │  - library browsing, search, reader view      │
        │  - version history per page                    │
        │  - tags (manual + AI) and nested collections      │
        └──────────────────────────────────────────┘
```

### Key architectural property: capture path never touches the backend

The desktop extension, the share-sheet PWA, and the CLI depend **only** on
the Worker and R2 — both public, both authenticated via bearer token. None of
them ever need the backend to be network-reachable. The backend's only
required connectivity is **outbound**: polling the Worker/D1 API and pulling
objects from R2. It can run with zero inbound firewall rules and the entire
archiving loop still works end to end.

Backend network reachability is a concern **only** for the optional
dashboard (browsing your library, search, login). How that's exposed (LAN
only, reverse proxy, VPN, tunnel, etc.) is entirely up to the deployer and is
intentionally out of scope for this project — the repo should document the
requirement ("must be reachable by whatever device you want the dashboard
on") without assuming any specific networking solution.

---

## 3. Capture Flow

1. User adds a URL to the queue, either:
   - Directly in the desktop extension while browsing, or
   - Remotely via the share-sheet PWA (Android) or iOS Shortcut (phone) or
     CLI — these only enqueue, they never capture.
2. Enqueueing hits the Worker, which writes a row to `queue_items` in D1.
3. The desktop extension polls D1 (via the Worker) for pending queue items
   and can notify the user something needs archiving.
4. User selects a queued item (or a page they're currently on, for direct/
   unqueued capture) and triggers capture.
5. Extension captures:
   - Full inlined single-page HTML (SingleFile-style)
   - Readability.js-extracted plain text (run **in the extension**, against
     the live DOM, before any re-archival loses render-time state)
   - A screenshot of the current viewport via `chrome.tabs.captureVisibleTab`
     (works identically in Firefox — both implement the WebExtensions API)
6. Extension requests a presigned R2 upload URL from the Worker, uploads all
   three artifacts directly to R2 (bypassing Worker body-size limits;
   presigned R2 PUT supports objects far larger than any archived page will
   ever be).
7. Extension notifies the Worker that the upload is complete → Worker writes
   a `pending_captures` row to D1 (and marks the `queue_items` row, if any,
   as `captured`).
8. Backend, on its own polling schedule, discovers the new `pending_captures`
   row, pulls the blobs from R2, zstd-compresses the HTML, stores it on local
   disk, computes a content hash, deletes the R2 objects, writes rows to
   Postgres, and finally pushes a lightweight mirror row back to D1 for the
   bookmark-list feature (see §7).
9. (Optional, async) Backend enqueues an AI job to summarize/tag the capture
   using the Readability text.
10. (Separate, decoupled job) Backend periodically syncs local zstd-
    compressed archives to any S3-compatible bucket for backup. This is
    intentionally decoupled from the ingestion path so it can't block or
    complicate saving new captures.

### Thumbnail handling

No attempt is made to standardize the *capture* viewport — forcing a browser
resize before every capture would be jarring for the user. Instead, the
screenshot is captured at whatever viewport size the user currently has, and
the **output** is standardized: resized/center-cropped to a fixed thumbnail
size (e.g., in the extension before upload, or on the backend with a small
image-processing step). The dashboard displays all thumbnails at a
consistent size via `object-fit: cover` regardless of native capture
dimensions.

### Re-archiving the same URL

Re-archiving a previously captured URL is **not** an update — it's a new
version under the same logical page. The backend groups captures by
`normalized_url` (see §8, URL normalization) into a `pages` row, and each
individual capture becomes a new `captures` row linked to that page. The
dashboard shows all historical versions of a page with their capture
timestamps. Each capture stores a `content_hash`; if it matches the previous
capture's hash, the dashboard can flag the version as "unchanged since last
capture" without needing to diff full HTML — the version is still stored in
full (cheap, since it's compressed), just visually flagged.

---

## 4. Storage Strategy

- **R2 is temporary only.** It exists purely to get large payloads from the
  extension (which may not have a stable public endpoint to push to) to the
  backend (which may not be reachable to receive a push). Once the backend
  has pulled and locally stored a capture's blobs, they are deleted from R2.
- **Local disk is canonical.** The backend stores the zstd-compressed HTML
  (HTML compresses extremely well with zstd, commonly 80-90% size reduction)
  on local disk, referenced by path from the `captures` table.
- **Backup is separate and decoupled.** A distinct scheduled backend job
  syncs the local compressed archive to any S3-compatible bucket the user
  configures. This is not part of the ingestion critical path.
- **Database choice: Postgres, not SQLite**, despite this being a personal
  archive. Originally SQLite was considered sufficient for a single user, but
  the requirement for real user accounts (family members using one
  deployment, and potential future multi-tenant hosted version) tips this in
  Postgres's favor:
  - SQLite's single-writer lock becomes a real constraint with concurrent
    family members archiving and querying at once.
  - Multi-tenant isolation and future hosted-DB migration paths are native to
    Postgres.
  - Docker Compose makes the extra container a non-issue operationally — it's
    one more service with a volume, no meaningful complexity increase over
    SQLite.

---

## 5. Authentication

### Requirements driving the design

- The backend must never need to be publicly reachable for the core
  archiving flow to work.
- Multiple devices (desktop extension, phone shortcut, CLI, PWA) need
  independent, individually revocable credentials.
- Real user accounts are wanted (to support family members on one shared
  deployment, and to keep the door open for a future hosted/paid version)
  without adding SQLite-scale operational complexity.

### Design: opaque bearer tokens, D1 credential mirror

- Postgres (`users` table) is the source of truth for accounts
  (username + password hash).
- On account creation or password change, the backend pushes the **hash
  only** to a mirrored `users` table in D1. The Worker never talks to the
  backend to authenticate — it checks the D1 mirror directly. This keeps the
  backend fully non-public while still allowing real-time login through the
  Worker.
- Device pairing: a device (extension, PWA, CLI) authenticates once via
  username/password (or a pairing code) against the Worker, which checks the
  D1 credential mirror and — on success — issues an **opaque bearer token**.
- **Why an opaque token instead of a JWT:** JWTs earn their complexity when
  the recipient needs to trust embedded claims *without* a database lookup
  (self-contained signature verification). Because request volume here is low
  and a database check is being done on every request regardless (to support
  per-device revocation), a JWT provides no practical benefit over a plain
  lookup table — it would add signing/claims-schema complexity for no gained
  guarantee. A 32-byte CSPRNG-generated random token, base64url-encoded with
  a human-recognizable prefix (e.g. `rcl_live_...`), is simpler and equally
  secure at this scale.
- **Revocation:** store every issued token (hashed) rather than using
  short-lived tokens with refresh — given low request volume, the
  operational simplicity of "store everything, delete a row to revoke" beats
  the added complexity of a refresh-token rotation scheme.
- **Hashing at rest:** the raw token is shown to the user exactly once, at
  pairing time (GitHub personal-access-token style). Only `SHA-256(token)` is
  stored. This is **not** password-style hashing — the token already has ~256
  bits of entropy, so a slow/salted hash (bcrypt/argon2) buys nothing. Plain
  SHA-256 protects against the real risk (a leaked database dump containing
  usable plaintext bearer credentials), while staying fast for
  per-request lookups.
- **Uniqueness:** a `UNIQUE` constraint on `token_hash` turns an
  astronomically unlikely CSPRNG or SHA-256 collision into a provable
  impossibility — insertion fails and the pairing endpoint simply
  regenerates.

```sql
-- D1
CREATE TABLE tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash TEXT NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id),
  device_name TEXT NOT NULL,
  device_type TEXT NOT NULL,       -- 'extension' | 'pwa' | 'cli'
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TIMESTAMP
);
```

- `last_used_at` doubles as the data source for a future "manage devices"
  dashboard screen (see every paired device, when it was last active, revoke
  individually).
- The **dashboard's** login (if the user runs it) uses this exact same
  token — issued the same way, checked the same way by the backend against
  its own copy of `tokens` (or, since the backend already owns the source of
  truth, it can verify directly). There is no separate JWT-based dashboard
  auth layer distinct from the device-token system — one mechanism, used
  everywhere a credential is needed.

---

## 6. AI Enrichment (Optional)

- Entirely optional and asynchronous — never blocks capture or ingestion.
  A capture is fully valid, searchable, and browsable with zero AI fields
  populated.
- Runs against the Readability-extracted plain text, not the raw HTML —
  cheaper and produces better summaries than trying to parse rendered HTML.
- Supports **two backend types**, chosen by the user in configuration:
  - Ollama-compatible (local)
  - OpenAI-compatible (hosted)
  A single small interface (`Summarize(text) (summary, tags, error)`) covers
  both; nearly every other provider's API is close enough to one of these two
  shapes to fit behind the same interface later if needed.
- Tracked in its own `ai_jobs` table, one row per capture, decoupled from the
  `captures` table so enrichment status/failure never affects the capture's
  core validity.
- AI-generated tags are written to the same `page_tags` table as manual tags,
  distinguished by a `source` column (see §8).

---

## 7. Cross-Device Queue and Bookmark List

### Queue (phone → desktop archiving)

- Adding a URL from a phone (via Shortcut, PWA, or CLI) only **enqueues** it
  — it does not attempt to archive anything server-side. The intended
  workflow is deliberately: queue remotely, archive later from the desktop
  extension, where a real rendered/authenticated browser session exists.
- The desktop extension polls the queue via the Worker/D1 and can notify the
  user that items are waiting.
- Claiming is done with a conditional update (`WHERE status = 'pending'`) to
  prevent two devices from grabbing the same item simultaneously; a claimed
  item records which device claimed it and when.

### Bookmark-list mirror (extension as a browsable list)

- Separately from the queue, the extension can act as a lightweight bookmark
  list of everything already archived — similar to a browser's native
  bookmarks UI: just title + URL, no thumbnails.
- This is a **one-way, backend → D1 push**: after the backend finishes
  processing a capture, it upserts a row into a D1 `archived_pages` table.
  This is the mirror-image of the credential mirror (backend → D1, rather
  than D1 → backend), keeping the same principle: the extension only ever
  needs to talk to the Worker/D1, never the backend.
- The extension does **not** live-sync this list. It caches the list locally
  (`browser.storage.local` or IndexedDB) and refreshes on a coarse schedule
  (e.g., once per day) or on explicit user request, using an incremental
  "give me changes since X" query against `archived_pages.updated_at`.
- Because this list is just title + URL, no thumbnail storage is needed in
  R2 or D1 for this feature — thumbnails only need to exist wherever the
  full dashboard renders them (local disk / Postgres reference), and can
  still be deleted from R2 after backend pickup like the full HTML.

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

---

## 8. URL Normalization

Two URL fields are stored for every capture, never conflated:

- **`raw_url`** — exactly what was captured, byte-for-byte, never rewritten.
- **`normalized_url`** — a computed, canonical form used purely as the
  dedup/grouping key that determines which `pages` row a capture belongs to.

Normalization strategy:

- Adopt the **ClearURLs** community-maintained ruleset (regex-based rules per
  site/provider, actively maintained, MIT licensed) to strip known tracking
  parameters (`utm_*`, `fbclid`, `gclid`, `igshid`, etc.) without touching
  functionally meaningful query parameters. Do not hand-roll a tracking
  parameter list — the ClearURLs rules are maintained by a community tracking
  a constantly moving target, which a static list would quickly fall behind.
- Additional canonicalization beyond tracking-param stripping:
  - Lowercase the host.
  - Strip default ports (`:443`, `:80`).
  - Drop the URL fragment, unless the site is a known SPA that encodes
    meaningful route state in the fragment.
  - Sort remaining query parameters alphabetically for a stable key.
  - Strip trailing slash.

---

## 9. Data Model

### Postgres (backend-owned — canonical archive)

`BIGINT GENERATED ALWAYS AS IDENTITY` primary keys are used throughout
(rather than UUIDs) for smaller indexes and better insert/join performance at
this project's scale — appropriate for a single/family-scale personal
archive rather than a large multi-tenant system.

```sql
CREATE TABLE users (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

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
  raw_url TEXT NOT NULL,
  title TEXT,
  html_path TEXT NOT NULL,           -- local disk path, zstd-compressed
  html_size_bytes INTEGER NOT NULL,
  thumbnail_path TEXT,
  reader_text TEXT,                  -- Readability plain-text extraction
  content_hash TEXT NOT NULL,        -- powers "unchanged since last capture"
  captured_at TIMESTAMPTZ NOT NULL,
  backed_up_at TIMESTAMPTZ,          -- null until S3-compatible backup runs
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
-- than a closure table: simpler writes, and at this project's scale
-- (dozens/hundreds of collections, not thousands) a recursive CTE for
-- "this collection and all descendants" is fast enough that a closure
-- table's extra write-complexity isn't justified.
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
-- *membership* rows here — the pages themselves are untouched and simply
-- have no page_collections row, which is treated as "Unsorted" in the UI.
-- There is no dedicated "Unsorted" collection row; absence of membership
-- rows IS the Unsorted state.
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
  summary TEXT,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);
```

### D1 (Worker-owned — auth, queue, bookmark mirror only)

`queue_items` and `pending_captures` use client-generated UUIDs rather than
server-generated identity columns because the extension generates the ID
before the row exists server-side, for idempotency on retry (e.g. if an
upload-complete notification is retried after a network blip, it doesn't
create a duplicate row). Volume of in-flight items is low, so this has no
meaningful downside.

```sql
-- Mirrors Postgres users.id and password_hash for auth without ever
-- exposing the backend.
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash TEXT NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id),
  device_name TEXT NOT NULL,
  device_type TEXT NOT NULL,        -- 'extension' | 'pwa' | 'cli'
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TIMESTAMP
);

-- URLs waiting to be archived by the desktop extension
CREATE TABLE queue_items (
  id TEXT PRIMARY KEY,              -- client-generated UUID
  user_id INTEGER NOT NULL REFERENCES users(id),
  url TEXT NOT NULL,
  added_by_token_id INTEGER REFERENCES tokens(id),
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | claimed | captured | failed
  claimed_by_token_id INTEGER REFERENCES tokens(id),
  claimed_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Completed captures awaiting backend pickup from R2
CREATE TABLE pending_captures (
  id TEXT PRIMARY KEY,              -- client-generated UUID
  user_id INTEGER NOT NULL REFERENCES users(id),
  queue_item_id TEXT REFERENCES queue_items(id),  -- null for direct captures
  url TEXT NOT NULL,
  r2_key_html TEXT NOT NULL,
  r2_key_thumbnail TEXT,
  r2_key_readable TEXT,
  captured_at TIMESTAMP NOT NULL,
  fetched_by_backend BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Bookmark-list mirror, pushed by the backend after each capture is
-- processed. Pulled by the extension on its own daily/on-demand schedule.
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

`pending_captures.queue_item_id` is nullable specifically to support
**direct captures** — archiving a page the user is already on, which was
never queued in the first place.

---

## 10. Components Summary

| Component | Tech | Reachability required | Responsibility |
|---|---|---|---|
| Desktop browser extension | WebExtensions (Chrome/Firefox compatible) | Worker + R2 only | Poll queue, capture (HTML/reader text/thumbnail), upload to R2 |
| Share-sheet PWA | Static site, Cloudflare Pages | Worker only | Android share-target: enqueue a URL, nothing else |
| iOS Shortcut | Apple Shortcuts | Worker only | Enqueue a URL from iOS share sheet |
| CLI | Small script/binary | Worker only | Enqueue URLs, scriptable |
| Cloudflare Worker | TypeScript/JS on Workers | Public | Device auth (checks D1 credential mirror), issues bearer tokens, presigned R2 URLs, D1 read/write — intentionally kept as simple/dumb as possible |
| D1 | Cloudflare D1 (SQLite) | N/A (accessed via Worker only) | Tokens, queue, bookmark-list mirror |
| R2 | Cloudflare R2 | N/A (accessed via presigned URLs) | Temporary blob storage between capture and backend pickup |
| Backend | Go + Postgres, Docker Compose | Outbound-only required; inbound optional (dashboard) | Pull from R2, compress, store, version, search, tags, collections, AI enrichment, backup job, dashboard API |
| Dashboard | Svelte | Same as backend | Library browsing, search, reader view, version history, tags, collections |

---

## 11. Deployment

- **Backend**: Docker Compose, bundling the Go backend and Postgres as
  services with a persistent volume for compressed archives and the
  database.
- **Cloudflare side**: Terraform/OpenTofu module in the public repo,
  provisioning D1, R2, the Worker (and its routes/bindings), and the
  Cloudflare Pages project for the share-sheet PWA. Worker code lives
  alongside the module in the repo.
- **Networking**: the repo takes no position on how the backend/dashboard
  is exposed beyond the local machine — that's a deployment-time decision
  left to the operator (LAN-only, reverse proxy, VPN, tunnel, etc.). The
  core archiving flow (extension/PWA/CLI → Worker → R2 → backend polling)
  works identically regardless of that choice, since it never depends on
  backend reachability.

---

## 12. Repository Layout

Monorepo, structured flat by "what a thing is" rather than by architectural
layer. Components only get their own directory when they genuinely need
isolation (their own build tooling, dependency manifest, or — in the
Worker/PWA's case — a hard requirement of having **no** build step at all).

```
recueil/
├── cmd/
│   ├── server/            # main API/dashboard-serving binary
│   └── cli/                # recueil-cli, shares the same go.mod
├── internal/
├── migrations/
├── src/                     # Svelte dashboard source
├── Dockerfile
├── go.mod
├── package.json             # dashboard's Vite/Svelte package.json
├── vite.config.js
│
├── extension/                # WebExtension, own package.json (needs bundling
│   ├── src/                    # to pull in Readability.js as a dependency)
│   ├── manifest.json
│   └── package.json
│
├── terraform/                  # OpenTofu module
│   ├── main.tf
│   ├── variables.tf
│   ├── outputs.tf
│   ├── worker/                   # plain JS, no build step — deployed as-is
│   │   └── index.js
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
├── docker-compose.yml
├── README.md
└── LICENSE
```

### Notes on specific decisions

- **Go lives directly at repo root**, not under a `backend/` wrapper.
  `go build ./...` ignores non-Go directories entirely, so `cmd/`,
  `internal/`, `migrations/`, and `go.mod` coexist at root with no conflict
  against the JS/Terraform/Zola content living alongside them. The module
  path in `go.mod` (e.g. `github.com/<org>/recueil`) needs no subdirectory
  suffix now that it isn't nested.
- **CLI stays inside the same Go module as the server** (`cmd/cli` alongside
  `cmd/server`), sharing `internal/` — same reasoning as before: it needs the
  same DB models and auth logic, and splitting it into a separate module
  would mean duplicating code for no benefit.
- **The Svelte dashboard's source lives in root `src/`, not its own
  directory**, because its build output is embedded directly into the Go
  binary via `go:embed` — the deployed artifact is a single Go executable
  serving both the API and the dashboard UI, with no separate static-file
  hosting concern for self-hosters. This does mean the repo root
  simultaneously acts as a Go module root and an npm package root
  (`package.json`, `vite.config.js` at root); the two toolchains don't
  interfere, since each ignores files irrelevant to it. **Build order
  matters and isn't obvious from the file layout alone**: `pnpm build` must
  run before `go build`, so the repo should include a Makefile target (or
  equivalent) that sequences this correctly rather than relying on
  contributors to remember it.
- **The Worker and PWA live inside `terraform/`, not alongside the other JS,
  and are deliberately plain JavaScript with no build step.** This is a
  hard requirement, not a preference: the Terraform module deploys these
  files as-is (`terraform/worker/index.js`, `terraform/pwa/`), and if they
  required a build step, the Terraform module would only work for people who
  ran that build first — breaking "clone and `terraform apply`" as a valid
  path to using the module. Keeping them un-bundled, dependency-free, and
  physically next to the `.tf` files that reference them keeps the module
  genuinely standalone.
- **The extension gets its own directory** rather than sharing root `src/`
  with the dashboard. Unlike the Worker/PWA, it does need a real bundler
  (to pull in Readability.js as a dependency), so it isn't a "no build step"
  component — but it's also functionally unrelated to the dashboard, and a
  generic root `src/` shared between a Go project and two unrelated frontend
  concerns would be ambiguous. Its own `package.json` keeps its dependency
  list isolated from the dashboard's.
- **`website/` (Zola) is fully self-contained**, with Zola's own
  conventional layout (`config.toml`, `content/`, `templates/`, `static/`),
  and has no interaction with any other component.

### Toolchain specifics

- **pnpm workspace** now covers two members: the dashboard (root
  `package.json`) and the extension. The root of the repo itself must be
  declared as a workspace member (not just `extension`), since the
  dashboard's `package.json` lives at root alongside `go.mod`:
  ```yaml
  packages:
    - "."
    - "extension"
  ```
  This still provides the same benefits as a larger workspace would: a
  single `pnpm install` from root covers both packages, shared tooling
  devDependencies (linting/formatting) can be hoisted if desired, and each
  package independently resolves its own dependency versions via pnpm's
  content-addressable store with no forced version alignment between them.
- **The Worker and PWA are intentionally outside the pnpm workspace
  entirely** — they have no `package.json` and no dependencies to manage,
  consistent with their no-build-step requirement.
- **`backend/go.mod`'s module path** (now just `go.mod` at root) does not
  need to encode subdirectory location — Go resolves modules by walking up
  from the file being built to the nearest `go.mod`, and since it now sits
  at the actual repo root, this is simpler than the previous nested layout:
  the module path can directly match the repo's URL with no suffix.

## 13. Cost & Free Tier Fit

At personal/family scale, this architecture is expected to run almost
entirely within Cloudflare's free tier. The relevant free-tier limits
(verified June 2026):

| Service | Free tier limit |
|---|---|
| Workers | 100K requests/day, 10ms CPU time/invocation, 50 subrequests/invocation |
| D1 | 5GB storage, 5M rows read/day, 100K rows written/day |
| R2 | 10GB-month storage, 1M Class A ops/month (writes), 10M Class B ops/month (reads), zero egress fees |
| Pages | Unlimited bandwidth, free |

### Why the design fits comfortably

- **Request volume is low relative to limits.** Personal/family-scale usage
  (dozens of captures a day, periodic queue polling from the desktop
  extension, occasional enqueues from phone/CLI) is far below 100K Worker
  requests/day or 5M D1 reads/day — even polling once a minute from a device
  is only ~1,440 requests/day.
- **R2 never accumulates storage.** This is a direct consequence of the
  decision in §4 to treat R2 as a temporary buffer only, deleted immediately
  after the backend pulls each capture. R2 usage at any point in time is
  bounded by whatever's currently in flight between capture and the next
  backend poll — not by the size of the archive as a whole. Without that
  design choice, R2 storage would grow unbounded and eventually exceed the
  10GB free allowance.
- **D1 storage stays small by nature.** Queue rows, tokens, and the
  bookmark-list mirror (title + URL + timestamps, no thumbnails per §7) are
  all small text rows. Even an archive numbering in the thousands of pages is
  a rounding error against the 5GB D1 limit.
- **R2 Class B (read) operations are negligible.** The backend performs one
  read per capture during pickup — nowhere near the 10M/month allowance.

### Where care is still warranted

- **Worker CPU time (10ms/invocation)** is the tightest limit relative to
  what a single request might do. A Worker endpoint that chains an auth
  lookup, a D1 write, *and* R2 presigned-URL generation in one invocation
  could approach the ceiling. This reinforces the "dumb Worker" principle
  already established in §2 and §5: keep each Worker endpoint to as close to
  a single operation as possible (e.g., one D1 query, or one presigned-URL
  generation) rather than composing multiple operations into one request.
- If usage ever grows meaningfully beyond personal/family scale (e.g., a
  future hosted multi-tenant version), the Workers Paid plan ($5/month
  minimum) removes the daily request cap and raises CPU time substantially —
  but this is not expected to be necessary for the system as designed.

## 14. Open Questions / Future Decisions

- Should AI-sourced tags (`page_tags.source = 'ai'`) be visually
  distinguished from manual tags in the dashboard, or shown identically
  once applied?
- A future "manage devices" dashboard screen (list paired devices, last
  used, revoke) is enabled by data already in the schema (`tokens.last_used_at`)
  but not yet designed.
- Terraform/OpenTofu module layout, Worker API endpoint surface, and overall
  repo structure (monorepo vs. split repos) are the logical next design
  steps after this document.
