# Recueil — Design Document (v2)

> **v2 changelog:** dashboard auth switched to direct session-based auth against
> Postgres; added user roles and admin bootstrap; added capture idempotency key;
> content-hash now covers both raw HTML and reader text; backup responsibility
> explicitly punted to the operator (documented, not automated); defined
> backend↔Worker service authentication; added queue visibility timeout; added
> AI job retry/backoff; moved thumbnail generation from the extension to a new
> backend-side headless-Chrome screenshot service; clarified SingleFile
> integration as a vendored library, not a separate installed extension;
> acknowledged D1 mirroring risk explicitly; settled polling cadence.

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

Note that this principle applies to the *initial capture* only. Once a page
has been captured as a fully inlined HTML file, deriving further artifacts
from that already-captured file (e.g. a thumbnail — see §6) is a different,
safe operation: it's rendering static, already-authenticated content offline,
not re-fetching a live page.

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
        │  - pushes password-hash mirror to D1 ────────────────┘
        │    (via Worker, on account creation/password change)
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

The desktop extension, the share-sheet PWA, and the CLI depend **only** on
the Worker and R2 — both public, both authenticated via bearer token. None of
them ever need the backend to be network-reachable. The backend's only
required connectivity is **outbound**: polling the Worker/D1 API, pulling
objects from R2, and (per §5) making occasional authenticated calls to the
Worker for device-token revocation. It can run with zero inbound firewall
rules and the entire archiving loop still works end to end.

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
3. The desktop extension polls D1 (via the Worker) for pending queue items,
   on an infrequent schedule (see §7), and can notify the user something
   needs archiving. The extension also exposes a manual "check now" action
   in its popup for on-demand polling.
4. User selects a queued item (or a page they're currently on, for direct/
   unqueued capture) and triggers capture.
5. Extension captures:
   - Full inlined single-page HTML, via SingleFile's own capture code
     **vendored directly into the extension as a library** (see §3a) —
     not by messaging a separately installed SingleFile extension.
   - Readability.js-extracted plain text (run **in the extension**, against
     the live DOM, before any re-archival loses render-time state)
6. Extension requests a presigned R2 upload URL from the Worker, uploads both
   artifacts (HTML + reader text) directly to R2 (bypassing Worker body-size
   limits; presigned R2 PUT supports objects far larger than any archived
   page will ever be).
7. Extension notifies the Worker that the upload is complete → Worker writes
   a `pending_captures` row to D1, using a **client-generated UUID** as the
   row's id (and marks the `queue_items` row, if any, as `captured`).
8. Backend, on its own polling schedule, discovers the new `pending_captures`
   row, pulls the blobs from R2, zstd-compresses the HTML, stores it on local
   disk, computes content hashes (see §3b), deletes the R2 objects, writes
   rows to Postgres (idempotently — see §3c), and finally pushes a lightweight
   mirror row back to D1 for the bookmark-list feature (see §8).
9. Backend enqueues a **screenshot job** (async, decoupled — see §6).
10. (Optional, async) Backend enqueues an AI job to summarize/tag the capture
    using the Readability text (see §7).
11. Backup of the resulting Postgres data and local archive directory is the
    operator's own responsibility (see §14) — not part of this pipeline.

### 3a. SingleFile integration

SingleFile is not invoked as a separate, independently installed browser
extension via cross-extension messaging — that path isn't well-supported
(SingleFile is designed to be user-triggered via its own toolbar button, and
there's no first-class API for a third-party extension to invoke it and get
the result back programmatically).

Instead, SingleFile publishes its own capture logic as embeddable script
files intended for exactly this kind of reuse. The Recueil extension vendors
these files directly (e.g. `single-file-background.js`, plus a WebExtension
polyfill) and calls `extension.getPageData(...)` from its own content script
to get back `{ content, title, filename }`. This is "use SingleFile as a
library within our own extension," not "talk to a second installed
extension" — it avoids any dependency on a stable cross-browser extension ID,
`externally_connectable` support, or requiring the user to separately install
SingleFile at all.

The extension's own `package.json`/bundler setup (already needed to pull in
Readability.js) is the natural place to also vendor SingleFile's capture
code.

### 3b. Content hashing

Each capture stores **two** hashes:

- `content_hash` — over the full inlined HTML. Useful for exact
  byte-for-byte dedup detection.
- `reader_text_hash` — over the Readability-extracted plain text. This is
  the hash that drives the dashboard's "unchanged since last capture" flag.

The full-HTML hash is a poor signal for "did the visible content change" —
most real pages embed per-load-unique content (CSRF tokens, cache-busted
asset URLs, session IDs, timestamps) even when nothing meaningful changed, so
it will almost never repeat in practice. The reader-text hash is a much more
reliable (though still imperfect — Readability output can shift for reasons
unrelated to the main content) signal for that specific UI feature.

### 3c. Capture idempotency (crash recovery)

The `pending_captures.id` (a client-generated UUID, already required for
retry-safety on the upload-complete notification — see §8) doubles as an
idempotency key for backend ingestion:

```sql
ALTER TABLE captures ADD COLUMN source_capture_id TEXT UNIQUE;
```

Ingestion becomes: write the blob to disk, then `INSERT ... ON CONFLICT
(source_capture_id) DO NOTHING` into `captures`. If the row already exists
(a retry after a crash), skip straight to R2 cleanup and the D1
`fetched_by_backend` flag update. Ordering the steps this way — disk write,
then DB commit, then R2 delete, then D1 flag — means a crash at any point
either leaves the R2 object in place for a safe retry, or leaves only
harmless orphaned cleanup state; nothing can double-insert a capture.

### Re-archiving the same URL

Re-archiving a previously captured URL is **not** an update — it's a new
version under the same logical page. The backend groups captures by
`normalized_url` (see §9, URL normalization) into a `pages` row, and each
individual capture becomes a new `captures` row linked to that page. The
dashboard shows all historical versions of a page with their capture
timestamps.

---

## 4. Storage Strategy

- **R2 is temporary only.** It exists purely to get large payloads from the
  extension (which may not have a stable public endpoint to push to) to the
  backend (which may not be reachable to receive a push). Once the backend
  has pulled and locally stored a capture's blobs, they are deleted from R2.
- **Local disk is canonical.** The backend stores the zstd-compressed HTML
  (HTML compresses extremely well with zstd, commonly 80-90% size reduction)
  on local disk, referenced by path from the `captures` table. Thumbnails
  (see §6) are also stored on local disk, never in R2.
- **Backup is entirely the operator's responsibility** — see §14. The
  application itself performs no automated backup.
- **Database choice: Postgres, not SQLite**, despite this being a personal
  archive. Real user accounts (family members using one deployment, and a
  potential future multi-tenant hosted version) tip this in Postgres's
  favor: SQLite's single-writer lock becomes a real constraint with
  concurrent family members archiving/querying at once, and multi-tenant
  isolation / hosted-DB migration paths are native to Postgres. Docker
  Compose makes the extra container a non-issue operationally.
- **Bind mounts, not named Docker volumes**, for both the Postgres data
  directory and the local archive directory (see §14) — this makes it
  straightforward for whatever external backup tool the operator chooses to
  snapshot the directories directly from the host filesystem.

---

## 5. Authentication

### Requirements driving the design

- The backend must never need to be publicly reachable for the core
  archiving flow to work.
- Multiple devices (desktop extension, phone shortcut, CLI, PWA) need
  independent, individually revocable credentials.
- The **dashboard** is a separate case: it's only ever accessed over
  whatever network the operator has chosen to expose it on (LAN/VPN/tunnel),
  so it doesn't need to satisfy the "backend stays fully private" constraint
  the device-capture path does.
- Real user accounts are wanted (to support family members on one shared
  deployment, and to keep the door open for a future hosted/paid version).

### Two separate authentication mechanisms

**Devices (extension, PWA, CLI) → opaque bearer tokens, D1-owned.** This is
unchanged from the original design:

- Postgres `users` is the source of truth for accounts. On account creation
  or password change, the backend pushes the **hash only** to a mirrored
  `users` table in D1. The Worker checks the D1 mirror directly and never
  talks to the backend to authenticate a device — this is what lets the
  backend stay fully non-public.
- Device pairing: a device authenticates once via username/password (or a
  pairing code) against the Worker, which checks the D1 mirror and issues an
  **opaque bearer token** — a 32-byte CSPRNG-generated random value,
  base64url-encoded with a human-recognizable prefix (e.g. `rcl_live_...`).
  A JWT would add signing/claims-schema complexity for no benefit here,
  since a DB lookup already happens on every request (to support
  per-device revocation) and request volume is low.
- **Revocation:** every issued token is stored (hashed, `SHA-256(token)`) so
  revocation is just deleting a row — simpler at this request volume than a
  refresh-token rotation scheme. Plain SHA-256 (not password-style hashing)
  is appropriate because the token already has ~256 bits of entropy; a
  leaked hash alone doesn't let an attacker reconstruct a usable token.
- D1 remains the **sole** owner of device tokens — there is no copy of this
  table in Postgres.

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

**Dashboard → direct session auth against Postgres.** The dashboard is a
normal web app: it authenticates by checking `username`/`password_hash`
directly in Postgres and issues its own session (cookie-based), with no
bearer token, and no involvement of D1 or the Worker at all. This is simpler
than reusing the device-token mechanism and avoids needing a `tokens` table
in Postgres — the earlier design's ambiguity about "does the backend keep
its own copy of tokens" is resolved by not needing one.

### Revoking a device token from the dashboard

Because D1 is the sole owner of device tokens, a future "manage devices"
dashboard screen (list paired devices, revoke one) works by having the
**backend make an outbound, authenticated call to the Worker** ("delete
token row X") using the backend↔Worker service credential (see §5a) — never
the reverse. This is consistent with the backend remaining outbound-only.

### 5a. Backend ↔ Worker service authentication

The backend itself is a distinct, higher-privilege actor from any single
user's device — it polls for pending captures and pushes mirror rows across
*all* users in a deployment, and (per above) needs to issue revoke calls.
This needs its own credential, separate from the per-device token system.

**Decision: a static shared secret.**

- Generated via Terraform's `random_password` resource at `terraform apply`
  time, output with `sensitive = true` so it doesn't leak into plaintext
  state/CI output.
- Injected into the Worker as an environment binding/secret.
- The operator copies it from `terraform output` into the backend's `.env`
  after apply.
- Checked by the Worker as a header (e.g. `X-Service-Key`) on the small set
  of backend-only endpoints (poll `pending_captures`, push credential/
  bookmark mirror rows, revoke a device token).
- Rotation = regenerate + redeploy, which is acceptable at this operational
  scale (single backend per deployment, infrequent rotation).

Alternatives considered and rejected:
- Reusing the `tokens` table with a "service" row — doesn't fit, since
  `tokens.user_id` is scoped to one user and the backend needs cross-user
  access.
- mTLS or Cloudflare Access service tokens — real options, but add
  meaningfully more operational complexity (cert management, or an
  additional Cloudflare product dependency) for no real benefit at this
  scale.

### Account creation and roles

- **Admin-creates-users**, not open signup, for the initial version — fits
  the family/self-hosted use case and avoids needing email-verification
  infrastructure. Open signup (for a future hosted/SaaS mode) is expected to
  be a straightforward addition later, not a rearchitecture.
- **Bootstrap:** on first dashboard access with zero existing users, the
  dashboard prompts to create the first admin account.
- **Roles:** `admin` and `member`. Add to the `users` schema:
  ```sql
  ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'member';
  -- values: 'admin' | 'member'
  ```
  Admins can create/manage other users; members manage only their own
  bookmarks/tags/collections. Role is purely a backend/dashboard
  authorization concern and is **not** included in the D1 mirror — D1 only
  needs enough to authenticate a device and identify its owning `user_id`,
  never authorization decisions.

### Security note: D1 as a mirror target

D1 isn't directly internet-addressable on its own, but "not publicly
accessible" doesn't mean zero risk:

- The Worker itself is public, so a bug in its auth-check logic is a path to
  the D1-mirrored credentials. The Worker is kept intentionally minimal to
  limit this surface, but it isn't literally zero.
- Cloudflare, as the D1 host, has access to the data at rest — using any
  managed cloud service extends the trust boundary to that provider, which
  is a standard tradeoff of this architecture and not unique to Recueil.
- The practical residual risk is low: bearer-token hashes are SHA-256 of
  256-bit random values, so a leak alone doesn't yield a usable token.
  Password hashes (bcrypt/argon2, human-chosen, weaker entropy) are the more
  sensitive item in the mirror, which is exactly why they use a proper
  slow/salted hash rather than SHA-256.

This tradeoff is accepted as part of the design and should be stated plainly
in the repo's security documentation rather than left implicit.

---

## 6. Screenshot / Thumbnail Generation

**Moved from the extension to the backend.** The extension no longer
captures a screenshot at all — it uploads only HTML and reader text (see
§3). Thumbnail generation now happens as an async backend job, after a
capture's HTML has already been pulled from R2 and stored locally.

### Why this is safe, unlike a general "fetch and archive" fallback

The core design principle in §1 forbids the backend from ever fetching a
*live* URL — that's the CAPTCHA/paywall/auth problem the whole system exists
to avoid. Rendering the **already-captured, fully inlined SingleFile HTML**
is a different operation: no network requests, no live authentication
state, no CAPTCHA, and (since SingleFile strips scripts) no live JS
execution. It's an offline, sandboxed render of a static document that's
already been through the "real browser tab" capture path.

### Design

- **`chromedp`** (Go, drives Chrome/Chromium over the DevTools Protocol) —
  fits the existing Go backend without adding a Node dependency.
- Runs as a **separate sidecar container** in Docker Compose, using the
  `chromedp/headless-shell` image — a small, purpose-built headless Chrome
  build maintained specifically for this use case. Kept as its own service
  (not bundled into the backend image) so Chromium's dependency footprint
  and per-instance memory cost don't bloat the core backend image, and so it
  can be updated/pinned independently.
- The backend keeps a **long-running connection** to the headless-shell
  instance and opens a new tab per screenshot job, rather than cold-starting
  a browser process per capture (which adds ~1-3s of avoidable latency
  each time).
- **Bounded concurrency** — a small worker pool (e.g. 2-3 concurrent tabs),
  appropriate for modest self-hosted hardware.
- The HTML is served to the headless browser via `file://` or a brief
  ephemeral local HTTP server; since SingleFile inlines all resources as
  data URIs, there are no external resource loads to worry about either way.
- **Fully async and non-blocking**, matching the `ai_jobs` pattern (see
  §7): a capture is fully valid and browsable with no thumbnail, and a
  failed/slow screenshot never invalidates the capture. `captures.thumbnail_path`
  remains nullable. Bounded retry with backoff, same shape as §7.

### Consequence for the schema

Because the screenshot is no longer produced client-side and never touches
R2:
- `r2_key_thumbnail` is **removed** from the D1 `pending_captures` table.
- The extension only needs to request presigned URLs for two objects (HTML,
  reader text), not three.

### Tradeoff, stated explicitly

This adds a real piece of self-hosting weight — an extra container, its
memory overhead, and one more moving part — compared to the originally
proposed `chrome.tabs.captureVisibleTab` approach in the extension. In
exchange it removes the extension's dependency on the user's current
scroll/viewport state entirely and produces consistent, full-page-quality
thumbnails server-side. Given Compose already orchestrates Postgres, this
was judged worth the added weight.

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
- Runs against the Readability-extracted plain text, not the raw HTML —
  cheaper and produces better summaries than trying to parse rendered HTML.
- Supports **two backend types**, chosen by the user in configuration:
  Ollama-compatible (local) or OpenAI-compatible (hosted). A single small
  interface (`Summarize(text) (summary, tags, error)`) covers both.
- Tracked in its own `ai_jobs` table, one row per capture, decoupled from the
  `captures` table so enrichment status/failure never affects the capture's
  core validity.
- AI-generated tags are written to the same `page_tags` table as manual tags,
  distinguished by a `source` column (see §9).

### Retry and failure handling

```sql
ALTER TABLE ai_jobs ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ai_jobs ADD COLUMN next_attempt_at TIMESTAMPTZ;
```

On failure: increment `attempts`; if under a small max (e.g. 3), set
`status` back to `pending` with `next_attempt_at` pushed out (simple
exponential backoff); once attempts are exhausted, mark `status = 'failed'`
permanently with `error` preserved. The dashboard surfaces failed jobs as a
small badge on the capture with a manual retry action — no dead-letter queue
is needed given this is optional and low-stakes; the failed row itself
serves that purpose.

The same `attempts`/`next_attempt_at`/bounded-retry shape is reused for the
screenshot job in §6.

---

## 8. Cross-Device Queue and Bookmark List

### Queue (phone → desktop archiving)

- Adding a URL from a phone (via Shortcut, PWA, or CLI) only **enqueues** it
  — it does not attempt to archive anything server-side. The intended
  workflow is deliberately: queue remotely, archive later from the desktop
  extension, where a real rendered/authenticated browser session exists.
- The desktop extension polls the queue via the Worker/D1 (see §7 polling
  cadence in the original numbering — now consolidated below) and can
  notify the user that items are waiting.
- Claiming is done with a conditional update (`WHERE status = 'pending'`) to
  prevent two devices from grabbing the same item simultaneously; a claimed
  item records which device claimed it and when.

### Queue visibility timeout

A claimed item can get stuck if the claiming device dies mid-capture or the
tab is closed. Rather than a separate scheduled sweep job, this is handled
as **lazy reclaim folded into the existing claim query**:

```sql
WHERE status = 'pending'
   OR (status = 'claimed' AND claimed_at < now() - interval '15 minutes')
```

15 minutes is comfortably more than enough time to pull 2-3 blobs from R2
and write a DB record, and this avoids needing a Cron Trigger or any
additional scheduled infrastructure, consistent with the "dumb Worker"
philosophy.

### Bookmark-list mirror (extension as a browsable list)

- Separately from the queue, the extension can act as a lightweight bookmark
  list of everything already archived — similar to a browser's native
  bookmarks UI: just title + URL, no thumbnails.
- This is a **one-way, backend → D1 push**: after the backend finishes
  processing a capture, it upserts a row into a D1 `archived_pages` table —
  the mirror-image of the credential mirror (backend → D1, rather than
  D1 → backend), keeping the same principle: the extension only ever needs
  to talk to the Worker/D1, never the backend.
- The extension does **not** live-sync this list. It caches the list locally
  and refreshes on a coarse schedule (see §7 polling cadence below) or on
  explicit user request, using an incremental "give me changes since X"
  query against `archived_pages.updated_at`.
- Because this list is just title + URL, no thumbnail storage is needed in
  R2 or D1 for this feature.

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

Settled as **infrequent background polling with on-demand override**,
rather than tight polling or any push mechanism:

- Extension → queue (D1 via Worker): every 5-15 minutes in the background,
  plus a manual "check now" button in the extension popup for on-demand
  polling.
- Extension → bookmark-list mirror refresh: coarse (e.g. once per day) or on
  explicit user request.
- Backend → `pending_captures` (D1 via Worker): every few minutes. No
  on-demand path is needed here since nothing is synchronously waiting on
  it.

No WebSocket/push infrastructure (e.g. a Durable Object) is used — that
would be real added infrastructure for a problem infrequent polling plus a
manual refresh button already solves adequately at this scale. (The "once a
minute" figure used in the original §13 cost analysis was illustrative
headroom math, not a spec.)

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

`BIGINT GENERATED ALWAYS AS IDENTITY` primary keys are used throughout
(rather than UUIDs) for smaller indexes and better insert/join performance at
this project's scale.

```sql
CREATE TABLE users (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'member',   -- 'admin' | 'member'
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
  source_capture_id TEXT UNIQUE,     -- pending_captures.id (D1), for
                                      -- ingestion idempotency (see §3c)
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

There is **no `tokens` table in Postgres** — device tokens are owned
entirely by D1 (see §5), and the dashboard uses its own session mechanism
against `users` directly, so no bearer-token table is needed on the backend
side at all.

### D1 (Worker-owned — auth, queue, bookmark mirror only)

`queue_items` and `pending_captures` use client-generated UUIDs rather than
server-generated identity columns, for idempotency on retry (see §3c) and
because the extension generates the ID before the row exists server-side.

```sql
-- Mirrors Postgres users.id and password_hash for device auth without
-- ever exposing the backend. Does NOT include `role` — authorization is a
-- backend/dashboard concern only.
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

`pending_captures.queue_item_id` is nullable specifically to support
**direct captures** — archiving a page the user is already on, which was
never queued in the first place.

Backend↔Worker service calls (polling, mirror pushes, token revocation) are
authenticated via the shared service secret (§5a), not a row in `tokens`.

---

## 11. Components Summary

| Component | Tech | Reachability required | Responsibility |
|---|---|---|---|
| Desktop browser extension | WebExtensions (Chrome/Firefox compatible) | Worker + R2 only | Poll queue, capture (HTML via vendored SingleFile, reader text), upload to R2 |
| Share-sheet PWA | Static site, Cloudflare Pages | Worker only | Android share-target: enqueue a URL, nothing else |
| iOS Shortcut | Apple Shortcuts | Worker only | Enqueue a URL from iOS share sheet |
| CLI | Small script/binary | Worker only | Enqueue URLs, scriptable |
| Cloudflare Worker | TypeScript/JS on Workers | Public | Device auth (checks D1 credential mirror), issues bearer tokens, presigned R2 URLs, D1 read/write, service-secret-gated backend endpoints |
| D1 | Cloudflare D1 (SQLite) | N/A (accessed via Worker only) | Device tokens, queue, bookmark-list mirror |
| R2 | Cloudflare R2 | N/A (accessed via presigned URLs) | Temporary blob storage between capture and backend pickup |
| Backend | Go + Postgres, Docker Compose | Outbound-only for archiving; inbound optional (dashboard, LAN/VPN) | Pull from R2, compress, store, version, search, tags, collections, AI enrichment, dashboard session auth, dashboard API |
| Screenshot service | chromedp + `chromedp/headless-shell`, Docker | Backend-internal only (no inbound, no outbound) | Renders already-captured inlined HTML offline, produces thumbnails |
| Dashboard | Svelte | Same as backend | Library browsing, search, reader view, version history, tags, collections, user/session management |

---

## 12. Deployment

- **Backend**: Docker Compose, bundling the Go backend, Postgres, and the
  new headless-Chrome screenshot sidecar as services. Postgres's data
  directory and the local archive directory both use **bind mounts** (not
  named volumes) so an external backup tool can snapshot them directly from
  the host (see §14).
- **Cloudflare side**: Terraform/OpenTofu module in the public repo,
  provisioning D1, R2, the Worker (and its routes/bindings), the Cloudflare
  Pages project for the share-sheet PWA, and a `random_password` resource
  for the backend↔Worker service secret (output as `sensitive`, to be
  copied into the backend's `.env` after `terraform apply`).
- **Networking**: the repo takes no position on how the backend/dashboard
  is exposed beyond the local machine — that's a deployment-time decision
  left to the operator (LAN-only, reverse proxy, VPN, tunnel, etc.). The
  core archiving flow (extension/PWA/CLI → Worker → R2 → backend polling)
  works identically regardless of that choice, since it never depends on
  backend reachability.

---

## 13. Repository Layout

Monorepo, structured flat by "what a thing is" rather than by architectural
layer. Components only get their own directory when they genuinely need
isolation (their own build tooling, dependency manifest, or — in the
Worker/PWA's case — a hard requirement of having **no** build step at all).
The screenshot service does not add a new top-level directory: it's driven
by Go code in the existing backend module (via `chromedp`, connecting to the
sidecar container over the network) plus a new service definition in
`docker-compose.yml`.

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
│   ├── src/                    # to pull in Readability.js and vendored
│   ├── manifest.json            # SingleFile capture code as dependencies)
│   └── package.json
│
├── terraform/                  # OpenTofu module
│   ├── main.tf                   # includes random_password for the
│   ├── variables.tf                # backend↔Worker service secret
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
├── docker-compose.yml            # backend + postgres + screenshot sidecar
├── README.md                      # includes backup guidance, see §14
└── LICENSE
```

### Notes on specific decisions

(Unchanged from v1 — see original rationale for Go-at-root, CLI sharing the
server's Go module, the dashboard's build output being embedded via
`go:embed`, the Worker/PWA's no-build-step requirement, the extension's own
directory/bundler, and `website/`'s self-containment.)

---

## 14. Backup & Restore

**The application performs no automated backup.** This is a deliberate
choice: baking `pg_dump` (or equivalent) into the backend's own image or
shelling out to it from the Go binary is an awkward dependency for an
application binary to carry, and commits the project to tracking Postgres
version compatibility indefinitely. Instead, backup is documented as the
operator's responsibility.

### What must be backed up

Two things, together, on the same schedule:

1. **The Postgres database** — via `pg_dump` or equivalent. This is the
   irreplaceable half: page groupings, tags, collections, version history,
   accounts. Note that copying Postgres's raw data directory while the
   container is running is **not** safe without WAL-aware tooling — a
   proper dump (or a backup tool that understands Postgres's on-disk
   format) is required, not a raw file copy.
2. **The local archive directory** (zstd-compressed HTML + thumbnails) —
   a plain directory copy/sync is fine here, since these are static files
   once written.

Because both bind-mount to the host filesystem (§4, §12), any external
backup tool (rsync, restic, a `pg_dump | rclone` pipeline, etc.) can operate
on them directly. The README should include one example recipe as a
starting point, without the application running it itself.

**Consistency matters**: if the two are backed up on different schedules or
by different mechanisms, a restore can leave a `captures` row pointing at an
`html_path` that wasn't captured in the same backup window, or vice versa.
Both should be backed up in the same job/window.

### Restore

- The archive directory must be restored to the **same mount path** it was
  originally running at — `captures.html_path` values are not stored
  relative to a configurable root, so a different path layout on restore
  will break lookups.
- After restoring Postgres from a backup, the **D1 credential mirror can be
  stale** relative to the restored state (e.g. password changes or account
  creations made after the backup was taken won't be reflected, or deleted/
  changed accounts may still have valid mirrored credentials). A manual
  **resync command** (CLI or admin dashboard action) re-runs the existing
  credential-mirror-push logic as an idempotent bulk operation across all
  users, rather than only firing on the create/password-change event hook.
  This should be run after any Postgres restore.

---

## 15. Open Questions / Future Decisions

- Should AI-sourced tags (`page_tags.source = 'ai'`) be visually
  distinguished from manual tags in the dashboard, or shown identically
  once applied?
- The "manage devices" dashboard screen (list paired devices, last used,
  revoke) is enabled by data already in the schema (`tokens.last_used_at`
  in D1) and by the backend↔Worker service secret, but the UI itself isn't
  yet designed.
- Whether to harden first-admin bootstrap with a one-time setup token
  printed to backend logs (guards against the narrow race where the
  dashboard is briefly reachable before the operator locks down
  networking), versus the simpler "first user to reach the dashboard claims
  admin" — currently leaning toward the latter for simplicity, revisit if
  needed.
- Open signup (for a future hosted/SaaS mode) is expected to layer onto the
  existing `users`/`role` schema without rearchitecting it, but the actual
  design (invite flow vs. open registration, tenant isolation) is not yet
  specified.
- Terraform/OpenTofu module layout and Worker API endpoint surface are the
  logical next design steps after this document.
