+++
title = "Deploying recueil"
weight = 1
template = "docs-page.html"

[extra]
audience = "operators"
dek = "Cloudflare infrastructure via Terraform, the backend via Docker Compose, and creating your first account."
+++

Two layers, done in order: the Cloudflare side (a Worker, a D1 database, an R2
bucket, and a custom domain route), then the backend itself (Postgres, the
`recueil server` web process, and the `recueil agent` background-job process)
via Docker Compose.

## Cloudflare infrastructure

This is provisioned through an OpenTofu/Terraform module — the only supported
path. It's not that the manual alternative is impossible, but it's a lot more
than clicking a few buttons (there are a lot of resources to create/configure),
and so it's easy to get wrong by hand, plus you lose the ability to just bump a
pinned reference on version updates to automatically pull in all of the
necessary changes — you'd be reading the Terraform source anyway to know what
changed, so at that point you're doing Terraform's job without Terraform's
benefit. If you'd rather not use Terraform at all, the module source itself is
the source of truth for exactly what needs to exist; you're on your own for
tracking changes to it going forward.

Reference the module from your own root config, which supplies a configured
`cloudflare` provider and owns state storage — this directory is a module, not a
standalone root config:

```hcl
module "recueil" {
  source = "github.com/mfinelli/recueil//terraform?ref=v1.0.1"

  account_id       = var.cloudflare_account_id
  name_prefix      = "yourname"           # must be globally unique
  zone_name        = "yourdomain.com"     # must already exist in your account
  worker_subdomain = "recueil"

  # See "R2 API credentials" below before running apply.
  r2_access_key_id     = var.recueil_r2_access_key_id
  r2_secret_access_key = var.recueil_r2_secret_access_key
}

output "service_secret" {
  value     = module.recueil.service_secret
  sensitive = true
}

output "d1_database_id" {
  value = module.recueil.d1_database_id
}
```

Three other variables that aren't shown above but all default to `true`:
`enable_browser_integrity_check_bypass` provisions a narrow WAF rule that skips
Cloudflare's Browser Integrity Check for recueil's non-browser clients (the CLI,
the backend itself) — harmless to leave on even if your zone doesn't have BIC
enabled. `enable_pwa` deploys the share-sheet PWA's static assets alongside the
Worker; turn it off if you don't want that client available. `enable_links`
controls whether the browser extension offers saving a page as a link, and
should match the backend's `enable_links` setting.

### R2 API credentials

One credential can't be Terraform-provisioned and has to exist before your first
`apply`: the Worker issues presigned upload URLs directly against R2's
S3-compatible API, which needs its own Access Key ID + Secret Access Key pair,
separate from the Cloudflare API token that provisions everything else.

1. Cloudflare dashboard → **R2** → **Manage R2 API Tokens** → **Create API
   Token**.
2. Scope it to **Object Read & Write**, restricted to the bucket this module
   creates (`<name_prefix>-recueil`) if you'd rather not grant account-wide R2
   access.
3. Pass the **Access Key ID** and **Secret Access Key** (note that Cloudflare
   only shows the secret once) into `r2_access_key_id`/`r2_secret_access_key`.

Rotating this later means generating a new R2 API token and running `apply`
again with the new values, then revoking the old one from the same screen.

### What you'll need for the next step

`terraform apply` prints `worker_url`, `d1_database_id`, `r2_bucket_name`, and
`service_secret` — all four go into the backend's configuration below.

## Running the backend

A starting point, not a drop-in final config — fill in the placeholder values,
generate real secrets, and put a reverse proxy in front of `server`'s published
port.

```yaml
services:
  postgres:
    image: postgres:18-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: recueil
      POSTGRES_USER: recueil
      POSTGRES_PASSWORD: "<generate a real password>"
    volumes:
      - ./data/postgres:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U recueil"]
      interval: 15s
      timeout: 3s
      retries: 10

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

  # Chromium (M113+) silently forces its DevTools listener to 127.0.0.1
  # no matter what flags are passed -- a non-configurable security decision --
  # which makes the sidecar unreachable from any other container on this same
  # network without this plain TCP forward sharing chromedp's network
  # namespace.
  chromedp-proxy:
    image: alpine/socat:latest
    restart: unless-stopped
    network_mode: "service:chromedp"
    depends_on:
      - chromedp
    command: ["tcp-listen:9222,fork,reuseaddr", "tcp:127.0.0.1:9223"]

  server:
    image: mfinelli/recueil:1.0.1
    restart: unless-stopped
    command: ["recueil", "server"]
    depends_on:
      postgres:
        condition: service_healthy
    ports:
      - "127.0.0.1:8080:8080" # only the reverse proxy should reach this
    environment: &recueil-env
      DATABASE_URL:
        postgres://recueil:<same password as above>@postgres:5432/recueil
      LISTEN_ADDR: ":8080"
      WORKER_URL: "<from terraform output>"
      WORKER_SERVICE_SECRET: "<from terraform output>"
      PAIRING_TOKEN_KEY: "<openssl rand -base64 32>"
      CLOUDFLARE_ACCOUNT_ID: "<your account_id, same as the module input>"
      CLOUDFLARE_D1_DATABASE_ID: "<from terraform output>"
      CLOUDFLARE_API_TOKEN:
        "<your Cloudflare API token, same as the provider config>"
      ARCHIVE_DIR: /data/archive
      LINKS_DIR: /data/links
      R2_ACCOUNT_ID: "<your account_id, same as the module input>"
      R2_BUCKET_NAME: "<from terraform output>"
      R2_ACCESS_KEY_ID: "<the R2 API credential from above>"
      R2_ACCESS_KEY_SECRET: "<the R2 API credential from above>"
    volumes:
      - ./data/archive:/data/archive
      - ./data/links:/data/links
    healthcheck:
      test: ["CMD", "wget", "--spider", "-q", "http://127.0.0.1:8080/info"]
      interval: 10s
      timeout: 3s
      retries: 5

  agent:
    image: mfinelli/recueil:1.0.1
    restart: unless-stopped
    command: ["recueil", "agent"]
    depends_on:
      postgres:
        condition: service_healthy
      chromedp:
        condition: service_started
      chromedp-proxy:
        condition: service_started
    environment:
      <<: *recueil-env
      SIDECAR_URL: "http://chromedp:9222"
      SIDECAR_RENDER_HOST: "agent"
    volumes:
      - ./data/archive:/data/archive
      - ./data/links:/data/links
```

`ARCHIVE_DIR` must already exist and be writable, and both `server` and `agent`
write to it, so it must be mounted as read-write in both. `server` and `agent`
both check this when they start and refuse to start otherwise. They don't create
missing directories because in a container (the recommended deployment option),
a missing directory usually means a bind mount is missing, and creating one
would put your archive in the container's filesystem, where it vanishes the next
time the container is recreated.

`LINKS_DIR` is where favicons for links (bookmark-only entries) are stored. It's
deliberately a separate directory from `ARCHIVE_DIR` — neither can be inside the
other — and, like the archive, should be a bind mount so it survives the
container being recreated. Links are on by default; if you don't want them, set
`ENABLE_LINKS: "false"` and leave `LINKS_DIR` and its volume out. The Terraform
module's `enable_links` should match. With links on, `server` and `agent` refuse
to start if `LINKS_DIR` isn't an existing, writable directory.

Every setting here can also come from a TOML file instead of environment
variables — mount it into the container and point `server`/`agent` at it with
`-c`/`--config`. See
[Configuration Reference](@/docs/operators/configuration-reference.md) for the
file format and the full list of settings either way.

### Running the binary directly instead

Docker Compose is the documented, supported path — the same reasoning as
Terraform above applies here too: it's what's actually tested, and a second set
of instructions for running things by hand is a second thing to keep in sync
forever. That said, `server` and `agent` are just two subcommands of the one
`recueil` binary; nothing about running them requires a container. If you'd
rather run them directly, you'll need to provide everything Compose was
providing for you — Postgres reachable at your `DATABASE_URL`, a
`chromedp/headless-shell` instance (however you choose to run it) reachable at
`SIDECAR_URL`, and your own process supervisor (systemd, or whatever else) in
place of `restart: unless-stopped`. The `Dockerfile` and `compose.yaml` in the
repository spell out exactly what needs to be true at runtime; treat them as the
reference if you go this route.

## Wayback Machine submission (optional)

recueil can ask the Internet Archive to capture a page or link on a user's
request, which gives them a second public copy that doesn't depend on your
server. It's off unless you configure it, and it needs an archive.org account's
keys.

**Getting the keys.** Follow the Internet Archive's guide,
[Get your Internet Archive credentials](https://archive.org/developers/tutorial-get-ia-credentials.html):
sign in (or sign up) at archive.org, open
[archive.org/account/s3.php](https://archive.org/account/s3.php), tick the box
and choose **Generate New Keys**. Put them in `wayback_access_key` and
`wayback_secret_key` (see the
[configuration reference](@/docs/operators/configuration-reference.md#wayback-machine-submission)).

**What to know before turning it on.**

- **One account for everyone.** Every submission on your instance goes out under
  the same key pair, so the Internet Archive's limits (a handful of captures a
  minute, a few of any one URL a day) are shared by all your users. Two users
  submitting the same URL close together are handled by the Internet Archive
  itself, which answers a request for a URL captured in the last hour with the
  same recent capture, but a busy moment can still hit a limit; the user sees
  its message and tries again later. Who submitted what stays private to each
  user.
- **It's only ever on demand.** Nothing is sent automatically, and viewing a
  page or link never contacts the Internet Archive.
- **Snapshots are public**, and the URL is shared with a third party. Your users
  are told so on the screen and in the
  [reader documentation](@/docs/readers/wayback-submission.md), but it's worth
  deciding whether that's acceptable for the people who use your instance.
- **`agent` only records the outcome.** The Internet Archive does the capturing
  itself; `agent`'s local cycle (every `agent_local_poll_interval_seconds`, 5
  minutes by default) asks how each pending submission went and stores the
  answer, so a result appears within a few minutes.

## Putting a reverse proxy in front

Required — `server` shouldn't be exposed directly. An nginx example; the same
shape applies to Caddy, Traefik, or whatever you'd rather use. Certificate
issuance itself (Let's Encrypt/certbot or similar) is out of scope for this
guide.

```nginx
server {
  listen 80;
  listen [::]:80;
  server_name recueil.example.com;
  return 301 https://$host$request_uri;
}

server {
  listen 443 ssl http2;
  listen [::]:443 ssl http2;
  server_name recueil.example.com;

  ssl_certificate /etc/letsencrypt/live/recueil.example.com/fullchain.pem;
  ssl_certificate_key /etc/letsencrypt/live/recueil.example.com/privkey.pem;
  ssl_session_timeout 1d;
  ssl_session_cache shared:MozSSL:10m;
  ssl_session_tickets off;
  ssl_protocols TLSv1.3;
  ssl_prefer_server_ciphers off;

  location / {
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_pass http://127.0.0.1:8080;
  }
}
```

{% <callout label="Heads up"> %} nginx's default `client_max_body_size` is 1MB —
comfortably enough for everything except
[manual upload](@/docs/readers/manual-upload.md), which needs a bigger one to
match `capture_manual_upload_max_bytes` (100MB by default; see
[Configuration Reference](@/docs/operators/configuration-reference.md)). Add
`client_max_body_size 100M;` inside the `location / {}` block above (or whatever
value matches your own configured limit) — otherwise nginx itself rejects an
oversized upload with a 413 before it ever reaches `server`, which looks like
recueil's limit misbehaving when it's actually this one. {% </callout> %}

{% <callout label="Heads up"> %} This example is intentionally simple: it
forwards everything to `server`, including `/metrics`. If you want to restrict
access see [Metrics](@/docs/operators/metrics.md#who-can-reach-metrics) for what
exactly is exposed and the options that you have. {% </callout> %}

## Creating your first account

On first startup, if there are no accounts yet, the backend generates a one-time
bootstrap token and prints it to `server`'s logs (`docker compose logs server`).
It's valid for one hour and regenerated on restart if unused. Visit your
instance's dashboard and it'll show a "create first admin" screen asking for
that token alongside a username and password. Once that account exists, use it
to sign in normally; create any further accounts with `recueil user create` (see
[Administration](@/docs/operators/administration.md)).

## Staying updated

Bump the image tag in your Compose file and the module ref in your Terraform
config together, then redeploy. Database migrations — both Postgres and D1 — run
automatically every time `recueil server` starts; there's no separate migration
step to remember. A release that needs more than that has a section in
[Upgrade Guides](@/docs/operators/upgrade-guides.md).
