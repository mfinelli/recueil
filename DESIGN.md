# Recueil — Design Document

## 1. Overview

Recueil is a self-hosted personal web archiving tool, built to replace a
patchwork of several existing self-hosted archivers — each storing content
differently, none of them syncing with one another — held together with custom
glue scripts.

### Motivating problems with the previous setup

- Headless-browser archivers fail on sites with CAPTCHAs, paywalls, or content
  behind interaction (click-to-expand, infinite scroll, etc).
- Multiple tools store multiple redundant formats (WARC, PDF, screenshots,
  MHTML, etc.), most of which are never used.
- Keeping several self-hosted tools in sync requires custom glue scripts.

### Core design principle

**Capture happens in a real, already-authenticated, already-rendered browser tab
— not a headless fetch.** This is the fix for the CAPTCHA/paywall problem. The
system deliberately does **not** add any server-side "fetch and archive a URL"
fallback — doing so would reintroduce the exact failure mode being solved.

This principle applies to the _initial capture_ only. Deriving further artifacts
from an already-captured file (e.g. a thumbnail — see §6a) is a different, safe
operation: rendering static, already-authenticated content offline, not
re-fetching a live page.

### Format decision

Store exactly one artifact format per capture: a fully inlined single HTML file
(SingleFile-style — CSS, images, fonts inlined as data URIs), plus a plain-text
Readability extraction, plus a thumbnail image. No WARC, no PDF, no MHTML. The
HTML is the only artifact ever uploaded by a capturing client; the Readability
extraction and the thumbnail are both produced later, offline, by the backend
(§6a/§6b) — not synchronously at capture time.

---

## 2. High-Level Architecture

```
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
the dashboard supports directly uploading an already-captured, fully inlined
SingleFile-style HTML file plus its URL. This bypasses R2, D1, and the Worker
entirely: a single authenticated `POST` straight into the backend, gated the
same way as any other dashboard endpoint.

- Reader-text extraction, title parsing, content hashing, URL normalization, and
  grouping into `pages` are all identical to any other capture path — a manual
  upload of an already-captured URL is just another new version under the same
  page.
- Uses a backend-generated UUID as the starting `source_capture_id` (§3c already
  covers the idempotency scheme in full; manual upload just supplies the id
  itself, since there's no client to generate one).
- Needs its own, larger `RequestSize` limit scoped to this one route — the
  global 1MB cap would reject a real SingleFile archive immediately, since
  inlined images/fonts routinely push these into the tens of megabytes.
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
it and returns the result. Background, the capture bundle, and the popup are
three separate esbuild entry points — different contexts (service worker,
content-script world, extension page) loading at different times, so bundling
them together would mean the largest thing in the build (`single-file-core`)
parses on every service-worker wake for no benefit.

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

-- Explicit, bidirectional page-to-page links — pairwise edges, not a
-- shared link-group/cluster concept: linking B and C to each other later
-- doesn't imply any relationship between A and C just because both are
-- linked to B. Each relationship stored once, as a canonically-ordered
-- pair (the CHECK enforces page_id_a < page_id_b), rather than twice as
-- both A-B and B-A rows — "everything linked to page X" is simply
-- WHERE page_id_a = X OR page_id_b = X, so there's no direction to get
-- backwards and no risk of a duplicate reverse-direction insert. The
-- ordering check also rules out a page linking to itself as a side effect
-- (page_id_a < page_id_b can never hold when they're equal). No user_id
-- column, same as page_tags/page_collections above: ownership is already
-- enforced by both referenced pages belonging to the same user.
CREATE TABLE page_links (
  page_id_a BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  page_id_b BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (page_id_a, page_id_b),
  CHECK (page_id_a < page_id_b)
);
CREATE INDEX idx_page_links_page_id_b ON page_links(page_id_b);  -- the
                                      -- primary key covers page_id_a = X
                                      -- efficiently; this covers the other
                                      -- half of the bidirectional OR query

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

Two things, together, on the same schedule:

1. **The Postgres database** — via `pg_dump` or equivalent. Note that copying
   Postgres's raw data directory while the container is running is **not** safe
   without WAL-aware tooling — a proper dump (or a backup tool that understands
   Postgres's on-disk format) is required, not a raw file copy.
2. **The local archive directory** (zstd-compressed HTML + thumbnails) — a plain
   directory copy/sync is fine here, since these are static files once written.

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

Read-only access to a user's archive for local MCP clients. Intentionally scoped
to only answering questions about the archive, not manipulating it — no write
tools right now and none are planned.

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
