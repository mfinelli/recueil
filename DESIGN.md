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
Readability extraction, plus a thumbnail image. No WARC, no PDF, no MHTML. The
HTML is the only artifact ever uploaded by a capturing client; the Readability
extraction and the thumbnail are both produced later, offline, by the backend
(see §6/§6a) — not synchronously at capture time.

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
        │  - captures HTML only (via vendored          │
        │    SingleFile library — no Readability       │
        │    vendored here; see §3a/§6a)               │
        │  - uploads to R2 via presigned URL           │
        │  (no longer captures a screenshot — see §6)  │
        └──────────────────────────────────────────────┘
                    │
                    │ (async, outbound-only polling)
                    ▼
        ┌───────────────────────────────────────────────────────────────┐
        │      Backend (Go + Postgres, Docker)                          │
        │  - polls Worker/D1 for pending captures                       │
        │  - pulls blobs from R2, then deletes from R2                  │
        │  - zstd-compresses HTML, stores locally                       │
        │  - enqueues async screenshot job (§6)                         │
        │  - enqueues async readability extraction job (§6a)            │
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
        │  thumbnails (§6) and         │   │                              │
        │  Readability extractions     │   │                              │
        │  (§6a) from already-captured │   │                              │
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
   - Remotely via the share-sheet PWA (Android) or iOS Shortcut (phone) or CLI —
     these only enqueue, they never capture.
2. Enqueueing hits the Worker, which writes a row to `queue_items` in D1.
3. The desktop extension polls D1 (via the Worker) for pending queue items, on
   an infrequent schedule (see §7), and can notify the user something needs
   archiving. The extension also exposes a manual "check now" action in its
   popup for on-demand polling.
4. User selects a queued item (or a page they're currently on, for direct/
   unqueued capture) and triggers capture.
5. Extension captures full inlined single-page HTML, via SingleFile's own
   capture code **vendored directly into the extension as a library** (see §3a)
   — not by messaging a separately installed SingleFile extension. The extension
   does **not** run Readability extraction itself — see §3a and §6a for why that
   moved to an async backend job.
6. Extension requests a presigned R2 upload URL from the Worker, uploads the
   HTML directly to R2 (bypassing Worker body-size limits; presigned R2 PUT
   supports objects far larger than any archived page will ever be).
7. Extension notifies the Worker that the upload is complete → Worker writes a
   `pending_captures` row to D1, using a **client-generated UUID** as the row's
   id (and marks the `queue_items` row, if any, as `captured`).
8. Backend, on its own polling schedule, discovers the new `pending_captures`
   row, pulls the HTML blob from R2, zstd-compresses it, stores it on local
   disk, computes the content hash (see §3b), deletes the R2 object, writes rows
   to Postgres (idempotently — see §3c), and finally pushes a lightweight mirror
   row back to D1 for the bookmark-list feature (see §8).
9. Backend enqueues a **screenshot job** (async, decoupled — see §6) and a
   **Readability extraction job** (async, decoupled — see §6a) against the same
   locally-stored HTML.
10. (Optional, async) Once the Readability job has populated `reader_text` for
    the capture, backend enqueues an AI job to summarize/tag it (see §7) — AI
    enrichment has a real dependency on readability extraction having already
    completed, unlike the screenshot job, which has no such dependency.
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

The extension **does not vendor Readability.js**. An earlier revision of this
design had the extension run Readability against the live, rendered DOM
immediately at capture time, on the reasoning that this happens "before any
re-archival loses render-time state." That reasoning no longer drives the
architecture: §3d's manual-upload pathway forced the question of how to extract
reader text from an already-captured HTML file with no live DOM available at
all, and the answer — run Readability against the file offline, in a real
(headless) browser — turned out to work just as well for every other capture
path too, not just manual upload. Extraction was therefore deferred uniformly to
a single async backend job (§6a), and the extension was simplified to produce
and upload HTML only. The one honest tradeoff, stated plainly rather than
glossed over: this bets on SingleFile's serialization being a faithful enough
snapshot of the live page that nothing Readability actually needs gets lost
between "live DOM" and "SingleFile's static output" — a reasonable bet given
SingleFile's whole purpose is producing a faithful static snapshot, but a real
relaxation of the original guarantee, not a free lunch.

The extension's own `package.json`/bundler setup exists to vendor SingleFile's
capture code and a WebExtension polyfill — no longer Readability.js, which was
the original reason this setup existed at all. Whether the extension still needs
a real bundler for just those two things, or whether that setup can be
simplified now that Readability.js is out of the picture entirely, is worth
revisiting once the extension is actually built.

### 3b. Content hashing

Each capture stores **two** hashes:

- `content_hash` — over the full inlined HTML. Useful for exact byte-for-byte
  dedup detection. Computed synchronously at ingestion (§3), since the HTML is
  the one artifact available immediately.
- `reader_text_hash` — over the Readability-extracted plain text. This is the
  hash that drives the dashboard's "unchanged since last capture" flag. Unlike
  `content_hash`, this is populated asynchronously, once the Readability
  extraction job (§6a) completes — `reader_text`/`reader_text_hash` are both
  nullable on `captures` and simply absent (not zero, not empty-string) until
  then. The "unchanged since last capture" feature has nothing to compare
  against for a capture whose extraction hasn't run yet, or has failed.

The full-HTML hash is a poor signal for "did the visible content change" — most
real pages embed per-load-unique content (CSRF tokens, cache-busted asset URLs,
session IDs, timestamps) even when nothing meaningful changed, so it will almost
never repeat in practice. The reader-text hash is a much more reliable (though
still imperfect — Readability output can shift for reasons unrelated to the main
content) signal for that specific UI feature.

### 3c. Capture idempotency (crash recovery)

The `pending_captures.id` (a client-generated UUID, already required for
retry-safety on the upload-complete notification — see §8) doubles as a
transient idempotency key for backend ingestion:

```sql
ALTER TABLE captures ADD COLUMN source_capture_id TEXT UNIQUE;
```

**`source_capture_id` is nullable, and is cleared back to `NULL` once ingestion
of that capture is fully done** — corrected twice over from earlier revisions of
this document (which first left it `NULL` only for manual uploads, then briefly
made it `NOT NULL` for every capture, before landing here). Its only job is
letting a retry recognize an already-committed capture without redoing the whole
pipeline, and that job has a natural end: once the R2 object is deleted **and**
D1's `fetched_by_backend` flag is confirmed set, there is no further retry
window left to protect, and nothing else ever reads this column. Clearing it
isn't just tidiness — it's what keeps a permanent, forever-growing table from
carrying a column whose entire purpose is transient, and it's what lets the
`UNIQUE` constraint mean something meaningful (many "done" rows can all hold
`NULL` simultaneously without conflict, since Postgres never treats two `NULL`s
as equal).

Every capture gets a real, unique value while it's actually in flight,
regardless of source: **client-generated** for the extension/queue flow (the
device already generates this UUID before ever talking to the Worker, for the
upload-complete notification's own retry-safety), or **backend-generated** for
manual uploads (§3d), which have no client in the loop to generate one.

**Two separate problems, both real, both need solving — this section used to
only solve one of them:**

1. **A retry must not fail forever trying to re-fetch an R2 object that a prior
   attempt already deleted.** If the backend crashes between deleting the R2
   object and confirming the D1 flag, the next poll cycle sees the same
   `pending_captures.id` again — and naively re-running the whole pipeline would
   try to pull an object that's already gone.
2. **A conflict on `source_capture_id` must not be assumed to mean "this is a
   retry."** It could instead be a genuine collision — two different captures
   that happen to share an ID (astronomically unlikely for a random UUID, but
   not impossible, and not something to just hope never happens). Treating any
   conflict as "already handled, return the existing row" would silently discard
   the second capture's real data in that case, with no error and no visible
   sign anything was lost.

**The resolution to both, together:** ingestion always attempts the full
pipeline first — pull from R2, hash, compress to local disk (keyed by
`content_hash`, not `source_capture_id`; see the note on this below), then a
single Postgres transaction that upserts the page and inserts the capture. That
insert uses `content_hash` to tell the two problems above apart:
`INSERT ... ON CONFLICT (source_capture_id) DO UPDATE ... RETURNING`, then
compare the returned row's `content_hash` against the one just computed. A match
means a legitimate retry of the identical upload — safe no-op, return the
existing row. A mismatch means a genuine collision between two different
captures — generate a fresh UUID for _this_ capture and retry the insert (a
small bounded loop), never silently dropping the new capture's data.

**Only if that whole first attempt fails** does ingestion fall back to checking
Postgres for an already-committed row matching the original `source_capture_id`
(problem 1's actual fix): if found, whatever just failed — almost always the R2
fetch, because a prior attempt's delete already succeeded — is safe to treat as
"already done," and processing jumps straight to R2/D1 cleanup. If nothing is
found, the failure is real and gets surfaced normally (logged, retried on the
next poll). This fallback deliberately never runs _instead of_ the first
attempt, only _after_ it fails — gating it upfront (checking Postgres before
ever touching R2) was tried and rejected during implementation, specifically
because it would skip the content_hash comparison above entirely and reintroduce
problem 2 in a different place. R2's own `DeleteObject` (and R2's S3-compatible
equivalent) is documented to be idempotent — deleting an already-gone key
returns success, not an error — so the cleanup steps themselves need no special
"tolerate already deleted" handling either way.

Ordering the steps this way — disk write, then DB commit, then R2 delete, then
D1 flag, then (only once both cleanup calls have actually succeeded) clearing
`source_capture_id` — means a crash at any point either leaves the R2 object in
place for a safe retry (nothing durable happened yet), or leaves only harmless
orphaned cleanup state (the durable parts already succeeded; a failure to clear
`source_capture_id` specifically is harmless on its own, since D1 will never
resurface that capture's id once `fetched_by_backend` is set, so nothing will
ever look the stale value up again regardless).

This whole scheme is uniform across capture sources, not split into two code
paths as an earlier revision of this document assumed: manual upload (§3d) just
starts the process with a backend-generated UUID as its first candidate
`source_capture_id` instead of a client-supplied one, since it has no client to
supply one. Everything downstream — the content_hash-based conflict
disambiguation, the collision retry loop, the try-first/fallback-on-failure
pattern — is identical either way.

**Local disk storage is keyed by `content_hash`, not `source_capture_id`, for a
closely related reason** (see §4/`internal/archive`'s own docs for the full
reasoning): two captures whose `source_capture_id`s collide would also collide
on a `source_capture_id`-keyed disk path, and the atomic-rename write this
project uses silently overwrites whatever's already at the destination. That's a
worse outcome than the Postgres-side collision above, since it would corrupt an
unrelated, already-successfully-stored capture's file rather than just failing
to store the new one. `content_hash` doesn't have this failure mode: two
genuinely different captures colliding would require an actual SHA-256
collision, and two captures that happen to share byte-identical content
overwriting each other with identical bytes is a harmless no-op, not data loss.

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
- **Reader text is no longer extracted client-side for this pathway either.** An
  earlier revision of this design had the dashboard's own browser run
  Readability.js against the uploaded HTML at upload time, specifically to keep
  "extraction happens in a real browser, never server-side" consistent with how
  the extension worked. That reasoning inverted once this very pathway exposed
  the actual question underneath it: manual upload has no live DOM at all, only
  an already-captured static file — and Readability runs against that file just
  as validly whether it's a headless Chrome tab or the dashboard's own tab. Once
  that was true for manual uploads, it was true for every capture path, so
  extraction was unified into a single async backend job (§6a) that all captures
  share, extension-sourced or manually uploaded alike. This pathway needs no
  Readability-specific handling of its own anymore — a manually uploaded capture
  simply gets a `readability_jobs` row created the same as any other new capture
  (see §6a). The page title is read from the uploaded HTML's `<title>` tag at
  ingestion time, uniformly for every capture regardless of source (not a
  Readability output) — this pathway needs no special handling for title either;
  see §10's `captures` schema for why this ended up being the one real source of
  title for extension-sourced captures too, not just this pathway.
- **A backend-generated UUID as the starting `source_capture_id`, transient and
  eventually cleared to `NULL`** — this pathway's own account of §3c has gone
  through a couple of revisions: first `NULL` for manual uploads specifically,
  then briefly `NOT NULL` for every capture, before landing on what §3c now
  describes in full: nullable, real while a capture is actually in flight,
  cleared once ingestion is fully done. Manual upload doesn't need its own
  insert logic to fit this — it uses the exact same content_hash-based conflict
  handling and try-first/fallback-on-failure pattern as the extension/queue flow
  (§3c), just starting with a backend-generated candidate ID instead of a
  client-supplied one, since there's no client in the loop to supply one.
- **Everything downstream of ingestion is unchanged**: content hashing (§3b),
  URL normalization (§9), grouping into `pages` by `normalized_url` — a manual
  upload of an already-captured URL is just another new version under the same
  page, identical in kind to any other re-archive above. The async screenshot
  job (§6) and the async Readability extraction job (§6a) both apply unmodified,
  since both already explicitly operate on "already-captured, fully inlined
  SingleFile HTML on local disk" — which is exactly the shape of a manually
  uploaded file once ingestion has stored it. AI enrichment (§7) applies
  unmodified too, once (and only once) the Readability job has populated
  `reader_text` for this capture, same as any other capture.
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

### 3e. The agent process (background job triggering)

Both backend ingestion (§3c's `Ingester.RunOnce`) and the D1 bookmark-list
mirror sync (§8's `Syncer.SyncOnce`) were built as fully self-contained, fully
tested callable units with nothing actually invoking them — deliberate, not an
oversight, since the trigger mechanism was a genuinely separate decision worth
settling on its own.

**`recueil agent`: a dedicated subcommand/process, not a goroutine inside
`recueil server`.** Both share the same binary/image, deployed as separate
services in the same compose file with different commands. Two other shapes were
seriously considered and rejected:

- **A goroutine inside `server`** — the obvious lightest-weight option, and
  rejected specifically over shutdown coordination: cleanly stopping two
  different kinds of concurrent work (serving in-flight HTTP requests vs.
  finishing or abandoning a background job) inside one process, on one
  `SIGTERM`, is real complexity a separate process sidesteps entirely — each
  process only ever has to coordinate shutdown for its own single kind of work.
- **Cron** — ruled out early. The primary deploy target (Docker Compose) has no
  cron mechanism of its own; the host scheduling `docker exec`/ `docker run`
  invocations against a running compose stack isn't ergonomic; and a "poor man's
  cron" (a tick embedded in some other process) just reintroduces the same
  shutdown-coordination problem the goroutine option already lost on, while
  adding scheduling complexity on top.

A dedicated process also gets independent failure and resource isolation for
free, as a consequence of the deployment shape rather than anything special
built for it: a runaway or hung job (a headless-Chrome screenshot job spiking
memory, say — not built yet, but the same reasoning applies in advance) is
contained to the agent container and can't degrade HTTP request latency, and
Docker's own per-service restart policy handles recovering it without touching
the web process at all.

**Coordination layer: Postgres, not a message broker.** RabbitMQ and a
Redis-backed queue (`asynq`, the Go equivalent of Sidekiq — Redis itself isn't
Ruby-specific even though Sidekiq is) were both seriously considered, on the
reasoning that there's real job-ordering to coordinate: AI enrichment (§7) can
only run after readability extraction (§6a) succeeds. Neither was adopted,
because that ordering doesn't actually need a message-broker-level
dependency/DAG feature at all — it's expressed simply as _when a job row gets
created_: an `ai_jobs` row doesn't exist until whatever marks the corresponding
`readability_jobs` row `done` creates it, in the same transaction. The queue
itself never needs to understand the dependency; it only ever needs to answer
"give me pending rows," which Postgres already does. What a real message broker
actually buys over this — routing topologies, fan-out, many concurrent
independent consumers, back-pressure across separate services — isn't something
a single agent process at personal-archive scale ever exercises, and either
option would be a second stateful service (deploy it, back it up, keep it
patched) purely to gain capability this project doesn't need, when Postgres is
already a hard dependency regardless. The claim pattern this needs
(`UPDATE ... SET status = 'processing' WHERE status = 'pending' RETURNING *`) is
exactly what `queue_items.claim` (§2) already does in the Worker — not a new
pattern, the same one reused a layer down.

`screenshot_jobs`/`readability_jobs` (§6, §6a) already have the shape this
implies (`status`, `attempts`, `next_attempt_at`, `error`, `completed_at`) — not
incidentally job-queue-shaped, built that way on purpose.

**Postgres `LISTEN`/`NOTIFY`** (near-instant job pickup, layered on top of the
poll loop as a pure latency optimization — the poll loop stays the actual
correctness guarantee regardless, since `NOTIFY` isn't durable and a missed
notification with no fallback poll would just silently never process that job)
was discussed and deliberately deferred, not rejected. Plain polling is entirely
sufficient at this project's scale for now; worth reconsidering only if
poll-interval latency ever actually becomes a real complaint.

**Startup and migrations**: `agent` does **not** run migrations itself, unlike
`server`. Postgres migrations are safe to run from multiple processes
concurrently (goose's own session-level advisory lock, via `internal/pgmigrate`,
serializes that) — but D1 migrations have no equivalent locking, and
`server`/`agent` starting together in Compose gives no ordering guarantee
between them. Rather than have `agent` run Postgres migrations but not D1 (an
asymmetry that would need its own explanation every time someone reads the
code), it runs neither: `server` owns migrations exclusively, and `agent`
assumes they're already applied. If `agent` happens to start first, its earliest
cycle(s) simply fail against a not-yet-ready schema, get logged, and self-heal
on the next tick once `server` catches up — the same graceful-degradation shape
`RunOnce`/`SyncOnce` already have for a single failed item, just one level up,
at the whole-cycle granularity.

**One shared ticker, both jobs run sequentially per tick** — `Ingester.RunOnce`
then `Syncer.SyncOnce`, both on the same interval
(`agent_poll_interval_seconds`, default 120), not two independently-scheduled
loops. The simplest thing that works; splitting them onto separate cadences is a
natural, easy follow-up if one ever genuinely needs to run more or less often
than the other, not a constraint this design paints itself into. A cycle runs
synchronously within the same `select` loop iteration that reads the ticker
channel, deliberately not spawned into its own goroutine per tick —
`time.Ticker`'s channel buffers exactly one pending tick, so a cycle that runs
longer than the interval simply means some ticks are silently dropped rather
than a backlog of queued cycles building up; the next cycle starts as soon as
the current one finishes and at least one tick has fired since, not once per
missed interval. Either job failing is logged, not propagated as the agent
process's own failure — the same "log and continue" philosophy
`RunOnce`/`SyncOnce` already apply at their own per-item/per-batch level, one
layer further up.

---

### 3f. The CLI (`recueil auth` / `recueil enqueue`)

The CLI's own two commands, and specifically why their configuration handling
deliberately diverges from `server`/`agent`'s:

- **Two different config postures for two different audiences.**
  `server`/`agent` require an explicit `--config` file or environment variables
  — no automatic search of `$HOME` or the working directory (§13a) — a
  deliberate choice for production processes, where implicit config-discovery
  could silently pick up an unintended file. `auth`/`enqueue` are the opposite
  kind of thing: an end user's personal tool, where automatic discovery is the
  expected, idiomatic UX (the same shape `git`, `ssh`, and most CLI tools
  already have people trained on). This isn't a reversal of the earlier
  decision, it's a second, narrower one for a genuinely different audience —
  `server`/`agent`'s existing explicit-only behavior is completely unchanged.
- **No shared/nested `config.toml` at all, in the end** — considered (a
  `[server]` section vs. flat top-level keys) and set aside, for a sharper
  reason than "the CLI has nothing to configure yet": once the pairing token
  needed its own dedicated file anyway (below), and `worker_url` turned out to
  belong with that token rather than as an independent setting, there was
  nothing left for the CLI to read from `config.toml` at all. `enqueue`/`auth`
  don't touch Viper or `internal/config` in any way; every server-only key stays
  exactly as it is.
- **Pairing token input: masked prompt if a TTY, stdin otherwise — deliberately
  never a `--token` flag.** A flag would be visible in shell history and
  system-wide `ps` output for the process's whole lifetime — a real exposure for
  a bearer credential, not a theoretical one. `mattn/go-isatty` (already a
  dependency) decides which path to take; `golang.org/x/term.ReadPassword` (new,
  small, official) does the actual no-echo read. This gets scriptability for
  free without ever needing the flag: `echo "$TOKEN" | recueil auth --url ...`
  reads from stdin directly since stdin isn't a terminal in that case.
- **`internal/clicreds`: a dedicated file, not a field in `config.toml`.**
  `$XDG_CONFIG_HOME/recueil/credentials.json` (falling back to
  `$HOME/.config/recueil/`, the Base Directory spec's own documented default),
  `0600`, written via temp-file-then-atomic-rename (the same pattern
  `internal/archive` already uses, for the same reason: a crash or error partway
  through a write must never leave a half-written file at the real path). Two
  reasons this isn't just a `config.toml` field: `auth` rewriting part of a file
  a user might also hand-edit risks clobbering their formatting (nothing this
  project uses for TOML writing round-trips cleanly), and a bearer credential
  arguably deserves its own tighter-scoped file rather than sharing a general
  settings file's permissions regardless. `XDG_CONFIG_HOME` specifically, not
  `XDG_STATE_HOME` or `XDG_DATA_HOME` -- the Base Directory spec doesn't
  perfectly disambiguate this by its own letter (a token isn't quite
  "configuration," but it's even less "state"/session data or generated "data"
  either), so this follows the ecosystem's own precedent instead of relitigating
  the spec: `gh` (GitHub CLI) stores its own auth under `XDG_CONFIG_HOME` too.
- **`worker_url` is stored alongside the token, not as an independent setting.**
  A token is only ever meaningful for the specific Worker that issued it, so the
  two are one unit that's always captured, stored, and read together — not two
  related-but-separate values. Concretely:
  `recueil auth --url <worker-url> [--name <name>]` requires `--url` (there's no
  default to fall back to, and no config file to read one from either);
  `recueil enqueue` then reads both back from the one stored file, with no
  `--url` override flag on `enqueue` itself. A per-call override, or real
  multi-server profile support, was considered and deliberately deferred:
  there's no supporting machinery on the `auth` side yet (nothing to switch
  between), so adding the flag now would just be confusing rather than actually
  useful — an honest 401 if you ever do point a stored token at the wrong Worker
  is a fine failure mode until multi-profile support is worth building for real.
- **`internal/deviceapi`: `Pair` and `Client` are deliberately separate, not one
  unified type.** `POST /pair` is unauthenticated by nature — it's how a device
  obtains a bearer token in the first place, so it can't require one — while
  `Client.Enqueue` (`POST /queue`) requires a token already in hand. Forcing
  both into one type would mean either a `Client` that's usable before it has
  real credentials, or a separate construction path for pairing anyway — no
  simpler than just keeping them apart. Neither authenticates as the backend
  itself (unlike `internal/mirror` and `internal/ingest.WorkerClient`, both
  service-secret-gated); this package is specifically what a paired _device_
  does against the Worker's public, device-facing endpoints.
- **`recueil enqueue <url> [<url>...]`** accepts multiple URLs in one invocation
  (`POST /queue` has no batch form, so this is a client-side loop, one call per
  URL) and continues past an individual failure rather than stopping the whole
  batch — the same "one bad item shouldn't block the rest" philosophy already
  applied to `Ingester.RunOnce`/`Syncer.SyncOnce` (§3c, §8), reported as a
  summary and a non-zero exit if anything failed, rather than aborting partway
  through. Each URL gets its own freshly-generated `google/uuid` (already a
  dependency) as `POST /queue`'s client-generated `id` — the same
  idempotency-key pattern already established for that endpoint (a retried call
  with the same `id` is a safe no-op, not a duplicate enqueue).
- Schema-wise, there was nothing to add: `tokens.device_name` and `device_type`
  (already including `'cli'` in its allowed set) were already in place from
  Phase 2, and `POST /pair` already required and stored `device_name` in its
  request body. `recueil auth`'s only actual job here is supplying a sensible
  one — `os.Hostname()` by default, `--name` to override.

---

### 3g. Favicon capture

Captured client-side, the same way HTML is — not fetched by the backend. This is
a deliberate extension of §1's core principle, not an exception to it: a favicon
fetch is still a live request against a URL the extension already has an
authenticated browser context for (most favicons don't need that, but some do —
an intranet tool or private wiki is a real if narrow case), so the backend never
touches the live web at all, full stop, with no carve-out to reason about later.

**Selection — link-level, not pixel-level.** The extension resolves a candidate
URL by checking, in order: any `<link rel="icon">` /
`<link rel="apple-touch-icon">` tags declared on the page (preferring
`type="image/svg+xml"` over a raster candidate, and the largest declared `sizes`
among raster candidates), then falling back to the conventional root-relative
`/favicon.svg`, `/favicon.png`, `/favicon.ico`, tried in that order. If none of
that resolves to anything, `favicon_path` simply stays `NULL` for that capture —
not every site has one, and not finding one is never an error.

**No image processing, deliberately.** Whatever bytes come back — including a
legacy multi-resolution `.ico` container — are stored exactly as received. Every
modern browser renders `.ico` directly in an `<img>` tag, so there's no real
need to decode "the largest embedded image" out of one; that's a "revisit if it
becomes a felt problem" item, not a day-one requirement.

**Favicon is per-capture state, not page state**, the same way the HTML itself
is: `captures.favicon_path` (§10) is written once per capture and never mutated
or cleaned up afterward, so there's no dangling-reference risk across a page's
capture history (an old capture's favicon, if any, stays exactly as it was
captured). `pages.favicon_path` is denormalized from the _latest_ capture the
same way `pages.title` already is — including being overwritten back to `NULL`
if the latest capture genuinely didn't find one, not preserved from an earlier
capture that did.

**Disk layout — shares the capture's directory, keyed by its own hash.**
`internal/archive`'s `Store` was restructured around this: every asset belonging
to one capture (the HTML, now a favicon, later a screenshot) lives together
under a single directory, sharded by the capture's own `content_hash`
(`CaptureDir`). The HTML itself keeps a fixed filename inside that directory,
since the directory already encodes its identity — but a secondary asset like
the favicon is named by _its own_ content hash plus a real extension
(`WriteAsset`), never the html's. This matters concretely: two captures can have
byte-identical HTML while carrying genuinely different favicons (a static page
recaptured after the site's icon changed), so keying a favicon by the html's
hash would silently overwrite one capture's favicon with another's — precisely
the bug this package already exists to avoid, one level removed. Compression is
per-asset-type, not a blanket zstd: SVG (plain XML) compresses well and gets it;
PNG/ICO are already-compressed binary formats and are stored raw.

**R2 key convention mirrors the HTML object's.** `POST /captures/upload-urls`
accepts an optional `(favicon_ext, content_sha256_favicon)` pair — both present
or both absent, no half-specified request — and, when present, issues a second
presigned PUT alongside the HTML one, at a deterministic key
`pending/{userId}/{captureId}/favicon.{ext}` (`ext` ∈ `svg | png | ico`). The
extension itself is baked into the key (unlike `page.html`'s implicit,
always-the-same suffix) specifically so the backend can recover the real format
by reading the key back at ingestion (`filepath.Ext`), rather than needing a
separate mime/type column anywhere in Postgres or D1. `POST /queue/:id/complete`
and its direct-capture counterpart `POST /captures/complete` (added once actual
extension work reached this point — completing a capture that was never enqueued
in the first place, e.g. archiving a page the user is already on; see
`pending_captures.queue_item_id`'s own nullability) both take the same
treatment: the caller declares _whether_ it uploaded a favicon and in what
format (`favicon_ext`), and the Worker recomputes the deterministic key itself —
the same never-trust-a-client-supplied-key posture `r2_key_html` already has.

**Ingestion is best-effort, and never fails the capture.** A favicon fetch or
disk write failing at ingestion time is logged and otherwise ignored — an
unreachable or malformed favicon object is a cosmetic loss, never a reason to
lose an otherwise-good HTML capture. The favicon's R2 object gets cleaned up
alongside the HTML object's the same way, best-effort on that side too (a
leftover favicon object in R2's temporary buffer is harmless).

**The extension's own bookmark-list menu (§8) does not carry a stored favicon at
all — it live-fetches the site's current favicon at render time**, the same way
a browser's native bookmarks UI would. This was a deliberate choice among three
options considered: storing favicon bytes inline as a D1 `BLOB` on
`archived_pages` (favicons are small enough that this would've worked, and
remains the natural next step if live-fetching proves unsatisfying), a durable
copy in R2 (rejected outright — R2 is documented as a temporary buffer only, §4,
and every other object in it is deleted right after the backend pulls it;
keeping favicons there permanently would be a new, different lifecycle with no
other precedent in this design), or live-fetching with no sync/storage at all
(what's actually built). Live-fetching also sidesteps a real semantic question
the other two don't: whether the menu should show the favicon _as archived_ or
_as it is right now_ — for a live bookmark list, current is arguably the more
correct answer anyway.

---

### 3h. Browser extension architecture

**Single Manifest V3 codebase covers Chrome and Firefox.** Chrome's MV2 support
is fully gone (dead since October 2024); Firefox supports both indefinitely, but
nothing recueil needs (no blocking `webRequest`) actually requires MV2 there.
Upstream SingleFile forked into two separate repos (`SingleFile` for
Firefox/MV2, `SingleFile-MV3` for Chrome/Edge) specifically because migrating a
large, mature, feature-heavy extension is real, risky work its own maintainer is
deliberately delaying — confirmed directly by gildas-lormeau in a GitHub
discussion: Firefox is technically MV3-ready, he's "waiting until the last
moment to migrate" because "Manifest V3 extension development is a real pain."
That asymmetry doesn't apply to recueil: there's no existing MV2 codebase to
preserve, so a single MV3 codebase from day one is the right call, even though
it wasn't the right call for him. Safari is MV3-capable too but needs a
genuinely separate packaging/distribution pipeline (Xcode-wrapped,
`safari-web-extension-converter`) — deferred as a later, mechanical step once
the extension itself works, not attempted yet.

**`single-file-core` is a direct dependency, not a vendored fork of either
official extension.** Both `SingleFile` and `SingleFile-MV3` are full end-user
extensions (options pages, multiple upload destinations, auto-save, annotation)
built around a separate, genuinely engine-only npm package that also backs
`single-file-cli` headlessly with zero browser-extension APIs involved. recueil
depends on that same package directly and writes its own thin MV3 wrapper around
it — recueil's surface area (no auto-save, no annotation, no multiple upload
destinations) is much smaller than upstream's, so there was never a reason to
inherit their UI or their Firefox/Chrome fork split. Both share the same
AGPL-3.0-or-later license, so no licensing mismatch either.

**Capture is a two-step injection**, not a single call:
`scripting .executeScript({files: ["capture-inject.js"]})` loads the bundle
(defines a global, since `func`-injected functions can't themselves import
anything), then a separate
`executeScript({func: () => globalThis.__recueilSingleFile .captureFrame()})`
actually invokes it and returns the result. Background
(`extension/src/background/`), the injectable capture bundle
(`extension/src/capture-inject/`), and the popup (`extension/src/popup/`) are
three genuinely separate esbuild entry points/bundles, not one — they run in
different contexts (service worker vs. a page's content-script world vs. an
extension page) and load at different times, so bundling them together would
mean the largest thing in this build (`single-file-core`) parses on every
service-worker wake for no benefit.

**Resource fetching is direct-fetch-first, background-relay-fallback** — not the
reverse. A background-context fetch bypasses a page's own CORS restrictions (the
reason the relay exists at all: `single-file-core` needs to inline resources the
page itself couldn't otherwise read), but routing _every_ resource through the
background unconditionally means a capture's success depends on the background
staying alive for the entire operation, which is exactly the wrong shape under
MV3's non-persistent background model. Modeled directly on `SingleFile-MV3`'s
own `fetchResource` (`src/lib/single-file/fetch/content/content-fetch.js`),
which tries the page's own `fetch()` first and only relays on failure — most
resources on most pages are same-origin or already CORS-permitted, so this
resolves the large majority of fetches with no background round-trip at all.
Notably, `SingleFile-MV3` has no keepalive mechanism anywhere in its source (no
`runtime.connect` port, no alarm-based ping) — this fetch ordering is _why_, not
a gap they left unaddressed.

**Multi-frame (iframe) capture is implemented — embedded frames are inlined into
the top document, not dropped.** `single-file-core` already unconditionally
bundles frame-tree collection logic as a transitive dependency
(`processors/index.js` → `content-frame-tree.js`), gated behind the
`removeFrames` option and requiring the bundle to be injected into every frame
(`target.allFrames: true`), not just the top one. Turning it on took three
pieces, staged deliberately in isolated steps after an initial single-change
attempt broke even single-frame pages in a way that was hard to diagnose:

1. Injecting the bundle into every frame (`allFrames: true` on the `files` step,
   `removeFrames` still `true` so collection never runs) — confirmed safe on its
   own, both plain and iframe-containing pages captured correctly.
2. Flipping `removeFrames: false` — where the symptom appeared: `getPageData()`
   hung and Firefox reported
   `Could not establish connection. Receiving end does not exist.`, on _any_
   page including ones with zero iframes (the top frame still runs `getAsync`
   there, because `globalThis.frames` is always truthy — it reports its own
   empty frame list through the same path).
3. Adding a **background frame-tree relay**
   (`extension/src/background/frame-tree-relay.js`) — the actual fix.

The root cause is a transport split inside `content-frame-tree.js`'s
`sendMessage`, which chooses how a frame hands its serialized DOM back to the
top frame by reading `globalThis.browser`:

```js
if (targetWindow == top && browser && browser.runtime && browser.runtime.sendMessage) {
  browser.runtime.sendMessage(message); // expects the background to forward to frameId 0
} else {
  targetWindow.postMessage(...);        // in-page, no background involved
}
```

- On **Chrome**, `globalThis.browser` is `undefined` in the content-script world
  — `webextension-polyfill` is bundled as a module import, which under esbuild's
  CJS path never assigns `globalThis.browser`, and Chrome has no native
  `browser`. So the collector takes the `postMessage`/`MessageChannel` branch
  and coordinates entirely in-page; no background is involved and step 1's
  injection is all it needs.
- On **Firefox** (where `web-ext run` puts iterative testing),
  `globalThis .browser` is native, so a frame posts its result through
  `browser.runtime.sendMessage` and _expects the background to relay it to the
  top frame_. With no relay listener, that send both rejects with "Receiving end
  does not exist." _and_ never delivers the frame data — so the top frame's
  collection never completes. The hang and the error are the same event, which
  is why it fired even on zero-iframe pages.

`SingleFile-MV3` has exactly this relay and recueil simply lacked it: its
`background.js` imports `frame-tree/bg/frame-tree.js`, a small listener that
forwards `singlefile.frameTree.initResponse` / `ackInitRequest` to
`tabs.sendMessage(tabId, message, { frameId: 0 })` and returns a resolved
promise so the sender settles instead of rejecting. recueil's
`frame-tree-relay.js` is modeled directly on it, registered alongside the fetch
relay in `background/index.js`. It's a hard requirement on Firefox and a no-op
on Chrome (never invoked there), so both targets stay on one background code
path even though the content side diverges — which also keeps Chrome on its
background-independent in-page path, consistent with the direct-fetch-first
reasoning above.

Two earlier source-reading theories pointed at the content side rather than the
background, and neither fixed it — the more instructive one:
`content-frame-tree.js`'s `sendInitResponse` _first_ tries a synchronous
same-realm call, `top.singlefile.processors.frameTree.initResponse(message)`,
before falling back to `sendMessage`. Both official extensions get
`globalThis.singlefile` for free because their Rollup builds emit
`single-file-core/single-file.js` as its own output with
`output.name: "singlefile"`; recueil's wrapper entry point has no exports of its
own, so the equivalent esbuild `globalName` wouldn't reproduce it, and
`globalThis.singlefile = singlefile` (the already-imported namespace) would. But
that leg only matters for the top frame's own frames — cross-origin subframes
always throw on `top.singlefile` and fall through regardless — and the leg it
falls _through to_ is the `runtime.sendMessage` transport above, the one with no
receiver. That's why assigning the global didn't resolve the hang on its own: it
fixes a path the code only sometimes takes. It's deliberately left out of the
shipped fix; at most it's a latency optimization (one fewer round-trip for the
top frame's own frames) worth adding later as its own isolated step.

This was confirmed in a real capture, not just the toolchain — closing out the
earlier state where source-reading had twice produced a plausible theory that
didn't match observed behavior. `frameFetch` is wired to `relayFetch` explicitly
in `bundle-entry.js`, though that's documentation only: `core/util.js`'s own
`frameFetch || fetch` default already resolves to `relayFetch`.

---

### 3i. Queue-driven capture

**Human-in-the-loop by default, not as a detected-failure fallback.** The
original design assumed queue-driven capture would open a tab in the background
(`active: false`), wait for it to load, capture it, and close it — unsupervised,
the same shape as a headless-browser cron job. That assumption turned out to be
wrong for a specific, concrete reason: a CAPTCHA or paywall page captures
_successfully_ from `single-file-core`'s point of view — no error, no timeout,
just the wrong content, silently archived as if it were the real page (confirmed
directly: pages already archived this way exist in testing). There is no generic
signal — no DOM marker, no HTTP status, nothing — that distinguishes "this page
needs a human" from "this page loaded fine." Any design that tries to detect
that automatically doesn't work, and trying to solve it (auto-bypassing
CAPTCHAs, defeating paywalls) isn't something this project should be doing
anyway. So the design puts a human in the loop for every queue item, always —
not as a fallback path for failures the system noticed, since it fundamentally
can't notice this particular kind of failure.

**Concretely:**

- The popup shows a plain list of pending items (`GET /queue` — id and url are
  all that's meaningful to show), cached in `storage.local` and refreshed from
  four places: `runtime.onStartup`/`onInstalled`, a 6-hour alarm, the popup's
  own manual refresh button, and immediately after a successful pairing
  (otherwise the popup shows "nothing in the queue" until whichever of the first
  three happens to fire next, even when the instance already has real pending
  items). None of these run on every service-worker wake, which would mean an
  extra Worker round-trip on nearly every message this background handles,
  including ones with nothing to do with the queue.
- **This cached list is never authoritative.** Clicking an item sends the real,
  live `POST /queue/:id/claim` — reusing Phase 2's existing atomic claim
  (`UPDATE ... WHERE status = 'pending' OR (status = 'claimed' AND claimed_at < ...)`)
  and its 404/409/410 distinctions untouched; no new backend work was needed for
  any of this. A claim failure's status code is translated into a human-readable
  message in the background, before it ever crosses the `runtime.sendMessage`
  boundary back to the popup — a custom property like an error's `.status` isn't
  reliably preserved across that boundary the way `.message` is, so the
  translation has to happen while it's still a real, in-process object, not be
  reconstructed from whatever survives the crossing.
- **On a successful claim, a new tab opens focused, in the current window**
  (`tabs.create({url, active: true})`) — deliberately stealing focus, unlike the
  original background-tab assumption, precisely because this is now an explicit
  action the user just asked for, the same as clicking any other link.
- **The user solves whatever the page needs entirely by hand** — no detection,
  no automation, ever attempted.
- **Completion reuses the exact existing direct-capture pipeline, not a separate
  "queue capture" path.** `capture.js`'s `captureTab`/`captureActiveTab` take an
  optional `queueItemId`, sourced from a small `tabId -> queueItemId` map
  (`storage.js`, written by the claim step) — when set, completion calls
  `POST /queue/:id/complete` instead of `POST /captures/complete`; everything
  upstream of that one call (inject, hash, presign, upload) is identical either
  way. The map entry is only cleared on success, not on failure — a failed
  attempt (a transient network error) shouldn't lose the tab's association with
  the item it's fulfilling; retrying is just clicking "Save this page" again on
  the same tab, not going back to re-claim (which would be redundant anyway —
  this device already holds the claim).
- **An abandoned claim needs no explicit handling.** If the user closes the tab
  without ever completing it, nothing further happens on the extension side —
  the Worker's own claim already goes stale and becomes reclaimable (by any
  device) after 15 minutes, a mechanism that already existed before any of this
  was built. A `tabs.onRemoved` listener does tidy up the `tabId -> queueItemId`
  map entry on tab close, but purely for storage hygiene (so it doesn't grow
  without bound over a long browsing session), not because leaving it would be
  incorrect.
- **The tab auto-closes on success, but only for queue-driven captures, never
  direct ones.** A direct capture's tab is one the user already had open for
  their own reasons — closing it out from under them would be genuinely
  disruptive. A queue-driven tab exists _only_ because clicking a queue item
  created it; once the capture succeeds it's served its entire purpose, the same
  way a print-preview window closing after printing feels natural rather than
  disruptive. Left open on failure, so the user can see what went wrong or just
  retry. Best-effort (`.catch(() => {})`) either way: the capture itself has
  already fully succeeded by the point the tab close is attempted, so a failure
  to close (the user having already closed it themselves in the interim, say) is
  not a reason to report the capture as failed.
- **A missed periodic alarm doesn't accumulate.** Confirmed against Chrome's own
  documentation ("repeating alarms will fire at most once and then be
  rescheduled using the specified period starting from when the device wakes")
  and consistent with Firefox's own bug history (reports describe a missed alarm
  firing _late_, never multiple times to catch up) — a laptop suspended through
  several missed 6-hour ticks triggers exactly one refresh on resume, not one
  per missed tick.

The toolbar badge (`action.setBadgeText`/`setBadgeBackgroundColor`, cleared to
empty when the queue is empty) is updated in the same function that refreshes
the cache, so there's exactly one place that can ever disagree with the list the
popup shows — not a separately-maintained count that could drift from it.

---

## 4. Storage Strategy

- **R2 is temporary only.** It exists purely to get large payloads from the
  extension (which may not have a stable public endpoint to push to) to the
  backend (which may not be reachable to receive a push). Once the backend has
  pulled and locally stored a capture's blobs, they are deleted from R2.
- **Local disk is canonical.** The backend stores the zstd-compressed HTML (HTML
  compresses extremely well with zstd, commonly 80-90% size reduction) on local
  disk, referenced by path from the `captures` table. Thumbnails (see §6) and
  favicons (§3g) are also stored on local disk, never in R2 — every asset for
  one capture lives together under a single directory (`internal/archive`'s
  `CaptureDir`), sharded by the capture's own `content_hash`.
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
- **Operator account management via CLI, bypassing both the dashboard and the
  bootstrap token.** `recueil user create <username> [--role admin|member]` and
  `recueil user reset-password <username>` sit alongside the bootstrap-token
  flow above, not in place of it — they're for an operator who already has shell
  access to the box the backend runs on, useful anywhere the dashboard isn't
  available yet (e.g. before Phase 4 ships) or where curling the HTTP API by
  hand would be unpleasant. Both connect straight to Postgres using the same
  config `recueil server` reads (`config.Load()`), apply migrations the same way
  `server` does, and call the same `auth` package functions (`HashPassword`,
  `GeneratePairingToken`, `EncryptPairingToken`) and `sqlc` queries the HTTP
  handlers already use — there's no separate code path to keep in sync, just a
  different transport (direct DB access instead of HTTP). `user create` also
  pushes the new pairing token's hash to D1 via `internal/mirror`, exactly as
  `POST /api/setup`/`POST /api/auth/register` do, since a token that only exists
  in Postgres can't actually pair a device. `user reset-password` additionally
  calls `DeleteSessionsForUser`, invalidating any existing dashboard sessions —
  a pre-reset cookie staying valid would undercut the point of resetting a
  password. Neither command touches the bootstrap token itself; they're a
  straight-line administrative path that deliberately requires server-level
  access (the same trust boundary `server`/`agent`'s config already assumes),
  not a second way to satisfy the first-admin flow's own token requirement.

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
screenshot at all — it uploads only HTML (see §3). Thumbnail generation now
happens as an async backend job, after a capture's HTML has already been pulled
from R2 and stored locally.

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
  nullable. Bounded retry with backoff, same shape as §7, tracked in its own
  `screenshot_jobs` table (§10) — decoupled from `readability_jobs` (§6a)
  despite both running through the same headless-Chrome sidecar and often the
  same page load. This is deliberate, not an oversight: a screenshot can time
  out while extraction succeeds (or the reverse), and re-extraction after a
  Readability.js upgrade (§6a) has no reason to also redo a perfectly good
  screenshot. One combined table would need per-artifact status/attempts columns
  anyway to represent that independence, which is just two tables' worth of
  columns forced into one row — no real benefit over keeping them separate. The
  "same page load can serve both" idea is a scheduling optimization for
  whichever code ends up driving the sidecar (notice two pending jobs
  referencing the same capture, do one page load, write two separate
  completions), not a reason to merge the schema.

### Consequence for the schema

Because the screenshot is no longer produced client-side and never touches R2:

- `r2_key_thumbnail` is **removed** from the D1 `pending_captures` table.
- The extension only needs to request a presigned URL for one object (HTML) —
  see §6a for why `r2_key_readable` is removed too, for the same reason
  reader-text extraction moved off the extension entirely.
- New `screenshot_jobs` table (§10), mirroring `ai_jobs`'s
  `status`/`attempts`/`next_attempt_at`/`error`/`completed_at` shape exactly,
  one row per capture — this was referenced by name ("same shape as §7") in an
  earlier revision but never actually given a schema entry; that gap is closed
  here.

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

## 6a. Readability Extraction

**Moved from the extension (and, for manual uploads, the dashboard's browser) to
the backend, sharing the same headless-Chrome sidecar as §6.** Neither the
extension nor manual upload runs Readability.js anymore; every capture's reader
text is produced by a single async backend job, after the HTML has already been
pulled from R2 (or, for manual uploads, accepted directly) and stored locally.

### Why this is safe, and the one real tradeoff

The same reasoning as §6 applies for the same reason: rendering
already-captured, fully inlined, script-stripped HTML offline is not the "fetch
a live URL" operation §1 forbids. No network requests, no live authentication
state, no CAPTCHA.

The honest tradeoff, not glossed over: §3a's original design ran Readability
against a **live, rendered DOM**, specifically "before any re-archival loses
render-time state." Running it later against SingleFile's serialized output
instead bets that SingleFile's snapshot is a faithful enough substitute for that
live DOM that nothing Readability actually needs gets lost in between. This is a
reasonable bet — producing a faithful static snapshot is SingleFile's entire
purpose — but it is a real relaxation of the original guarantee, worth stating
plainly rather than assuming away.

### Design

- Runs in the **same headless-Chrome sidecar** as §6 (`chromedp` +
  `chromedp/headless-shell`), not a second browser instance — a single page load
  of the already-captured HTML can plausibly serve both the screenshot and the
  Readability extraction, though whether to actually combine them into one
  job/one page-load or keep them as two independently-scheduled jobs sharing one
  browser pool is an implementation-phase decision, not resolved here.
- Readability.js itself is **vendored into the backend** (or wherever the
  sidecar-driving code lives) and injected into the loaded page via
  `chromedp.Evaluate`, then run as `new Readability(document).parse()` against
  the real DOM that headless Chrome has rendered — this is the actual upstream
  Readability.js library, run in a real DOM, just no longer in the _original_
  capturing browser tab.
- **Fully async and non-blocking**, matching the `ai_jobs`/§6 pattern: a capture
  is fully valid, searchable (its `content_hash`-based dedup still works), and
  browsable with `reader_text`/`reader_text_hash` both `NULL` until extraction
  completes — or permanently, if it never succeeds. Bounded retry with backoff,
  same shape as §7.
- **Re-extraction, in place, no history kept.** If the vendored Readability.js
  is upgraded later, re-running extraction against a capture's already-stored
  HTML overwrites `reader_text`/`reader_text_hash`/`readability_version` on that
  `captures` row directly — no prior extraction's output is retained.
  `readability_version` records only which version most recently produced what's
  currently stored, not a history of every version ever tried.

### Consequence for the schema

- `captures.reader_text` and `captures.reader_text_hash` become **nullable**
  (§3b) — previously implicitly synchronous, now populated asynchronously or not
  at all.
- `captures.readability_version TEXT` (nullable) — new column, alongside
  `reader_text`, recording which vendored Readability.js version produced it.
  Lives on `captures` itself, **not** a separate job-owned copy, for a concrete
  technical reason: `captures.reader_text_tsv` (§10) is a Postgres
  `GENERATED ALWAYS AS` column, and generated columns can only reference other
  columns in the _same row_ — so the underlying `reader_text` has to live on
  `captures` directly for full-text search to work at all, unlike `ai_jobs`,
  which keeps its own copy of `summary` fully decoupled from `captures`.
- New `readability_jobs` table (§10), mirroring `ai_jobs`'s
  `status`/`attempts`/`next_attempt_at`/`error`/`completed_at` retry-and-backoff
  shape exactly, one row per capture — but holding **no** copy of the extracted
  text itself (that lives on `captures`, per above). Reading a capture's full
  readability state means joining `captures` and `readability_jobs`, not reading
  either table alone.
- `pending_captures.r2_key_readable` (D1) is **removed** entirely — no client
  will ever populate it going forward, since no client extracts or uploads
  reader text anymore.

---

## 7. AI Enrichment (Optional)

- Entirely optional and asynchronous — never blocks capture or ingestion. A
  capture is fully valid, searchable, and browsable with zero AI fields
  populated.
- Runs against the Readability-extracted plain text, not the raw HTML — cheaper
  and produces better summaries than trying to parse rendered HTML. This
  introduces a real **sequencing dependency** on §6a that didn't exist when
  extraction was synchronous: the AI job for a capture should not run (or should
  itself wait/reschedule) until that capture's `readability_jobs` row shows a
  completed extraction with non-null `reader_text` — unlike the screenshot job
  (§6), which has no such dependency on anything else async.
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
- The extension does **not** live-sync this list either. It caches the list
  locally and refreshes on a coarse schedule (see §7 polling cadence below) or
  on explicit user request, using its own incremental "give me changes since X"
  query against `archived_pages.updated_at` — a separate concern from the
  backend's own sync job above, just reusing the same column for the same
  reason.
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
function — deliberately, since ClearURLs is expected to be the first entry, not
the only one. A future step might be a different third-party library, or a
hand-rolled Recueil-specific ruleset; the pipeline shape means adding one never
requires touching an existing step, and steps can be freely reordered.
Implemented as `internal/urlnorm`: a `Step` interface
(`Normalize(ctx, rawURL string) (string, error)`, string in/string out —
deliberately not a shared parsed-URL representation, so an external library with
its own string-based API is trivial to slot in as a step) and a `Pipeline` that
runs a sequence of `Step`s, each fed the previous one's output. Today's pipeline
is exactly two steps, run in this order:

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
- **Two upstream behaviors are deliberately not ported at all** — not bugs, not
  future work, structurally excluded from `internal/urlnorm`'s own data model:
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
  title TEXT,                        -- denormalized from latest capture
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
                                      -- service (§6); null until then
  favicon_path TEXT,                 -- captured client-side alongside the
                                      -- HTML itself (§3g), so -- unlike
                                      -- thumbnail_path -- populated
                                      -- synchronously at ingestion, not by
                                      -- a later async job; NULL whenever no
                                      -- favicon was found, which is a
                                      -- normal, non-error outcome
  reader_text TEXT,                  -- Readability plain-text extraction;
                                      -- populated asynchronously by the
                                      -- readability job (§6a) -- NULL until
                                      -- that job completes, or permanently
                                      -- if it never succeeds
  readability_version TEXT,          -- vendored Readability.js version that
                                      -- produced reader_text; overwritten in
                                      -- place on re-extraction, no history
                                      -- kept (§6a)
  content_hash TEXT NOT NULL,        -- full-HTML hash (exact dedup)
  reader_text_hash TEXT,             -- powers "unchanged since last capture";
                                      -- nullable for the same reason as
                                      -- reader_text above (§3b, §6a)
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
- **The dashboard (not yet built) can let a user correct a capture's detected
  language after the fact**, choosing from whatever configs this Postgres
  instance actually has, or "other" (mapping to `simple`) — a plain
  `UPDATE captures SET language = ...`, which Postgres automatically recomputes
  `reader_text_tsv` (and its GIN index) for as part of that same statement, the
  same way it already does whenever `reader_text` itself changes (e.g.
  re-extraction, §6a). No manual reindex, no extra synchronization code needed.

```sql
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

-- Retry/backoff bookkeeping for the async Readability extraction job (§6a),
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

-- Retry/backoff bookkeeping for the async screenshot job (§6), one row per
-- capture -- same shape as readability_jobs above, and deliberately its own
-- table rather than merged with it, even though both run through the same
-- headless-Chrome sidecar and often the same page load (see §6's "Design"
-- subsection for why: independent failure modes, and re-extraction after a
-- Readability.js upgrade has no reason to redo a perfectly good screenshot).
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
-- the extension. r2_key_readable has been removed for the same reason —
-- Readability extraction also moved backend-side (see §6a), so no client
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

| Component                 | Tech                                                                                                    | Reachability required                                              | Responsibility                                                                                                                                                 |
| ------------------------- | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Desktop browser extension | WebExtensions (Chrome/Firefox compatible)                                                               | Worker + R2 only                                                   | Poll queue, capture HTML via vendored SingleFile (no Readability — see §3a/§6a), upload to R2                                                                  |
| Share-sheet PWA           | Static site, Cloudflare Pages                                                                           | Worker only                                                        | Android share-target: enqueue a URL, nothing else                                                                                                              |
| iOS Shortcut              | Apple Shortcuts                                                                                         | Worker only                                                        | Enqueue a URL from iOS share sheet                                                                                                                             |
| CLI                       | Small script/binary                                                                                     | Worker only                                                        | Enqueue URLs, scriptable                                                                                                                                       |
| Cloudflare Worker         | Plain JS (ES modules), no build step — `@ts-check` + JSDoc for static type-checking, ESLint for linting | Public                                                             | Device auth (checks D1 credential mirror), issues bearer tokens, presigned R2 URLs, D1 read/write, service-secret-gated backend endpoints                      |
| D1                        | Cloudflare D1 (SQLite)                                                                                  | N/A (accessed via Worker only, except backend migrations — §5b)    | Device tokens, queue, bookmark-list mirror, schema-migration bookkeeping                                                                                       |
| R2                        | Cloudflare R2                                                                                           | N/A (accessed via presigned URLs)                                  | Temporary blob storage between capture and backend pickup                                                                                                      |
| Backend                   | Go + Postgres, Docker Compose                                                                           | Outbound-only for archiving; inbound optional (dashboard, LAN/VPN) | Pull from R2, compress, store, version, search, tags, collections, AI enrichment, dashboard session auth, dashboard API, Postgres + D1 schema migrations (§5b) |
| Headless-Chrome sidecar   | chromedp + `chromedp/headless-shell`, Docker                                                            | Backend-internal only (no inbound, no outbound)                    | Renders already-captured inlined HTML offline; produces thumbnails (§6) and Readability extractions (§6a)                                                      |
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
│   │                             # deliberately separate from
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
│   ├── src/                    # to pull in vendored SingleFile capture code
│   ├── manifest.json            # and a WebExtension polyfill — no longer
│   └── package.json             # Readability.js; see §3a/§6a)
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
(§13a). The CLI's own commands (`auth.go`, `enqueue.go`) landed as flat files
directly in `cmd/`, confirming they share `go.mod`/the single binary cleanly —
not a separate `cmd/cli/` subdirectory as an earlier revision of this tree
assumed, before the `main.go`/`cobra` restructure had actually produced
`server.go`/`agent.go` as the pattern to follow.

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
  restored files at the expected relative layout (see §4/§6a's
  `internal/urlnorm`-adjacent ingestion package for the actual on-disk layout,
  e.g. hex-prefix sharding by capture ID) — the config value can point anywhere,
  but it does have to point somewhere real.
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
- **Resolved this round: Readability extraction moved from the extension (and
  the dashboard's browser, for manual uploads) to a single deferred, async
  backend job, sharing the headless-Chrome sidecar with the screenshot job — see
  §3a, §3d, §6a.** This was prompted by manual upload (§3d) forcing the question
  of how to extract reader text with no live DOM available at all; the answer
  generalized to every capture path, not just that one. Chosen explicitly over a
  native-Go Readability port (e.g. `go-shiori/go-readability`) on the reasoning
  that the headless-Chrome sidecar already exists for screenshots, so running
  the actual upstream Readability.js inside it is less net-new machinery than it
  would be without that sidecar already being built — this is the _same_
  tradeoff reasoning as §6's own chromedp choice, just applied a second time now
  that there are two things worth rendering a page for. Real, stated
  consequences: `captures.reader_text`/`reader_text_hash` are now nullable
  (previously implicitly synchronous), a new `readability_jobs` table exists
  (§10), and `pending_captures.r2_key_readable` is removed from D1 entirely. The
  already-built Phase 3 Worker code (`handleGetUploadUrls`/
  `handleCompleteQueueItem`, and the `pending_captures` migration), which had
  been built against this design's _previous_ (two-object-upload) shape, has
  since been revised to match — single-object (HTML-only) upload throughout.
- **New this round: `screenshot_jobs` given an actual schema entry (§10),
  closing a gap in the previous revision.** §6 already said the screenshot job
  needed "bounded retry with backoff, same shape as §7," but no table was ever
  defined for it — only `ai_jobs` and, later, `readability_jobs` were. Also
  decided explicitly this round: `screenshot_jobs` and `readability_jobs` stay
  as two separate tables despite both running through the same headless-Chrome
  sidecar and often the same page load, rather than merging them into one — see
  §6's "Design" subsection for the reasoning (independent failure modes;
  re-extraction after a Readability.js upgrade shouldn't force a redundant
  re-screenshot).
