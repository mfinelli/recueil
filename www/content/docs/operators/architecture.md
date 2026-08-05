+++
title = "Architecture"
weight = 5
template = "docs-page.html"

[extra]
audience = "operators"
dek = "The pieces, how they fit together, and what happens when you capture a page."
+++

## The pieces

- **A Cloudflare Worker** — a dumb relay, not an application server. It handles
  device pairing, the cross-device queue, and issuing short-lived presigned
  upload URLs. It never touches your archive's actual content.
- **D1** (Cloudflare's SQLite) — device tokens and the queue live here. Not
  canonical data; see
  [Storage & Backups](@/docs/operators/storage-and-backups.md).
- **R2** (Cloudflare's object storage) — a temporary buffer for capture blobs in
  transit, deleted the moment your backend has pulled them.
- **The backend** — two processes, one binary: `server` runs the dashboard and
  its API; `agent` runs everything in the background — pulling captures in,
  generating screenshots and readable text, optional AI enrichment. See
  [Administration](@/docs/operators/administration.md).
- **Postgres and local disk** — canonical. Everything that actually matters
  lives here, not in Cloudflare.
- **A headless-Chrome sidecar** — renders already-captured HTML offline to
  produce thumbnails and extracted readable text. Never fetches a live page;
  only ever looks at what's already been captured.
- **The dashboard** — a Svelte app served by `server`, for browsing, searching,
  and organizing your archive.
- **Capture clients** — the browser extension, the share-sheet PWA, the iOS
  Shortcut, and the CLI. All four only ever talk to the Worker; none of them
  know your backend exists.

## Why Cloudflare, for something self-hosted

It's a fair question — "self-hosted" usually means just your own server. The
Worker exists because the backend is deliberately outbound-only: it has no open
inbound port, no public exposure, nothing to attack from the internet. Something
still has to give your capture clients somewhere reachable to push a URL or
upload a blob to, especially since a device enqueuing something (your phone,
say) usually isn't reachable from anywhere either. The Worker is that somewhere
— a thin, stateless relay, not a second copy of your application.

For a personal deployment, this should run entirely within Cloudflare's free
tier. Nothing about the design assumes otherwise: request volume, D1 usage, and
R2 storage are all well inside what personal-scale archiving actually generates,
and the backend's poll intervals are deliberately tuned to stay comfortably
within it rather than push against the limit.

## A capture, start to finish

1. You enqueue a URL — directly from the extension, or from anywhere else (the
   PWA, a Shortcut, the CLI) that just adds it to the queue instead.
2. The Worker writes it to the queue in D1.
3. A paired browser picks it up — either you captured directly, or the extension
   claimed a queued item — and captures the page from a real, already-logged-in
   tab.
4. The extension uploads the result to R2 through a presigned URL, no backend
   involved at any point so far.
5. `agent`'s background cycle pulls that blob from R2, deletes it from R2,
   compresses it, and stores it on local disk, with a row committed to Postgres.
6. `agent` picks the new capture up again for the screenshot and readable-text
   jobs (both run through the headless-Chrome sidecar), and optional AI
   enrichment if you've configured it.
7. It shows up in your dashboard — `server` reading all of the above back out of
   Postgres.

## Why the backend never has to be reachable from outside

Every step above — enqueue, queue, capture, upload, pull — only ever depends on
the Worker and R2, both already public and already authenticated. The backend's
reachability only matters for the dashboard itself, and how you expose that
(LAN, VPN, tunnel, reverse proxy) is entirely your own call — see
[Deploying recueil](@/docs/operators/deploying-recueil.md). The archiving loop
works the same either way.
