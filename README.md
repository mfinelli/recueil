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
