# Recueil — Design Document

## 1. Overview

Recueil is a self-hosted personal web archiving tool designed to replace a
collection of separate archivers and the glue required to keep them together.

The previous setup had three main problems:

- Headless browsers cannot reliably capture pages that require an authenticated
  session, human interaction, CAPTCHA solving, or access behind a paywall.
- Multiple archivers produce overlapping formats such as WARC, PDF, screenshots,
  and MHTML, most of which are unnecessary.
- Combining several independent tools requires custom synchronization and
  maintenance.

### Core design principle

**Recueil captures webpages directly from a real, already-authenticated,
already-rendered browser tab rather than fetching the live URL from the server
with a headless browser.**

Recueil captures the page as it has actually been rendered and authenticated in
the user's browser, allowing it to archive content that depends on browser
sessions, authentication, cookies, client-side state, or other context that
would not be available to a server-side fetch.

This restriction applies to initial capture only. Once a page has been captured,
the backend may safely process the resulting static HTML offline. Server-side
headless Chrome is used for this post-capture processing and not for the capture
itself.

### Archive format

Each capture consists of:

- A fully inlined, SingleFile-style HTML document containing the page and its
  resources.
- A plain-text Readability extraction.
- A thumbnail.
- Optionally, a favicon captured from the browser.

The HTML is the canonical captured artifact. Readability text and thumbnails are
derived asynchronously by the backend.

Recueil deliberately does not store WARC, PDF, or MHTML copies.

---

## 2. Architecture

Recueil is split between a public Cloudflare-based capture relay and a private
backend that owns the archive.

```text
┌──────────────────┐     ┌───────────────────┐     ┌──────────────────┐
│  iOS Shortcut    │     │  Share-sheet PWA  │     │       CLI        │
│  (enqueue only)  │     │  (Cloudflare      │     │  (enqueue only)  │
│                  │     │   Pages, public)  │     │                  │
└────────┬─────────┘     └────────┬──────────┘     └─────────┬────────┘
         │                        │                          │
         └────────────────────────┼──────────────────────────┘
                                  ▼
                        ┌───────────────────────┐
                        │  Cloudflare Worker    │  (dumb relay + auth)
                        │  - device auth        │
                        │  - enqueue URL        │
                        │  - presigned R2 URLs  │
                        │  - D1 read/write      │
                        │  - service-secret-    │
                        │    gated backend API  │
                        └─────────┬─────────────┘
                                  │
                    ┌─────────────┼─────────────┐
                    ▼                           ▼
             ┌─────────────┐             ┌─────────────┐
             │     D1      │             │     R2      │
             │ (queue,     │             │ (temp blob  │
             │  device     │◄────┐       │  storage)   │
             │  tokens,    │     │       │             │
             │  bookmark   │     │       │             │
             │  mirror)    │     │       │             │
             └──────┬──────┘     │       └──────┬──────┘
                    │            │              │
                    │  poll      │              │  pull
                    ▼            │              ▼
        ┌──────────────────────────────────────────────┐
        │         Desktop Browser Extension            │
        │  - reads queue from D1 (via Worker)          │
        │  - user selects item → loads URL             │
        │  - captures HTML only (via SingleFile)       │
        │  - uploads to R2 via presigned URL           │
        └──────────────────────────────────────────────┘
                    │
                    │ (async, outbound-only polling)
                    ▼
        ┌───────────────────────────────────────────────────────────────┐
        │      Backend (Go + Postgres, Docker)                          │
        │  - polls Worker/D1 for pending captures                       │
        │  - pulls blobs from R2, then deletes from R2                  │
        │  - zstd-compresses HTML, stores locally                       │
        │  - enqueues async screenshot job (§6a)                        │
        │  - enqueues async readability extraction job (§6b)            │
        │  - runs optional AI enrichment (summary/tags),                │
        │    once reader_text exists (§7)                               │
        │  - pushes bookmark-list mirror row to D1                      │
        │    (via Worker, after each capture is processed)              │
        │  - pushes pairing-token-hash mirror to D1                     │
        │    (via Worker, on account creation/regeneration/revocation)  │
        │  - authenticates the dashboard directly (session              │
        │    auth against its own Postgres `users` table —              │
        │    no token/D1 involvement)                                   │
        │  - serves dashboard API (reachable on LAN/VPN/etc.,           │
        │    reachability is the operator's responsibility)             │
        └───────────────────────────────────────────────────────────────┘
                    │                               │
                    ▼                               ▼
        ┌──────────────────────────────┐   ┌──────────────────────────────┐
        │  Headless-Chrome             │   │   Svelte Dashboard           │
        │  sidecar (chromedp +         │   │  - library browsing, search  │
        │  headless-shell              │   │  - version history per page  │
        │  container, driven           │   │  - tags (manual + AI),       │
        │  by the backend) —           │   │    nested collections        │
        │  produces both               │   │                              │
        │  thumbnails (§6a) and        │   │                              │
        │  Readability extractions     │   │                              │
        │  (§6b) from already-captured │   │                              │
        │  offline HTML                │   │                              │
        └──────────────────────────────┘   └──────────────────────────────┘
```

**Key architectural principle:** The public Cloudflare layer acts as a
lightweight relay between capture clients and the private backend. D1 stores
only transient state such as device credentials and the capture queue, while R2
provides temporary storage for uploads. The private backend is the canonical
system of record for archived content and metadata.

The backend requires only outbound network access. How users reach the backend
is a deployment concern outside Recueil's scope; it may be exposed over a LAN,
VPN, or another private network according to the needs and security requirements
of the instance administrator.

### Components

**Browser extension**

The primary capture client. It captures pages from the user's real browser
session and uploads the resulting HTML. It also handles the cross-device queue
and optional browser bookmark synchronization.

**Cloudflare Worker**

A small public relay between capture clients and the private backend. It handles
device authentication, queue operations, temporary R2 uploads, and the small
amount of data that must be available to clients without exposing the backend.

**D1**

Stores device credentials, the cross-device capture queue, and a lightweight
mirror of archived pages used by the extension.

D1 is not the canonical archive.

**R2**

Temporary storage for captured blobs while they wait for the private backend.
Objects are removed after successful ingestion.

**Backend**

The canonical application and archive. It owns users, captures, page history,
metadata, search, tags, collections, asynchronous processing, and the dashboard.

The backend does not need to accept inbound connections for the capture
pipeline. It polls the Worker and R2 instead.

**Agent**

Background process responsible for asynchronous processing and other tasks that
require access to the local environment, including post-capture processing
through headless Chrome.

**Headless Chrome**

Used only to process already-captured HTML. It generates thumbnails and performs
Readability extraction. It never fetches the original live page.

**Dashboard**

A Svelte application for browsing and searching the archive, viewing page
versions, reading extracted content, managing tags and collections, and managing
the user's account and devices.

### Network boundary

The core capture path does not depend on the backend being publicly reachable:

```text
Browser / phone / CLI
        │
        ▼
Worker → R2 → private backend
```

The dashboard is different. It requires whatever network access the operator
chooses to provide, such as a LAN, VPN, reverse proxy, or tunnel.

The deployment does not prescribe a particular way of exposing it.

---

## 3. Capture Flow

1. User adds a URL to the queue, either:
   - Directly in the desktop extension while browsing, or
   - Remotely via the share-sheet PWA (Android), iOS Shortcut, or CLI — these
     only enqueue, they never capture.
2. Enqueueing hits the Worker, which writes a row to `queue_items` in D1.
3. The desktop extension polls D1 (via the Worker) for pending queue items on an
   infrequent schedule (§8) and can notify the user something needs archiving,
   plus a manual "check now" action in its popup.
4. User selects a queued item (or a page they're currently on, for direct/
   unqueued capture) and triggers capture.
5. Extension captures full inlined single-page HTML via SingleFile's capture
   code (§3a).
6. Extension requests a presigned R2 upload URL from the Worker and uploads the
   HTML directly to R2 (bypassing Worker body-size limits).
7. Extension notifies the Worker that the upload is complete → Worker writes a
   `pending_captures` row to D1, using a client-generated UUID as the row's id
   (and marks the `queue_items` row, if any, as `captured`).
8. Backend, on its own polling schedule, discovers the new `pending_captures`
   row, pulls the HTML blob from R2, zstd-compresses it, stores it on local
   disk, computes the content hash (§3b), deletes the R2 object, writes rows to
   Postgres (idempotently — §3c), and pushes a lightweight mirror row back to D1
   for the bookmark-list feature (§8).
9. Backend enqueues a screenshot job (§6a) and a Readability extraction job
   (§6b) against the same locally-stored HTML.
10. Once the Readability job has populated `reader_text`, backend enqueues an AI
    job to summarize/tag it (§7) — AI enrichment has a real dependency on
    readability extraction, unlike the screenshot job.

### 3a. SingleFile integration

SingleFile isn't invoked as a separately installed extension via cross-extension
messaging — there's no API for a third-party extension to do that and get a
result back programmatically. Instead, the extension depends directly on
SingleFile's capture library (`single-file-core`) and calls it from its own
capture bundle (§3h covers the injection mechanism) — avoiding any dependency on
a stable extension id, or requiring the user to install SingleFile separately.

### 3b. Content hashing

Each capture stores two hashes:

- `content_hash` — over the full inlined HTML, for exact dedup detection.
  Computed synchronously at ingestion (§3).
- `reader_text_hash` — over the Readability-extracted text, driving the
  dashboard's "unchanged since last capture" flag. Populated asynchronously once
  the Readability job (§6b) completes; nullable and absent until then.

The full-HTML hash rarely repeats in practice — most pages embed per-load-unique
content (CSRF tokens, timestamps) even when nothing meaningful changed — so the
reader-text hash is the more useful signal for that feature.

### 3c. Capture idempotency (crash recovery)

`captures.source_capture_id` is a transient idempotency key — nullable, uniquely
constrained, real only while a capture is in flight (client-generated for the
extension/queue flow, backend-generated for manual uploads, §3d), cleared to
`NULL` once ingestion is fully done. It exists to solve two problems a naive
retry gets wrong:

- **A retry must not fail forever re-fetching an R2 object a prior attempt
  already deleted** (e.g. a crash between the R2 delete and confirming D1's
  `fetched_by_backend` flag).
- **A conflict on this column isn't necessarily a retry.** It could be a genuine
  collision between two different captures sharing an id (astronomically
  unlikely, not impossible) — treating any conflict as "already handled" would
  silently drop the second capture's data.

**Resolution:** ingestion always tries the full pipeline first — pull from R2,
hash, compress to disk, then
`INSERT ... ON CONFLICT (source_capture_id) DO UPDATE ... RETURNING`. The
returned row's `content_hash` disambiguates the two cases: a match means a
legitimate retry (no-op); a mismatch means a real collision, so a fresh UUID is
generated and the insert retried (bounded to 5 attempts). Only if that whole
attempt fails does ingestion fall back to checking Postgres for an
already-committed row under the original id — safe to treat as "already done" if
found, a real failure otherwise. This fallback never runs as an upfront gate,
since that would skip the `content_hash` check and reopen the same collision
risk.

> The insert also uses Postgres's `xmax = 0` idiom to report whether the row was
> newly inserted or returned via `ON CONFLICT` — that's what tells the caller
> whether to enqueue screenshot/readability jobs, so a retry never
> double-enqueues them.

Cleanup runs disk write → DB commit → R2 delete → D1 flag → clear
`source_capture_id`, in that order, so a crash at any point either leaves the R2
object in place for a safe retry, or leaves only harmless orphaned state (a
failed clear is harmless, since nothing looks the value up again once
`fetched_by_backend` is set).

**Disk storage is keyed the same way, one layer down:**
`archive.Store.NewCapture` mints a backend-generated UUIDv7 directory
exclusively (`os.Mkdir`, bounded to 5 attempts on `EEXIST`), never a
client-supplied id — so a `source_capture_id` collision can never overwrite
another capture's files. An `EEXIST` means zero bytes were written yet (retry
with a new id, nothing at risk); a `source_capture_id` duplicate caught later by
the insert means a _different_ attempt already committed this pending capture,
so the directory this attempt just wrote is a harmless redundant copy left for
`recueil gc` (§4) to reclaim — not a collision, and nothing to retry.

#### Re-archiving the same URL

Re-archiving a previously captured URL is **not** an update — it's a new version
under the same logical page. Captures are grouped by `normalized_url` (§9) into
a `pages` row; the dashboard shows all historical versions with their capture
timestamps.

### 3d. Manual upload (bypassing the queue)

For a page captured somewhere the extension wasn't installed — an email
attachment, a device without the extension, a file handed over by someone else —
the dashboard's Library screen (an Upload button beside the search box, opening
a modal) supports directly uploading an already-captured, fully inlined
SingleFile-style HTML file plus its URL, and optionally a favicon, via
`POST /api/manual-upload`. This bypasses R2, D1, and the Worker entirely: a
single authenticated request straight into the backend (`internal/httpapi`),
never touching `internal/ingest` at all.

- Reader-text extraction (async, same `readability_jobs` row any other capture
  gets), content hashing, URL normalization, and grouping into `pages` all
  produce identical results to any other capture path — a manual upload of an
  already-captured URL is just another new version under the same page — but the
  code itself is a separate, smaller implementation
  (`internal/httpapi/manualupload.go`) against `archive.Store`/
  `urlnorm.Pipeline`/`db.Queries` directly, not a shared code path with
  `internal/ingest`: that package's own pipeline is built tightly around pulling
  an already-uploaded blob from R2 via a `pendingcaptures.PendingCapture`, and
  bending it to also accept bytes already in hand isn't worth the coupling for
  the small amount of logic (title/language extraction, hashing) that actually
  overlaps.
- Uses a backend-generated UUID as the starting `source_capture_id` (§3c already
  covers the idempotency scheme in full; manual upload just supplies the id
  itself, since there's no client to generate one).
- Registered outside the rest of `/api`'s shared middleware
  (`middleware.AllowContentType("application/json")`, a global 1MB
  `middleware.RequestSize`) — chi middleware only stacks, it never unsets, so a
  route needing multipart/form-data and a much larger body gets its own
  top-level mount instead, the same way `/mcp` already does for its own,
  different reasons. The size ceiling is `capture_manual_upload_max_bytes`
  (`config.Config.ManualUploadMaxBytes`, defaulting to 100MB) — configurable
  rather than a hardcoded constant, since a real SingleFile archive with inlined
  images/fonts routinely runs into the tens of megabytes, and how big is
  reasonable depends on the operator's own disk and pages. Surfaced to the
  dashboard via `GET /api/capture-config`'s `manual_upload_max_bytes`, so the
  upload form can reject an oversized file client-side rather than only after a
  slow upload.
- A favicon upload is validated against the same closed extension set §3g's
  client-side favicon capture already uses (`svg`/`png`/`ico`) — the only shapes
  `archive.Store.WriteAsset`'s compress-by-extension logic understands — derived
  from the uploaded file's own name, since there's no capture URL to derive it
  from the way the extension's own favicon fetch has.
- `captures.source` (`'extension'` | `'manual_upload'`, §10) records which path
  a capture came through, for the dashboard to show directly.

### 3e. The agent process (background job triggering)

`recueil agent` is a separate subcommand/process from `recueil server`, sharing
the same binary and deployed as its own container — so it can coordinate its own
shutdown independently of in-flight HTTP requests, and a runaway job (a hung
screenshot render, say) stays isolated from the web process rather than
degrading request latency.

Job coordination is plain Postgres, not a message broker: the only real ordering
need — AI enrichment must not run before readability extraction succeeds — is
expressed simply as "when does the job row get created" (an `ai_jobs` row
doesn't exist until the readability job creates it, in the same transaction),
not a broker-level dependency feature.

`agent` doesn't run migrations itself — only `server` does, since D1 migrations
have no equivalent of Postgres's advisory lock to safely coordinate two
processes starting at once. `agent`'s earliest cycles simply retry until
`server` catches up.

The worker cycle also ingests saved links (§17c) when links are enabled, right
after capture ingestion; a failure there fails the cycle's heartbeat like any
other, except for a Worker that predates links, which is not a failure.

Two tickers, split by destination: `agent_worker_poll_interval_seconds`
(default 1800) for everything talking to the Cloudflare Worker, and
`agent_local_poll_interval_seconds` (default 300) for everything touching only
this process's own Postgres — keeping the Worker-facing side comfortably inside
Cloudflare's free tier while local work still picks up quickly.

### 3f. The CLI (`recueil auth` / `recueil enqueue`)

Two different config postures for two different audiences. `server`/`agent`
require an explicit `--config` file or environment variables — no automatic
discovery, since a production process silently picking up an unintended file is
a real risk. `auth`/`enqueue` are the opposite: a personal tool, where automatic
`$XDG_CONFIG_HOME` discovery is the expected UX (the same shape `git`/`ssh`
already train people on). Neither command touches Viper or `internal/config` —
everything they need is read from their own dedicated credentials file instead.

- **`worker_url` is stored alongside the pairing-derived token, not as an
  independent setting** — a token is only ever meaningful for the Worker that
  issued it, so the two are captured, stored, and read together.
- `recueil enqueue <url> [<url>...]` loops one `POST /queue` call per URL
  (there's no batch endpoint) and continues past an individual failure rather
  than stopping the whole batch, reporting a summary and a non-zero exit if
  anything failed — the same "one bad item shouldn't block the rest" shape as
  `Ingester.RunOnce`/`Syncer.SyncOnce` (§3c, §8). Each URL gets its own
  freshly-generated UUID as the idempotency key.

### 3g. Favicon capture

Captured client-side, the same way HTML is — not fetched by the backend. This
extends §1's core principle rather than exempting an exception to it: a favicon
fetch is still a live request against a URL the extension already has an
authenticated browser context for, so the backend never touches the live web at
all.

- **Selection is link-level, not pixel-level.** The extension checks
  `<link rel="icon">`/`<link rel="apple-touch-icon">` tags first (preferring
  SVG, then the largest declared raster size), then falls back to
  `/favicon.svg`, `/favicon.png`, `/favicon.ico` in that order. `favicon_path`
  simply stays `NULL` if none resolve — not every site has one.
- **No image processing.** Whatever bytes come back — including a legacy
  multi-resolution `.ico` — are stored exactly as received; every modern browser
  already renders `.ico` directly.
- **Per-capture state, like the HTML itself**: `captures.favicon_path` (§10) is
  written once and never mutated. `pages.favicon_path` is denormalized from the
  latest capture the same way `pages.title` is, including reverting to `NULL` if
  the latest capture didn't find one.
- **Shares the capture's directory (§4)**, taking a plain role-based filename
  (`favicon.{ext}`) since a capture directory holds exactly one of each asset.
  `favicon_hash`/`thumbnail_hash` remain columns regardless — the only integrity
  check available for a file nothing re-derives. Compression is per-asset-type:
  SVG gets zstd'd, PNG/ICO (already compressed) are stored raw.
- **The R2 key bakes in the real extension** (`.../favicon.{ext}`, `ext` ∈
  `svg | png | ico`) so the backend recovers the format from the key itself at
  ingestion, rather than needing a separate mime/type column. The Worker always
  recomputes this key itself — never trusts one supplied by the client, the same
  posture `r2_key_html` already has.
- **Ingestion is best-effort and never fails the capture.** A favicon fetch or
  disk-write failure is logged and ignored — a cosmetic loss, never a reason to
  lose an otherwise-good HTML capture.

### 3h. Browser extension architecture

A single Manifest V3 codebase covers both Chrome and Firefox — Chrome's MV2
support is gone entirely, and nothing recueil needs requires staying on MV2 for
Firefox. Safari is MV3-capable too but needs a separate packaging/distribution
pipeline so deferred for now (§16).

**Capture is a two-step injection.** `scripting.executeScript` first loads the
capture bundle as a file (a `func`-injected function can't import anything
itself, so the bundle has to land as a global first), then a second call invokes
it and returns the result. Background, the capture bundle, the popup and a small
link bundle (§17c) are separate esbuild entry points — different contexts
(service worker, content-script world, extension page) loading at different
times, so bundling them together would mean the largest thing in the build
(`single-file-core`) parses on every service-worker wake for no benefit.

**Resource fetching tries the page's own `fetch()` first, relaying through the
background only on failure.** A background-context fetch bypasses a page's CORS
restrictions, which is why the relay exists at all — but routing every resource
through the background unconditionally would tie a capture's success to the
background staying alive for the whole operation, the wrong shape under MV3's
non-persistent background model. Most resources are same-origin or already
CORS-permitted, so this resolves the large majority of fetches with no
background round-trip.

> A second relay (`background/frame-tree-relay.js`) exists for the same reason,
> one layer up: Firefox requires the background to forward a frame's captured
> DOM back to the top frame, and without it, multi-frame capture silently fails
> — even on pages with no iframes at all. Chrome needs no equivalent; it
> coordinates entirely in-page.

### 3i. Queue-driven capture

**Human-in-the-loop by default, not a fallback for detected failures.** The
original design assumed an unsupervised background tab: open it, wait for it to
load, capture it, close it. That's wrong for a specific reason: a CAPTCHA or
paywall page captures _successfully_ from `single-file-core`'s point of view —
no error, no timeout, just the wrong content, silently archived as if it were
the real page. There's no generic signal — no DOM marker, no HTTP status — that
distinguishes "this page needs a human" from "this page loaded fine," and
building one (auto-bypassing CAPTCHAs, defeating paywalls) isn't something this
project should do anyway. So a human is in the loop for every queue item,
always.

- The popup shows a plain list of pending items (`GET /queue`), cached in
  `storage.local` and refreshed on startup, a 6-hour alarm, manual refresh, and
  right after pairing. The cache is never authoritative: clicking an item sends
  the real, live `POST /queue/:id/claim` (the same atomic claim/404/409/410
  shape as §2), and on success opens a new, focused tab — deliberately stealing
  focus, since this is now an explicit action the user just asked for.
- The user solves whatever the page needs entirely by hand — no detection, no
  automation attempted.
- Completion reuses the exact direct-capture pipeline: `captureTab`/
  `captureActiveTab` take an optional `queueItemId` (tracked in a small
  `tabId -> queueItemId` map), and call `POST /queue/:id/complete` instead of
  `POST /captures/complete` when it's set. Everything upstream (inject, hash,
  presign, upload) is identical either way.
- An abandoned claim needs no explicit handling — the Worker's claim already
  goes stale and becomes reclaimable after 15 minutes, the same mechanism the
  queue already has (§2). A tab-close listener tidies up the tracking map purely
  for storage hygiene.
- The tab auto-closes on success, but only for queue-driven captures, never
  direct ones — a direct capture's tab is one the user already had open for
  their own reasons, and closing it out from under them would be disruptive.
  Left open on failure so the user can see what went wrong.

### 3j. Bookmark sync (native browser bookmarks, not a custom list)

Syncs archived pages into the browser's native bookmarks, not a custom in-popup
list — native bookmarks already have a full-featured, familiar UI (search,
folders, the browser's manager) a cramped popup view would never match, and
favicon display comes for free from the browser itself.

- **Recueil only ever touches bookmarks inside one dedicated folder it creates
  and manages** — it never searches the wider bookmark tree. Anything a person
  does inside that folder by hand — adding, renaming, moving — is unsupported:
  it gets overwritten or removed on the next ordinary sync, not just at
  teardown.
- **Reconciled by URL, not by tracking bookmark ids.** `GET /archived-pages`'s
  `raw_url` is sourced from `pages.normalized_url` — the exact column `pages`'
  `UNIQUE (user_id, normalized_url)` constraint is built on — so it's already a
  stable, unique identity key. Diffing the fetched list directly against
  `browser.bookmarks.getChildren(folderId)` by URL is simpler than a tracked-id
  map and avoids a second copy of the tree that could drift from the truth. It
  also means a bookmark that arrived via the browser's sync from another device
  needs no special "adopt" handling — it just looks like a URL that's already
  there.
- **The dedicated folder gets the same create-or-adopt treatment.** Chrome and
  Firefox use different, non-portable ids for "Other Bookmarks"/ "Unfiled
  Bookmarks" (and the title itself can be locale-translated), so neither a
  hardcoded id nor a title match is reliable. A throwaway probe bookmark
  (created the same way the real folder would be) discovers the real default
  container's id empirically, then removes itself. Neither browser allows
  creating anything as a sibling of "Bookmarks Bar" — landing inside the default
  container is the closest to top-level actually achievable.
- **Opt-in, not bundled into pairing.** `bookmarks` is a distinct, user-visible
  optional permission, requested only when the popup's toggle is turned on.
  Turning sync off relinquishes the permission too, not just stops syncing while
  holding it.
- No incremental sync on the extension side — a full-list pull, diffed locally,
  is the right level of complexity at a personal archive's scale (§8 covers the
  same reasoning from the backend's sync job).

### 3k. Internationalization (i18n)

The native WebExtensions i18n API, not a library:
`_locales/<locale>/messages.json` files, `__MSG_key__` substitution in
`manifest.json`, `browser.i18n.getMessage()` in code — zero new dependencies,
consistent with this project's bias toward a platform primitive over a library
wherever one exists.

- **Every lookup goes through one wrapper** (`src/common/i18n.js`'s `t()`),
  never `browser.i18n.getMessage()` directly — the native API has no way to
  select a locale other than the browser's current UI language, so if the popup
  ever grows a manual override, `t()` is the one place that needs to change.
- **Only strings recueil itself authors are translated** — never passthrough
  browser/network error text. A raw `fetch()` failure or HTTP response body
  isn't recueil's own writing, so there's nothing to look up a translation for.
- **`en` is `default_locale`** — both the fallback for missing keys and the
  source of truth for what keys exist. `manifest.base.json`'s
  `name`/`description` are localized too, the one place the browser substitutes
  `__MSG_*__` outside of code; general extension-page HTML has no equivalent,
  which is why `popup.html`'s static text stays an English fallback until
  `popup.js`'s `t()` calls overwrite it at runtime.

---

## 4. Storage Strategy

- **R2 is temporary only.** It exists purely to get large payloads from the
  extension (which may not have a stable public endpoint to push to) to the
  backend (which may not be reachable to receive a push). Once the backend has
  pulled and locally stored a capture's blobs, they are deleted from R2.
- **Local disk is canonical.** The backend stores the zstd-compressed HTML on
  local disk, referenced by path from the `captures` table. Thumbnails (§6a) and
  favicons (§3g) are also stored on local disk — every asset for one capture
  lives together under a single directory.
- **One capture, one directory.** Each capture's directory is minted by
  `internal/archive`'s `Store.NewCapture` as a backend-generated UUIDv7, sharded
  three levels deep (`{id[-4:-2]}/{id[-2:]}/{id}/`, git's object-store shape — a
  flat directory with hundreds of thousands of entries degrades badly for `ls`,
  backup tools, and anything else that walks it). The shard comes from the id's
  _trailing_ characters, not its leading ones, since UUIDv7's leading bits are a
  millisecond timestamp — sharding on those would drop everything captured in
  the same period into one bucket.

  Directories aren't keyed by content hash: two captures with byte-identical
  HTML don't get aliased onto one directory. `html_path`/`favicon_path`/
  `thumbnail_path` are stored columns, so every read resolves row → path → disk
  regardless — content-addressing's only real benefit would have been
  deduplication, which isn't a goal here and rarely fires anyway (§3b:
  per-load-unique content means two captures rarely produce identical bytes). It
  would also have made per-user deletion unprovable, since one tenant's bytes
  could physically persist as long as another tenant happened to share them —
  relevant given the multi-tenant hosted deployment mentioned below.
  `content_hash`/`favicon_hash`/`thumbnail_hash` remain columns for exact-dedup
  detection, §3c's retry-vs-collision disambiguation, and integrity checking —
  they just don't name anything on disk.

  The `os.Mkdir`/`EEXIST` collision-avoidance mechanism itself is covered in
  §3c; `captures.html_path` additionally carries a `UNIQUE` constraint
  (migration `00004`) as database-level belt-and-suspenders on top of it — a
  collision that's already astronomically unlikely, given UUIDv7's 74 random
  bits.

- **Deleting a page or capture doesn't reclaim its on-disk files
  synchronously.** `DELETE /api/pages/{id}`/`DELETE /api/captures/{id}` remove
  the Postgres rows (cascading to jobs/tags/collection memberships) but leave
  the HTML/favicon/thumbnail files in place.
- **`recueil gc` (`internal/gc`) is the operator-run sweep that reclaims them.**
  It reads the live set of paths Postgres still references
  (`ListReferencedArchivePaths`), walks every file `archive.Store`'s root
  actually contains, and removes whatever isn't in that live set — `--dry-run`
  reports the same scan/removal counts and reclaimable bytes without deleting
  anything. Two safety rails guard against an otherwise silent, total failure
  mode:
  - **A 15-minute recency floor** — the same window used elsewhere for claim
    staleness. Ingestion writes to disk _before_ committing to Postgres (§3c),
    so an in-flight capture — including `archive.Store`'s `.tmp-*` files
    mid-write — is legitimately absent from the live set; anything modified more
    recently is left alone regardless.
  - **An orphan-fraction refusal** — if more than half of at least 100 scanned
    files come back unreferenced, the run removes nothing and reports an error
    instead (`--force` overrides). The live set is built by comparing stored
    path strings against walked path strings, so any future normalization
    mismatch between the two would otherwise silently mark the entire archive as
    garbage; this refusal is what stops that from being a one-shot way to delete
    every capture on the instance.

  A companion pass (`Store.WalkEmptyDirs`) prunes now-empty shard directories
  left behind once their last file is removed, since a capture's directory is
  created before anything is written into it and would otherwise accumulate
  indefinitely.

- **A second pass sweeps the link favicon store (§17e).** `recueil gc` runs the
  captures pass and then the links pass in one command (`Runner.Run`, then
  `Runner.RunLinks`; `--skip-captures`/`--skip-links` run just one, and the
  links pass skips itself when links are off). They share one sweep
  implementation but are otherwise independent: each has its own root, its own
  live set (`ListReferencedLinkFaviconPaths` for links), its own recency floor
  and orphan-fraction refusal, and its own report. If a pass stops (the refusal,
  or an error), the command stops before the next pass starts.

- **Backup is entirely the operator's responsibility** — see §14. The
  application itself performs no automated backup.
- **Database choice: Postgres, not SQLite**, despite this being a personal
  archive. Real user accounts (family members on one deployment, and a potential
  future multi-tenant hosted version) tip this toward Postgres: SQLite's
  single-writer lock becomes a real constraint with concurrent family members
  archiving/querying at once, and multi-tenant isolation / hosted-DB migration
  paths are native to Postgres. Docker Compose makes the extra container a
  non-issue operationally.
- **Bind mounts, not named Docker volumes**, for both the Postgres data
  directory and the local archive directory (§14) — straightforward for whatever
  external backup tool the operator chooses to snapshot the directories directly
  from the host filesystem.

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

Device pairing doesn't mirror the account's login credential into D1 at all — a
Cloudflare Worker's free-tier 10ms CPU cap makes any properly slow password hash
(e.g., bcrypt) infeasible to verify there, and mirroring the credential under a
weaker algorithm wouldn't fix the underlying exposure anyway. Instead, each
account gets a separate, single-purpose credential — a **pairing token** — used
only to authenticate a device once in exchange for a bearer token. It's never
used to log into the dashboard, and the dashboard password is never used to pair
a device.

- Generated automatically at account creation: 32-byte CSPRNG value,
  base64url-encoded, `rcl_pair_...` prefix. One per user, valid indefinitely
  until regenerated or revoked.
- **Postgres stores it reversibly** — `users.pairing_token_enc`, AES-256-GCM — a
  deliberate departure from how every other credential here is stored, justified
  because a pairing token isn't a user-chosen secret carrying a password's
  stakes; it's closer to an API key, and the dashboard needs to redisplay it on
  demand rather than forcing a regenerate-to-view flow. The AES key
  (`PAIRING_TOKEN_KEY`) is operator-generated and lives in the backend's `.env`
  alongside the Worker service secret (§5a) and D1 migration credential (§5b) —
  it never leaves the backend's trust boundary.
- **D1 stores only `SHA-256(pairing_token)`** — the token already carries ~256
  bits of entropy, so a leaked hash alone yields nothing usable. A full D1
  compromise now exposes no password-derived material at all, only a credential
  whose sole purpose is pairing new devices.
- **Device pairing is single-credential** — a device submits only the pairing
  token, no username; the Worker hashes it, looks up the owning `user_id`
  directly, and issues an opaque bearer token (32-byte CSPRNG, `rcl_live_...`
  prefix, hashed at rest in D1's `tokens` table, revoked by row deletion).
  Implemented as `POST /pair` (request:
  `pairing_token`/`device_name`/`device_type`; response: the raw bearer token,
  shown exactly once). `tokens.last_used_at` is touched on every subsequent
  authenticated device request via a fire-and-forget write
  (`ExecutionContext.waitUntil`), so it never adds latency to the request it's
  authenticating.
- **Pairing-token management** — session-gated backend endpoints:
  `GET /api/pairing-token` (decrypts and returns the current token — kept
  viewable rather than show-once-then-hashed, since losing it would otherwise
  force an unwanted regenerate; also returns `worker_url`, since pairing a
  device needs both together), `POST /api/pairing-token/regenerate` (issues a
  new one, overwrites both copies), and `DELETE /api/pairing-token` (revokes
  without reissuing, blocking further pairing).
- D1's `users` table (§10) holds only `pairing_token_hash` and a `user_id`
  foreign-key target — nothing else about an account lives there.

```sql
-- D1
CREATE TABLE tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash TEXT NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id),
  device_name TEXT NOT NULL,
  device_type TEXT NOT NULL,       -- 'extension' | 'pwa' | 'cli' | 'shortcut'
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at TEXT
) STRICT;

CREATE INDEX idx_tokens_user_id ON tokens(user_id);
```

**Dashboard → direct session auth against Postgres, DB-backed sessions.** The
dashboard authenticates by checking `username`/`password_hash` directly in
Postgres, with no D1 or Worker involvement. Sessions are DB-backed (a `sessions`
table), using the same hashed-opaque-token shape as device tokens — a 32-byte
CSPRNG value (`rcl_sess_...`), stored as its SHA-256 hash, with the raw value
held only in an `HttpOnly`, `SameSite=Lax` cookie. This keeps sessions revocable
the same way device tokens are (delete the row), at the cost of a DB lookup per
request.

```sql
-- Postgres
CREATE TABLE sessions (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  session_hash TEXT NOT NULL,
  user_id BIGINT NOT NULL,
  user_agent TEXT,
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
`last_seen_at` updates on every authenticated request. Logout deletes the row.
`sessions` and D1's `tokens` are two distinct, independently-revocable
credential systems for two distinct kinds of client, so there's no Postgres
`tokens` table at all.

`user_agent` is captured once at sign-in (verbatim, unparsed) and parsed fresh
on every read (Active Sessions screen) rather than split into columns at write
time. No IP address column — a self-hosted tool's IP-derived "location" would be
meaningless without a trusted-proxy configuration this app doesn't have.

### Manage Devices dashboard screen

D1 is the sole owner of device tokens, so this needs real plumbing, not just a
UI: `GET /internal/tokens?user_id=`/`DELETE /internal/tokens/:id?user_id=` (two
Worker endpoints, gated by the service secret, §5a — the `user_id` on the delete
call is checked against the token's actual owner, so a backend-side bug passing
the wrong id pair deletes nothing rather than someone else's device); a backend
passthrough (`internal/devices`, `GET /api/devices`/`DELETE /api/devices/{id}`)
since the dashboard has no Worker credential of its own; and
`recueil device list <username>`/ `recueil device revoke <username> <device-id>`
as an operator-only CLI escape hatch for the rare lost-device case (`revoke`
lists first rather than revoking blind, so a wrong device id fails clearly
before ever reaching the Worker).

`GET /api/devices`/`DELETE /api/devices/{id}` are strictly self-scoped for every
role — managing _another account's_ devices isn't a session-authenticated web
capability at all, the same reasoning that keeps user creation itself CLI-only.
Reaching into another user's access shouldn't be one browser session away. The
one exception, `GET /api/admin/stats`, doesn't cross this boundary in the sense
that matters — it exposes aggregated byte/capture counts per username, nothing
identifying or actionable, so there's no real account access to protect.

Revocation is **not** a live push to the device — a revoked extension/PWA/CLI
keeps working until its next request to the Worker, which then 401s and requires
re-pairing.

### Active Sessions dashboard screen

Self-scoped endpoints backed entirely by the `sessions` table's `user_agent`
column — no Worker/D1 involvement, since sessions have always lived entirely in
Postgres.

- User-Agent parsing uses `github.com/medama-io/go-useragent`, not a hand-rolled
  regex — browser/OS strings shift too often (new versions, Chrome's User-Agent
  Reduction effort) for a one-off implementation to track.
- Parsed at read time (`sessionResponseFromSession`, fresh on every
  `GET /api/sessions`), not stored as separate columns at write time.
- The current session is never revocable through this endpoint — `DeleteSession`
  checks the request's session id (`auth.SessionIDFromContext`) against the one
  being deleted and refuses (400) if they match, since deleting your own live
  session would 401 your very next request with no obvious explanation. Signing
  out (`POST /api/auth/logout`) is the correct way to end that one.

### API tokens (machine access, e.g. MCP)

A third credential type, distinct from both device tokens and sessions — for a
**program** acting on a user's behalf against the backend's HTTP API directly
(the MCP server, §15), outside a browser, needing a credential valid until
explicitly revoked rather than tied to a session's TTL or the Worker/D1
device-pairing path.

Neither existing mechanism fits: a session's 30-day TTL assumes a browser that
can refresh it, which a long-running local MCP client has no way to do; and the
pairing-token → device-token flow is specifically a Worker/D1 relay — the
backend itself never inspects that credential, and MCP calls hit the backend's
HTTP server directly, so routing through D1 would add a pointless round-trip for
no benefit.

A fourth token in the existing hashed-opaque-token family, Postgres-only, same
shape as `sessions`/D1 device tokens but with no `expires_at` — closer to a
personal access token:

```sql
CREATE TABLE api_tokens (
  id BIGINT GENERATED ALWAYS AS IDENTITY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL,
  name TEXT NOT NULL,           -- user-supplied label, e.g. "Claude Desktop"
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ,
  CONSTRAINT api_tokens_pkey PRIMARY KEY (id),
  CONSTRAINT api_tokens_token_hash_key UNIQUE (token_hash),
  CONSTRAINT api_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_api_tokens_user_id ON api_tokens(user_id);
```

- Generation/storage reuse existing primitives (`GenerateSessionToken`'s same
  shape, `HashToken`'s same SHA-256) under a new `rcl_api_...` prefix. Shown
  once at creation, never redisplayed — unlike the pairing token, which is
  redisplayed because losing it forces a regenerate; an API token just gets
  replaced.

New self-scoped endpoints: `POST /api/tokens` (mint; response: the raw token,
once), `GET /api/tokens` (list, never the token/hash), `DELETE /api/tokens/{id}`
(revoke). `internal/auth.RequireAPIToken` is structurally parallel to
`RequireSession` — extracts the bearer token, hashes, looks up `api_tokens`,
loads the user via the same `auth.UserFromContext` every handler already reads
from — and is mounted only on the MCP route group, not on `/api/*` generally.

Surfaced in the dashboard as a second list on the existing Manage Devices screen
rather than a new page: an API token isn't literally a device, but both lists
answer the same question — "what has standing access to my archive right now."

### 5a. Backend ↔ Worker service authentication

The backend is a distinct, higher-privilege actor from any single user's device
— it polls for pending captures and pushes mirror rows across _all_ users in a
deployment, and issues revoke calls. This needs its own credential, separate
from the per-device token system.

**A static shared secret**, generated via Terraform's `random_password`
resource, injected into the Worker as a binding, and copied by the operator into
the backend's `.env`. Checked by the Worker as a header (`X-Service-Key`) on the
small set of backend-only endpoints. Rotation is regenerate + redeploy —
acceptable given a single backend per deployment and infrequent rotation.

Two alternatives were rejected: reusing the `tokens` table with a "service" row
(`tokens.user_id` is scoped to one user; the backend needs cross-user access),
and mTLS or Cloudflare Access service tokens (real options, but more operational
complexity than this scale needs).

### 5b. Backend ↔ Cloudflare D1 migrations

The backend applies D1 schema migrations itself at startup, rather than
requiring `wrangler d1 migrations apply` — consistent with the same "no external
tool needed to run the binary" goal that also keeps Postgres migrations
self-applied (§13a). This is the one place the backend talks to Cloudflare
directly rather than exclusively through the Worker; it doesn't weaken the
"backend stays fully non-public" property elsewhere (§2, §11), since that
property is about _inbound_ reachability, and this is a new _outbound_ path
only.

- Migrations live at `terraform/worker/migrations/*.sql`, embedded into the Go
  binary via `go:embed`. Applied migrations are tracked in a `schema_migrations`
  table (§10) the backend creates and owns — not wrangler's `d1_migrations`
  convention, since wrangler isn't part of this project's toolchain anywhere.
- Credential: a Cloudflare API token scoped to `D1:Edit` on this one database —
  a real Cloudflare account-level token, narrower in scope than a full-account
  one, and distinct from the Worker service secret.
- Runs once at startup, alongside the bootstrap-admin check; a no-op once
  nothing's pending.

The alternative — a Worker endpoint that runs migrations, keeping the Worker as
the sole thing that ever touches D1 — was considered and rejected: it would mean
a schema change requires a Worker redeploy even when nothing about the Worker's
code changed, worse operator friction than holding one additional
narrowly-scoped credential.

### Account creation and roles

- **Registration is gated by `ENABLE_OPEN_REGISTRATION` (default `false`).**
  When enabled, anyone who can reach the dashboard can create a `member` account
  via a signup form, no invite step — appropriate for a small family/friends
  deployment, or a future hosted/SaaS mode where open signup is expected. Closed
  by default: a self-hosted personal archiving tool has no business letting
  anyone who can reach it create an account without the operator opting in, and
  the bootstrap flow plus `recueil user create` already cover account creation
  without it.
- **The first admin is created via an in-memory bootstrap token.** On startup,
  if `users` is empty, the backend generates a random token
  (`rcl_bootstrap_...`, 1-hour expiry), prints it to the logs, and holds it
  entirely in a process-local value — a restart before it's used simply
  generates a new one, with nothing stale left behind. The dashboard's "create
  first admin" screen requires this token alongside a username/password; it's
  marked consumed only after the account is actually created successfully, so a
  request that fails after validation (a username race, a transient DB error)
  can retry with the same token rather than needing a fresh one. This closes the
  narrow race where the dashboard is briefly reachable before the operator has
  locked it down — without the token, reaching the setup screen isn't enough to
  claim admin. (This assumes exactly one backend process, already implicit in
  §5a's service-secret rotation reasoning — a second replica would hold its own
  independent token, invisible to the first.)
- **Roles: `admin` and `member`.** `users.role`
  (`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'member'`). Admins
  can create/manage other users; members manage only their own
  bookmarks/tags/collections. Purely a backend/dashboard authorization concern,
  not included in the D1 mirror — D1 only needs enough to authenticate a device
  and identify its owning `user_id`, never authorization decisions.
- **Operator account management via CLI**
  (`recueil user create <username> [--role admin|member]`,
  `recueil user reset-password <username>`), for an operator with shell access
  to the box — connects straight to Postgres using the same config
  `recueil server` reads, and calls the same `auth`/`sqlc` functions the HTTP
  handlers use, so there's no separate code path to keep in sync. `user create`
  pushes the new pairing token's hash to D1 the same way
  `POST /api/setup`/`POST /api/auth/register` do. `user reset-password`
  additionally invalidates any existing dashboard sessions, so a pre-reset
  cookie can't stay valid.

### Security note: D1 as a mirror target

D1 isn't directly internet-addressable on its own, but "not publicly accessible"
doesn't mean zero risk:

- The Worker itself is public, so a bug in its auth-check logic is a path to the
  D1-mirrored credentials. The Worker is kept intentionally minimal to limit
  this surface, but it isn't literally zero.
- Cloudflare, as the D1 host, has access to the data at rest — a standard
  tradeoff of using any managed cloud service, not unique to this project.
- Every credential D1 holds — bearer-token hashes, the pairing-token hash — is
  `SHA-256` of a CSPRNG-generated, ~256-bit-entropy value, never anything
  human-chosen or password-derived. The corresponding risk lives on the Postgres
  side instead: `users.pairing_token_enc` is reversible by design (see above),
  so a compromise of both a Postgres backup and `PAIRING_TOKEN_KEY` would expose
  usable pairing tokens — still not the account password itself, and a pairing
  token alone only grants the ability to pair a new device, not dashboard
  access.
- The backend's D1 migration credential (§5b) is a second, narrower extension of
  this trust boundary — scoped to `D1:Edit`, used only at startup, outbound-only
  from the backend, same as everything else in §2.

This tradeoff is accepted explicitly as part of the design.

### 5c. Cloudflare Browser Integrity Check bypass

recueil's own non-browser Go clients — the CLI and the backend's Worker-facing
clients — send every request with a fixed User-Agent (`recueil/1.0`).
Cloudflare's Browser Integrity Check (BIC), when enabled on the zone, tends to
flag exactly this shape of traffic (no browser TLS/JA3 fingerprint, no
navigation headers) and drop it before it reaches the Worker.

A Terraform-provisioned `cloudflare_ruleset` (`browser_integrity_check_bypass`,
default enabled) skips BIC for requests matching that User-Agent — keyed on
User-Agent alone, not on a bearer token or service-key header, since
`POST /pair` is unauthenticated by design and carries neither. BIC is a
low-stakes anti-scraping heuristic, not a real security boundary, so identifying
"one of our own clients" by User-Agent alone is sufficient; the Worker's
per-route bearer-token/`X-Service-Key` checks (§5, §5a) do the actual
authentication work entirely on their own.

The browser extension is untouched by any of this — its requests already carry a
real browser's TLS fingerprint and User-Agent. The User-Agent string itself is a
fixed protocol constant (`recueil/1.0`), not the actual release version —
coupling the two would mean every app release needs a coordinated
`terraform apply` to keep the WAF rule's exact-match expression working.

### 5d. Dashboard settings (`user_settings`)

A dedicated table, not more columns on `users` — dashboard preferences (language
today, plausibly theme/display preferences later) are a different kind of thing
from account-identity concerns, with no reason to share a row that
authentication code paths read and write. `user_settings.user_id` is the table's
primary key: a 1:1 extension of `users`, not a one-to-many relationship.

No row exists until a user's first `PATCH` — no backfill migration, no
row-creation hook on any account-creation path. `GET /api/settings` treats "no
row" and "a row with `language` explicitly `NULL`" identically (both render
`{"language": null}`, meaning "no override, fall back to auto-detection");
`PATCH /api/settings` is accordingly an upsert
(`ON CONFLICT (user_id) DO UPDATE`), not an update that assumes a row already
exists.

### 5e. Dashboard i18n (Paraglide JS)

A compiler, not a runtime library — a different choice from the extension's i18n
(§3k) because the two problems are different: the extension has a real platform
primitive (`browser.i18n`) already built in; nothing equivalent exists for
arbitrary UI strings in a Vite-built SPA. Paraglide JS was chosen as SvelteKit's
officially-recommended i18n integration, and its compile-time model (typed,
tree-shaken message functions rather than runtime dictionary lookups) is the
closest available equivalent to §3k's "lean on a compiler over a runtime
abstraction" instinct.

recueil isn't SvelteKit, so only Paraglide's framework-agnostic Vite plugin
applies — its SvelteKit-specific integration (URL-based locale routing, server
middleware, SSR cookie strategies) solves problems a client-only SPA with no SSR
doesn't have.

Locale resolution is a custom strategy backed by `user_settings.language`, not
any of Paraglide's built-in cookie/localStorage/URL strategies:
`src/lib/locale.ts` defines a `custom-userSettings` client strategy whose
`getLocale()` reads a plain in-memory cache (client-side custom strategies must
be synchronous), populated once by `session.svelte.ts`'s existing bootstrap
sequence before the Router ever mounts. Strategy order is
`["custom-userSettings", "preferredLanguage", "baseLocale"]` — an explicit
override wins outright; absent that, Paraglide's `preferredLanguage` strategy
reads `navigator.languages`, falling back to `baseLocale` (`en`).

No Svelte reactivity around locale changes — Paraglide's `setLocale()` triggers
a full page reload by default, and this project leans into that rather than
fighting it (wrapping every localized string in a `$derived` just to make
`m.*()` reactive would be real, ongoing boilerplate for a change that happens
rarely). `locale.ts` exports its own `applyLanguageOverride()` rather than
calling `setLocale()` directly, though, since `setLocale()`'s type only accepts
a concrete `Locale` — it has no way to express "clear the override, fall back to
`preferredLanguage`/`baseLocale`," which is exactly what picking "Automatic" in
the language selector needs to do.

---

## 6. Screenshot / Thumbnail Generation and Readability Extraction

Two async backend jobs render a capture's already-stored HTML through a shared
headless-Chrome sidecar (`chromedp` + `chromedp/headless-shell`, a separate
container so Chromium's footprint stays out of the backend image): one takes a
screenshot for the dashboard's thumbnail, the other runs Readability.js to
extract reader text. Both run only once a capture's HTML has already been pulled
from R2 and stored locally (§3) — rendering an already-captured, script-stripped
document offline is not the "fetch a live URL" operation §1 forbids: no network
requests, no live auth state, no CAPTCHA, no live JS.

- The backend keeps a long-running connection to the sidecar
  (`internal/sidecar`, shared by both jobs — a `chromedp.RemoteAllocator`
  connection already supports many concurrent tabs, so there's no benefit to
  each job holding its own) and opens a new tab per job rather than
  cold-starting a browser process each time.
- HTML is served to the sidecar via a brief ephemeral local HTTP server, not
  `file://` — the sidecar has no filesystem access to the agent's local archive.
  `sidecar_url`/`sidecar_render_host` cover the two directions of this
  connection, since local dev (sidecar in Docker, agent on the host) and an
  all-Docker deployment need different values on each side.
- Both jobs are fully async and non-blocking, with the identical shape: bounded
  worker-pool concurrency (default 3), exponential-backoff retry
  (`30s * 2^(attempts-1)`, capped at 30 minutes, default 3 attempts), and atomic
  `FOR UPDATE SKIP LOCKED` claiming with a 15-minute stale-claim reclaim
  (matching the D1 queue's timeout). A startup `/json/version` ping fails loudly
  if the sidecar is unreachable.
- Tracked in two separate tables, `screenshot_jobs` and `readability_jobs`
  (§10), not one combined table — the two jobs fail independently (a screenshot
  can time out while extraction succeeds, or vice versa), and re-extraction
  after a Readability.js upgrade shouldn't force a redundant re-screenshot.

### 6a. Screenshot / Thumbnail Generation

A fixed 1280×800 viewport, not a full-page screenshot: uniform thumbnails in a
dashboard grid matter more than maximal content.

### 6b. Readability Extraction

Used by every capture path, including manual upload (§3d) — a manually uploaded
file has no live browser tab or extension to extract reader text client-side, so
extraction has to happen backend-side regardless, and once it does, every other
capture path shares the same path too.

- Readability.js — the real, unmodified upstream library — is injected via
  `chromedp.Evaluate` and run as `new Readability(document).parse()` against the
  rendered DOM. It's embedded via `go:embed` at the repo root (`main.go`) and
  threaded into `internal/readability` via `Params`, since `go:embed` can't
  cross package boundaries.
- Re-extraction happens in place, no history kept: upgrading the vendored
  Readability.js and re-running overwrites
  `reader_text`/`reader_text_hash`/`readability_version` directly.
- `captures.reader_text`/`reader_text_hash` are nullable (§3b) — populated
  asynchronously, or not at all if extraction never succeeds.
  `captures.readability_version` is stored alongside the capture itself, because
  `captures.reader_text_tsv` (§10) is a `GENERATED ALWAYS AS` column and can
  only reference other columns in the same row.

---

## 7. AI Enrichment (Optional)

- Entirely optional and asynchronous — never blocks capture or ingestion. A
  capture is fully valid, searchable, and browsable with zero AI fields
  populated.
- Runs against the Readability-extracted plain text, not the raw HTML — cheaper
  and produces better summaries than trying to parse rendered HTML. This
  introduces a sequencing dependency on §6b: an `ai_jobs` row simply doesn't
  exist until the readability job creates it (on success).
- **A single OpenAI-compatible backend.** Ollama, llama.cpp's server, and
  effectively every hosted provider besides Anthropic have all standardized on
  the same `/v1/chat/completions` request/response shape, so one configurable
  base URL + API key + model name covers all of them.
- `ai_summary`/`ai_model` live on `captures` directly. `ai_model` is kept
  alongside `ai_summary` specifically so a summary can be regenerated later
  against a different model, knowing what produced the existing one; no
  `ai_summary_hash`, since this data only ever lives in Postgres (nothing on
  disk to verify against) and LLM output is non-deterministic even for identical
  input, so a hash couldn't answer "did this change" usefully anyway.
- AI-generated tags are written to the same `page_tags` table as manual tags,
  distinguished by a `source` column (§9), and generated by a separate chat
  completion call from the summary — simpler prompts, no dependency on the model
  reliably producing one combined structure. Tag parsing is intentionally
  lenient (a comma-separated list, not JSON or structured output), since support
  for those varies significantly across compatible servers, especially smaller
  local models. The dashboard visually distinguishes AI tags from manual ones
  via the `source` column.

### Retry and failure handling

On failure: increment `attempts`; if under a small max (default 3), set `status`
back to `pending` with `next_attempt_at` pushed out (exponential backoff, same
shape as §6a/§6b); once exhausted, mark `status = 'failed'` permanently with
`error` preserved. Failed jobs across all three of screenshot/readability/AI
surface together on the dashboard's Queue screen (§8) — one place for everything
currently stuck, rather than a per-capture badge on the capture detail view.

### Manual retry

- `GET /api/jobs` (all three of `screenshot_jobs`/`readability_jobs`/ `ai_jobs`,
  self-scoped, grouped under their own response keys) and
  `POST /api/jobs/{kind}/{id}/retry` (`{kind}` one of
  `screenshot`/`readability`/`ai` — a single dispatching handler, since only the
  query called differs). No flag column is needed the way `queue_items`' manual
  retry uses one (§8): these three tables are only ever claimed by the backend's
  `ClaimDueScreenshotJobs`/`ClaimDueReadabilityJobs`/`ClaimDueAIJobs`, so a
  retry can just reset the row directly
  (`status = 'pending', next_attempt_at = NULL, error = NULL, claimed_at = NULL`)
  and the backend's next poll picks it up.
- **Does not reset `attempts`.** A manual retry doesn't grant a fresh budget; it
  spends the next one. If it fails again, the existing
  `attempts+1 >= MaxAttempts` check fires exactly as it would for any other
  attempt and the job goes back to permanently `failed`.
- A readability job that succeeds on retry creates its `ai_jobs` row in the same
  transaction as any other successful completion — nothing needs to special-case
  "this was a retry."

### Implementation

Toggled off entirely by an empty `ai_base_url`, not a separate `ai_enabled`
boolean — `cmd/agent.go` simply never constructs an `*ai.Runner` when it's
unset. `ClaimDueAIJobs` needs no readiness join: a row only ever exists once
`internal/readability`'s `commitDone` creates it, so its mere existence already
implies `reader_text` is set. A capture whose readability extraction permanently
fails simply never gets an `ai_jobs` row — AI enrichment silently never runs for
it, which is correct: there's no text to summarize.

---

## 8. Cross-Device Queue and Bookmark List

### Queue (phone → desktop archiving)

- Adding a URL from a phone (via Shortcut, PWA, or CLI) only **enqueues** it —
  it does not attempt to archive anything server-side. The intended workflow is:
  queue remotely, archive later from the desktop extension, where a real
  rendered/authenticated browser session exists.
- The desktop extension polls the queue via the Worker/D1 (see "Polling cadence"
  below) and can notify the user that items are waiting.
- Claiming is done with a conditional update (`WHERE status = 'pending'`) to
  prevent two devices from grabbing the same item simultaneously; a claimed item
  records which device claimed it and when.
- Implemented as three bearer-token-authenticated Worker endpoints, all
  operating purely between a device and D1 — the backend doesn't participate in
  the normal device-enqueue path at all:
  - `POST /queue` — enqueue. `id` is client-generated (idempotent retry via
    `INSERT ... ON CONFLICT(id) DO NOTHING` — see §3c's identical reasoning for
    `pending_captures`).
  - `GET /queue` — lists this user's pending items, plus any claimed item whose
    claim has gone stale (see visibility timeout below). Listing doesn't claims.
  - `POST /queue/:id/claim` — the atomic claim, via a conditional
    `UPDATE ... WHERE ... RETURNING`. This, not the listing endpoint, is where
    the two-devices-race-for-the-same-item risk lives.

**One exception: the dashboard's "recapture" action.** PageDetail's recapture
button asks the backend to re-enqueue a page's most recent capture's URL — the
backend has no rendered/authenticated browser session of its own to capture
with, so this still only ever enqueues, same as a device would. It's a fourth,
service-secret-gated Worker endpoint (`POST /internal/queue-items`), not a
bearer-token one: the backend generates the `id` itself (there's no device on
the other end to have generated one) and leaves `added_by_token_id` `NULL`. Once
the row exists it's indistinguishable from any other queued item.

**Claim failure is not a single status code** — a failed claim distinguishes
three cases rather than a uniform `409`:

- `404` — the item doesn't exist, or belongs to a different user. These two
  cases are collapsed together rather than distinguished, so a claim attempt
  never leaks cross-user existence.
- `410` — the item is in a terminal state (`captured` or `failed`): it used to
  be claimable and permanently isn't anymore. More precise than a bare 404 for
  "this happened, but it's over."
- `409` — the item is actively claimed by another device and the claim hasn't
  gone stale yet: a temporary conflict worth retrying later.

Distinguishing these costs one extra `SELECT`, but only on the failure path — a
successful claim is still a single `UPDATE ... RETURNING` with no additional
round trip.

### Queue visibility timeout

A claimed item can get stuck if the claiming device dies mid-capture or the tab
is closed. Rather than a separate scheduled sweep job, this is handled as lazy
reclaim folded into the existing claim query:

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
and would otherwise accumulate forever.

- **`POST /internal/queue-items/cleanup`**, service-secret gated, called on the
  backend's schedule — this is a maintenance sweep across the whole deployment.
- **Deletes only `captured` items**, and only once older than a 72-hour
  retention window (long enough to be useful for auditability/debugging shortly
  after the fact, short enough not to accumulate indefinitely). `failed` items
  are **not** touched — they're kept indefinitely.
- **`failed` items get a manual-retry action**, not left as a dead end. A
  `queue_items.manual_retry` flag (D1, default `0`) lets the Queue screen
  request another attempt without losing the `failed` status the screen lists
  against — cleared automatically once a device's `POST /queue/:id/claim` picks
  the item up (both the claim query's `WHERE` and `GET /queue`'s listing query
  add `OR (status = 'failed' AND manual_retry = 1)`). Backed by two
  service-secret-gated Worker endpoints (`GET /internal/queue-items`, see below,
  and `POST /internal/queue-items/:id/retry`), proxied through
  session-protected, self-scoped `GET`/`POST /api/queue-items...` on the
  dashboard side. No automatic retry and no separate/longer expiry — expected
  volume is low at this project's personal/family scale.
- **`GET /internal/queue-items` returns every `pending`/`claimed`/`failed` item
  unconditionally, plus `captured` items from the last 15 minutes** — the same
  window the claim visibility-timeout uses elsewhere, reused rather than picking
  a second number for what's conceptually the same "still worth a glance" idea.
  `claimed_at` is in the response too.
- **The retention clock is `claimed_at`, not `created_at`.** An item can sit
  `pending` for a long time before being claimed; it's time since completion
  that should drive retention, not time since the original enqueue. There is no
  dedicated "when did this finish" timestamp on `queue_items` — `claimed_at` is
  used as a pragmatic proxy, reasonable at this project's scale since the gap
  between a successful claim and the capture completing is seconds to minutes,
  not enough to matter for a 72-hour window.

### Pending-capture claiming and cleanup

`pending_captures` is the queue's downstream sibling — a device has finished
capturing, and the row exists until the backend pulls the blobs from R2 and
commits.

- **Backend pickup is an atomic claim, not a plain read.**
  `POST /internal/pending-captures` claims a batch and returns it in one
  `UPDATE ... RETURNING`, the same shape `POST /queue/:id/claim` already uses.
  SQLite has no `FOR UPDATE SKIP LOCKED`, but D1 serializes writes, so the
  single statement is atomic on its own.

  **The bug this fixes is a silent duplicate capture, not merely wasted work.**
  Two agent processes polling at roughly the same time both ingest the same row;
  the second one's insert should be caught by `captures`'
  `ON CONFLICT (source_capture_id)` guard, but isn't — because the last thing
  ingestion does is clear `source_capture_id` back to `NULL` (§3c), and Postgres
  treats `NULL`s as distinct in a unique index. Once the first agent finishes,
  there's nothing left for the second to conflict with, so it inserts a
  duplicate capture row. Nothing downstream catches it either: the two agents
  mint their own separate archive directories, so `captures.html_path`'s
  `UNIQUE` constraint doesn't fire, and the `pages` upsert simply attaches both
  to the same page.

  Keeping `source_capture_id` populated forever does **not** fix it: that value
  is client-generated, and making a permanent dedup guarantee depend on it is
  precisely what §3c's collision retry loop exists because we can't do. One
  worker per job is the fix; a stronger idempotency key downstream is not.

  **A one-hour stale-claim window, not the 15 minutes used everywhere else** — a
  stuck `queue_item` has a human waiting to capture something, so reclaiming
  quickly is worth the risk of two devices racing. Nothing waits on a pending
  capture; the backend polls on its own schedule regardless. The cost of a short
  window is real: an ingestion still running when its claim expires lets a
  second agent in, the exact duplicate this exists to prevent.

  **No claimant column**, unlike `queue_items.claimed_by_token_id`: every agent
  presents the same service secret and has no per-instance identity, so
  `claimed_at` alone is all the stale-reclaim needs.

- **`GET /internal/pending-captures?user_id=`** — a read-only, user-scoped
  listing on the same path the claim `POST` uses. Different verb, different
  operation: the `POST` claims across every user and mutates `claimed_at`; the
  `GET` never claims, or a dashboard left open would starve the ingester. It
  exists because the window between "a device finished uploading" and "the
  backend has ingested it" was otherwise invisible — up to 30 minutes at the
  agent's default Worker poll interval, during which a capture looks like
  nothing happened at all. Surfaced as the Queue screen's third section. There's
  no status column to filter on: `(fetched_by_backend, claimed_at)` maps to
  waiting/ingesting/ingested, with no failed state — a row whose ingestion keeps
  failing is indistinguishable from one merely waiting its turn.

- **`POST /internal/pending-captures/cleanup`**, mirroring the queue-item sweep
  above, including its 72-hour retention window. **Only successfully ingested
  rows (`fetched_by_backend = 1`) are swept**; a row still at `0` is either
  waiting for pickup or failing ingestion repeatedly, and this table has no
  status column that could tell those apart, so both are kept indefinitely
  rather than risk discarding the only record of a capture that never landed.
  Surfacing and retrying persistently-failing rows needs an `attempts`/`error`
  column and something equivalent to `POST /queue/:id/fail` — not built yet.

`pending_links` (§17c) has the same three: an atomic claim
(`POST /internal/pending-links`), the fetched flag, and a cleanup sweep, with
the same one-hour stale-claim window and 72-hour retention. The claim is ordered
oldest-saved first, and its batches are smaller because a row can carry an
inline favicon. Unlike the `pending_captures` ones, these routes are not gated
by the links flag (§17f).

### Bookmark-list mirror (backend → D1 → the browser's native bookmarks)

- Separately from the queue, the extension syncs everything already archived
  into the browser's native bookmarks, not a custom in-popup list (§3j covers
  the extension side). This section covers the backend → D1 half.
- This is a **one-way, backend → D1 push** — the mirror-image of the credential
  mirror, keeping the same principle: the extension only ever needs to talk to
  the Worker/D1, never the backend.
- **Schedule-based, not triggered on individual mutations.** An event-triggered
  push wouldn't handle deletion (a deleted page was never "updated," it's just
  gone), and would require every future code path that touches `pages` to
  remember to also push a D1 update. A schedule doesn't care how or where
  Postgres changed; it just asks "what's different now" on its own cadence,
  triggered by `recueil agent` (§3e) the same way backend ingestion is.
- **The sync checkpoint is read directly from D1's data — `MAX(updated_at)`
  across `archived_pages`** — not a separately-tracked watermark on the backend,
  which would have to be kept correct by hand and could drift from what D1
  actually contains if a push silently fails partway. Deriving the checkpoint
  from D1's state makes that whole class of drift structurally impossible: the
  checkpoint and the data are the same read, by construction.
- **Two passes each sync cycle:**
  1. **Incremental upsert** — `pages WHERE updated_at > $checkpoint` (all of it,
     unpaginated: at this project's scale a full delta in one call is fine),
     pushed to D1 in one request.
  2. **Deletion reconciliation** — the only way a schedule-based sync can ever
     notice a deletion, since a deleted row was never "updated." The backend
     fetches D1's full current `page_id` set and its current Postgres `page_id`
     set, diffs them locally, and deletes from D1 whatever's no longer in
     Postgres.
- **Per-page mirror exclusion** —
  `pages.excluded_from_mirror BOOLEAN NOT NULL DEFAULT FALSE` (§10,
  dashboard-toggleable on PageDetail), a pure Postgres-side push filter with no
  D1 schema change. Both passes above handle it without special-casing:
  incremental upsert simply excludes it from the query, and deletion
  reconciliation's "desired set" excludes it too — an excluded page looks
  identical to a deleted one from that pass's point of view, so the same
  diff-and-delete logic removes it either way. Un-excluding just bumps
  `updated_at` like any other edit, so the page reappears on the next cycle.
- **The incremental push's atomicity is what makes the checkpoint safe without
  any extra ordering logic on the backend.** The push endpoint applies its whole
  batch via the Worker's `env.DB.batch()`, which is transactional: either every
  row lands, or none do. So there's no scenario where a partial failure leaves
  D1's `MAX(updated_at)` ahead of some unpushed row — either the full delta
  lands and the new max correctly reflects it, or nothing lands and the next
  cycle's `WHERE updated_at > $checkpoint` naturally retries the identical set.
- The extension does **not** live-sync this list either — it refreshes on a
  coarse schedule (see "Polling cadence" below) or on explicit user request,
  with **no incremental checkpoint at all**: it pulls the whole current list
  every time and diffs it locally (§3j). A scale-appropriate simplification: a
  personal archive is realistically hundreds to low-thousands of pages, nowhere
  near where an incremental `since` parameter would start to matter.

```sql
-- D1
CREATE TABLE archived_pages (
  page_id INTEGER PRIMARY KEY,      -- matches Postgres pages.id; never
                                     -- D1-generated, always supplied
                                     -- explicitly by the backend
  user_id INTEGER NOT NULL REFERENCES users(id),
  raw_url TEXT NOT NULL,
  title TEXT,
  latest_capture_at TEXT NOT NULL,
  updated_at TEXT NOT NULL           -- directly mirrors Postgres
                                     -- pages.updated_at -- not "when this
                                     -- D1 row was last written." The
                                     -- backend always sets this explicitly
                                     -- to the source value on every push,
                                     -- never lets D1 stamp its own clock --
                                     -- this is what makes MAX(updated_at)
                                     -- a meaningful sync checkpoint at all
);
CREATE INDEX idx_archived_pages_user_id ON archived_pages(user_id);
CREATE INDEX idx_archived_pages_updated_at ON archived_pages(updated_at);
```

### Polling cadence

Settled as **infrequent background polling with on-demand override**, rather
than tight polling or any push mechanism:

- Extension → queue (D1 via Worker): every 5-15 minutes in the background, plus
  a manual "check now" button in the extension popup.
- Extension → bookmark-list mirror refresh: coarse (e.g. once per day) or on
  explicit user request.
- Backend → `pending_captures` (D1 via Worker): every few minutes. No on-demand
  path is needed here since nothing is synchronously waiting on it.

No WebSocket/push infrastructure (e.g. a Durable Object) is used — that would be
real added infrastructure for a problem infrequent polling plus a manual refresh
button already solves adequately at this scale.

---

## 9. URL Normalization

Two URL fields are stored for every capture:

- **`raw_url`** — exactly what was captured, byte-for-byte.
- **`normalized_url`** — a computed, canonical form used purely as the
  dedup/grouping key that determines which `pages` row a capture belongs to.

### Runs in the backend

Normalization happens entirely backend-side (Go), at ingestion time — not in the
Cloudflare Worker, for two reasons:

- **Manual upload (§3d) has no Worker involved at all** — it's a direct
  dashboard→backend upload, bypassing R2/D1/the Worker entirely, so a
  Worker-side normalization step would simply never run for that path.
- **The Worker's "plain JS, no build step, no dependencies" constraint (§11)
  rules it out anyway.** ClearURLs' ruleset (below) has no dependency-free JS
  implementation to embed; whichever side implements it needs a real
  regex/JSON-parsing dependency, and only the backend is free to take one on.

### Pipeline architecture

Normalization is a **pipeline of independent steps**, not a single hardcoded
function, since ClearURLs is expected to be the first entry, not the only one —
a future step (a different library, a hand-rolled ruleset) can be added without
touching existing ones. Implemented as `internal/urlnorm`: a `Step` interface
(`Normalize(ctx, rawURL string) (string, error)`, string in/string out rather
than a shared parsed-URL representation, so an external library's string-based
API slots in trivially) and a `Pipeline` that runs a sequence of `Step`s.
Today's pipeline is two steps:

1. **ClearURLs** — strips known tracking parameters and unwraps redirect-wrapper
   URLs.
2. **Recueil's own additional canonicalization** — host/scheme casing, default
   ports, fragment, query-param ordering, trailing slash (below).

### ClearURLs: a Go port, vendored as a git submodule

Adopts the **ClearURLs** community-maintained ruleset (regex-based rules per
site/provider, actively maintained, LGPL-3.0) to strip known tracking parameters
(`utm_*`, `fbclid`, `gclid`, etc.) and unwrap tracking-redirect wrapper URLs,
without touching functionally meaningful query parameters.

- **The ruleset (`data.min.json`) is vendored as a git submodule** at
  `internal/urlnorm/clearurls-rules`, pinned to a specific commit and embedded
  directly via `go:embed` — the upstream project doesn't publish to any package
  registry, so a pinned submodule is the closest equivalent to a normal
  version-constrained dependency. Updating to a newer snapshot is a manual
  operation (advance the pin, commit that pointer change on its own).
- **`internal/urlnorm`'s `ClearURLs` type is a Go port of the real extension's
  algorithm**, not an inference from the ruleset's documentation, which
  describes the data shape but not every matching/precedence detail — every
  behavior was checked against the actual upstream JS source. Notably: providers
  are matched in the ruleset's file order, since a matched redirection
  short-circuits the rest of that pass; a full cleaning pass repeats until it
  produces no further change (a redirect unwrapping can reveal a URL a
  _different_ provider now matches); and rule/`referralMarketing` entries are
  matched as a full, case-insensitive, anchored match against the parameter
  name, not a substring.
- **Uses `github.com/dlclark/regexp2`, not stdlib `regexp`** — Go's stdlib RE2
  engine can't compile some patterns the real ruleset relies on (lookaround and
  similar PCRE-ish constructs).
- **Two upstream behaviors aren't ported**: `completeProvider` ("block this
  request outright") is a live-browsing concept with no meaning for a URL
  someone already chose to archive; `forceRedirection` is a live-tab
  navigation-interception technique with no meaning once you're transforming an
  already-known URL string rather than intercepting a real navigation.
  `redirections` itself (the URL-unwrapping transformation) _is_ ported —
  `forceRedirection` is a separate flag about _how_ a live browser performs that
  same unwrap during navigation.

### Recueil's additional canonicalization

Runs as the pipeline's second step, after ClearURLs has already stripped
tracking parameters and unwrapped redirects:

- Lowercase the host and scheme (Go's `net/url.Parse` doesn't lowercase the
  scheme itself, which matters for the default-port comparison below).
- Strip default ports (`:443` for `https`, `:80` for `http`).
- Drop the URL fragment unconditionally — the intended exception (preserving a
  fragment for a known SPA that encodes route state there) has no implementation
  yet (§16).
- Sort remaining query parameters alphabetically for a stable key.
- Strip trailing slash, including a bare root `/` — `example.com` and
  `example.com/` normalize identically.

---

## 10. Data Model

### Postgres (backend-owned — canonical archive)

`BIGINT GENERATED ALWAYS AS IDENTITY` primary keys are used throughout. Every
constraint (primary keys, unique constraints, checks, and foreign keys) is
explicitly named in the real migrations (`<table>_pkey`, `<table>_<column>_key`,
`<table>_<column>_check`, `<table>_<column>_fkey`) rather than left to
Postgres's auto-generated names — this makes a later
`ALTER TABLE ... DROP CONSTRAINT` referenceable by a name stated in the
migration file, rather than needing to look up whatever Postgres happened to
call it. The blocks below simplify constraint syntax for readability; the
migrations themselves are the source of truth for exact names.

```sql
CREATE TABLE users (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,       -- bcrypt
  pairing_token_enc TEXT,            -- AES-256-GCM, reversible; nullable --
                                      -- cleared on revoke, until regenerated
                                      -- (§5). Source for the D1
                                      -- pairing_token_hash mirror and for
                                      -- dashboard redisplay.
  role TEXT NOT NULL DEFAULT 'member',   -- 'admin' | 'member'
  display_name TEXT,                 -- nullable; UI falls back to username
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Dashboard sessions (§5) — DB-backed, hashed opaque tokens, same shape as
-- D1 device tokens. Revocation is a row delete (logout); no idle timeout,
-- only the absolute expires_at. user_agent is captured once at sign-in,
-- verbatim, and parsed at read time by the Active Sessions screen (§5).
CREATE TABLE sessions (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  session_hash TEXT NOT NULL UNIQUE,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  user_agent TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);

-- One row per distinct URL ever archived, grouped by normalized_url
CREATE TABLE pages (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  normalized_url TEXT NOT NULL,
  title TEXT,                        -- denormalized from latest capture,
                                      -- also directly PATCH-able as a
                                      -- manual override
                                      -- (PATCH /api/pages/{id}) -- a later
                                      -- recapture overwrites an override
                                      -- the same way it always overwrites
                                      -- this column; no separate
                                      -- title_override column
  latest_capture_at TIMESTAMPTZ NOT NULL,  -- also denormalized from latest
                                      -- capture (via GREATEST, tolerating
                                      -- out-of-order ingestion) -- feeds
                                      -- the D1 bookmark-list mirror's
                                      -- latest_capture_at column directly
  excluded_from_mirror BOOLEAN NOT NULL DEFAULT FALSE,  -- opt a page out of
                                      -- the D1 bookmark-list mirror (§8);
                                      -- purely a Postgres-side push filter,
                                      -- no corresponding D1 column exists
  favicon_path TEXT,                 -- denormalized from the latest
                                      -- capture's favicon_path (§3g),
                                      -- the same way title is -- including
                                      -- back to NULL if the latest capture
                                      -- didn't find one
  notes TEXT,                        -- free-text, user-authored
                                      -- (PATCH /api/pages/{id}) -- a light
                                      -- markdown subset (bold/italic/lists;
                                      -- src/lib/markdown.ts), stored as
                                      -- source and rendered client-side.
                                      -- Page-level like tags/
                                      -- collections, not per-capture.
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, normalized_url)
);
CREATE INDEX idx_pages_user_id ON pages(user_id);

-- One row per capture event: the version history
CREATE TABLE captures (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  page_id BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  source_capture_id TEXT UNIQUE,     -- transient ingestion-idempotency key
                                      -- (§3c); client-generated for the
                                      -- extension/queue flow, backend-
                                      -- generated for manual uploads (§3d);
                                      -- cleared back to NULL once ingestion
                                      -- of this capture is fully done
  source TEXT NOT NULL DEFAULT 'extension'  -- 'extension' | 'manual_upload'
    CHECK (source IN ('extension', 'manual_upload')),
  raw_url TEXT NOT NULL,
  title TEXT,
  html_path TEXT NOT NULL UNIQUE,    -- path relative to the backend's
                                      -- configured archive-directory root,
                                      -- zstd-compressed. UNIQUE is
                                      -- belt-and-suspenders, not primary
                                      -- defense (§4's os.Mkdir/EEXIST check
                                      -- runs first, before this constraint
                                      -- could ever fire)
  html_compressed_size_bytes INTEGER NOT NULL,
  html_uncompressed_size_bytes INTEGER NOT NULL,  -- both stored, not just
                                      -- the compressed size on
                                      -- disk, so the dashboard can surface
                                      -- real compression-ratio numbers
  thumbnail_path TEXT,               -- populated async by the screenshot
                                      -- job (§6a); NULL until then
  thumbnail_size_bytes INTEGER,
  thumbnail_hash TEXT,               -- sha256 of the thumbnail bytes --
                                      -- an integrity check;
  favicon_path TEXT,                 -- captured client-side alongside the
                                      -- HTML itself (§3g), so -- unlike
                                      -- thumbnail_path -- populated
                                      -- synchronously at ingestion, not by
                                      -- a later async job; NULL whenever no
                                      -- favicon was found, which is a
                                      -- normal, non-error outcome
  favicon_size_bytes INTEGER,
  favicon_hash TEXT,
  reader_text TEXT,                  -- Readability plain-text extraction;
                                      -- populated asynchronously by the
                                      -- readability job (§6b) -- NULL until
                                      -- that job completes, or permanently
                                      -- if it never succeeds
  readability_version TEXT,          -- vendored Readability.js version that
                                      -- produced reader_text; overwritten in
                                      -- place on re-extraction, no history
                                      -- kept (§6b)
  content_hash TEXT NOT NULL,        -- full-HTML hash
  reader_text_hash TEXT,             -- powers "unchanged since last capture";
                                      -- nullable for the same reason as
                                      -- reader_text above (§3b, §6b)
  ai_summary TEXT,                   -- populated asynchronously by the AI
                                      -- job (§7), once readability
                                      -- extraction has produced reader_text
  ai_model TEXT,                     -- which model produced ai_summary --
                                      -- kept alongside it so a summary can
                                      -- be regenerated later against a
                                      -- different model
  language REGCONFIG NOT NULL DEFAULT 'simple',  -- see below for why
                                      -- REGCONFIG, not TEXT, and why
                                      -- 'simple' as the fallback
  captured_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_captures_page_id ON captures(page_id);

ALTER TABLE captures ADD COLUMN reader_text_tsv tsvector
  GENERATED ALWAYS AS (to_tsvector(language, coalesce(reader_text, ''))) STORED;
CREATE INDEX idx_captures_fts ON captures USING GIN (reader_text_tsv);
```

**Full-text search is per-capture-language, not hardcoded to English.** Applying
English stemming rules to French or German text produces garbage tokens, since
stemming is language-specific by nature.

- **`language` is typed `REGCONFIG`, not `TEXT`.** Casting a language name to
  `regconfig` (`'french'::regconfig`) is a catalog lookup, which Postgres
  classifies as `STABLE`, not `IMMUTABLE` — and generated columns require an
  `IMMUTABLE` expression. Storing the already-resolved `regconfig` value
  directly means the generated `reader_text_tsv` expression
  (`to_tsvector(language, ...)`) is a plain column reference with no cast
  anywhere in it, satisfying the immutability requirement. The cast from a
  language name to `regconfig` still happens, just once, at INSERT/UPDATE time —
  an ordinary, unrestricted operation, not inside a generated expression.
- **Detection happens at ingestion**, parsing the captured HTML's
  `<html lang="...">` attribute (the standard HTML5 way a page declares its
  content language) — not guaranteed to be present or accurate, but a
  reasonable, zero-cost signal already sitting in every capture.
- **The detected tag is validated against this specific Postgres instance's live
  `pg_ts_config` catalog, not a hardcoded Go-side list of "languages Postgres
  supports."** Which configs are available depends on the running Postgres
  version; asking the live catalog is the only source that's authoritative for
  that.
- **Falls back to `'simple'`** — no language-specific stemming, but never
  actively wrong for any language — whenever there's no `lang` attribute, the
  detected tag has no known mapping (e.g. Chinese, Japanese, Korean: languages
  Postgres has no snowball stemmer for at all, since they need segmentation
  rather than stemming), or the mapped candidate doesn't actually exist on this
  Postgres instance.
- **The dashboard lets a user correct a capture's detected language after the
  fact** (`PatchCaptureLanguage`), choosing from whatever configs this Postgres
  instance has, or "Other" (mapping to `simple` — "simple" isn't a real
  language, and showing Postgres's internal name for "no stemming" as if it were
  one just reads as a stray option nobody explained) — a plain
  `UPDATE captures SET language = ...`, which Postgres automatically recomputes
  `reader_text_tsv` (and its GIN index) for as part of that same statement, the
  same way it already does whenever `reader_text` itself changes (e.g.
  re-extraction, §6b). No manual reindex, or extra synchronization code is
  needed.

```sql
CREATE TABLE tags (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, name),
  UNIQUE (user_id, slug)
);

-- Tags live on pages, not captures: tags describe the subject matter of
-- the URL, which doesn't change per-version. Both manual and AI-applied
-- tags coexist here, distinguished by `source`.
CREATE TABLE page_tags (
  page_id BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  tag_id BIGINT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'ai')),
  PRIMARY KEY (page_id, tag_id)
);
CREATE INDEX idx_page_tags_tag_id ON page_tags(tag_id);

-- Nested collections. Adjacency list (parent_id self-reference) rather
-- than a closure table: simpler writes, and at this project's scale a
-- recursive CTE for "this collection and all descendants" is fast enough
-- that a closure table's extra write-complexity isn't justified.
--
-- Uniqueness is per (user_id, parent_id, name), but that can't be a
-- single UNIQUE table constraint: parent_id is nullable for top-level
-- collections, and Postgres treats NULL as distinct from itself in a
-- unique constraint, so a plain UNIQUE(user_id, parent_id, name) would
-- silently allow two top-level collections named the same thing. Two
-- partial unique indexes instead — one per case, since each is a normal
-- (non-NULL) unique check within its own partition. slug gets the
-- identical treatment, for the identical reason — four partial indexes
-- total, not folded into a compound (name, slug) pair, so a name
-- collision and a slug collision each surface as their own distinct
-- conflict rather than an ambiguous "the pair wasn't unique" error.
CREATE TABLE collections (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  parent_id BIGINT REFERENCES collections(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  description TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX collections_user_id_name_top_level_key
  ON collections(user_id, name) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX collections_user_id_slug_top_level_key
  ON collections(user_id, slug) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX collections_user_id_parent_id_name_key
  ON collections(user_id, parent_id, name) WHERE parent_id IS NOT NULL;
CREATE UNIQUE INDEX collections_user_id_parent_id_slug_key
  ON collections(user_id, parent_id, slug) WHERE parent_id IS NOT NULL;
CREATE INDEX idx_collections_user_id ON collections(user_id);
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
CREATE INDEX idx_page_collections_collection_id ON page_collections(collection_id);

-- Explicit, bidirectional page-to-page relations — pairwise edges, not a
-- shared group/cluster concept: relating B and C to each other later
-- doesn't imply any relationship between A and C just because both are
-- related to B. Each relationship stored once, as a canonically-ordered
-- pair (the CHECK enforces page_id_a < page_id_b), rather than twice as
-- both A-B and B-A rows — "everything related to page X" is simply
-- WHERE page_id_a = X OR page_id_b = X, so there's no direction to get
-- backwards and no risk of a duplicate reverse-direction insert. The
-- ordering check also rules out a page being related to itself as a side
-- effect (page_id_a < page_id_b can never hold when they're equal). No user_id
-- column, same as page_tags/page_collections above: ownership is already
-- enforced by both referenced pages belonging to the same user. (Named
-- page_links in v1; renamed in v2 by migration 00016, §17b.)
CREATE TABLE page_relations (
  page_id_a BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  page_id_b BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (page_id_a, page_id_b),
  CHECK (page_id_a < page_id_b)
);
CREATE INDEX idx_page_relations_page_id_b ON page_relations(page_id_b);  -- the
                                      -- primary key covers page_id_a = X
                                      -- efficiently; this covers the other
                                      -- half of the bidirectional OR query

-- Links (§17): bookmark-only entries, their own entity rather than pages
-- with zero captures. raw_url is what was open in the tab (what the
-- dashboard opens); normalized_url is the same urlnorm output pages use, and
-- is what de-duplicates a link and lets the dashboard notice the same URL is
-- also an archived page. title and the favicon come from the extension's
-- live tab (the backend never fetches them), so all of them are nullable. The
-- favicon's size and hash are columns here, on the link, because a link has no
-- sub-entity (captures, for a page) to carry them: favicon_size_bytes is the
-- on-disk size, favicon_hash the SHA-256 of the original bytes, as on
-- captures. A re-save of the same normalized_url refreshes the one row (no
-- version history).
-- No separate idx_links_user_id: the unique constraint's index already
-- serves WHERE user_id = X.
CREATE TABLE links (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  raw_url TEXT NOT NULL,
  normalized_url TEXT NOT NULL,
  title TEXT,
  favicon_path TEXT,
  favicon_size_bytes INTEGER,
  favicon_hash TEXT,
  notes TEXT,
  source TEXT NOT NULL DEFAULT 'extension',
  excluded_from_mirror BOOLEAN NOT NULL DEFAULT FALSE,  -- opt a link out of the
                                      -- bookmark sync (§17j), as for pages
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT links_source_check CHECK (source IN ('extension', 'manual')),
  UNIQUE (user_id, normalized_url)
);

-- Tags and collections are the same rows pages use; only the membership
-- tables are separate. No source column, unlike page_tags: a link has no body
-- text for the AI job to read, so every link tag is manual. No user_id on
-- either, same as page_tags/page_collections.
CREATE TABLE link_tags (
  link_id BIGINT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
  tag_id BIGINT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (link_id, tag_id)
);
CREATE INDEX idx_link_tags_tag_id ON link_tags(tag_id);

CREATE TABLE link_collections (
  link_id BIGINT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
  collection_id BIGINT NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (link_id, collection_id)
);
CREATE INDEX idx_link_collections_collection_id
  ON link_collections(collection_id);

-- Links related to other links, as page_relations relates pages: one row per
-- pair, smaller id first, visible from both ends.
CREATE TABLE link_relations (
  link_id_a BIGINT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
  link_id_b BIGINT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (link_id_a, link_id_b),
  CHECK (link_id_a < link_id_b)
);
CREATE INDEX idx_link_relations_link_id_b ON link_relations(link_id_b);

-- A page related to a link, in either direction: two different kinds of item, so
-- just (page, link) with no ordering rule, one row per pair.
CREATE TABLE page_link_relations (
  page_id BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  link_id BIGINT NOT NULL REFERENCES links(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (page_id, link_id)
);
CREATE INDEX idx_page_link_relations_link_id ON page_link_relations(link_id);

-- Wayback Machine submissions (§17i): one row per user and URL, holding the state
-- of the latest attempt. last_submitted_at is the last SUCCESSFUL attempt's
-- submitted_at and survives a later failure.
CREATE TABLE wayback_submissions (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  normalized_url TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed')),
  job_id TEXT,                      -- archive.org's SPN2 job id
  submitted_at TIMESTAMPTZ NOT NULL,
  last_submitted_at TIMESTAMPTZ,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, normalized_url)
);
CREATE INDEX idx_wayback_submissions_pending
  ON wayback_submissions(submitted_at) WHERE status = 'pending';

-- Decoupled from captures so a capture remains fully valid/browsable
-- with zero AI enrichment ever having run. ai_summary/ai_model live on
-- captures directly (above), not here — a nullable column already gives
-- "fully valid with nothing populated" regardless of which table it's on,
-- and captures is where the dashboard already reads from.
CREATE TABLE ai_jobs (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  capture_id BIGINT NOT NULL REFERENCES captures(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'processing', 'done', 'failed')),
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  claimed_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ
);
CREATE INDEX idx_ai_jobs_capture_id ON ai_jobs(capture_id);

-- Retry/backoff bookkeeping for the async Readability extraction job (§6b),
-- one row per capture — same shape as ai_jobs above.
CREATE TABLE readability_jobs (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  capture_id BIGINT NOT NULL REFERENCES captures(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'processing', 'done', 'failed')),
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  claimed_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ
);
CREATE INDEX idx_readability_jobs_capture_id ON readability_jobs(capture_id);

-- Retry/backoff bookkeeping for the async screenshot job (§6a), one row per
-- capture — same shape as readability_jobs above, and intentionally its
-- own table rather than merged with it, even though both run through the
-- same headless-Chrome sidecar and often the same page load (§6:
-- independent failure modes, and re-extraction after a Readability.js
-- upgrade has no reason to redo a perfectly good screenshot).
CREATE TABLE screenshot_jobs (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  capture_id BIGINT NOT NULL REFERENCES captures(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'processing', 'done', 'failed')),
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  claimed_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ
);
CREATE INDEX idx_screenshot_jobs_capture_id ON screenshot_jobs(capture_id);

-- One row per user, user_id itself as the primary key rather than its own
-- identity column — a 1:1 extension of users (§5d), not a
-- one-to-many table, so there's no reason for a row to have an identity
-- distinct from the user it belongs to. No row exists until a user's
-- first PATCH /api/settings — there is no backfill migration and no
-- row-creation hook on any account-creation path.
CREATE TABLE user_settings (
  user_id BIGINT NOT NULL PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  language TEXT CHECK (language IN ('en', 'fr')),  -- NULL means no explicit
                                      -- override -- falls back to
                                      -- auto-detecting from the browser
                                      -- (§5d). A closed set, the
                                      -- same as `theme` below: adding a
                                      -- language is a migration.
  theme TEXT CHECK (theme IN ('light', 'dark')),  -- NULL means "automatic"
                                      -- (follow prefers-color-scheme). Same
                                      -- CHECK treatment as role/source/status
                                      -- elsewhere in this schema
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per agent cycle ("worker" or "local", matching
-- agent_worker_poll_interval_seconds/agent_local_poll_interval_seconds,
-- §3e), updated whenever that cycle completes with no step failing —
-- backs the recueil_agent_last_success_seconds metric (§13a).
CREATE TABLE agent_heartbeats (
  cycle TEXT PRIMARY KEY CHECK (cycle IN ('worker', 'local')),
  last_success_at TIMESTAMPTZ NOT NULL
);
```

There is **no `tokens` table in Postgres** — device tokens are owned entirely by
D1 (§5), and the dashboard uses its own DB-backed `sessions` table above, so no
bearer-token table is needed on the backend side at all.

### D1 (Worker-owned — auth, queue, bookmark mirror only)

D1 tables use `STRICT` (enforcing declared column types, since SQLite is
dynamically typed by default) and, where a table's primary key is non-integer
and only ever looked up by that key, `WITHOUT ROWID` (avoiding an unnecessary
hidden-rowid indirection).

`queue_items` and `pending_captures` use client-generated UUIDs rather than
server-generated identity columns, for idempotency on retry (see §3c) and
because the extension generates the ID before the row exists server-side.

```sql
-- Bookkeeping for the backend's D1 migration runner (§5b)
CREATE TABLE schema_migrations (
  id TEXT PRIMARY KEY,
  applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT, WITHOUT ROWID;

-- Mirrors Postgres users.id for device pairing without ever exposing the
-- backend. Holds only pairing_token_hash — no password-derived value of
-- any kind (§5). id is never D1-generated: it's always supplied explicitly
-- from the Postgres-side value on every mirror-push INSERT, so plain
-- `INTEGER PRIMARY KEY` (rowid alias, not AUTOINCREMENT) is correct here.
-- `username` is dropped entirely: pairing is single-credential (submit the
-- pairing token, no username), so the Worker never needs to look a user up
-- by name. `pairing_token_hash` is nullable — a revoked user
-- (`DELETE /api/pairing-token`, no reissue) has this cleared to `NULL` until
-- they regenerate.
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  pairing_token_hash TEXT UNIQUE,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT;

CREATE TABLE tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_hash TEXT NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id),
  device_name TEXT NOT NULL,
  device_type TEXT NOT NULL,        -- 'extension' | 'pwa' | 'cli' | 'shortcut'
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
--
-- added_by_token_id/claimed_by_token_id are ON DELETE SET NULL: device
-- revocation (§8) is DELETE FROM tokens, and without SET NULL that DELETE
-- throws a foreign key violation the moment the revoked device has ever
-- added or claimed a queue item. SET NULL is what makes the "revoked
-- device leaves nothing to name" LEFT JOIN behavior described below true.
--
-- manual_retry (§8) lets a failed item become claimable again without
-- losing its `failed` status for the Queue screen's display purposes --
-- cleared automatically the moment a device claims it.
CREATE TABLE queue_items (
  id TEXT PRIMARY KEY,              -- client-generated UUID
  user_id INTEGER NOT NULL REFERENCES users(id),
  url TEXT NOT NULL,
  added_by_token_id INTEGER REFERENCES tokens(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | claimed | captured | failed
  claimed_by_token_id INTEGER REFERENCES tokens(id) ON DELETE SET NULL,
  manual_retry INTEGER NOT NULL DEFAULT 0,
  claimed_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT, WITHOUT ROWID;

-- Note there is no denormalized device-name column here: the dashboard's
-- listing endpoint resolves claimed_by_token_id through a LEFT JOIN against
-- tokens at read time (§8). A copy would only be a second thing to keep
-- correct when a device is renamed or revoked.
CREATE INDEX idx_queue_items_user_status ON queue_items(user_id, status);
CREATE INDEX idx_queue_items_added_by_token_id ON queue_items(added_by_token_id);
CREATE INDEX idx_queue_items_claimed_by_token_id ON queue_items(claimed_by_token_id);

-- Completed captures awaiting backend pickup from R2. r2_key_favicon (§3g)
-- is the one exception to "the extension only ever uploads HTML": a favicon
-- is a separate resource that has to be fetched, not derived from
-- the already-captured HTML, so it stays a client-upload concern -- nullable,
-- since not every capture has one.
CREATE TABLE pending_captures (
  id TEXT PRIMARY KEY,              -- client-generated UUID
  user_id INTEGER NOT NULL REFERENCES users(id),
  queue_item_id TEXT REFERENCES queue_items(id),  -- null for direct captures
  url TEXT NOT NULL,
  r2_key_html TEXT NOT NULL,
  r2_key_favicon TEXT,               -- e.g. ".../favicon.svg" -- the real
                                      -- extension lives in the key itself
                                      -- (§3g), not a separate mime column
  captured_at TEXT NOT NULL,
  fetched_by_backend INTEGER NOT NULL DEFAULT 0,
  claimed_at TEXT,                   -- backend pickup is an atomic claim,
                                      -- not a plain read (§8) -- without it
                                      -- two agent processes both ingest the
                                      -- same row and the second silently
                                      -- writes a duplicate capture. No
                                      -- claimant column, unlike
                                      -- queue_items.claimed_by_token_id:
                                      -- every agent presents the same
                                      -- service secret and has no
                                      -- per-instance identity
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
) STRICT;

CREATE INDEX idx_pending_captures_user_id ON pending_captures(user_id);
CREATE INDEX idx_pending_captures_queue_item_id ON pending_captures(queue_item_id);
CREATE INDEX idx_pending_captures_fetched_by_backend ON pending_captures(fetched_by_backend);

-- Links saved by a device (§17), awaiting backend pickup: pending_captures'
-- counterpart, with the same client-generated id and atomic claim. No HTML,
-- only a title and a favicon, which arrives one of two ways: inline up to the
-- Worker's cap (parked here as base64 in favicon_data), or through R2 (only
-- its key, in r2_key_favicon, whose extension carries the format). favicon_ext
-- is set either way, with exactly one of the two -- the CHECKs say so. Marking
-- a row fetched clears all three. saved_at is normalized to UTC ISO-8601, so
-- the backend's oldest-saved-first claim is chronological.
CREATE TABLE pending_links (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  queue_item_id TEXT REFERENCES queue_items(id),  -- nullable: a direct save
  url TEXT NOT NULL,
  title TEXT,
  favicon_ext TEXT,
  favicon_data TEXT,
  r2_key_favicon TEXT,
  saved_at TEXT NOT NULL,
  fetched_by_backend INTEGER NOT NULL DEFAULT 0,
  claimed_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CHECK (favicon_ext IS NULL OR favicon_ext IN ('svg', 'png', 'ico')),
  CHECK (
    (favicon_ext IS NULL
      AND favicon_data IS NULL AND r2_key_favicon IS NULL)
    OR
    (favicon_ext IS NOT NULL
      AND (favicon_data IS NULL) <> (r2_key_favicon IS NULL))
  )
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_pending_links_user_id ON pending_links(user_id);
CREATE INDEX idx_pending_links_queue_item_id ON pending_links(queue_item_id);
CREATE INDEX idx_pending_links_fetched_by_backend ON pending_links(fetched_by_backend);

-- Bookmark-list mirror, kept in sync by the backend's scheduled sync
-- job (internal/mirror.Syncer -- see §8 for the full design), not pushed
-- on individual mutations. Pulled by the extension on its own coarse/
-- on-demand schedule.
CREATE TABLE archived_pages (
  page_id INTEGER PRIMARY KEY,      -- matches Postgres pages.id; never
                                     -- D1-generated
  user_id INTEGER NOT NULL REFERENCES users(id),
  raw_url TEXT NOT NULL,
  title TEXT,
  latest_capture_at TEXT NOT NULL,
  updated_at TEXT NOT NULL          -- mirrors Postgres pages.updated_at
                                     -- verbatim -- the sync checkpoint
                                     -- itself (§8), not D1's clock
) STRICT;
CREATE INDEX idx_archived_pages_user_id ON archived_pages(user_id);
CREATE INDEX idx_archived_pages_updated_at ON archived_pages(updated_at);

-- The links counterpart (§17j), for the extension's "recueil links" bookmark
-- folder: pushed by the same sync job, links that are excluded_from_mirror never
-- reaching it. No latest_capture_at, which a link has no equivalent of.
CREATE TABLE archived_links (
  link_id INTEGER PRIMARY KEY,      -- matches Postgres links.id
  user_id INTEGER NOT NULL REFERENCES users(id),
  raw_url TEXT NOT NULL,            -- the link's raw_url, not the normalized one
  title TEXT,
  updated_at TEXT NOT NULL          -- links.updated_at verbatim (the checkpoint)
) STRICT;
CREATE INDEX idx_archived_links_user_id ON archived_links(user_id);
CREATE INDEX idx_archived_links_updated_at ON archived_links(updated_at);
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
| Desktop browser extension | WebExtensions (Chrome/Firefox compatible)                                                               | Worker + R2 only                                                   | Poll queue, capture HTML via vendored SingleFile (no Readability — see §3a/§6b), upload to R2                                                                  |
| Share-sheet PWA           | Static site, served as Cloudflare Workers static assets bound to the same Worker                        | Worker only                                                        | Android share-target: enqueue a URL, nothing else                                                                                                              |
| iOS Shortcut              | Apple Shortcuts                                                                                         | Worker only                                                        | Enqueue a URL from iOS share sheet                                                                                                                             |
| CLI                       | Small script/binary                                                                                     | Worker only                                                        | Enqueue URLs, scriptable                                                                                                                                       |
| Cloudflare Worker         | Plain JS (ES modules), no build step — `@ts-check` + JSDoc for static type-checking, ESLint for linting | Public                                                             | Device auth (checks D1 credential mirror), issues bearer tokens, presigned R2 URLs, D1 read/write, service-secret-gated backend endpoints                      |
| D1                        | Cloudflare D1 (SQLite)                                                                                  | N/A (accessed via Worker only, except backend migrations — §5b)    | Device tokens, queue, bookmark-list mirror, schema-migration bookkeeping                                                                                       |
| R2                        | Cloudflare R2                                                                                           | N/A (accessed via presigned URLs)                                  | Temporary blob storage between capture and backend pickup                                                                                                      |
| Backend                   | Go + Postgres, Docker Compose                                                                           | Outbound-only for archiving; inbound optional (dashboard, LAN/VPN) | Pull from R2, compress, store, version, search, tags, collections, AI enrichment, dashboard session auth, dashboard API, Postgres + D1 schema migrations (§5b) |
| Headless-Chrome sidecar   | chromedp + `chromedp/headless-shell`, Docker                                                            | Backend-internal only (no inbound, no outbound)                    | Renders already-captured inlined HTML offline; produces thumbnails (§6a) and Readability extractions (§6b)                                                     |
| Dashboard                 | Svelte                                                                                                  | Same as backend                                                    | Library browsing, search, reader view, version history, tags, collections, user/session management                                                             |

---

## 12. Deployment

- **Backend**: Docker Compose, bundling the Go backend, Postgres, and the
  headless-Chrome sidecar (§6) as services. Postgres's data directory and the
  local archive directory both use **bind mounts** (not named volumes) so an
  external backup tool can snapshot them directly from the host (see §14).
- **Cloudflare side**: Terraform/OpenTofu module in the public repo,
  provisioning D1, R2, the Worker (and its routes/bindings, plus the share-sheet
  PWA's static files bound to that same Worker), a `random_password` resource
  for the backend↔Worker service secret (§5a), and a `cloudflare_api_token`
  resource scoped to `D1:Edit` on the D1 database for the backend's migration
  runner (§5b) — both output as `sensitive`, to be copied into the backend's
  `.env` after `terraform apply`.
- **Networking**: the repo takes no position on how the backend/dashboard is
  exposed beyond the local machine — that's a deployment-time decision left to
  the operator (LAN-only, reverse proxy, VPN, tunnel, etc.). The core archiving
  flow (extension/PWA/CLI → Worker → R2 → backend polling) works identically
  regardless of that choice, since it never depends on backend reachability.

---

## 13. Repository Layout

A monorepo, structured flat by "what a thing is" rather than by architectural
layer — a component only gets its own directory when it needs isolation (its own
build tooling, dependency manifest, or, for the Worker/PWA, the hard requirement
of having no build step at all). See the root README for the current layout and
package map.

### 13a. Implementation Stack & Tooling

Build, test, and lint tooling is documented in the root/extension/terraform
READMEs, not here — this section covers only tooling choices that reflect a real
architectural constraint, not a build-process detail.

- **`recueil server` owns a single signal-aware context**
  (`signal.NotifyContext` on `SIGINT`/`SIGTERM`), passed down via
  `cmd.Context()` rather than each subcommand creating its own — this is what
  its graceful shutdown waits on to stop accepting requests cleanly.
- **The archived-HTML endpoint sends a defensive
  `Content-Security-Policy: script-src 'none'`**, even though SingleFile's
  capture already strips scripts — belt-and-suspenders, since that response is
  served same-origin with the authenticated dashboard, and anything that slipped
  through would otherwise run with access to the session cookie.
- **`internal/metrics` is Postgres-only, even where D1 has the more complete
  picture** — real queue depth (`queue_items`/`pending_captures` counts) lives
  only in D1, and a typical Prometheus scrape interval hitting the Worker on
  every tick risks the Cloudflare free tier for no operational benefit worth
  that cost. Every `(job, status)` combination is emitted explicitly on every
  scrape, including zeros, rather than only whatever a scrape's query happens to
  return — PromQL's `rate()`/`sum()` behave far more predictably against a
  continuously-present-at-0 series than one that silently appears and
  disappears. `recueil_agent_last_success_seconds{cycle}` is gated on every step
  of that cycle succeeding, not just the cycle running, since recording a
  heartbeat regardless of outcome would hide exactly the failure it exists to
  catch.
- **Links and Wayback submissions are reported the same way** (§17). Wayback
  submissions are a fourth `recueil_jobs_total` / oldest-pending-age job type
  (`job="wayback"`), in the series' own vocabulary — a submission's `succeeded`
  reports as `done` — and, since they are one row per URL holding its latest
  attempt, they count URLs by state rather than every submission ever made.
  `recueil_links_total` is the link count, and the link favicon store's
  footprint is `recueil_storage_bytes{kind="link_favicon"}`, kept separate from
  page favicons (its own series, from its own query, because the two stores are
  separate). All of it is always emitted, zeros included, even with the features
  off. Pending saved links (the extension's saves not yet ingested) live only in
  D1, so, like pending captures, they are deliberately not reported; the
  Postgres-only rule is why. The agent's heartbeats already cover the new work:
  link ingestion is in the worker cycle and the Wayback sweep in the local
  cycle, and a cycle's heartbeat is gated on every step of it succeeding.
- **Testing convention: no mocks for anything that talks to a real dependency.**
  DB-touching code runs against a real Postgres (`internal/dbtest`); code that
  calls an external HTTP API runs against a real `httptest.Server`; the
  sidecar-driving jobs (§6) run against a real `chromedp` instance.
  `internal/httpapi` handler tests are external `_test` packages, exercising
  only exported constructors the way a real caller would.
- **The dashboard is plain Svelte 5 + Vite (no SvelteKit)** — the session model
  is already a same-origin `httpOnly`/`SameSite=Lax` cookie (§5) checked via
  ordinary chi middleware, so there's no SSR or server-side data-loading need
  SvelteKit's extra layer would earn its keep for.
- **Frontend tests of Svelte 5 runes need `resolve.conditions: ['browser']`
  alongside `environment: 'jsdom'`** in the Vitest config — without it, `$state`
  silently resolves to Svelte's inert SSR runtime rather than a live reactive
  signal, testing the wrong thing without ever failing loudly.

This isn't comprehensive — it's the subset of tooling decisions that would
otherwise cost real debugging time to rediscover. Visual/UX design tokens and
patterns live in `DESIGN_SYSTEM.md`, not here.

---

## 14. Backup & Restore

**The application performs no automated backup.** This is an intentional choice:
baking `pg_dump` (or equivalent) into the backend's image or shelling out to it
from the Go binary is an awkward dependency for an application binary to carry,
and commits the project to tracking Postgres version compatibility indefinitely.
Instead, backup is documented as the operator's responsibility.

### What must be backed up

Two things (three, with links enabled), together, on the same schedule:

1. **The Postgres database** — via `pg_dump` or equivalent. Note that copying
   Postgres's raw data directory while the container is running is **not** safe
   without WAL-aware tooling — a proper dump (or a backup tool that understands
   Postgres's on-disk format) is required, not a raw file copy.
2. **The local archive directory** (zstd-compressed HTML + thumbnails) — a plain
   directory copy/sync is fine here, since these are static files once written.
3. **The link favicon directory** (§17e), when links are enabled — the same kind
   of plain directory copy.

Both bind-mount to the host filesystem in the example `compose.yaml`/README
config (`./data/postgres`, `./data/archive`), not named Docker volumes: the
whole point is that both are real, inspectable paths on the host, readable by
any external backup tool directly, without going through Docker at all.

**Consistency matters**: if the two are backed up on different schedules or by
different mechanisms, a restore can leave a `captures` row pointing at an
`html_path` that wasn't captured in the same backup window, or vice versa. Both
should be backed up in the same job/window.

### Restore

- **`captures.html_path` is stored relative to the backend's configured
  archive-directory root**, not as an absolute path — the operator can restore
  to any location and simply point the (already-required) archive-directory
  config value at it, move the archive directory later without a database
  migration, and the database itself doesn't bake in one host's specific
  filesystem layout. One real constraint: whatever archive-directory path the
  backend is configured with at restore time must actually contain the restored
  files at the expected relative layout (§4 covers the on-disk sharding) — the
  config value can point anywhere, but it does have to point somewhere real.
- After restoring Postgres from a backup, the **D1 credential mirror can be
  stale** relative to the restored state (e.g. password changes or account
  creations made after the backup was taken won't be reflected, or deleted/
  changed accounts may still have valid mirrored credentials).
  **`recueil user resync`** re-runs the same idempotent `mirror.PushUser` push
  already used at create/regenerate/revoke time across every account: decrypts
  each account's `pairing_token_enc` where present, re-hashes it, and re-pushes
  it (or pushes `nil` for an account with a revoked/NULL token, clearing any
  stale D1 hash left over from before the restore). CLI-only, not an admin
  dashboard action, matching the operator-only precedent already set for device
  management. Should be run after any Postgres restore.

---

## 15. MCP Server

Read-only access to a user's archive (and, with links on, their saved links) for
local MCP clients. Intentionally scoped to only answering questions about the
archive, not manipulating it — no write tools right now and none are planned.

**Transport and reachability.** Streamable HTTP, mounted at `POST /mcp` on the
existing backend HTTP server (`internal/httpapi.NewRouter`) — reachable wherever
the dashboard already is (LAN, VPN, or tunnel, etc; see §12), nothing routed
through the Worker/D1. `internal/mcpapi` is a sibling to `internal/httpapi`, not
a subpackage of it: both are HTTP-facing surfaces over the same
`internal/db`/`internal/auth`, and `/mcp` is mounted in `router.go` alongside
`/api`, not nested under it.

**Stateless mode.** `StreamableHTTPOptions.Stateless = true`. Required outright
for the `2026-07-28` protocol revision (session resumability -- Last-Event-ID,
standalone GET -- is dropped from that revision entirely), and a reasonable
default regardless: this is a single-process backend, so there's no
session-affinity/sticky-routing problem to design around by picking it. Clients
on an older protocol revision still negotiate down automatically; the SDK
(`v1.7.0`) supports every revision from `2024-11-05` through `2026-07-28` in one
build.

**Tools** (all read-only, all scoped to the authenticated user via
`auth.UserFromContext`, same as every `internal/httpapi` handler):

- `search_archive(query, limit?)` — wraps `SearchPages`.
- `list_recent(limit?)` — wraps `ListPages` with no query.
- `list_tags()` — wraps `ListTags`.
- `list_pages_by_tag(tag_slug, limit?)` — `GetTagBySlug` first (ownership
  check + resolves the id), then `ListTagPages`.
- `list_collections()` — wraps `ListCollectionsByUser`.
- `list_pages_by_collection(collection_id, limit?)` — `GetCollectionByID` first
  (same ownership-check-then-list shape as tags), then `ListCollectionPages`.
- `get_page(page_id, capture_id?)` — page metadata (title, url, notes, tags,
  collections) plus one capture's `reader_text`/`ai_summary`, defaulting to the
  latest capture. _Not_ a 1:1 mirror of the dashboard's `GET /api/pages/{id}` +
  `GET /api/captures/{id}` split: those stay separate because a page-detail
  _view_ shouldn't eagerly load every capture's full text, but a single MCP tool
  call is the unit of work being asked for, and forcing two round trips for the
  common case ("give me this page's content") doesn't serve that. The other
  available captures (id + date, no text) are listed alongside, so a model can
  ask for a specific `capture_id` without a separate "list this page's versions"
  tool. `capture_id`, when given, is checked against the resolved page's id
  before its text is returned — `GetCaptureByIDForUser` only scopes by
  `user_id`, not by the specific page passed alongside it, so without this check
  a caller could name a `page_id` it owns and a `capture_id` belonging to a
  _different_ one of its own pages and get back metadata from one page paired
  with content from another.

**Link tools** (§17), registered only when the links feature is on
(`mcpapi.NewHandler`'s `enableLinks`; with it off a client never sees them),
read-only like the rest, and in `internal/mcpapi/links.go`:

- `search_links(query?, tag_slug?, collection_id?, limit?)` — wraps
  `SearchLinks` (a case-insensitive substring match over title, URL and notes,
  with LIKE's wildcards escaped; a link has no body text to full-text search)
  or, with no query, `ListLinks`, newest saved first. `tag_slug` and
  `collection_id` narrow it the way they do `GET /api/links`: each is checked
  against the caller's own first (a normal error result otherwise, as
  `list_pages_by_tag` does), and a collection means its direct links only. One
  tool rather than a `list_recent` and a `list_links_by_tag` and so on, since
  each of those would be this one with a filter, and every tool a model is shown
  costs context.
- `get_link(link_id)` — a link's title, URL (as saved, not normalized), notes,
  tags and collections by name, and save time.

Both return **`page_id`** when the same URL is also an archived page (the
same-URL hint, §17h): a link has no content of its own, so that is how a model
gets from "I saved this" to the page's full text, by passing it to `get_page`.
The existing tag and collection tools need no link versions, because tags and
collections are shared with pages (§17b) and the link tools take them as
filters.

**`limit` handling.** `SearchPages`/`ListPages` already take `limit`/`offset`
and return `total_count` via a window function — reused as-is, default 20,
capped at 100. `ListTagPages`/`ListCollectionPages` have neither (dashboard-
scale, unpaginated by design, per those queries' comments) — fetched in full and
sliced to the same default/cap in Go, rather than adding a new query variant for
two call sites.

---

## 16. Known Limitations

- **Safari packaging.** The extension is MV3-capable on Safari, but Safari
  requires a separate packaging/distribution pipeline (Xcode-wrapped, via
  `safari-web-extension-converter`) rather than loading the same build the other
  browsers use. Not attempted yet — see §3h.
- **Fragment-aware URL canonicalization for SPAs.** `urlnorm.Canonicalize` drops
  every URL fragment unconditionally. The intended exception — preserving a
  fragment for a known SPA that encodes meaningful route state there — has no
  implementation and no site list to check against yet. See §9.

---

## 17. Links (Bookmark-Only Entries)

> **Status: designed, not yet implemented.** This section records the agreed
> design for a post-v1 feature so it can be built across sessions without
> re-deciding anything. As pieces ship, fold them into the sections they belong
> to (§3, §5, §8, §10, §15) and correct anything that turned out differently in
> practice. Anything under "Open questions" (§17m) is deliberately undecided.

### 17a. Motivation and scope

Recueil archives knowledge and articles. Some things worth keeping a pointer to
can't be archived well by that model: videos, git repositories, multi-page sites
where capturing every page is impractical, and JavaScript-heavy apps and demos
that SingleFile's output reduces to nothing. §1's format decision (one inlined
HTML file per capture) is right for articles and is **not** relaxed by this
feature.

A **link** is a bookmark-only entry for those cases: a URL with the niceties
recueil already has — a locally stored favicon, tags, collections, a one-way
sync into the browser's native bookmarks — plus on-demand submission to the
Internet Archive's Wayback Machine, which can do a better job on the tricky
pages than recueil can (§17i).

The alternative considered was running a dedicated bookmark manager (linkding)
alongside recueil. Rejected for the same reason §1 gives for replacing the
previous setup: two tag systems, two collection trees, nothing relating them,
and an MCP server (§15) that would only ever see half of what's saved.

**Non-goals:**

- Two-way bookmark sync. §3j's rule applies unchanged: recueil owns its folder
  and manual edits are overwritten.
- Replacing the browser's own bookmark manager.
- Any server-side fetching or crawling, including to backfill missing titles or
  favicons (§17c).

### 17b. A separate entity, not a state of `pages`

Considered and rejected: a link as a `pages` row with zero captures. Every
existing query that joins `captures` (library listing, search, MCP `get_page`,
the D1 mirror, whose `latest_capture_at` is `NOT NULL`) would have to tolerate a
captureless page, which is a risky change to released v1 behavior for no real
benefit. Links are instead their own first-class tables, and an existing setup
sees no change in any existing behavior.

**Naming.** "Links" in code, API, docs and UI. "Bookmark" already means the
archived-pages mirror (§3j, §8), and reusing it would be confusing.

**Data model.** The `links`, `link_tags` and `link_collections` tables are in
§10 (migration `00014_create_links.sql`). A link has `raw_url`, `normalized_url`
(unique per user, from the same urlnorm pipeline as pages, §9), a nullable
`title` supplied by the extension (§17c), a favicon (`favicon_path`,
`favicon_size_bytes`, `favicon_hash`, all nullable; they live on the link
because it has no sub-entity like a page's captures to carry them), nullable
`notes`, and a `source`: where the current title and favicon came from, as
`captures.source` is for a capture, `extension` (read from a live browser tab)
or `manual` (typed into the Add link form, §17h; not `manual_upload` as on
captures, since nothing is uploaded). Saving the same normalized URL again
refreshes that one row, faithfully to what the extension just saw in the live
tab: `raw_url`, `title` and the three favicon columns all take the new values,
including `NULL`. A site that has stopped serving a favicon (or a title) doesn't
keep showing the old one, the same way `pages.favicon_path` follows the latest
capture; the old favicon file is simply no longer referenced, and gc reclaims it
(§17e). The favicon columns always move together, and `source` follows the
latest save too, so a manually added link saved again from the browser becomes
an `extension` link (its title and favicon now come from the tab). `notes` are
the user's own and `created_at` is the first save; a re-save touches neither.
The queries (`queries/links.sql`, `link_tags.sql`, `link_collections.sql`,
`link_relations.sql`) mirror the page equivalents.

**Related links.** A link can be related to other links as a page can be related
to other pages, with a table of the same shape, `link_relations`: one row per
pair with the smaller id first (a `CHECK` that also rules out relating a link to
itself), no `user_id` of its own (the handlers verify both links belong to the
requester before writing a pair), cascading when either link is deleted, and
visible from both ends. The API mirrors the page routes:
`GET /api/links/relation-candidates` (title-or-URL typeahead, capped like the
page picker), `POST /api/links/{id}/related-links` with `{"link_id": …}` and
`DELETE /api/links/{id}/related-links/{relatedLinkId}`, and the link detail
response carries `related_links`. The screen calls the section "Related links".
The MCP tools don't expose any relationship.

**Naming relationships.** Every relationship between two items is named by _what
it relates_, so "links" never means two things at once (it had come to mean the
entity, a page related to pages, and a link related to links). Before v2 the
page version was `page_links`, `/api/pages/{id}/links` and the `links` field on
a page, shown as "Linked pages". For v2, a major release and so the time for a
breaking change, it became:

|             | Table                 | Routes                                                           | Response field                                       | UI section                                       |
| ----------- | --------------------- | ---------------------------------------------------------------- | ---------------------------------------------------- | ------------------------------------------------ |
| page ↔ page | `page_relations`      | `/api/pages/{id}/related-pages`                                  | `related_pages`                                      | Related pages                                    |
| link ↔ link | `link_relations`      | `/api/links/{id}/related-links`                                  | `related_links`                                      | Related links                                    |
| page ↔ link | `page_link_relations` | `/api/pages/{id}/related-links`, `/api/links/{id}/related-pages` | `related_links` on a page, `related_pages` on a link | Related links on a page, Related pages on a link |

The pickers are `GET /api/pages/relation-candidates` and
`GET /api/links/relation-candidates`, and a request body names the target by its
kind, `{"page_id": …}` or `{"link_id": …}`.

**Relating a page to a link** is the third table, and it is just
`(page_id, link_id)`: two different kinds of item, so there is no ordering rule
and no `CHECK`, one row per pair, cascading when either end is deleted, and like
the others no `user_id` of its own (the handlers verify both ends belong to the
requester). Both ends have routes, named by what they relate, and write the same
row: `POST /api/pages/{id}/related-links` with `{"link_id": …}` and
`POST /api/links/{id}/related-pages` with `{"page_id": …}`, each with a matching
`DELETE …/{relatedLinkId}` or `…/{relatedPageId}`. A page's detail carries
`related_links` (always empty with links off, as the same-URL hint is always
null then) and a link's carries `related_pages`. **A page and a link with the
same URL can be related**: the same-URL hint already ties those, relating them
is harmless, and filtering them out of the picker would cost more than it saves.
The pickers are the existing ones for the kind being searched, called without an
exclusion since a page's id and a link's id can't be compared. The screens show
it as a second section on each: "Related pages" and "Related links" on both a
page and a link, one list per kind, each searching only its own kind. A merged
mixed list is a possible later change. Migration `00016` renames `page_links`,
its constraints and its index; its `Down` is the exact reverse. **It is the one
v2 migration that changes something v1 uses.** Every other v2 migration only
adds tables, which an older binary never looks at (goose doesn't object to a
database that is ahead of the migrations it embeds), so rolling back to v1 is
normally just the image tag. With this rename it also takes reverting `00016`,
which nothing in recueil runs by itself (`recueil` only ever applies migrations
upward); the upgrade guide gives the one goose command.

- **Tags and collections are shared with pages** (working assumption, to be
  confirmed when the UX is mocked up, §17h): the existing `tags`/`collections`
  tables are reused with the new join tables above, so one tag or collection can
  span both pages and links. The cost is that anything listing or counting a tag
  or collection (tag counts, collection views, MCP) has to consider both
  entities.
- **No jobs.** A link has no HTML, so there is no screenshot, readability or AI
  job, no `reader_text`, and no AI tags (`source` on `page_tags` has no
  equivalent here — every link tag is manual). Search over links is over title
  and URL (and notes).
- **No versions.** A link is one row, not a page-with-history.
- Wayback submission state is covered in §17i.

### 17c. Capture path

The extension gets a second action beside the full capture: **save as link**. It
stores the tab's favicon and title and nothing else — no HTML.

This extends §1's principle rather than exempting an exception to it. The save
happens in the real, already-authenticated tab, so the extension already has
everything it needs (the same reasoning as §3g's client-side favicon capture),
and the backend still never touches the live web. The corollary is that **the
backend never fills in missing metadata server-side** (no title fetch, no
favicon fetch): a link that arrives without them simply has none, and the
dashboard falls back to the URL and the existing `Globe` icon.

**Adding from a phone, Shortcut or CLI** is unchanged: those only enqueue a URL
(§8), and the user later picks, in the desktop extension, whether to capture it
or save it as a link (§17d). A link can also be added by hand in the dashboard
(§17h), with a title and favicon typed or picked there; that route talks to the
backend directly, and recueil still never fetches a title or favicon itself.

**Transport** follows the existing pattern in §3/§8, since the extension only
ever talks to the Worker. A bearer-token `POST /links` writes a row to a new D1
table, `pending_links` (§10), whose client-generated UUID id makes a retried
save a recognizable no-op (`204`, checked before the queue item's status, as in
the capture completions). The backend claims rows on the agent's Worker ticker
(§3e), ingests them into Postgres, writes the favicon to the link store (§17e),
flags the row fetched, and a cleanup sweep removes old fetched rows (72 hours,
only `fetched_by_backend = 1`), all the way `pending_captures`' does (§8).
Favicon selection, the "no image processing" rule and "best-effort, never fails
the save" (§3g) apply unchanged.

The Worker endpoints:

- **`POST /links`** (device): the direct save, for a page the user is already on
  (the counterpart of `POST /captures/complete`). Body: `link_id`, `url`,
  `saved_at`, and optionally `title` and a favicon (below).
- **`POST /queue/:id/complete-link`** (device): the same save for a claimed
  queue item (the counterpart of `POST /queue/:id/complete`, §17d). The body is
  the direct save's minus `url`, which comes from the queue item.
- **`POST /links/upload-urls`** (device): a presigned R2 `PUT` for a favicon too
  large to send inline, the favicon half of `POST /captures/upload-urls` with
  the same checksum binding and stateless signing. The key,
  `pending-links/{user_id}/{link_id}/favicon.{ext}`, is always recomputed by the
  Worker, never taken from a client, and its prefix keeps these objects apart
  from captures' `pending/`.
- **`POST /internal/pending-links`**,
  **`POST /internal/pending-links/:id/fetched`** and
  **`POST /internal/pending-links/cleanup`** (backend, service secret): the
  atomic claim, the fetched flag, and the sweep, each the counterpart of the
  `pending_captures` one. A claim returns smaller batches (default 25, cap 100)
  because a row can carry an inline favicon.

**Validation at the door.** Only what would otherwise leave a row the backend
can never ingest, plus what the capture routes already check. `url` (direct
save) must parse as an absolute URL, exactly the check `POST /queue` makes: no
scheme restriction and no length cap, since the ingestion pipeline normalizes
any parseable URL, so neither would stop a row being ingested, and what a link
may point at is the extension's call. `saved_at` must be an ISO-8601 timestamp
and is stored normalized to UTC: ingestion parses it as RFC 3339 and nothing
marks a row that keeps failing, so a malformed one would be retried forever, and
the claim order needs timestamps that sort as time. (The two capture completion
routes make the same check on `captured_at`.) `title` may be any string or none,
stored as given, and an empty one becomes NULL at ingestion, as it does for
captures; only a defensive 4096-character cut applies (nothing specifies a title
length, and real ones run to a few hundred characters), and a longer title is
cut rather than refusing a cosmetic field's save.

**Favicon transport.** A favicon travels **inline in the save request**, as
`favicon_ext` plus base64 `favicon_data`, up to **128 KB** of raw bytes; larger
ones go to R2 through `/links/upload-urls`, after which the save carries only
`favicon_ext` and the Worker records the recomputed key. A favicon is small, but
not "a few KB": on one real instance they average about 18 KB, and a legacy
multi-resolution `.ico` can be several times that, so the cap is generous rather
than tight. The common case is one Worker request instead of the capture flow's
two (the upload-URLs call and the completion), which matters on Cloudflare's
free tier. Cloudflare's own ceilings are nowhere near the cap (a D1 row may be 2
MB, base64 included; a request body 100 MB); the cap exists to bound what's
parked in a D1 row and parsed inside a request's CPU budget. Nothing is ever
dropped for being large; it just takes the R2 path, as a capture's favicon
always does. An inline favicon over the cap gets a `413` naming the R2 route,
and a request whose declared body is far larger than any inline favicon is
refused before it's parsed. In `pending_links` the inline bytes live in
`favicon_data` and an R2 favicon's key in `r2_key_favicon`, exactly one of them
alongside `favicon_ext`, which the schema's `CHECK`s enforce; marking a row
fetched clears all three, since the backend has the favicon on disk by then.

**The cap lives in one place, the Worker, and devices learn it from
`GET /config`** (`favicon_inline_max_bytes`, alongside `links_enabled`, §17f).
Changing it is a Worker change either way, but the extension never carries its
own copy to keep in step: Workers and extensions upgrade on different schedules
(`terraform apply` versus a browser store), and a stale extension is harmless,
because the Worker enforces its own cap and the extension treats a `413` as "use
R2", which works at any size. The extension caches the config (§17f). If the
Worker can't be reached it shows the normal "can't reach the server" error,
since the save would fail too, and there is no default cap compiled into the
extension for it to fall back on; a stale cached value is used until a refresh
succeeds.

**No `source_capture_id` equivalent.** On captures it makes the append-only
`captures` insert idempotent across a retry (a crash after the Postgres commit
but before the D1 fetched flag would otherwise insert a duplicate version, and
the R2 object a retry needs is already gone) and survives the rare id collision
(§3c). A link has neither problem: ingestion upserts on
`(user_id, normalized_url)`, so a retry just refreshes the same row, and no two
links can collide. What links keep from the capture design is the other two
pieces: the client UUID as the primary key, and the atomic claim (`claimed_at`)
that stops two agents ingesting one row. The claim is ordered **oldest-saved
first** (`saved_at`, then `created_at`), so when the same URL is saved twice
before a poll, the later save is processed last and is the one that sticks.

**Backend ingestion** (`ingest.LinkIngester`, run by the agent's worker cycle
(§3e) right after capture ingestion, and only with links on). It claims a batch
and, for each link in saved order: parses `saved_at`, normalizes the URL with
the same pipeline pages use, gets the favicon onto disk, upserts the link,
deletes the favicon's R2 object if it had one (best-effort, as for a capture's
favicon), and only then sets the fetched flag, the same ordering as captures and
for the same reason. It's one statement and no transaction, because a link is
one row with no follow-on jobs. A failure on one link is logged and leaves it
unfetched to be claimed again, without stopping the batch. The pending row's
`saved_at` is used only to order the claim and is not written into the link: the
row's own `created_at` and `updated_at` are ingest time (`NOW()`), as for pages
and captures, because `updated_at` is the mirror's incremental-sync checkpoint
(§8, §17j) and a device's clock has no place in it (a link ingested late, from a
retry or a device whose clock runs behind, would otherwise get a timestamp older
than the checkpoint and never be mirrored).

What the upsert does to the link's favicon depends on what the pending row
declared, which is what lets a refresh be both faithful and safe:

- **No favicon declared** (the device saw none): the favicon columns are cleared
  (§17b). The site stopped serving one.
- **Declared and stored**: the columns are set to the new favicon.
- **Declared but couldn't be stored**, for any reason (the R2 object is gone,
  the bytes aren't valid, the disk write failed): everything else is refreshed
  but **the existing favicon is left exactly as it is**
  (`UpsertLinkKeepingFavicon`), and a brand-new link simply has none. That is a
  failure on our side, not the site's, and it must not turn into losing a
  favicon the link already had.

The third case is also what handles the one retry hazard, which is in the R2
path only and so rare squared (a favicon over the cap plus a crash between
deleting its R2 object and setting the fetched flag): the retry finds the row
declares an R2 favicon whose object is gone, and the favicon the first attempt
stored stays. With an inline favicon the retry has the bytes again, so it's a
plain refresh. A retry that does write a second favicon directory orphans the
first for `gc`.

A Worker that doesn't have the pending-links routes yet (it predates links, or
hasn't been redeployed since) answers `404`; the agent logs a warning each cycle
and carries on rather than failing, and the cleanup sweep skips it quietly. The
Worker and backend are deployed separately, so either may be first (§17g).

**The extension** (`background/link.js`, `link-config.js`,
`capture-inject/link-entry.js`). "Save as link" is **a plain text link under the
capture button in the popup, not a second button**: the lesser, occasional
action, which shouldn't compete with the one the popup is opened for. It is
hidden until the background confirms links are on for this instance, and shares
the capture button's status line (only one of the two runs at a time). Saving
injects a small bundle of its own (`link-inject.js`: just the title and the same
`selectFavicon` a capture uses, through the same fetch relay, with none of
`single-file-core`) into the top frame only, then sends the save: `POST /links`
for a direct save, or `POST /queue/:id/complete-link` for a tab opened by a
claimed queue item (which is then forgotten and closed, exactly as for a queued
capture; a failed save keeps the claim). If the page can't be injected into
(`chrome://`, a store page), the link is still saved, with the title the browser
already has and no favicon: a link to a page we can't read is still a link.

**The Worker's config is cached.** `GET /config` (`links_enabled`,
`favicon_inline_max_bytes`) is read lazily and cached for a day, keyed by the
instance and cleared on unpair: it is a setting turned on once and left alone,
so opening the popup costs at most one Worker request a day per device (the free
tier is a real constraint). What a day of staleness could cost is covered
without shortening it: the cache is refreshed on every browser start, so a
feature switched on shows up at the next start, and a save the Worker refuses
with a `404` (it `404`s every link route while the feature is off) forces a
refresh and, if links are now off, reports that clearly instead of a raw error
(a `404` that isn't about the feature, such as a queue item that no longer
exists, is rethrown as it is). If the Worker can't be reached, a stale cached
answer is used rather than failing, except for a rejected token, which is never
papered over; with nothing cached the error propagates, and there is no default
cap compiled into the extension, since the cap lives in the Worker alone. A
Worker that predates links (no `/config`, a `404`) means links aren't available,
and that answer is cached too. A favicon over the cached cap goes through R2,
and a `413` from the Worker on an inline favicon (its cap changed since this was
cached) refreshes the cache and takes the R2 route, which works at any size.

### 17d. Queue: no `kind`

`queue_items` gets **no new `kind` column**. The user already knows whether they
want a capture or a link when they pick the item in the extension, and a `kind`
could only ever be a hint — nothing stops the user from taking either action on
any queued URL anyway. Choosing wrong is self-correcting: queue the URL again
and delete the unwanted entry.

What is required is **completion**, and it mirrors the two capture routes. A
link saved from a queued item goes to **`POST /queue/:id/complete-link`**: the
extension passes the item's id (it already tracks `tabId -> queueItemId`, §3i)
and the Worker marks the item `captured` in the same batch as the insert, with
the same `404`/`410`/`409` answers `POST /queue/:id/complete` gives (the item
must exist for this user, not already be captured or failed, and be claimed by
this very device) — otherwise the item stays pending and keeps getting
re-offered. A link saved from a page the user is already on goes to
**`POST /links`** (§17c), with no queue item. **As for a queued capture, the URL
is the queue item's, read from D1, not the body's.** That keeps a page and a
link saved from the same queue item under the same normalized URL even when the
URL redirects, which the same-URL hint and the re-queue way of promoting or
demoting (below) both rely on.

Accepted consequences:

- The Queue screen's "Captured" badge also appears for items completed as links.
  The label is slightly off but cosmetic; "Done" would fit both if it's ever
  worth rewording.
- The 72-hour `captured`-item cleanup (§8) applies unchanged.

**Promoting or demoting** a URL between a page and a link works through the same
mechanism and needs no special case: re-queue the URL (the dashboard's existing
recapture path, `POST /internal/queue-items`, §8), capture or save it in the
browser, then delete whichever entry isn't wanted. Copying tags, collections or
notes across is explicitly deferred; revisit only if it proves annoying in
practice.

### 17e. Link favicon store

Link favicons live in **their own store, separate from the captures archive**
(§4): its own root (`links_dir`, implemented in `internal/linkicons`) and its
own **bind mount**, required whenever the feature is enabled (§17f), which is
the default.

Links and captures are different entities, and a separate store keeps them well
separated: each has its own root, bind mount, backup, `gc` pass and live set,
and neither has to know about the other (a deployment with links off has no link
store at all). It also avoids a practical hazard, though that alone could have
been worked around by teaching one `recueil gc` about both kinds of file: its
sweep of the archive root (§4) treats every file Postgres doesn't reference as
garbage, so link favicons can't live where it can reach them. Config enforces
the separation: **`links_dir` may not be `archive_dir`, inside it, or contain
it** (compared as cleaned absolute paths; symlinks aren't resolved), and
`recueil` refuses to start otherwise. Keeping the store outside the archive root
also means an old `recueil gc` binary can't touch it, regardless of upgrade
ordering.

**Same layout as the archive.** Within its root the store is laid out exactly as
the archive is (§4): every saved favicon gets its own sharded, UUIDv7-named
directory (`{id[32:34]}/{id[34:36]}/{id}/`, minted by
`archive.Store.NewCapture`, whose name is only historical) holding one
plain-named file, `favicon.{ext}`. Nothing is shared between links, even for
byte-for-byte identical icons: favicons are small (tens of KB, not megabytes),
so deduplicating them isn't worth a second storage model (a content-addressed
layout was considered and rejected here for the same reason it was for captures,
§4). A re-save of a link writes a new directory, and the old favicon, no longer
referenced by the row, is left for gc.

- **Format.** Same compress-by-extension rule as §3g (SVG zstd'd, PNG/ICO stored
  raw) and the same closed `svg | png | ico` extension set; `Write` rejects
  anything else and empty data. `links.favicon_size_bytes` is the on-disk size
  and `links.favicon_hash` the SHA-256 of the original bytes, as on captures.
  These rules are one piece of code, `archive.Store.WriteFavicon`, used by both
  the link store and capture ingestion, so a page's favicon and a link's can't
  drift apart.
- **Startup check.** With links enabled, `server` and `agent` call
  `linkicons.Store.Check` before doing anything else: `links_dir` must exist, be
  a directory and be writable, or the process exits with an error that names
  both fixes (provide the directory, or set `enable_links = false`). It can't
  tell a real bind mount from a directory inside the container's own filesystem,
  which loses its contents when the container is recreated, so the operator docs
  say so explicitly.
- **Reclaiming.** The second `recueil gc` pass (§4) sweeps this store against
  `ListReferencedLinkFaviconPaths`, with the same recency floor, orphan-fraction
  refusal, empty-directory pruning and `--dry-run`/`--force` as the first. It
  reclaims a deleted link's favicon, one a re-save replaced, one a re-save
  cleared because the site stopped serving a favicon (§17b), and the empty
  directory left by a write that failed partway.
- **Backup (§14)** gains a third item, on the same schedule as the other two,
  when links are enabled.

### 17f. Feature flag

The feature ships with v2 and is **on by default** (`enable_links`, default
`true`), so a deployment that doesn't want it turns it off. Enabling it also
needs a new bind mount for the link favicon store (§17e); the backend fails at
startup, with an error that names both fixes (provide the store directory, or
set `enable_links = false`), if the flag is on and the store isn't usable. The
release notes for v2 should call this out.

- **Migrations run unconditionally.** Postgres and D1 migrations are all
  additive (§17g), so the schema doesn't depend on the flag and turning it on
  later needs no migration step.
- **The backend gates behavior, not schema.** With the flag off: link routes
  404, the agent does no link ingestion or Wayback work, the MCP link tools
  (§17k) aren't registered, and `GET /api/capture-config` reports
  `links_enabled: false` so the dashboard hides the Links nav item. This is the
  same shape as `enable_open_registration` and `ai_base_url`. **Landed:** the
  `enable_links` config key, `links_dir` (§17e) and the `links_enabled` field on
  `GET /api/capture-config`; the rest of the gating lands with each piece it
  gates.
- **The extension can't read backend config** — it only talks to the Worker
  (§2). So the Worker has a matching switch: Terraform's `enable_links` variable
  (default `true`) becomes a `LINKS_ENABLED` plain-text binding, and a
  bearer-authenticated `GET /config` returns `links_enabled` (and
  `favicon_inline_max_bytes`, §17c), which the extension reads to show or hide
  "save as link". Only the exact string `"true"` counts as enabled, so a Worker
  deployed without the binding reads as off rather than as a feature the backend
  might not be ingesting. This makes **two flags that must agree**; the operator
  docs say so. The extension side lands with the extension action (§17c). **The
  extension must cache this config** for a while (and reuse the last known value
  if the Worker is unreachable) rather than call `GET /config` on every popup
  open or capture, to stay comfortably within Cloudflare's free tier (settled in
  §17c: a day, refreshed on browser start and when a save is refused). The
  Worker side of the switch gates only what a **device** can do: `POST /links`
  and `POST /links/upload-urls` are `404` while it's off. The backend-facing
  `/internal/pending-links` routes are never gated, so a backend and Worker
  whose flags disagree degrade quietly (the claim just returns nothing) instead
  of the agent logging an error on every poll.

### 17g. Rollout compatibility

This ships after v1, so existing setups must not break.

- Every schema change is additive. D1 gets new tables only (`pending_links`,
  `archived_links`, §17j); **`queue_items` is untouched** (§17d). No existing
  table needs a data migration.
- The backend applies D1 migrations at startup (§5b) while the Worker deploys
  separately through Terraform, so either can go first: new D1 tables are unused
  until something writes to them, and new Worker endpoints are unused until an
  extension calls them. A backend upgraded before the Worker gets a `404` on the
  pending-links routes, logs a warning each cycle and carries on (no failed
  heartbeat), so it simply sees no pending links; a Worker upgraded before the
  backend only exposes `GET /config` until the extension is too.
- An old extension ignores the new button and keeps working. An old extension
  that claims a queue item meant to become a link just does a full capture,
  which §17d already accepts.
- An old `recueil gc` can't touch the link store (§17e).
- With the flag off, there is no behavior change at all.

### 17h. Dashboard

The screens below were mocked up and agreed before building, and are built in
stages; each says what has landed.

- **Nav.** "Links" sits after Library and is hidden when links are off.
  `session.linksEnabled` is read once at bootstrap from
  `GET /api/capture-config`'s `links_enabled`; the `/links` route is guarded the
  same way and redirects to the library when the feature is off. **Landed.**
- **The Links page (`/links`)** is list-only, with no grid and no view toggle,
  since a link has no thumbnail. It lists newest saved first (`created_at`)
  under Library's own search box (matching title, URL and notes) and pagination.
  **Landed.** A row (`LinkList.svelte`, not `PageList`) shows the favicon (the
  `Globe` fallback when there's none), the title (the normalized URL when there
  is none), the normalized URL in mono, the saved date and a trailing
  external-link icon. **The row opens the link's own detail screen; the trailing
  icon opens the saved URL in a new tab** (`target="_blank" rel="noreferrer"`).
  They are sibling links, since anchors can't nest: the row is a two-column grid
  with the row's link (favicon, title, URL, date) in the first column and the
  icon button in the second, and the whole row takes the hover background.
- **Open `raw_url`, not `normalized_url`.** Normalization drops the fragment
  (§9), which breaks hash-routed apps, so what a link opens (here and on the
  detail screen) is the URL as saved; rows show the normalized one. The bookmark
  mirror should push `raw_url` too (§17j).
- **Row markers.** The only one is "Also archived", shown when
  `matching_page_id` is set, on its own third line under the URL. It is a plain
  indicator (`role="img"` labelled "This URL is also archived as a page"), not a
  link, because it can't nest inside the row link; the detail screen carries the
  real link. No Wayback state appears in rows. **Landed.**
- **Link detail (`/links/:id`)** mirrors the page detail minus captures,
  recapture, AI tags and versions: a back link, the title with a pencil (hint:
  "Saving this link again from your browser replaces the title."), a source row
  (`raw_url` and an external-link icon), the same-URL callout, Tags,
  Collections, Notes, **Related links**, the Internet Archive block (stage 3)
  and an actions row with Delete link only, and the same "Sync with my browser's
  bookmarks" checkbox as a page's (checked unless the link is excluded, §17j).
  Related links are a page's "Related pages" for links (§17b), and both are the
  one `RelatedItems.svelte` component driven by props: a compact list of rows
  that open the other item, a pencil that reveals remove buttons and a debounced
  title-or-URL search, already-related items filtered out client-side. It was
  left off the original design by oversight. **Landed.**
- **Same-URL hint.** A one-line callout under the source row on both detail
  screens: "This URL is also archived as a page: {title}" and "This URL is also
  saved as a link: {title}". It is a read-time join on `normalized_url`, not a
  stored relationship, so there is nothing to keep consistent, and having both
  is no conflict: the user deletes whichever they don't want. Page detail reads
  `matching_link_id`, and both screens get the other entity's title from the
  same response (`SameUrlHint.svelte`, shared). **Landed.**
- **Cleaning a link's URL.** A link keeps the URL as it was saved (`raw_url`),
  tracking parameters and all, because that is what it opens, what the bookmark
  sync pushes and what the MCP tools return. When that differs from what
  normalization would make of it, the link's screen offers a **cleaner URL** and
  a "Use this URL" action (`POST /api/links/{id}/clean-url`, with `cleaned_url`
  on the detail response, null when there is nothing to clean). The cleaned URL
  is the normalized URL **with the raw URL's fragment put back**, since
  normalization drops the fragment and keeping raw URLs is precisely what lets
  hash-routed apps work. **The server computes it; the client can't supply
  one**, since an arbitrary raw URL could stop matching the link's normalized
  identity, which is untouched. The action moves `updated_at`, so the bookmark
  sync pushes the new URL. It is idempotent and **not permanent**: saving the
  same URL again from the browser refreshes the raw URL from the tab, as it does
  the title, rather than tracking an override (the screen doesn't mention it).
  It applies to links only. A page keeps no raw URL (its screens already use the
  normalized one), and a capture's raw URL is a record of what that version was
  captured from, which is not rewritten. **Landed.**
- **Adding a link by hand.** An "Add link" button in the toolbar opens a modal
  copied from the manual-upload one: URL (required), title (optional) and
  favicon (optional, `.svg`/`.png`/`.ico`), with the note that recueil never
  visits the page itself, so a title and icon are only there if given. On
  success it goes to the new link's screen, as an upload goes to the page. **The
  route is `POST /api/links`**, JSON, in the ordinary `/api` group: unlike
  `POST /api/manual-upload`, which takes a whole archived page and so needs its
  own multipart mount and a far larger limit, a favicon is small enough to
  travel as base64 (`favicon_ext` plus `favicon_data`, the extension's own two
  fields) within that group's 1 MiB limit, capped at 512 KB raw (a `413` beyond
  that), several times any real favicon. (A multipart `POST /api/links` would
  also have shadowed the group's `GET /api/links`.) It normalizes the URL with
  the same pipeline, stores the title as given (blank is none, cut at 4096
  characters) and the favicon in the link store (best-effort, as for manual
  upload), fetches nothing, and records `source = manual`. **It is
  create-only:** a URL that's already a link is a `409` carrying the existing
  link's id and changes nothing, because an extension save refreshes a link (and
  clears a missing title or favicon: the site has none) but here "none given"
  means "I didn't provide one", and a refresh would silently wipe the existing
  link's. A page with the same URL is no conflict. On a `409` the form says so
  and offers to open the existing link. The detail screen labels a manual link
  "added manually" beside its source URL (a link saved from the browser says
  nothing), the way a capture's "manual upload" label does; the word avoids
  "upload", since nothing is uploaded. **Landed.**
- **Tags and collections** show a one-line count above the page list rather than
  a second list: "{n} links have this tag." or "{n} links in this collection."
  with a "View links" link, only when n > 0 and the feature is on (the
  collection count is direct links only, like its page list). It opens
  `/links?tag={slug}` or `/links?collection={id}`, where the Links page shows a
  "Filtered by tag/collection" pill with a clear button. A tag used only by
  links keeps "No pages have this tag yet." with the count line above it
  (`LinkCountLine.svelte`; the filter reads `router.querystring`, so a filtered
  view can be bookmarked or reloaded, and the pill's name comes back in the
  response). The delete confirmations say "page or link" (tags) and "pages and
  links are kept" (collections), since deleting either only removes the
  membership. **Landed.**
- **Wayback** (§17i) gets an "Internet Archive" block directly above the actions
  row on both detail screens (`WaybackBlock.svelte`). Never submitted: "Not
  submitted to the Wayback Machine.", a "Check for existing snapshots" link and
  a "Submit to Wayback Machine" button. Pending: "Submitting to the Wayback
  Machine…" with the button off. Submitted: "Submitted to the Wayback Machine",
  the last-submitted date-time, a "View snapshots" link and a "Submit again"
  button. Failed: archive.org's reason, "Try again", and any earlier success
  alongside. Both links go to the calendar of snapshots for the submitted
  (normalized) URL; no snapshot URL is stored for display. The privacy line says
  archive.org fetches the URL itself as a logged-out visitor, that the URL is
  shared with a third party, and that the snapshot will be public. **There is no
  polling and no live progress:** submitting shows the pending state returned by
  the request, and a result appears on a later load of the screen once the agent
  has recorded it. Without a configured key pair (`wayback_enabled` false) only
  the links are offered, with no button and no privacy line. **Landed.**
- **The API** (`internal/httpapi/links.go`), all under `/api/links` and routed
  only with the flag on (with it off every one of them 404s): `GET /api/links`
  (newest saved first, paginated like `GET /api/pages`; `?q=` is a
  case-insensitive substring match over title, raw and normalized URL and notes,
  since a link has no body text to run full-text search on, with LIKE's own
  wildcards escaped so `100%` means `100%`; every row carries
  `matching_page_id`), `POST /api/links` (add by hand, below),
  `GET /api/links/{id}` (adds its tags and collections),
  `PATCH /api/links/{id}`, `DELETE /api/links/{id}`,
  `GET /api/links/{id}/favicon`, and `POST`/`DELETE` under `/tags` and
  `/collections`, which follow the page equivalents exactly (the same tag and
  collection rows, the same slug rules and the same 400/404/409 answers).
  **`PATCH` sets the title and/or notes**, as for pages. A title is a plain
  overwrite with no record that it was edited: the next save of the link
  replaces it with whatever the tab says (§17b), the way a recapture replaces a
  page's, and that is deliberate. An empty title is refused. The favicon isn't
  editable. **`DELETE` is a pure database operation**, as for pages: the favicon
  file is left for `gc` (§17e). The page detail response gains
  `matching_link_id`, the other half of the same-URL hint (always null with
  links off). Each detail response also carries the other entity's title
  (`matching_page_title` on a link, `matching_link_title` on a page, null when
  it has none), so the same-URL callout can name it without a second request;
  list rows carry only the id. A tag's or a collection's links are listed by
  filtering rather than by their own routes: `GET /api/links` also takes `?tag=`
  (a slug) and `?collection=` (an id, direct links only, like a collection's
  page list), each of which must be the caller's own (a `404` otherwise) and
  which apply together with `?q=` and with each other, and the response then
  names what it was filtered by (`tag`, `collection`). The tag and collection
  page lists (`GET /api/tags/{slug}/pages`, `GET /api/collections/{id}/pages`)
  gain `link_count` for their count line (always 0 with links off).

### 17i. Wayback Machine submission

Submitting to the Internet Archive's Wayback Machine is **on demand only** —
never automatic — and applies to **links and to ordinary captured pages** both.
Checked against the current Save Page Now 2 (SPN2) documentation (updated July
2026), which changed several things this section originally assumed.

- **Respects §1.** Archive.org does the fetching, not recueil, as a logged-out
  visitor. It sees only what a stranger would, not what the user saw in their
  own browser, and **the snapshot is public**. The URL is shared with a third
  party, so the screen says so, and says not to submit URLs that carry secrets
  in the query.
- **A key pair is required, not optional.** SPN2 needs an archive.org account's
  IA-S3 keys (`Authorization: LOW <access>:<secret>`); the unauthenticated route
  is documented only with much lower limits, and the established clients dropped
  it because a job's final outcome can't be confirmed without a key. Setting
  `wayback_access_key` and `wayback_secret_key` (both or neither) is what turns
  the feature on, as an empty `ai_base_url` turns AI off. Without them the
  dashboard offers only the two public calendar links. The operator docs link to
  Internet Archive's own instructions for generating the keys.
- **No global rate limiter and no job queue.** The limits (7 captures a minute
  and 30,000 a day for a keyed account, and a few captures of any one URL per
  day: the same document gives both 5 and 10 in different places) are far above
  what hand-pressed buttons reach, and archive.org also answers a request for a
  URL captured within the last hour with that recent capture instead of making a
  new one. So a refusal is just reported, in archive.org's own words, and the
  user presses the button again. This replaces the `wayback_jobs` table and
  Queue screen entries first proposed.
- **State is keyed by URL, not by page or link.** One `wayback_submissions` row
  per user and normalized URL holds the latest attempt (`pending`, `succeeded`
  or `failed`), archive.org's job id, when it was submitted, `last_submitted_at`
  (the last _success_, which a later failure doesn't erase) and the last error.
  A page and a link with the same URL share it, which is right because Wayback
  captures URLs, and it survives a page being deleted and captured again. The
  normalized URL is what's sent. **It is per user on purpose:** keyed by URL
  alone, one user would see that another had submitted a URL, which reveals that
  someone else on the instance saved it. What users do share is the archive.org
  account: every submission on an instance goes out under the operator's one key
  pair, so archive.org's limits and its own recent-capture handling are per
  instance, not per user. Two users submitting the same URL half an hour apart
  should both succeed, the second being handed the first's recent capture by
  archive.org itself, which needs no state of ours to be shared; if one is ever
  refused, it is by one of those shared limits (a rare cost of the shared
  account that is accepted over leaking saved URLs between users). **The
  snapshot URL is not stored**; "View snapshots" links to the public calendar
  (`https://web.archive.org/web/*/<url>`) instead.
- **How a submission runs.** The request that starts one does a single quick
  call (`POST /save`, which returns a job id at once) and records a `pending`
  row. From then on **archive.org does the capturing itself**, whatever the
  browser or the request is doing; what recueil needs later is only to learn how
  it went (`GET /save/status/<job id>`), and **only the agent does that**: its
  regular local cycle asks about every submission still pending and records the
  answer, so the outcome is stored even if the user closed the tab. **The
  dashboard never asks.** Viewing a page or link reads the stored row and never
  contacts archive.org, and there is no polling anywhere: the Submit button is
  disabled while a submission is pending, and the result shows up the next time
  the screen is loaded after the agent's next cycle (a few minutes by default).
  That is a deliberate simplification: one asker instead of two, no new polling
  pattern in the dashboard, and for a feature used occasionally a wait of
  minutes costs nothing. archive.org only keeps a job's status for about an
  hour, so a submission still pending after that is failed ("archive.org did not
  report a result"), by the sweep and, until the sweep gets there, in how the
  screen shows it. The updates are guarded by status and job id, so asking about
  an old job can't clobber a newer attempt. A second submission for a URL is
  refused while one is in flight.
- **No availability check.** The Wayback availability API would only tell the
  dashboard whether snapshots exist, but calling it whenever a screen is viewed
  would send the URL to archive.org without the user having asked, which is
  exactly what "on demand only" rules out. "Check for existing snapshots" is a
  link the user follows themselves.
- **Identified to archive.org.** Requests carry a `User-Agent` naming the
  project and its repository, so someone at the Internet Archive who sees them
  knows where to come with questions.
- **The API** (`internal/httpapi/wayback.go`). `POST /api/pages/{id}/wayback`
  and `POST /api/links/{id}/wayback` start a submission of the item's normalized
  URL and answer `202` with its state; they are routed only when a key pair is
  configured (and, for links, links are on), so otherwise they `404`. `409`
  while one is already in flight, and `502` with archive.org's own message if it
  refuses the request (nothing is recorded then) or "could not reach
  archive.org". The page and link detail responses carry a `wayback` object
  (`status`, `submitted_at`, `last_submitted_at`, `error`), or null if the URL
  was never submitted; it is a plain read of the stored row, shown even if the
  keys have since been removed, and a row still pending past an hour is shown as
  failed so the Submit button can't stay disabled when the agent has been down.
  `GET /api/capture-config` gains `wayback_enabled` so the dashboard knows
  whether to offer the button.
- **Landed:** the configuration, the table, the client and service, the agent's
  sweep, the API, and the dashboard block.

**Sources** (where the facts above come from, to re-check when the API changes):

- The Save Page Now 2 public API documentation, by the Internet Archive:
  <https://archive.org/details/spn-2-public-api-page-docs-2025-10-22>. The
  request and status shapes, the limits (captures per minute and per day, per
  URL per day), the hour its job statuses are kept for, and the one-hour default
  for skipping a recent capture. The revision read carries an update date of
  2026-07-22.
- Getting the keys:
  <https://archive.org/developers/tutorial-get-ia-credentials.html>, which sends
  the user to <https://archive.org/account/s3.php>.
- Why keys are treated as required: the `wayback-machine-archiver` project's
  README, <https://github.com/agude/wayback-machine-archiver>, which dropped its
  unauthenticated mode so a job's final outcome can be confirmed.
- The availability API, <https://archive.org/help/wayback_api.php>, which is
  deliberately not used (see above).

### 17j. Browser bookmark sync

A second sync, the same shape as the existing one (§3j, §8), with its own
folder:

- A new D1 table, `archived_links`, mirrors links the same way `archived_pages`
  mirrors pages: a scheduled backend job, checkpoint read from D1's
  `MAX(updated_at)`, an incremental upsert pass plus a deletion-reconciliation
  pass. **Landed** (`internal/mirror`, `terraform/worker`): `Syncer.SyncOnce`
  runs the links cycle after the pages one when links are on, and the two are
  independent, so a failure in one never stops the other and both errors are
  reported. The Worker side is `GET /archived-links` (device-facing, `404` while
  links are off) and
  `/internal/archived-links/{last-sync,mirror,link-ids,delete}`
  (service-secret-gated, never gated by the flag, as `/internal/pending-links`
  isn't). A Worker deployed before this backend has no such routes; the `404` is
  logged as a warning and skipped, not failed, and links start syncing once the
  Worker catches up.
- The extension reconciles by URL against **its own dedicated folder**, separate
  from the archived-pages folder, **completely owned by recueil**. Sync is
  **one-way**, and any manual change in that folder is overwritten on the next
  sync (§3j).
- It's its own opt-in toggle in the popup. It can reuse the same optional
  `bookmarks` permission the existing sync already requests.
- The folder starts **flat**. Mapping collections to subfolders is deferred.
- The same URL can appear in both folders if a page and a link share it. That's
  harmless; the docs should mention it.
- **Per-link exclusion**, as for pages: `links.excluded_from_mirror` (default
  `FALSE`, so links are synced unless excluded), set through
  `PATCH /api/links/{id}` and the checkbox on the link's screen. It is purely a
  Postgres-side filter on what the sync pushes, with no D1 column of its own;
  setting it moves `updated_at`, and a link that was already synced and is then
  excluded is removed by the deletion-reconciliation pass, which looks identical
  to the link having been deleted. **The column, API and checkbox have landed;
  the sync itself is next.**
- **Two top-level folders.** The links folder is a sibling of the archived-pages
  folder (named "recueil links"), not nested inside it, so enabling, disabling
  and tearing down one never touches the other and there is no shared parent to
  keep consistent. The optional `bookmarks` permission is shared, so it's
  relinquished only when **both** syncs are off, and unpairing tears down both.
- The mirrored URL is the link's `raw_url`, not its normalized one (§17h), which
  is a unique identity key per user all the same: a distinct normalized URL has
  a distinct raw URL, and the normalized one is unique.
- **The extension** (`background/bookmarks.js`): both syncs are the same code
  told apart by a small descriptor (folder name, endpoint, which flag and folder
  id in storage), so the reconcile-by-URL, adopt-or-create-the-folder and
  teardown rules of §3j apply to links unchanged. The alarm, browser start and
  pairing run both (independently: one failing never stops the other, and each
  is a no-op if it isn't enabled), and unpairing takes both down. In the popup
  the links toggle sits under the archived-pages one and is shown only when the
  instance has links on, **or when it's already enabled**, so it can still be
  turned off after the feature is. If the instance has links off, or a Worker
  that predates them, `GET /archived-links` answers `404`: that is no list to
  reconcile against rather than an empty one, so the folder is left exactly as
  it is instead of swept clean, and it reconciles again once links are back.
  (The archived-pages sync has no such tolerance: its route has always existed.)
  **Landed.**

### 17k. MCP

Link support landed **with the feature**, so it ships complete. It stays
read-only (§15's "no write tools" is unchanged) and scoped to the authenticated
user like every other tool, and the link tools are **registered only when the
flag is on** (§17f). The tools, `search_links` and `get_link`, are described in
§15. The decision worth recording here: the existing tag and collection tools
got no link versions, since tags and collections are shared with pages (§17b)
and `search_links` takes them as filters. A link carries no body text, so its
value to a model is title, URL, notes, tags and collections, and `page_id` (when
the same URL is also an archived page) is the way on to the full text via
`get_page`. `limit` handling matches §15 (default 20, cap 100).

### 17l. Implementation order and housekeeping

The feature is intended to ship **complete** (links, flag, MCP, bookmark sync,
Wayback), but it touches the schema, Worker, extension, backend, dashboard,
mirror and docs at once, so it should be built in stages. A reasonable order:

1. Tables and migrations, the flag, the favicon store, Worker endpoints, backend
   ingestion, the extension action, the dashboard Links page, and the MCP tools
   (all landed).
2. Browser bookmark sync (§17j) (landed).
3. Wayback submission (§17i), for links and pages together (landed).

Housekeeping that should not be forgotten:

- i18n: new strings in both the dashboard (Paraglide, §5e) and the extension
  (`_locales`, §3k), `en` and `fr`.
- The docs site: enabling the flag, the new bind mount, the Worker-side switch,
  backup (§14), and the one-way folder behavior.
- `DESIGN_SYSTEM.md` when the Links page is built, and the README's package map
  for any new `internal/` packages.
- `recueil gc`'s documentation if it grows a link-store pass (§17m), and
  `internal/metrics` for any new job tables (§13a) (done: see below).

### 17m. Open questions

- Whether tags and collections really are shared tables with pages (§17b), or
  links get their own.
- Browser bookmark import as an onboarding path, and collections as subfolders
  (§17j) — mentioned, neither designed nor scheduled.
