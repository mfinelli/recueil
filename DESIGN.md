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
  global 1MB cap (§13a) would reject a real SingleFile archive immediately,
  since inlined images/fonts routinely push these into the tens of megabytes.
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
require an explicit `--config` file or environment variables (§13a) — no
automatic discovery, since a production process silently picking up an
unintended file is a real risk. `auth`/`enqueue` are the opposite: a personal
tool, where automatic `$XDG_CONFIG_HOME` discovery is the expected UX (the same
shape `git`/`ssh` already train people on). Neither command touches Viper or
`internal/config` — everything they need is read from their own dedicated
credentials file instead.

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
    claim has gone stale (see visibility timeout below). Listing never claims.
  - `POST /queue/:id/claim` — the actual atomic claim, via a conditional
    `UPDATE ... WHERE ... RETURNING`. This, not the listing endpoint, is where
    the two-devices-race-for-the-same-item risk actually lives, and where the
    phase-2 brief's instruction to "build the idempotency and visibility-timeout
    logic in at this point, not later" was aimed.

**One exception (Phase 13): the dashboard's "recapture" action.** PageDetail's
recapture button asks the backend to re-enqueue a page's most recent capture's
URL — the backend has no rendered/authenticated browser session of its own to
capture with (see §2's own reasoning for why capture only ever happens from a
real tab), so this still only ever enqueues, same as a device would. It's a
fourth, service-secret-gated Worker endpoint (`POST /internal/queue-items`,
alongside the failed-queue-item review endpoints from Phase 9), not a
bearer-token one: the backend generates the `id` itself (there's no device on
the other end to have generated one) and leaves `added_by_token_id` `NULL` (the
schema already allows this). Once the row exists it's indistinguishable from any
other queued item — picked up by whichever device next polls `GET /queue`, same
claim/visibility-timeout/cleanup handling as the rest of this section.

**Claim failure is not a single status code.** A failed claim distinguishes
three cases, decided during Phase 2 rather than left as a uniform `409`:

- `404` — the item doesn't exist, or belongs to a different user. These two
  cases are collapsed together rather than distinguished, so a claim attempt
  never leaks cross-user existence.
- `410` — the item is in a terminal state (`captured` or `failed`): it used to
  be claimable and permanently isn't anymore. This is more precise than a bare
  404 (which conventionally means "wrong ID") for "this happened, but it's
  over."
- `409` — the item is actively claimed by another device and the claim hasn't
  gone stale yet: a temporary conflict worth retrying later.

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
  backend's own schedule (once or twice a day is plenty) — **not** a Cloudflare
  Cron Trigger, for exactly the same "keep the Worker dumb, let the backend own
  scheduling" reasoning already applied to the visibility-timeout reclaim above.
  Not scoped to a single user — this is a maintenance sweep across the whole
  deployment, not a per-device operation, so it takes no `user_id` parameter the
  way the device-facing endpoints do.
- **Deletes only `captured` items**, and only once older than a 72-hour
  retention window (long enough to be useful for auditability/debugging shortly
  after the fact, short enough not to accumulate indefinitely). `failed` items
  are **not** touched — they're kept indefinitely, a deliberate decision as of
  Phase 9 (see below), not a deferred one.
- **`failed` items are surfaced to the user with a manual-retry action (Phase
  9), not left as a dead end.** A `queue_items.manual_retry` flag (D1, default
  `0`) lets the dashboard's Queue screen ask for another attempt without losing
  the `failed` status itself — `failed` stays the durable, queryable "needs
  attention" state the screen lists against; the flag layers "and it should be
  claimable again" on top, cleared automatically the moment some device's
  `POST /queue/:id/claim` picks the item up (both the claim query's `WHERE` and
  `GET /queue`'s own listing query were extended with
  `OR (status = 'failed' AND manual_retry = 1)` to make this possible — see the
  migration and `handleClaimQueueItem`/`handleListQueue` in `terraform/index.js`
  for the exact shape). Two new service-secret-gated Worker endpoints back this:
  `GET /internal/queue-items` (see below for what it returns as of the
  dashboard's own Queue-screen recency-window work) and
  `POST /internal/queue-items/:id/retry`, called by the backend's own
  `internal/queueitems` client (structured like `internal/devices`) via
  session-protected, self-scoped `GET`/`POST /api/queue-items...`. No automatic
  retry mechanism and no separate/longer expiry were built — expected volume is
  low at this project's personal/family scale, and an operator can always
  intervene by hand if that stops holding.
- **`GET /internal/queue-items` originally only returned `status=failed` (a
  required query parameter), matching the Queue screen it existed for at the
  time.** Broadened once the screen's own scope grew to "what's currently
  happening," not just "what needs manual attention": it now returns every
  `pending`/`claimed`/`failed` item unconditionally, plus `captured` items from
  the last 15 minutes — the same window `handleListQueue`/
  `handleClaimQueueItem`'s own claim visibility-timeout already uses elsewhere
  in this file, reused rather than picking a second, different number for what's
  conceptually the same "still worth a glance" idea. The `?status=` parameter is
  gone entirely; there's nothing left to select between. `claimed_at` is now in
  the response too — it already existed on the table (used internally for the
  retention clock, see above), just wasn't surfaced.
  `internal/httpapi.ListQueueItems` (renamed from `ListFailedQueueItems`) is the
  passthrough on the dashboard side; the recency window itself is computed
  entirely on the Worker side (`datetime('now', '-15 minutes')`), never in Go or
  in the dashboard's own JS.
- **The retention clock is `claimed_at`, not `created_at`.** An item can sit
  `pending` for a long time before being claimed; it's time since actual
  completion that should drive retention, not time since the original enqueue.
  There is no dedicated "when did this finish" timestamp on `queue_items` today
  — `claimed_at` is used as a pragmatic proxy, reasonable at this project's
  scale since the gap between a successful claim and the capture actually
  completing is seconds to minutes, not enough to matter for a 72-hour window.
  If a future phase's `complete`/`fail` endpoint adds a dedicated completion
  timestamp, this is a one-line filter change, not a design change.

### Pending-capture claiming and cleanup

`pending_captures` is the queue's downstream sibling — a device has finished
capturing, and the row exists until the backend pulls the blobs from R2 and
commits. Two things it lacked, both added together:

- **Backend pickup is an atomic claim, not a plain read.**
  `POST /internal/pending-captures` (a `POST`, not the original `GET`, because
  it now mutates) claims a batch and returns it in one `UPDATE ... RETURNING`,
  the same shape `POST /queue/:id/claim` already uses. SQLite has no
  `FOR UPDATE SKIP LOCKED`, but D1 serializes writes, so the single statement is
  atomic on its own.

  **The bug this fixes is a silent duplicate capture, not merely wasted work.**
  Two agent processes polling at roughly the same time both ingest the same row;
  the second one's insert should be caught by `captures`'
  `ON CONFLICT (source_capture_id)` guard, but isn't — because the last thing
  ingestion does is clear `source_capture_id` back to `NULL` (§3c), and Postgres
  treats `NULL`s as distinct in a unique index. So once the first agent
  finishes, there is nothing left for the second to conflict with, and it
  inserts a duplicate capture row. Nothing downstream catches it either: the two
  agents mint their own separate archive directories, so `captures.html_path`'s
  `UNIQUE` constraint doesn't fire, and the `pages` upsert simply attaches both
  to the same page.

  Deliberately **not** fixed by keeping `source_capture_id` populated forever,
  which would also have worked: that value is client-generated, and making a
  permanent dedup guarantee depend on it is precisely what §3c's collision retry
  loop exists because we can't do. One worker per job is the fix; a stronger
  idempotency key downstream is not.

  **A one-hour stale-claim window, not the 15 minutes used everywhere else** — a
  deliberate departure, for a real asymmetry. A stuck `queue_item` has a human
  waiting to capture something, so reclaiming quickly is worth the risk of two
  devices racing. Nothing at all waits on a pending capture; the backend polls
  on its own schedule regardless. The only cost of a long window is that a
  genuinely dead agent's work waits longer to be retried, while the cost of a
  short one is real — an ingestion still running when its claim expires lets a
  second agent in, which is the exact duplicate this exists to prevent.

  **No claimant column**, unlike `queue_items.claimed_by_token_id`: devices race
  each other and it's worth knowing which won, but every agent presents the same
  service secret and has no per-instance identity. `claimed_at` alone is all the
  stale-reclaim needs.

- **`GET /internal/pending-captures?user_id=`** — the dashboard's own read-only,
  user-scoped listing, on the same path the claim `POST`s to. Same path,
  different verb, different operation: the `POST` is the backend taking work
  across every user and it mutates `claimed_at`; the `GET` is one person looking
  at their own rows and mutates nothing. Listing must never claim, or a
  dashboard left open would starve the ingester.

  This exists because the window between "a device finished uploading" and "the
  backend has ingested it" was otherwise completely invisible — and at the
  agent's default 1800s Worker poll interval, that's up to half an hour in which
  a capture looks like nothing happened at all. It's surfaced as the Queue
  screen's third section, between the capture queue and the enrichment jobs,
  matching the actual lifecycle order.

  Rows already ingested within the last 15 minutes are included, the same
  recency window `GET /internal/queue-items` and `GET /api/jobs` already use, so
  a capture doesn't vanish from the screen the instant it moves on. Unlike those
  two there's no status column to filter on: `(fetched_by_backend, claimed_at)`
  is the entire state, and its three reachable combinations map to
  waiting/ingesting/ingested. **There is deliberately no failed state among
  them** — a row whose ingestion keeps failing is indistinguishable from one
  merely waiting its turn (the same fact that makes the cleanup sweep keep
  both), so the section states its expected window in the hint text and lets an
  obviously-stale row speak for itself, rather than inventing a distinction the
  data can't support. Doing that properly needs the `attempts`/`error` column
  this table doesn't have yet.

- **`GET /internal/queue-items` also gained a device name**, via a `LEFT JOIN`
  against `tokens` on `claimed_by_token_id`. For a `claimed` item this is the
  actionable part — it says which browser to go and finish the capture in — and
  for a `failed` one it says where it went wrong. The join is deliberately
  `LEFT`: `claimed_by_token_id` is `NULL` for an item nobody has picked up, and
  device revocation is a row delete rather than a soft-delete, so a revoked
  device leaves nothing to name. Either way the item itself must still list.

- **`POST /internal/pending-captures/cleanup`**, mirroring the queue-item sweep
  above, including its 72-hour retention window. Nothing had ever deleted a
  `pending_captures` row, so the table grew forever. **Only successfully
  ingested rows (`fetched_by_backend = 1`) are swept**; a row still at `0` is
  either waiting for pickup or failing ingestion repeatedly, and this table has
  no status column that could tell those apart, so both are kept indefinitely
  rather than risk discarding the only record of a capture that never landed.
  Surfacing and retrying persistently-failing rows needs an `attempts`/`error`
  column and something equivalent to `POST /queue/:id/fail` — deferred, not
  forgotten. The retention clock is `claimed_at`, for the same reasoning the
  queue-item sweep documents; unlike there, though, it isn't merely a reasonable
  proxy — `fetched_by_backend = 1` is only reachable by an agent that claimed
  the row first, so it's guaranteed non-`NULL` on exactly the rows this deletes.

### Bookmark-list mirror (backend → D1 → the browser's own native bookmarks)

- Separately from the queue, the extension syncs everything already archived
  into the browser's own native bookmarks — not a custom in-popup list. See §3j
  for why that changed from the original custom-UI plan and how the extension
  side actually works; this section covers the backend → D1 half only, which
  didn't change.
- This is a **one-way, backend → D1 push** — the mirror-image of the credential
  mirror (backend → D1, rather than D1 → backend), keeping the same principle:
  the extension only ever needs to talk to the Worker/D1, never the backend.
- **Schedule-based, not triggered on individual mutations** — reconsidered from
  an earlier revision of this document, which had the backend push a row
  immediately after processing each capture. That doesn't handle deletion (a
  deleted page was never "updated," it's just gone — an event-triggered push on
  capture-processing would never notice), and more importantly it requires every
  future code path that ever touches `pages` (a deletion endpoint, a re-tagging
  endpoint, whatever else) to remember to also push a D1 update — exactly the
  same fragility already avoided elsewhere in this project (why `updated_at`
  itself isn't left to individual queries to set by hand). A schedule doesn't
  care how or where Postgres changed; it just asks "what's different now" on its
  own cadence. What actually triggers the sync job to run is `recueil agent`
  (§3e) — the same shared trigger as backend ingestion (§3c): both are callable
  units (`internal/ingest.Ingester.RunOnce`, `internal/mirror.Syncer.SyncOnce`)
  invoked from one ticker loop.
- **The sync checkpoint is read directly from D1's own data — `MAX(updated_at)`
  across `archived_pages` — not a separately-tracked watermark value stored
  anywhere on the backend.** Considered and rejected: a Postgres-side "last
  synced at" row, which has to be kept correct by hand and can drift from what
  D1 actually contains if a push silently fails partway. Deriving the checkpoint
  from D1's own state makes that whole class of drift structurally impossible —
  the checkpoint and the data are the same read, by construction. The one real
  cost is a small Worker read endpoint whose only job is exposing that value;
  judged worth it and in keeping with what this Worker already does elsewhere
  (`GET /internal/pending-captures` already answers a factual question about
  D1's own data the same way).
- **Two passes each sync cycle:**
  1. **Incremental upsert** — `pages WHERE updated_at > $checkpoint` (all of it,
     unpaginated — no `LIMIT`/cursor: at this project's scale a full delta in
     one call is fine, and pagination would reintroduce a subtler version of the
     same equal-timestamp boundary problem the checkpoint design otherwise
     avoids), pushed to D1 in one request.
  2. **Deletion reconciliation** — the only way a schedule-based sync can ever
     notice a deletion at all, since a deleted row was never "updated." The
     backend fetches D1's full current `page_id` set (a raw list, no comparison
     logic in the Worker — see below) and its own current Postgres `page_id`
     set, diffs them locally, and deletes from D1 whatever's no longer in
     Postgres. Deletion itself isn't built yet; this pass runs correctly
     regardless, simply finding nothing to remove until it exists.
- **Per-page mirror exclusion** —
  `pages.excluded_from_mirror BOOLEAN NOT NULL DEFAULT FALSE` (§10). No D1
  schema change needed at all: exclusion is purely a Postgres-side filter on
  what the backend chooses to push, not a concept D1 needs to know about. Both
  passes above already have everything needed for this to fall out for free,
  without any special-casing:
  - **Incremental upsert** simply never selects excluded pages
    (`GetPagesUpdatedSince` adds `AND NOT excluded_from_mirror`), so a newly-
    excluded page is never (re-)pushed.
  - **Deletion reconciliation**'s Postgres-side set is redefined from "every
    page_id that exists" to "every page_id that belongs in the mirror"
    (`GetMirrorEligiblePageIDs`, same `WHERE NOT excluded_from_mirror`). A page
    that gets excluded _after_ already being synced looks identical to this pass
    as a page that was deleted outright — both are simply "in D1 but no longer
    in the desired set" — so the exact same diff-and-delete logic removes it,
    with zero new code in `internal/mirror` itself.
  - Un-excluding a page works the same way any other edit does: the toggle bumps
    `updated_at` like any `pages` mutation must (§8's own checkpoint design
    already depends on this), so the page simply reappears in the next cycle's
    incremental upsert once the flag flips back.
  - No dashboard toggle for this yet — the column and query-level filtering
    exist now, but the actual UI to set it is built alongside the dashboard
    itself (Phase 5), same as every other dashboard-only feature.
- **The incremental push's atomicity is what makes the checkpoint safe without
  any extra ordering logic on the backend.** The push endpoint applies its whole
  batch via the Worker's own `env.DB.batch()`, which is transactional: either
  every row in the batch lands, or none do. So there's no scenario where a
  partial failure leaves D1's `MAX(updated_at)` ahead of some unpushed row —
  either the full delta lands and the new max correctly reflects all of it, or
  nothing lands and the next cycle's `WHERE updated_at > $checkpoint` naturally
  retries the identical, unchanged set. (An earlier line of reasoning about this
  design assumed a separately-tracked, non-atomic push would need the backend to
  push rows in strict ascending `updated_at` order and stop at the first
  failure, to avoid exactly this gap — that concern doesn't apply once the
  checkpoint comes from D1's own atomically-updated state instead.)
- **Every Worker endpoint involved stays deliberately dumb**, consistent with
  this Worker's stated design: it reads or writes exactly what it's told, and
  never computes a diff or a decision itself. `GET .../last-sync` answers a
  factual question; `POST .../mirror` upserts whatever batch it's given;
  `GET .../page-ids` returns a raw list; `POST .../delete` deletes exactly the
  ids it's given. All the actual logic — what changed, what to delete — lives on
  the backend.
- The extension does **not** live-sync this list either. It refreshes on a
  coarse schedule (see "Polling cadence" below) or on explicit user request —
  but, unlike the backend's own Postgres → D1 sync above, with **no incremental
  checkpoint at all**: it pulls the whole current list every time and diffs it
  locally (§3j). That's a deliberate scale-appropriate simplification, not an
  oversight — a personal archive is realistically hundreds to low-thousands of
  pages, nowhere near where an incremental `since` parameter would start to
  matter the way it does for the backend's own sync job above.
- Because this list is just title + URL, no thumbnail storage is needed in R2 or
  D1 for this feature.

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

### Runs in the backend, not the Worker

Normalization happens entirely backend-side (Go), at ingestion time — not in the
Cloudflare Worker. Two independent reasons converge on the same answer:

- **Manual upload (§3d) has no Worker involved at all** — it's a direct
  dashboard→backend upload, bypassing R2/D1/the Worker entirely. A Worker-side
  normalization step would simply never run for that capture path, and a user
  manually uploading a file has no reason to have already normalized the URL
  themselves.
- **The Worker's "plain JS, no build step, no dependencies" constraint (§11,
  §13a) rules it out anyway.** ClearURLs' ruleset (below) has no existing Go
  _or_ dependency-free-JS implementation to embed; whichever side implements it
  needs a real regex/JSON-parsing dependency, and only the backend is free to
  take on dependencies at all.

### Pipeline architecture

Normalization is a **pipeline of independent steps**, not a single hardcoded
function — , since ClearURLs is expected to be the first entry, not the only
one. A future step might be a different third-party library, or a hand-rolled
Recueil-specific ruleset; the pipeline shape means adding one never requires
touching an existing step, and steps can be freely reordered. Implemented as
`internal/urlnorm`: a `Step` interface
(`Normalize(ctx, rawURL string) (string, error)`, string in/string out —
intentionally not a shared parsed-URL representation, so an external library
with its own string-based API is trivial to slot in as a step) and a `Pipeline`
that runs a sequence of `Step`s, each fed the previous one's output. Today's
pipeline is exactly two steps, run in this order:

1. **ClearURLs** — strips known tracking parameters and unwraps redirect-wrapper
   URLs (below).
2. **Recueil's own additional canonicalization** — host/scheme casing, default
   ports, fragment, query-param ordering, trailing slash (below) — also just a
   `Step`, not a hardcoded tail bolted onto step 1.

### ClearURLs: a Go port, vendored as a git submodule

Adopt the **ClearURLs** community-maintained ruleset (regex-based rules per
site/provider, actively maintained, LGPL-3.0 licensed — corrected from an
earlier revision of this document, which stated MIT) to strip known tracking
parameters (`utm_*`, `fbclid`, `gclid`, etc.) and unwrap tracking-redirect
wrapper URLs, without touching functionally meaningful query parameters. Do not
hand-roll a tracking-parameter list.

- **The ruleset (`data.min.json`) is vendored as a git submodule** at
  `internal/urlnorm/clearurls-rules` — inside the package that actually uses it,
  not at the repo root — pinned to a specific commit and embedded directly as
  `[]byte` via `go:embed` (`//go:embed clearurls-rules/data.min.json`, entirely
  local to `internal/urlnorm`). This is a deliberate departure from how the
  Postgres/D1 migration directories are embedded (those live at the repo root
  and get embedded in `main.go`, then threaded down through `cmd` — see §13a):
  that indirection exists there because `cmd/server.go` itself needs to read
  those directories directly. Nothing outside `internal/urlnorm` ever needs the
  ClearURLs ruleset, so embedding it locally, as a single file rather than a
  directory `embed.FS` needing an `fs.Sub`/`fs.ReadFile` step to extract the one
  file back out of it, avoids indirection this package has no use for. The
  vendoring-as-a-submodule reasoning itself is unrelated to where the embed
  directive lives: it's a deliberate consequence of the upstream project not
  publishing to any package registry (npm, a Go module proxy, or otherwise) that
  could be depended on directly with a version constraint the normal way; a
  submodule pinned to a commit is the closest equivalent, giving reproducible
  builds the same way a registry version pin would. Updating to a newer ruleset
  snapshot is a deliberate, manual operation — advance the submodule's pinned
  commit, commit that pointer change on its own, and cut a new Recueil release —
  never automatic.
- **`internal/urlnorm`'s `ClearURLs` type is a Go port of the real extension's
  own algorithm** (`pureCleaning`/`_cleaning`/ `removeFieldsFormURL` in
  ClearURLs/Addon's `core_js`), not an inference from the ruleset format's own
  documentation — the documentation describes the data shape but not every
  matching/precedence detail (anchoring, case-sensitivity, iteration order, the
  redirection short-circuit). Every behavior was checked against the actual
  upstream JS source directly. Notably: providers are matched in the ruleset's
  own file order (not alphabetical, not a Go map's randomized order — order
  matters because a matched redirection immediately short-circuits the rest of
  that pass); a full cleaning pass repeats until it produces no further change
  (handles a redirect-wrapper unwrapping to reveal a URL a _different_ provider
  now matches); and each `rules`/`referralMarketing` entry is matched as a full,
  case-insensitive, anchored match against the parameter name (`^rule$`), not a
  substring/prefix match.
- **Uses `github.com/dlclark/regexp2`, not stdlib `regexp`.** Go's stdlib
  `regexp` (RE2) can't compile some patterns the real ruleset relies on
  (lookaround and similar PCRE-ish constructs); `regexp2` is a real, PCRE-like
  engine that can. This is a real dependency addition, acceptable because it's
  backend-only — the Worker's dependency-free constraint doesn't apply here.
- **Two upstream behaviors are not ported at all** — not bugs, not future work,
  structurally excluded from `internal/urlnorm`'s own data model:
  - `completeProvider` ("block this request outright") is a live-browsing
    concept — dropping a tracking-pixel request before it's ever made. It
    doesn't apply to a URL a user already chose to archive: a bookmark is
    definitionally not a stray tracking request, so this essentially never
    legitimately fires against real Recueil input regardless.
  - `forceRedirection` is a live-tab browser-navigation technique (directly
    rewriting a browser's own `main_frame` object when a site defeats normal
    redirect interception). It has no meaning once you're transforming an
    already-known URL string rather than intercepting a real navigation event —
    which Recueil never does. `redirections` itself (the actual URL-string
    transformation: unwrap a tracking-gateway URL to its real destination) _is_
    ported; `forceRedirection` is a separate, unrelated flag about _how_ a live
    browser would perform that same unwrap during real navigation.

### Recueil's own additional canonicalization

Runs as the pipeline's second `Step` (`urlnorm.Canonicalize`), after ClearURLs
has already had a chance to strip tracking parameters and unwrap redirects:

- Lowercase the host, and the scheme (the latter not originally listed here,
  added because Go's own `net/url.Parse` doesn't lowercase the scheme itself,
  which is both a correctness requirement for the default-port comparison below
  and a reasonable canonicalization in its own right per RFC 3986).
- Strip default ports (`:443` for `https`, `:80` for `http`).
- Drop the URL fragment, unless the site is a known SPA that encodes meaningful
  route state in the fragment. **Not implemented yet** — no such site list
  exists, so the fragment is dropped unconditionally for now; this is a known,
  stated gap, not a silent one.
- Sort remaining query parameters alphabetically for a stable key.
- Strip trailing slash (including a bare root `/`, so `example.com` and
  `example.com/` normalize identically — a deliberate consequence of applying
  this unconditionally, not an overlooked edge case).

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
  title TEXT,                        -- denormalized from latest capture,
                                      -- but also directly PATCH-able as a
                                      -- manual override (Phase 13,
                                      -- PATCH /api/pages/{id}) -- a later
                                      -- recapture overwrites an override
                                      -- the same way it always overwrites
                                      -- this column, deliberately: no
                                      -- separate title_override column, no
                                      -- display-time fallback
  latest_capture_at TIMESTAMPTZ NOT NULL,  -- also denormalized from latest
                                      -- capture (via GREATEST, tolerating
                                      -- out-of-order ingestion) -- feeds
                                      -- the D1 bookmark-list mirror's own
                                      -- latest_capture_at column directly
  excluded_from_mirror BOOLEAN NOT NULL DEFAULT FALSE,  -- opt a page out of
                                      -- the D1 bookmark-list mirror (§8);
                                      -- purely a Postgres-side push filter,
                                      -- no corresponding D1 column exists
  favicon_path TEXT,                 -- denormalized from the latest
                                      -- capture's own favicon_path (§3g),
                                      -- the same way title is -- including
                                      -- back to NULL if the latest capture
                                      -- genuinely didn't find one
  notes TEXT,                        -- free-text, user-authored (Phase 14,
                                      -- PATCH /api/pages/{id}) -- a light
                                      -- markdown subset (bold/italic/lists;
                                      -- src/lib/markdown.ts), stored as
                                      -- source and rendered client-side,
                                      -- same as reader_text/ai_summary's
                                      -- own "store source, render on read"
                                      -- choice. Page-level like tags/
                                      -- collections, not per-capture.
                                      -- Deliberately not mirrored to D1:
                                      -- personal annotations, not bookmark
                                      -- structure
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, normalized_url)
);

-- One row per capture event: the version history
CREATE TABLE captures (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  page_id BIGINT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  source_capture_id TEXT UNIQUE,     -- transient ingestion-idempotency key
                                      -- (§3c); client-generated for the
                                      -- extension/queue flow, backend-
                                      -- generated for manual uploads (§3d);
                                      -- cleared back to NULL once ingestion
                                      -- of this capture is fully done --
                                      -- nothing reads it after that
  source TEXT NOT NULL DEFAULT 'extension',  -- 'extension' | 'manual_upload'
                                      -- (§3d) — mirrors page_tags.source
  raw_url TEXT NOT NULL,
  title TEXT,
  html_path TEXT NOT NULL,           -- path relative to the backend's
                                      -- configured archive-directory root,
                                      -- zstd-compressed (see §14 for why
                                      -- relative rather than absolute)
  html_compressed_size_bytes INTEGER NOT NULL,
  html_uncompressed_size_bytes INTEGER NOT NULL,  -- both stored, not just
                                      -- the compressed size actually on
                                      -- disk, so the dashboard can surface
                                      -- real compression-ratio numbers
  thumbnail_path TEXT,               -- populated async by the screenshot
                                      -- service (§6a); null until then
  favicon_path TEXT,                 -- captured client-side alongside the
                                      -- HTML itself (§3g), so -- unlike
                                      -- thumbnail_path -- populated
                                      -- synchronously at ingestion, not by
                                      -- a later async job; NULL whenever no
                                      -- favicon was found, which is a
                                      -- normal, non-error outcome
  reader_text TEXT,                  -- Readability plain-text extraction;
                                      -- populated asynchronously by the
                                      -- readability job (§6b) -- NULL until
                                      -- that job completes, or permanently
                                      -- if it never succeeds
  readability_version TEXT,          -- vendored Readability.js version that
                                      -- produced reader_text; overwritten in
                                      -- place on re-extraction, no history
                                      -- kept (§6b)
  content_hash TEXT NOT NULL,        -- full-HTML hash (exact dedup)
  reader_text_hash TEXT,             -- powers "unchanged since last capture";
                                      -- nullable for the same reason as
                                      -- reader_text above (§3b, §6b)
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

**Full-text search is per-capture-language, not hardcoded to English** —
corrected from an earlier revision of this document, which assumed all captured
content would be English. That assumption actively makes search _worse_, not
just unhelpful, for any other language: applying English stemming rules to
French or German text produces garbage tokens, since stemming is
language-specific by nature.

- **`language` is typed `REGCONFIG`, not `TEXT`.** Casting a language name to
  `regconfig` (`'french'::regconfig`) is a catalog lookup, which Postgres
  classifies as `STABLE`, not `IMMUTABLE` — and generated columns require an
  `IMMUTABLE` expression. Storing the already-resolved `regconfig` value
  directly means the generated `reader_text_tsv` expression
  (`to_tsvector(language, ...)`) is a plain column reference with no cast
  anywhere in it, satisfying the immutability requirement. The cast from a
  language name to `regconfig` still happens, just once, at INSERT/UPDATE time —
  an ordinary, unrestricted operation, not inside a generated expression.
- **Detection happens at ingestion**, parsing the captured HTML's own
  `<html lang="...">` attribute (the standard HTML5 way a page declares its
  content language) — not a Readability output, and not guaranteed to be present
  or accurate, but a reasonable, zero-cost signal already sitting in every
  capture.
- **The detected tag is validated against this specific Postgres instance's live
  `pg_ts_config` catalog, not a hardcoded Go-side list of "languages Postgres
  supports."** Which configs are actually available genuinely depends on the
  running Postgres version; asking the live catalog is the only source that's
  authoritative for that.
- **Falls back to `'simple'`** — no language-specific stemming, but never
  actively wrong for any language, unlike guessing — whenever there's no `lang`
  attribute, the detected tag has no known mapping (e.g. Chinese, Japanese,
  Korean: languages Postgres has no snowball stemmer for at all, since they need
  segmentation rather than stemming), or the mapped candidate doesn't actually
  exist on this Postgres instance.
- **The dashboard lets a user correct a capture's detected language after the
  fact** (`PatchCaptureLanguage`, Phase 6), choosing from whatever configs this
  Postgres instance actually has, or "Other" (mapping to `simple`; relabeled
  from the raw config name in Phase 14 — "simple" isn't a real language, and
  showing Postgres's own internal name for "no stemming" as if it were one just
  reads as a stray option nobody explained) — a plain
  `UPDATE captures SET language = ...`, which Postgres automatically recomputes
  `reader_text_tsv` (and its GIN index) for as part of that same statement, the
  same way it already does whenever `reader_text` itself changes (e.g.
  re-extraction, §6b). No manual reindex, no extra synchronization code needed.
  Every other option's own label is translated into the dashboard's current
  locale (Phase 14, `lib/languageNames.ts`) rather than shown as Postgres's raw
  config name — the opposite direction from Settings' language picker (which
  shows each option self-named, since there you're choosing your own language
  and need to recognize it among others; here you're already reading the
  dashboard in your language and labeling someone else's content, so the labels
  themselves should match).

```sql
CREATE TABLE tags (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id),
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
  source TEXT NOT NULL DEFAULT 'manual',  -- 'manual' | 'ai'
  PRIMARY KEY (page_id, tag_id)
);

-- Nested collections. Adjacency list (parent_id self-reference) rather
-- than a closure table: simpler writes, and at this project's scale a
-- recursive CTE for "this collection and all descendants" is fast enough
-- that a closure table's extra write-complexity isn't justified. (That
-- recursive-descendant capability has never actually been needed:
-- CollectionDetail deliberately shows only a collection's own direct
-- pages plus its sub-collections as links, not a subtree rollup -- see
-- §13a. If that changes, this adjacency-list choice is exactly why it'd
-- still be cheap to add.)
--
-- Uniqueness is per (user_id, parent_id, name), but that can't be a
-- single UNIQUE table constraint: parent_id is nullable for top-level
-- collections, and Postgres treats NULL as distinct from itself in a
-- unique constraint, so a plain UNIQUE(user_id, parent_id, name) would
-- silently allow two top-level collections named the same thing. Two
-- partial unique indexes instead (implemented in Phase 6, replacing an
-- earlier revision of this section that had the single-constraint bug
-- above) -- one per case, since each is a normal (non-NULL) unique check
-- within its own partition. slug (added alongside description/timestamps
-- in the tags/collections slug work) gets the identical treatment, for
-- the identical reason: two more partial indexes, not folded into the
-- existing two as a compound (name, slug) pair -- a name collision and a
-- slug collision should each surface as their own distinct conflict, not
-- an ambiguous "the pair wasn't unique" error.
CREATE TABLE collections (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id),
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

-- Explicit, bidirectional page-to-page links (Phase 15) -- pairwise edges,
-- not a shared link-group/cluster concept: linking B and C to each other
-- later doesn't imply any relationship between A and C just because both
-- are linked to B. Each relationship stored once, as a canonically-ordered
-- pair (the CHECK enforces page_id_a < page_id_b), rather than twice as
-- both A-B and B-A rows -- "everything linked to page X" is simply
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

-- Retry/backoff bookkeeping for the async Readability extraction job (§6b),
-- one row per capture -- same shape as ai_jobs above, EXCEPT it holds no
-- copy of the extracted text itself. reader_text/reader_text_hash/
-- readability_version live on captures directly (see that table above),
-- not here, because captures.reader_text_tsv is a Postgres
-- GENERATED ALWAYS AS column and generated columns can only reference
-- other columns in the same row -- unlike ai_jobs.summary, which has no
-- such constraint and can stay fully decoupled. Reading a capture's full
-- readability state means joining captures and readability_jobs, not
-- reading either table alone.
CREATE TABLE readability_jobs (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  capture_id BIGINT NOT NULL REFERENCES captures(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | done | failed
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);

-- Retry/backoff bookkeeping for the async screenshot job (§6a), one row per
-- capture -- same shape as readability_jobs above, and intentionally its own
-- table rather than merged with it, even though both run through the same
-- headless-Chrome sidecar and often the same page load (see §6: independent
-- failure modes, and re-extraction after a Readability.js upgrade has no
-- reason to redo a perfectly good screenshot).
CREATE TABLE screenshot_jobs (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  capture_id BIGINT NOT NULL REFERENCES captures(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | done | failed
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ,
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
-- project's toolchain.
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
-- added or claimed a queue item -- discovered as a real 500 in testing,
-- not by design review. SET NULL is what actually makes the "revoked
-- device leaves nothing to name" LEFT JOIN behavior described below true,
-- rather than merely intended.
CREATE TABLE queue_items (
  id TEXT PRIMARY KEY,              -- client-generated UUID
  user_id INTEGER NOT NULL REFERENCES users(id),
  url TEXT NOT NULL,
  added_by_token_id INTEGER REFERENCES tokens(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'pending',  -- pending | claimed | captured | failed
  claimed_by_token_id INTEGER REFERENCES tokens(id) ON DELETE SET NULL,
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

-- Completed captures awaiting backend pickup from R2.
-- Note: r2_key_thumbnail has been removed — screenshots are generated
-- backend-side from the already-pulled HTML (see §6a), never uploaded by
-- the extension. r2_key_readable has been removed for the same reason —
-- Readability extraction also moved backend-side (see §6b), so no client
-- ever uploads reader text anymore. r2_key_favicon (§3g) is the one
-- exception to "the extension only ever uploads HTML": a favicon is a
-- genuinely separate resource that has to be fetched, not derived from the
-- already-captured HTML, so it stays a client-upload concern -- nullable,
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
  captured_at TIMESTAMP NOT NULL,
  fetched_by_backend BOOLEAN NOT NULL DEFAULT FALSE,
  claimed_at TIMESTAMP,              -- backend pickup is an atomic claim,
                                      -- not a plain read (§8) -- without it
                                      -- two agent processes both ingest the
                                      -- same row and the second silently
                                      -- writes a duplicate capture. No
                                      -- claimant column, unlike
                                      -- queue_items.claimed_by_token_id:
                                      -- every agent presents the same
                                      -- service secret and has no
                                      -- per-instance identity
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Bookmark-list mirror, kept in sync by the backend's own scheduled sync
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
                                     -- itself (§8), not D1's own clock
);
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

| Component                 | Tech                                                                                                                                                                           | Reachability required                                              | Responsibility                                                                                                                                                 |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Desktop browser extension | WebExtensions (Chrome/Firefox compatible)                                                                                                                                      | Worker + R2 only                                                   | Poll queue, capture HTML via vendored SingleFile (no Readability — see §3a/§6b), upload to R2                                                                  |
| Share-sheet PWA           | Static site, served as Cloudflare Workers static assets bound to the same Worker below (Phase 9 — see §13's own note on the reversal from a separate Cloudflare Pages project) | Worker only                                                        | Android share-target: enqueue a URL, nothing else                                                                                                              |
| iOS Shortcut              | Apple Shortcuts                                                                                                                                                                | Worker only                                                        | Enqueue a URL from iOS share sheet                                                                                                                             |
| CLI                       | Small script/binary                                                                                                                                                            | Worker only                                                        | Enqueue URLs, scriptable                                                                                                                                       |
| Cloudflare Worker         | Plain JS (ES modules), no build step — `@ts-check` + JSDoc for static type-checking, ESLint for linting                                                                        | Public                                                             | Device auth (checks D1 credential mirror), issues bearer tokens, presigned R2 URLs, D1 read/write, service-secret-gated backend endpoints                      |
| D1                        | Cloudflare D1 (SQLite)                                                                                                                                                         | N/A (accessed via Worker only, except backend migrations — §5b)    | Device tokens, queue, bookmark-list mirror, schema-migration bookkeeping                                                                                       |
| R2                        | Cloudflare R2                                                                                                                                                                  | N/A (accessed via presigned URLs)                                  | Temporary blob storage between capture and backend pickup                                                                                                      |
| Backend                   | Go + Postgres, Docker Compose                                                                                                                                                  | Outbound-only for archiving; inbound optional (dashboard, LAN/VPN) | Pull from R2, compress, store, version, search, tags, collections, AI enrichment, dashboard session auth, dashboard API, Postgres + D1 schema migrations (§5b) |
| Headless-Chrome sidecar   | chromedp + `chromedp/headless-shell`, Docker                                                                                                                                   | Backend-internal only (no inbound, no outbound)                    | Renders already-captured inlined HTML offline; produces thumbnails (§6) and Readability extractions (§6b)                                                      |
| Dashboard                 | Svelte                                                                                                                                                                         | Same as backend                                                    | Library browsing, search, reader view, version history, tags, collections, user/session management                                                             |

---

## 12. Deployment

- **Backend**: Docker Compose, bundling the Go backend, Postgres, and the new
  headless-Chrome screenshot sidecar as services. Postgres's data directory and
  the local archive directory both use **bind mounts** (not named volumes) so an
  external backup tool can snapshot them directly from the host (see §14).
- **Cloudflare side**: Terraform/OpenTofu module in the public repo,
  provisioning D1, R2, the Worker (and its routes/bindings, plus the share-sheet
  PWA's static files bound to that same Worker as of Phase 9 — see §13's own
  note on why this isn't a separate Cloudflare Pages project), a
  `random_password` resource for the backend↔Worker service secret (§5a), and a
  `cloudflare_api_token` resource scoped to `D1:Edit` on the D1 database for the
  backend's migration runner (§5b) — both output as `sensitive`, to be copied
  into the backend's `.env` after `terraform apply`.
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
│   ├── agent.go              # `recueil agent` — the background job runner
│   │                            # (ticker-driven Ingester.RunOnce +
│   │                            # Syncer.SyncOnce; see §3e)
│   ├── auth.go               # `recueil auth` — pairs this device, stores
│   │                            # the result via internal/clicreds
│   └── enqueue.go            # `recueil enqueue` — submits URLs to the
│                                 # Worker's queue, via internal/deviceapi
├── internal/
│   ├── config/               # viper-based config: --config TOML file, env
│   │                            # vars, defaults set in this package's own
│   │                            # init() (§13a)
│   ├── clicreds/              # where `recueil auth`/`enqueue` store/read
│   │                             # this device's pairing result (§3f) --
│   │                             # intentionally separate from
│   │                             # internal/config, not one more thing
│   │                             # that package's server-oriented Load()
│   │                             # has to stay agnostic about
│   ├── deviceapi/              # the CLI's own client for the Worker's
│   │                             # public, device-facing endpoints
│   │                             # (POST /pair, POST /queue) -- distinct
│   │                             # from internal/mirror and
│   │                             # internal/ingest.WorkerClient, both of
│   │                             # which authenticate as the backend
│   │                             # itself, never as a device (§3f)
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
│   ├── devices/                 # backend's service-secret-authenticated
│   │                              # client for the Manage Devices Worker
│   │                              # endpoints (GET/DELETE /internal/tokens)
│   │                              # -- added Phase 6; same credential tier
│   │                              # as mirror/ and ingest.WorkerClient, a
│   │                              # different actor from deviceapi/'s
│   │                              # paired-device bearer token
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
├── index.html                # Vite entry point
├── Dockerfile
├── go.mod
├── package.json             # root: the Svelte dashboard's own package
│                               # (Svelte/Vite/sass/svelte-check/
│                               # svelte-spa-router deps) plus repo-wide
│                               # shared tooling (eslint, prettier, vitest,
│                               # typescript) — NOT the Worker's own deps;
│                               # see §13a
├── vite.config.ts
├── svelte.config.js          # exports vitePreprocess() so svelte-check
│                               # understands SCSS the same way Vite does
├── tsconfig.json              # dashboard's own TS config (src/**, plus
│                               # vite.config.ts/svelte.config.js)
├── vitest.config.js          # root-level; covers Worker tests now, expected
│                               # to grow a Svelte-scoped project (§13a)
├── eslint.config.js           # root-level; per-directory-scoping (§13a) —
│                               # now covers the dashboard's src/**/*.svelte
│                               # and src/**/*.ts too, not just the Worker
├── Makefile                   # test-db-up/test-db-down/test — the same
│                                 # commands drive docker-compose.test.yml
│                                 # locally and in CI (§13a)
├── docker-compose.test.yml    # dedicated ephemeral test Postgres (§13a) —
│                                 # distinct port + tmpfs, separate from the
│                                 # dev database below
│
├── terraform/                  # OpenTofu module, the Worker's own source, AND
│   │                              # the share-sheet PWA's static files --
│   │                              # reversed from an earlier revision of this
│   │                              # tree, which kept the Worker's source flat
│   │                              # alongside the OpenTofu config and put pwa/
│   │                              # at the repo root: once the PWA started
│   │                              # deploying as static assets bound to this
│   │                              # same cloudflare_workers_script resource
│   │                              # (Phase 9) rather than a separate
│   │                              # Cloudflare Pages project, both needed to
│   │                              # live somewhere `path.module`-relative, so
│   │                              # the Worker's own source moved into its own
│   │                              # worker/ subdirectory and pwa/ became its
│   │                              # sibling
│   ├── main.tf                   # includes random_password for the
│   │                                # backend↔Worker service secret (§5a) and
│   │                                # a cloudflare_api_token scoped to D1:Edit
│   │                                # for the backend's migration runner (§5b)
│   ├── variables.tf
│   ├── outputs.tf
│   ├── versions.tf
│   ├── waf.tf                    # Browser Integrity Check bypass ruleset
│   │                                # for backend→Worker service-secret
│   │                                # traffic (§5c)
│   ├── README.md
│   ├── worker/                   # plain JS, no build step for deployment —
│   │   │                            # local test/lint tooling doesn't change
│   │   │                            # that
│   │   ├── index.js
│   │   ├── package.json          # Worker's own devDependencies (wrangler,
│   │   │                            # @cloudflare/*, @aws-crypto/*,
│   │   │                            # @smithy/*) — added Phase 6, makes
│   │   │                            # terraform/worker/ a pnpm workspace
│   │   │                            # member; see §13a for why ESLint/Vitest
│   │   │                            # themselves stay root-level regardless
│   │   ├── tsconfig.json          # @ts-check/JSDoc type-checking, index.js only
│   │   ├── migrations/            # D1 schema — applied by the backend (§5b),
│   │   │   │                        # not by wrangler
│   │   │   ├── 0000_schema_migrations.sql
│   │   │   └── 0001_users.sql
│   │   └── tests/                 # @cloudflare/vitest-pool-workers — real
│   │       │                        # simulated D1 via Miniflare, not mocks
│   │       ├── apply-migrations.js
│   │       ├── fetch.test.js
│   │       └── handleUserMirror.test.js
│   └── pwa/                       # static share-target PWA (Phase 9), no
│       │                            # build step, no dependencies — same
│       │                            # "no build step" constraint as worker/,
│       │                            # for the same reason (deploys as plain
│       │                            # static files, not a build artifact).
│       │                            # Served by main.tf's
│       │                            # cloudflare_workers_script `assets`
│       │                            # block, alongside worker/'s own
│       │                            # content_file — one terraform apply for
│       │                            # the whole Cloudflare side, not a
│       │                            # separate Pages project + separate
│       │                            # `wrangler pages deploy` step
│       ├── index.html
│       ├── style.css               # reuses src/app.scss's exact token
│       │                              # values (extension/dashboard/PWA all
│       │                              # share them today) rather than a new
│       │                              # palette -- full reconciliation
│       │                              # against the marketing site's own
│       │                              # ledger/brass/stamp palette was its
│       │                              # own separate "dashboard visual
│       │                              # design system" pass -- see
│       │                              # DESIGN_SYSTEM.md
│       ├── app.js                  # pairs and enqueues same-origin (no
│       │                              # Worker URL field anywhere in this
│       │                              # app -- it's served by the Worker it
│       │                              # talks to)
│       ├── sw.js                   # minimal: satisfies installability,
│       │                              # cache-first for the app shell only
│       ├── manifest.json           # Web Share Target (GET, url/text/title)
│       ├── icon.svg
│       ├── token.html               # standalone (own token.js, no service
│       │                              # worker) -- exchanges a pairing token
│       │                              # for a bearer token and displays it
│       │                              # once, for pasting into a client that
│       │                              # can't run POST /pair itself (the
│       │                              # iOS Shortcut recipe below); not
│       │                              # part of app.js's own pair/enqueue
│       │                              # flow, and saves nothing itself
│       └── token.js

├── extension/                # WebExtension, own package.json (needs bundling
│   ├── src/                    # to pull in vendored SingleFile capture code
│   ├── manifest.json            # and a WebExtension polyfill — no longer
│   └── package.json             # Readability.js; see §3a/§6b)
│
├── www/                          # Zola site — self-contained, own layout
│   │                                # (named www/, not website/ as an
│   │                                # earlier revision of this tree had it)
│   ├── zola.toml
│   ├── content/
│   ├── templates/
│   └── sass/
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
(§13a). The CLI's own commands (`auth.go`, `enqueue.go`) landed as flat files
directly in `cmd/`, confirming they share `go.mod`/the single binary cleanly —
not a separate `cmd/cli/` subdirectory as an earlier revision of this tree
assumed, before the `main.go`/`cobra` restructure had actually produced
`server.go`/`agent.go` as the pattern to follow. Phase 6 additionally corrected
several other places where this tree had drifted from reality (some from before
this project's own working sessions had reality to check it against): the
Worker's source living flat in `terraform/`, not nested under a `worker/`
subdirectory; the marketing site's actual directory name (`www/`, not
`website/`); and `terraform/package.json`'s existence, per §13a.

### Notes on specific decisions

(Unchanged from v1 — see original rationale for Go-at-root, CLI sharing the
server's Go module, the dashboard's build output being embedded via `go:embed`,
the Worker/PWA's no-build-step requirement, the extension's own
directory/bundler, and `www/`'s self-containment.)

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
  below) doesn't open a second connection just to migrate. This goes a step
  further than D1's migration runner by adding the session lock, which D1's
  Cloudflare-API-based approach has no equivalent for.
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
  (`collectors.NewGoCollector`/`NewProcessCollector`) plus custom gauges — a
  `recueil_users_total` count, and, once the screenshot/readability/AI jobs
  existed to have something worth watching, `recueil_jobs_total{job,status}`
  (every (job, status) combination emitted explicitly every scrape, including
  zeros, rather than only whatever a given scrape's query happens to return —
  PromQL's `rate()`/`sum()` behave far more predictably against a
  continuously-present-at-0 series than one that silently appears and
  disappears) and `recueil_job_oldest_pending_age_seconds{job}` (absent, not
  zero, for a job type with nothing currently pending — a real backlog signal a
  raw pending count alone wouldn't surface as clearly). Every metric here is a
  `prometheus.Collector` that queries fresh on every scrape rather than
  maintaining cached state. Deliberately built on its own
  `prometheus.NewRegistry()`, not the global `prometheus.DefaultRegisterer` —
  same reasoning as choosing goose's `Provider` API over its package-level
  `SetBaseFS`/`SetDialect`: avoids hidden shared mutable state that could
  collide across multiple instantiations (confirmed via test: two
  independently-built registries never collide, which they would under the
  global default). A failed collection (e.g. the DB unreachable) is logged and
  simply omits that one metric rather than failing the whole scrape — confirmed
  for real, both the success and failure paths, independently for each custom
  gauge.

  **Everything here is Postgres-only by design, even where D1 has the more
  complete picture.** True queue depth (`queue_items`/`pending_captures` counts)
  lives only in D1, and a Prometheus scrape interval (15-60s typical) hitting
  the Worker on every tick risks the Cloudflare free tier for no operational
  benefit worth that cost.

  - `recueil_pages_total`, `recueil_captures_total`, and
    `recueil_storage_bytes{kind}` (`html_compressed` | `html_uncompressed` |
    `favicon` | `screenshot`) reuse `GetSystemStats` — the same query the
    dashboard's admin stats screen already runs — rather than adding a
    metrics-specific one. Doubles as free ingestion-throughput visibility
    (`rate()`/`increase()` over either total) without ever asking the Worker,
    which is the more useful "is the queue actually draining" signal anyway
    compared to a raw depth count.
  - `recueil_agent_last_success_seconds{cycle}` (`worker` | `local`, matching
    `AgentWorkerPollIntervalSeconds`/`AgentLocalPollIntervalSeconds` — see
    cmd/agent.go) answers a real gap: `recueil agent` is a separate deployed
    process from `recueil server` (§2), and `/metrics` is only mounted on
    `server`'s router, so nothing today surfaces whether the agent is still
    alive and succeeding versus silently stuck. Backed by a new
    `agent_heartbeats(cycle, last_success_at)` table the agent upserts into
    itself after a cycle completes — but only when every step in that cycle
    succeeded, not merely because the cycle ran: `workerCycle.run`'s heartbeat
    is gated on ingestion _and_ mirror sync both succeeding (explicitly
    excluding the once-per-`cleanupInterval` D1 sweep, which is best-effort
    maintenance, not part of every cycle), and `runLocalCycle`'s on screenshot,
    readability, and AI enrichment (when enabled) all succeeding. Recording a
    heartbeat regardless of outcome would hide exactly the failure this metric
    exists to catch. Same absent-not-zero shape as the job-age gauge: a cycle
    that's never recorded a success has no row at all, not a stale zero.

- **OpenTelemetry (distributed tracing) was considered and intentionally
  deferred, not rejected outright.** The core API/SDK
  (`go.opentelemetry.io/otel`) is actually light on its own (just `go-logr`),
  but any real exporter — confirmed even the OTLP-over-HTTP variant, not just
  gRPC — pulls in `google.golang.org/grpc`'s full tree, comparable in weight to
  `testcontainers-go` (§13a, rejected earlier for the same reason). More
  fundamentally, tracing's value scales with a request's hop count across
  services, and this project's current call graph is shallow (one backend
  process, Postgres, occasional Worker calls) — the architectural case isn't
  there yet, and self-hosted personal-scale operators are unlikely to be running
  a trace backend to send spans to regardless. Worth revisiting once the
  screenshot service (§6a) and AI enrichment (§7) exist as a genuine async
  multi-stage pipeline — that's the shape (multiple hops, independent failure
  points, a second real process boundary in the chromedp sidecar) where
  tracing's value proposition actually applies here.
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
  internally (confirmed via source and by intentionally panicking a handler:
  clean `500`, full stacktrace logged, server kept running) — neither needed
  adding separately. `CleanPath` is kept; `RedirectSlashes` is not, because
  `CleanPath`'s `path.Clean()` silently strips a trailing slash into chi's
  internal `RoutePath` before any redirect-based slash-handling middleware would
  ever see one — confirmed for real that a `POST` to a trailing-slash route
  variant hits the handler directly with no visible redirect, same method,
  making `RedirectSlashes` inert given this ordering (and a silent internal
  normalization is the safer behavior for a JSON API regardless — no HTTP
  redirect method-preservation question ever arises). `RequestSize` (1MB cap)
  and `Timeout` (30s, returning `504`) are route-agnostic hardening applied
  globally; `AllowContentType("application/json")` is scoped to the `/api`
  sub-router specifically, since it's enforcing the JSON API's data contract,
  not a general protection every current or future route should inherit
  (confirmed harmless on bodyless requests either way — it skips the check when
  `r.ContentLength == 0` — but scoped for what it communicates, not because
  scoping changes behavior today). `RealIP` was considered and not added:
  genuinely useful behind a trusted reverse proxy, but this project treats
  network exposure (LAN-only, VPN, tunnel, reverse proxy) as entirely the
  operator's choice (§2, §12) — blindly trusting a client-supplied header
  without knowing a proxy is actually in front would let anyone reachable spoof
  their IP in logs. `pprof` (`middleware.Profiler`) was also considered and not
  added: useful for an operator diagnosing their own instance, but exposes
  sensitive runtime info and its own CPU-cost surface, not something to mount on
  the same unauthenticated router as health checks without a separate,
  deliberate decision about how it's gated.
- **Testing:** `testify`, with table-driven cases (`t.Run` subtests, or
  `[]struct{...}` tables) where that reduces duplication rather than as a
  blanket rule. For code that calls an external HTTP API, tests run against a
  real `httptest.Server` plus that library's own base-URL override where one
  exists (e.g. `option.WithBaseURL` for `cloudflare-go`), rather than a
  hand-rolled interface mock — closer to the real request/response shape for the
  same effort. Handler-level tests (`internal/httpapi`) are written as external
  `_test` packages exclusively — exercising only the package's exported
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
  knowledge goes); its test files additionally need `globals.vitest`. Phase 6
  added the expected Svelte-dashboard-scoped block to this same file (`.svelte`/
  dashboard `.ts` globs, `typescript-eslint` + `eslint-plugin-svelte`) rather
  than a separate config file, as planned here.
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
- **Dependency ownership vs. tooling orchestration are separate concerns.**
  `terraform/package.json` (added in Phase 6) holds the Worker's own
  dependencies (`wrangler`, `@cloudflare/*`, `@aws-crypto/*`, `@smithy/*`) and
  makes `terraform/` a pnpm workspace member, mirroring `extension/`'s existing
  pattern (its own `package.json` for `esbuild`/`web-ext`/`crx3`, but no
  `eslint.config.js`/`vitest.config.js` of its own). ESLint and Vitest
  themselves, though, stay root-level and package-agnostic — they orchestrate
  across every workspace member (Worker, extension, and now the dashboard's own
  `src/`) via per-directory `files` globs / per-project scoping rather than each
  package running its own separate lint/test invocation. Root `package.json` is,
  correspondingly, the Svelte dashboard's own package (its `dependencies`/
  `devDependencies` are Svelte/Vite/dashboard-specific), not a shared dumping
  ground for every package's deps — an earlier revision of this section had that
  backwards. The "no build step" constraint is about what ships to Cloudflare on
  deploy; it was never a constraint on what local tooling (including a
  `package.json` purely for devDependencies) is allowed to exist for development
  and CI.

**Svelte Dashboard:**

- **Svelte 5 (runes), Vite, TypeScript, SCSS** — a real build step, unlike the
  Worker/PWA: `go:embed`-ing the built `dist/` output into the Go binary means
  nothing about the "no build step" constraint applies here (that constraint is
  specifically about what ships to Cloudflare on deploy). SCSS support comes
  from `@sveltejs/vite-plugin-svelte`'s own `vitePreprocess()` (exported via
  `svelte.config.js`, so `svelte-check` sees the same preprocessing Vite itself
  uses) — not the separate `svelte-preprocess` package, which the modern
  Vite-plugin-Svelte toolchain has made redundant for the common TS/SCSS case.
- **Routing:** `svelte-spa-router` — a small client-side router, not SvelteKit.
  SvelteKit's file-based routing/server-route/loader machinery would mostly go
  unused here: the session model is already a same-origin
  `httpOnly`/`SameSite=Lax` cookie (§5) checked via ordinary chi middleware, so
  there's no SSR or server-side data-loading need SvelteKit's extra layer would
  actually earn its keep for.
- **Dev workflow:** `pnpm dev` runs Vite's own dev server, whose config proxies
  `/api` to the Go backend (default `http://localhost:8080`, matching
  `listen_addr`'s own default) so the dashboard doesn't need a full Go rebuild
  on every frontend change. `dist/` is gitignored, not committed — built via
  CI/the Makefile like any other generated output, same convention as
  `internal/db/`.
- **Capture HTML delivery** (`GET /api/captures/{id}/html`) prefers passing
  through the archive's own on-disk zstd compression untouched
  (`Content-Encoding: zstd`) when the client's `Accept-Encoding` says it can
  handle it, rather than decompressing server-side just to maybe recompress.
  Otherwise it decompresses and leans on `middleware.Compress`'s existing
  gzip/deflate negotiation (its allowed-types list includes `text/html`
  specifically for this) rather than hand-rolling a second compression path —
  verified against chi's own `compress.go` source that it steps aside correctly
  once `Content-Encoding` is already set, so the two paths can't double-compress
  each other.

- **API client:** hand-rolled (`src/lib/api.ts`), not generated — there's no
  OpenAPI spec on the Go side to generate from, so response types
  (`src/lib/types.ts`) are manually kept in sync with `internal/httpapi`'s own
  response DTOs. A real, disclosed sync point (unlike `sqlc`'s automated
  Postgres↔Go one), judged acceptable while the API surface stays the size it
  currently is.
- **Session/auth:** Svelte 5 runes-based state (`src/lib/session.svelte.ts`),
  bootstrapped once via a module-level `sessionReady` promise that `App.svelte`
  awaits before ever mounting the router — route guards (`svelte-spa-router`'s
  `wrap({conditions})`) don't need their own "have we checked session state yet"
  handling as a result. `GET /auth/me` and the new `GET /api/setup-status`
  (unauthenticated, closing a real gap: there was no way to distinguish "show
  Setup" from "show Login" on first load) run via `Promise.allSettled`, not
  `Promise.all` — one failing shouldn't strand the app on the loading screen
  forever.
- **Favicon/thumbnail delivery** (`GET /api/pages/{id}/favicon`,
  `GET /api/pages/{id}/thumbnail`): unlike capture HTML, no content-negotiation
  dance — small already-binary images, not worth it. Deliberately no
  `Cache-Control` either: both URLs are page-identity- addressed, not
  content-addressed, so a later re-capture changing the favicon/thumbnail
  shouldn't risk being masked by a stale browser cache. Thumbnails aren't a
  denormalized `pages` column the way `favicon_path` is (they're written async
  by the screenshot job well after ingestion, not at `UpsertPage` time), so the
  thumbnail endpoint resolves the latest capture fresh per request instead.

- **Frontend logic testing**: a `"dashboard"` Vitest project alongside the
  existing Worker/extension ones, deliberately scoped to logic
  (`src/lib/*.test.ts`) rather than component rendering for now — a separate,
  later decision with its own setup cost (`@testing-library/svelte`). Testing
  Svelte 5 runes under Vitest needs `resolve.conditions: ['browser']` alongside
  `environment: 'jsdom'`; without it `$state` resolves to Svelte's inert SSR
  runtime rather than a live reactive signal, silently testing the wrong thing
  rather than failing loudly. Verified against Svelte's own official testing
  docs, not assumed.
- **`src/components/`** (new, alongside `src/lib/` and `src/routes/`): shared UI
  that's neither pure logic nor a routed page. `AppHeader.svelte` is the first
  resident — extracted once three real screens would otherwise be repeating the
  same title/nav/account bar, not decided in advance of needing it.
- **Optimistic writes, not refetch-after-write**: `PageDetail`'s tag/
  collection/mirror-toggle/language-correction actions all update local state
  directly from each write's own response. Reasonable for a single-user personal
  tool; not defended against concurrent-editor conflicts, and would need
  reconsidering if multi-user concurrent editing of the same page ever became a
  real scenario.
- **Collections management** (`Collections.svelte`): the tree is built
  client-side from the flat `(id, parent_id)` list `GET /api/collections`
  already returns, not requested pre-nested — consistent with
  `ListCollectionsByUser`'s own documented reasoning that a full-user listing
  doesn't need a recursive CTE. Cascading delete (removing a collection removes
  its whole subtree, per §10) surfaces a `confirm()` naming the actual
  descendant count before proceeding, rather than a silent or generic warning.

- **In-app reader view, not an iframed archived-HTML view.** Settled during
  planning: the archived HTML is a full, self-contained snapshot of the original
  page's own layout/CSS/images, and an iframe would mean fighting
  sizing/scrolling for the whole viewing session for little benefit over a plain
  new tab, which gets native zoom/find-in-page/the full viewport for free.
  `reader_text` gets the in-app treatment instead — nothing to sanitize, safe to
  render directly. The archived-HTML endpoint itself also picked up a defensive
  `Content-Security-Policy: script-src 'none'` regardless (belt-and-suspenders
  on top of the extension's own `blockScripts: true` capture setting, since the
  response is served same-origin with the dashboard).

- **Fonts and icons** (visual-pass tooling, Phase 12): `@fontsource/fraunces`
  and `@fontsource/ibm-plex-mono` self-host the dashboard's two non-body font
  families rather than pulling from a CDN — the dashboard is the authenticated
  half of a self-hosted tool, unlike the marketing site's public page.
  `@lucide/svelte` (the current official package; not the deprecated
  `lucide-svelte` v0 name) provides icons via per-icon subpath imports, with
  app-wide size/stroke-width defaults set once through its own `setLucideProps`
  context API rather than a local wrapper component. Same category as
  `chi`/`cobra`/`viper` above — a concrete tooling choice, not an architectural
  one. The actual design tokens/patterns these support (color palette,
  typography roles, breakpoints, icon usage conventions) live in the new
  `DESIGN_SYSTEM.md`, not here — that content is a living reference meant to be
  read while building a screen, a different shape than this section's own
  tooling-choice log.

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
  archive-directory root**, not as an absolute path — a reversal from an earlier
  revision of this document, which specified absolute paths on the reasoning
  that a restore then had to land at the exact same mount path or lookups would
  break. That's backwards: a relative path is strictly more flexible with no
  real cost — the operator can restore to any location and simply point the
  (already-required) archive-directory config value at it, move the archive
  directory later without a database migration, and the database itself doesn't
  bake in one host's specific filesystem layout. The one real constraint this
  leaves is unchanged in spirit, just relocated: whatever archive-directory path
  the backend is configured with at restore time must actually contain the
  restored files at the expected relative layout (see §4 for the actual on-disk
  layout: three levels of hex sharding on a per-capture UUIDv7) — the config
  value can point anywhere, but it does have to point somewhere real.
- After restoring Postgres from a backup, the **D1 credential mirror can be
  stale** relative to the restored state (e.g. password changes or account
  creations made after the backup was taken won't be reflected, or deleted/
  changed accounts may still have valid mirrored credentials).
  **`recueil user resync`** (Phase 9, `cmd/user.go`) is the manual resync
  command this section called for — CLI-only, not an admin dashboard action,
  matching the operator-only precedent already set for device management. It
  re-runs the same idempotent `mirror.PushUser` push already used at
  create/regenerate/revoke time across every account: decrypts each account's
  `pairing_token_enc` where present, re-hashes it, and re-pushes it (or pushes
  `nil` for an account with a revoked/NULL token, clearing any stale D1 hash
  left over from before the restore). Should be run after any Postgres restore.

---

## 15. MCP Server

Read-only access to a user's archive for local MCP clients (Claude Desktop,
etc.), per the decision recorded in §5's "API tokens" subsection. Deliberately
scoped to answering questions about the archive, not manipulating it — no write
tools in this phase, and none planned; if that changes it's a separate design
decision, not an extension of this one.

**Transport and reachability.** Streamable HTTP, mounted at `POST /mcp` on the
existing backend HTTP server (`internal/httpapi.NewRouter`) — reachable wherever
the dashboard already is (LAN or Tailscale, per the deployment decision in this
section's originating discussion), nothing routed through the Worker/D1.
`internal/mcpapi` is a sibling to `internal/httpapi`, not a subpackage of it:
both are HTTP-facing surfaces over the same `internal/db`/`internal/auth`, and
`/mcp` is mounted in `router.go` alongside `/api`, not nested under it.

**Auth**: `auth.RequireAPIToken`, unchanged from how it was built and tested —
this phase is the first thing to actually mount it.

**Stateless mode.** `StreamableHTTPOptions.Stateless = true`. Required outright
for the `2026-07-28` protocol revision (session resumability -- Last-Event-ID,
standalone GET -- is dropped from that revision entirely), and a reasonable
default regardless: this is a single-process backend, so there's no
session-affinity/sticky-routing problem to design around by picking it. Clients
on an older protocol revision still negotiate down automatically; the SDK
(`v1.7.0`) supports every revision from `2024-11-05` through `2026-07-28` in one
build.

**Cross-origin protection, explicitly configured, not left to the SDK's
default.** The SDK shipped a real CVE
([GO-2026-5771](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/jsonschema),
patched in v1.4.0): pre-patch, a malicious webpage could use DNS rebinding to
reach a localhost-bound MCP server that had no auth of its own. We're already
past that fix by virtue of pinning `v1.7.0`, and bearer-token auth changes the
threat model further in our favor regardless — unlike a cookie, a browser never
auto-attaches an `Authorization` header, so a malicious page can't ride existing
credentials the way classic CSRF/rebinding attacks depend on. Even so, defense
in depth is cheap here: `StreamableHTTPOptions.CrossOriginProtection` is
explicitly set to `http.NewCrossOriginProtection()` (stdlib, Go 1.25+, already
satisfied by this project's `go 1.26.5`) rather than relying on the SDK's own
default, which as of `v1.6.0` is _off_ unless opted back in via the
`enableoriginverification` `MCPGODEBUG` flag -- itself a compatibility shim
slated for removal in `v1.8.0`, not something worth building a permanent
dependency on. With zero trusted origins configured, `CrossOriginProtection`
still does exactly what's wanted here: browser-context cross-origin requests
(the only requests that carry `Sec-Fetch-Site`/`Origin` at all) get rejected,
while genuine MCP clients — which send neither header, being non-browser HTTP
clients — are unaffected.

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
  collections) plus one capture's actual `reader_text`/`ai_summary`, defaulting
  to the latest capture. Deliberately _not_ a 1:1 mirror of the dashboard's own
  `GET /api/pages/{id}` + `GET /api/captures/{id}` split: those stay separate
  because a page-detail _view_ shouldn't eagerly load every capture's full text,
  but a single MCP tool call is the actual unit of work being asked for, and
  forcing two round trips for the common case ("give me this page's content")
  doesn't serve that. The other available captures (id + date, no text) are
  listed alongside, so a model can ask for a specific `capture_id` without a
  separate "list this page's versions" tool. `capture_id`, when given, is
  checked against the resolved page's own id before its text is returned —
  `GetCaptureByIDForUser` only scopes by `user_id`, not by the specific page
  passed alongside it, so without this check a caller could name a `page_id` it
  owns and a `capture_id` belonging to a _different_ one of its own pages and
  get back metadata from one page paired with content from another.

**`limit` handling.** `SearchPages`/`ListPages` already take `limit`/`offset`
and return `total_count` via a window function — reused as-is, default 20,
capped at 100. `ListTagPages`/`ListCollectionPages` have neither (dashboard-
scale, unpaginated by design, per those queries' own comments) — fetched in full
and sliced to the same default/cap in Go, rather than adding a new query variant
for two call sites.

**Dependency.** `github.com/modelcontextprotocol/go-sdk v1.7.0`, no other new
third-party dependency — tool schemas are derived automatically from Go struct
tags (`AddTool`'s generic form), so `jsonschema-go` isn't imported directly.

## 16. Known Limitations

- **Safari packaging.** The extension is MV3-capable on Safari, but Safari
  requires a separate packaging/distribution pipeline (Xcode-wrapped, via
  `safari-web-extension-converter`) rather than loading the same build the other
  browsers use. Not attempted yet — see §3h.
- **Fragment-aware URL canonicalization for SPAs.** `urlnorm.Canonicalize` drops
  every URL fragment unconditionally. The intended exception — preserving a
  fragment for a known SPA that encodes meaningful route state there — has no
  implementation and no site list to check against yet. See §9.
