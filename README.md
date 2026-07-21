# recueil

## install

Step 1: create the cloudflare infrastructure using terraform: see the README in
the terraform directory. This produces the values `worker_url`,
`worker_service_secret`, and the `r2_*`/`cloudflare_*` config values the
production deployment below needs.

Step 2: run the self-hosted backend (Postgres, the `recueil server` web process,
the `recueil agent` background-job process, and the headless-Chrome sidecar the
screenshot job needs) via Docker Compose -- see "production" below for a sample.

## production

This is a starting point, not a drop-in final config -- fill in the placeholder
values, generate real secrets, and put a real reverse proxy (TLS termination,
etc.) in front of `server`'s published port rather than exposing it directly as
shown here.

**Why `chromedp-proxy` is here too, not just in local dev's `compose.yaml`:** it
might look like the kind of workaround that's only needed for the local dev
split (running `recueil agent` directly on your own machine against a
containerized sidecar). It isn't -- since Chromium M113/M114, Chromium silently
forces its DevTools listener to `127.0.0.1` no matter what
`--remote-debugging-address` is passed, as a deliberate, non-configurable
security decision. That makes the sidecar's real listener unreachable from _any_
other network participant, including another container on this exact same
Compose network. `chromedp-proxy` (a plain TCP forward sharing `chromedp`'s
network namespace) is the fix either way, which is why it's a permanent part of
this sidecar's architecture rather than a local-dev-only detail. The only things
that actually differ from the local dev `compose.yaml` at the root of this repo:
no `ports:` published for `chromedp`/`chromedp-proxy` at all (nothing outside
this Compose network ever needs to reach them, since `agent` is on the same
network now), and no `extra_hosts` entry (that was specifically for reaching a
`recueil agent` process running directly on the host's own machine, which isn't
the case here -- `sidecar_render_host` points at the `agent` service's own
Compose DNS name instead).

```yaml
---
services:
  postgres:
    image: postgres:18-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: recueil
      POSTGRES_USER: recueil
      POSTGRES_PASSWORD: "<generate a real password>"
    volumes:
      - recueil-postgres:/var/lib/postgresql

  # See compose.yaml at the repo root for the fuller explanation of every
  # flag/setting here -- this is the same sidecar, just without the
  # local-dev-only host reachability bits (no published ports, no
  # extra_hosts).
  chromedp:
    image: chromedp/headless-shell:latest
    restart: unless-stopped
    user: nobody
    entrypoint:
      - /headless-shell/headless-shell
      - --remote-debugging-port=9223
      - --disable-gpu
      - --enable-unsafe-swiftshader
      - --headless
      - --no-sandbox
    shm_size: "1gb"

  chromedp-proxy:
    image: alpine/socat:latest
    restart: unless-stopped
    network_mode: "service:chromedp"
    depends_on:
      - chromedp
    command: ["tcp-listen:9222,fork,reuseaddr", "tcp:127.0.0.1:9223"]

  server:
    image: mfinelli/recueil:latest # pin a real version tag in practice
    restart: unless-stopped
    command: ["recueil", "server"]
    depends_on:
      - postgres
    ports:
      - "8080:8080" # put a reverse proxy in front of this in practice
    environment: &recueil-env
      DATABASE_URL:
        postgres://recueil:<same password as above>@postgres:5432/recueil
      LISTEN_ADDR: ":8080"
      WORKER_URL: "<from terraform output>"
      WORKER_SERVICE_SECRET: "<from terraform output>"
      PAIRING_TOKEN_KEY: "<openssl rand -base64 32>"
      CLOUDFLARE_ACCOUNT_ID: "<from terraform output>"
      CLOUDFLARE_D1_DATABASE_ID: "<from terraform output>"
      CLOUDFLARE_API_TOKEN: "<from terraform output>"
      ARCHIVE_DIR: /data/archive
      R2_ACCOUNT_ID: "<from terraform output>"
      R2_BUCKET_NAME: "<from terraform output>"
      R2_ACCESS_KEY_ID: "<from terraform output>"
      R2_ACCESS_KEY_SECRET: "<from terraform output>"
    volumes:
      - recueil-archive:/data/archive

  agent:
    image: mfinelli/recueil:latest # pin a real version tag in practice
    restart: unless-stopped
    command: ["recueil", "agent"]
    depends_on:
      - postgres
      - chromedp
      - chromedp-proxy
    environment:
      <<: *recueil-env
      # Both directions of the sidecar connection use this service's own
      # Compose DNS name -- agent -> sidecar (sidecar_url) and
      # sidecar -> agent's ephemeral render server (sidecar_render_host)
      # are different connections, but "chromedp"/"agent" resolve correctly
      # either way since everything's on the same Compose network here.
      SIDECAR_URL: "http://chromedp:9222"
      SIDECAR_RENDER_HOST: "agent"
    volumes:
      # Same volume, same path, as `server` above -- the agent writes
      # captures/screenshots/favicons here; the server reads them back out.
      - recueil-archive:/data/archive

volumes:
  recueil-postgres:
  recueil-archive:
```

## clients

Beyond the desktop browser extension (paired via the dashboard's Devices screen,
same as everything below), two thin remote-enqueue clients are served straight
off the same Cloudflare Worker the terraform module provisions -- neither needs
its own deploy step.

### Share-sheet PWA (Android, and anywhere else that supports Web Share Target)

Visit the Worker's own URL (the `worker_url` terraform output) on your phone and
add it to your home screen. The first launch asks for a pairing token (same
Devices screen as the extension) and a name for the device; after that, sharing
a page to it from any app enqueues the URL, no separate app install needed.
There's no "Worker URL" field to fill in here -- the page is served by the same
Worker it talks to, so pairing and enqueuing are both same-origin requests.

### iOS Shortcut

Apple Shortcuts aren't plain-text source -- they're built and exported through
the Shortcuts app itself, so there's no file in this repo to install directly.
This is the recipe for building one by hand.

The one real wrinkle, worth calling out up front: **a pairing token and a device
bearer token are not the same thing.** Pairing tokens (from the dashboard's
Devices screen) are exchanged once for a bearer token (`POST /pair`), and it's
the bearer token that actually authenticates `POST /queue` afterward. A Shortcut
has no way to run that exchange itself -- but it doesn't need to: a Shortcut's
own action fields (like a static `Authorization` header value) persist as part
of the shortcut's own definition once you type them in, the same as any other
hardcoded setting, so this only needs **one** shortcut, not two. The one extra
step is getting the bearer token in the first place, since it's not something
you'd otherwise see anywhere:

1. On any device, visit `https://<worker_url>/token.html` -- a small page served
   by the same Worker, built for exactly this: paste in a pairing token and a
   name for the device, and it exchanges it for a bearer token and displays it
   once (it isn't saved anywhere by that page itself -- copy it before
   navigating away). It shows up afterward in the dashboard's Devices screen
   like any other paired device, so it can be revoked independently later
   without affecting anything else.
2. Copy that token.

**"Recueil: Save Page"** (enable **Show in Share Sheet**, accepting URLs and
Safari web pages, in the shortcut's own settings):

1. **Get Current Date**, formatted as Unix time, combined with **Random Number**
   (0-999999) into one **Text** step (e.g.
   `Save-{Formatted Date}-{Random Number}`) -- Shortcuts has no built-in UUID
   generator, and `POST /queue` needs some client-generated, reasonably-unique
   `id` for each enqueue (see `terraform/worker/index.js`'s `handleEnqueue`); it
   only needs to be unique enough to not collide with another enqueue in the
   same second, not an actual UUID.
2. **Get Contents of URL** -- URL: `https://<worker_url>/queue`, method POST,
   headers `Content-Type: application/json` and
   `Authorization: Bearer <paste the token from step 1 above>`, request body
   (JSON): `{"id": <generated id>, "url": <Shortcut Input>}`.
3. **Show Notification** -- e.g. "Saved to Recueil".

Two things worth knowing about this approach: the token lives in plain text
inside the shortcut's own saved configuration (viewable if you open the shortcut
to edit it, same exposure any other client's stored credential has -- don't
export or share this particular shortcut with anyone), and Shortcuts'
`Get Contents of URL` treats the request as successful once it gets _any_ HTTP
response, including a 401 (a revoked or expired token) or a 500 -- it won't
surface that as a failure on its own. If enqueues silently stop landing, revisit
`token.html` for a fresh token and update the header.

## development

This repo uses a git submodule (`internal/urlnorm/clearurls-rules`, a pinned
snapshot of the [ClearURLs ruleset](https://github.com/ClearURLs/Rules) used by
`internal/urlnorm` for URL normalization) embedded directly into the Go binary
at build time. Clone with submodules, or initialize them afterward:

```sh
git clone --recurse-submodules https://github.com/mfinelli/recueil.git
# or, if already cloned:
git submodule update --init
```

The Go build (and `go:embed` specifically) will fail without this checked out.
To pull in a newer ruleset snapshot later:
`cd internal/urlnorm/clearurls-rules && git pull origin master` (or pin to a
specific commit/tag), then commit the resulting submodule pointer change as its
own commit.
