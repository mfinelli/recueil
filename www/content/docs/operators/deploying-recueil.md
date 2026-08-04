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
  source = "github.com/mfinelli/recueil//terraform?ref=v1.0.0"

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

Two other variables that aren't shown above but both default to `true`:
`enable_browser_integrity_check_bypass` provisions a narrow WAF rule that skips
Cloudflare's Browser Integrity Check for recueil's non-browser clients (the CLI,
the backend itself) — harmless to leave on even if your zone doesn't have BIC
enabled. `enable_pwa` deploys the share-sheet PWA's static assets alongside the
Worker; turn it off if you don't want that client available.

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
`service_secret` — all four go into the backend's own configuration below.

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
    image: mfinelli/recueil:1.0.0
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
      R2_ACCOUNT_ID: "<your account_id, same as the module input>"
      R2_BUCKET_NAME: "<from terraform output>"
      R2_ACCESS_KEY_ID: "<the R2 API credential from above>"
      R2_ACCESS_KEY_SECRET: "<the R2 API credential from above>"
    volumes:
      - ./data/archive:/data/archive
    healthcheck:
      test: ["CMD", "wget", "--spider", "-q", "http://127.0.0.1:8080/info"]
      interval: 10s
      timeout: 3s
      retries: 5

  agent:
    image: mfinelli/recueil:1.0.0
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
```

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

## Creating your first account

On first startup, if there are no accounts yet, the backend generates a one-time
bootstrap token and prints it to `server`'s logs (`docker compose logs server`).
It's valid for one hour and regenerated on restart if unused. Visit your
instance's dashboard and it'll show a "create first admin" screen asking for
that token alongside a username and password. Once that account exists, use it
to sign in normally; create any further accounts with `recueil user create` (see
[Administration](/docs/operators/administration/)).

## Staying updated

Bump the image tag in your Compose file and the module ref in your Terraform
config together, then redeploy. Database migrations — both Postgres and D1 — run
automatically every time `recueil server` starts; there's no separate migration
step to remember.
