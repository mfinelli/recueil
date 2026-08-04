# recueil

recueil is a self-hosted personal web archiver.

> [!NOTE]
>
> This README is for building and hacking on recueil. Looking to self-host or
> use it instead? See [the docs](https://recueil.app/docs/).

## Building from source

Prerequisites:

- Go, matching the version in `go.mod`
- Node.js (any current LTS) and pnpm
- [sqlc](https://sqlc.dev) `1.31.1` (generates `internal/db` from
  `migrations/`/`queries/` which isn't checked in, see below)
- `jq` (reads the build version out of `package.json`)

Clone with submodules — the build embeds `internal/urlnorm/clearurls-rules` (a
pinned snapshot of the [ClearURLs ruleset](https://github.com/ClearURLs/Rules))
directly into the Go binary, and `go:embed` fails without it checked out:

```shell
git clone --recurse-submodules https://github.com/mfinelli/recueil.git
# or, if already cloned:
git submodule update --init
```

To pull in a newer ruleset snapshot later:
`cd internal/urlnorm/clearurls-rules && git pull origin master` (or pin to a
specific commit/tag), then commit the resulting submodule pointer change as its
own commit.

Then:

```shell
make
```

Runs `sqlc generate`, builds the dashboard frontend, and compiles `recueil` with
version info baked in via `-ldflags`. The resulting binary is the same thing
`server`/`agent`/`gc`/`user`/`device`/`auth`/`enqueue` all live inside of — see
[CLI Reference](https://recueil.app/docs/readers/cli-reference/) and
[Administration](https://recueil.app/docs/operators/administration/) for what
each subcommand does.

## Repository layout

A monorepo — everything lives here rather than split across repos, including the
parts with their own independent build tooling.

- **`cmd/`** — the CLI's subcommands (`server`, `agent`, `gc`, `user`, `device`,
  `auth`, `enqueue`), all one binary.
- **`internal/`** — the Go backend, see below.
- **`src/`** — the Svelte dashboard, served by `server` via `go:embed`.
- **`extension/`** — the browser extension. It has its own build tooling and
  README — see [`extension/README.md`](extension/README.md).
- **`terraform/`** — the Cloudflare Worker (plain JS, no build step) and the
  OpenTofu/Terraform module that provisions it, D1, and R2. It has its own
  README — see [`terraform/README.md`](terraform/README.md).
- **`www/`** — the docs site and the marketing site, both Zola.
- **`migrations/`** — Postgres schema migrations (goose).
- **`queries/`** — SQL queries sqlc generates `internal/db` from.
- **`scripts/`** — small standalone maintenance scripts (e.g. checking that
  pinned tool versions agree across `go.mod`/`Dockerfile`/CI).

### `internal/`

| Package           | What it is                                                                                          |
| ----------------- | --------------------------------------------------------------------------------------------------- |
| `ai`              | The async AI enrichment job: summarize a capture's extracted text.                                  |
| `archive`         | The local, canonical disk store for captures.                                                       |
| `auth`            | Password hashing, session tokens, pairing-token encryption, the bootstrap flow.                     |
| `clicreds`        | Where `recueil auth` stores, and `recueil enqueue` reads, this device's pairing credential.         |
| `config`          | Loads backend configuration via Viper — TOML file, env vars, defaults.                              |
| `d1migrate`       | Applies pending D1 schema migrations at backend startup.                                            |
| `dbtest`          | The Postgres integration-test harness.                                                              |
| `deviceapi`       | What a paired device (the CLI) uses to talk to the Worker's device-facing endpoints.                |
| `devices`         | The backend's client for the dashboard's Manage Devices screen.                                     |
| `gc`              | Reclaims disk space `DELETE /api/pages/{id}`/`DELETE /api/captures/{id}` deliberately leave behind. |
| `httpapi`         | The dashboard-facing HTTP API.                                                                      |
| `ingest`          | The ingestion pipeline: pulls completed captures in from R2/D1.                                     |
| `mcpapi`          | The MCP-facing surface over a user's archive — read-only tools.                                     |
| `metrics`         | Builds the Prometheus registry served at `/metrics`.                                                |
| `mirror`          | Pushes backend-owned data outward to D1, via the Worker.                                            |
| `pendingcaptures` | The backend's client for the Worker's pending-captures endpoints.                                   |
| `pgmigrate`       | Applies pending Postgres schema migrations via goose.                                               |
| `queueitems`      | The backend's client for the dashboard's Queue screen.                                              |
| `r2`              | The backend's R2 client (distinct from the Worker's presigned-upload path).                         |
| `readability`     | The async reader-text extraction job.                                                               |
| `screenshot`      | The async screenshot/thumbnail job.                                                                 |
| `sidecar`         | Plumbing shared by every job that renders through the headless-Chrome sidecar.                      |
| `slug`            | Generates and validates the URL-facing slugs stored on tags/collections.                            |
| `urlnorm`         | Computes `normalized_url` for captures/pages.                                                       |

`internal/db` (sqlc-generated query code) isn't checked in — `make` generates
it. See [DESIGN.md](DESIGN.md) for the architecture and reasoning behind how
these fit together; this list is just a map.

## Local development

Postgres via Docker Compose:

```sh
just compose local   # or: just compose test, for the test-profile database
```

Then, for the backend itself:

```sh
just serve   # recueil server, against local.toml
just agent   # recueil agent, against local.toml
```

Both rebuild (`make all`) before running, so they always reflect your current
changes.

> [!IMPORTANT]
>
> **`local.toml` as committed will let `server`/`agent` start, but not do
> anything Cloudflare-facing.** Postgres-only things (the dashboard, logging in)
> work as-is; pairing, enqueuing, and the rest of the capture flow need a real
> Worker/D1/R2 to talk to. The `"todo"` values (`worker_url`,
> `worker_service_secret`, `cloudflare_account_id`, `cloudflare_d1_database_id`,
> `cloudflare_api_token`, `r2_*`) need to point at an actual Cloudflare
> deployment — see
> [Deploying recueil](https://recueil.app/docs/operators/deploying-recueil/) for
> provisioning one; a small deployment kept separate from any real archive works
> fine for this. AI enrichment (`ai_api_key`) is optional and only needed if
> you're testing that specifically — `ai_base_url`/`ai_model` are already set
> for [OpenRouter](https://openrouter.ai), swap them if you use something else.

> [!CAUTION]
>
> **`local.toml` is a tracked file, not gitignored — be careful not to commit
> real values into it.** Check `git diff local.toml` before committing anything
> that touches it. This is a real rough edge that I'll smooth out at some point
> (e.g., untracking it in favor of a checked-in `local.toml.example`), just not
> done yet.

For frontend work on the dashboard specifically, `pnpm run dev` (Vite, with HMR)
is faster than `just serve` — no Go rebuild or manual page refresh needed for
every change.

`just www-serve` is for the docs/marketing site specifically (Zola), not general
frontend work.

## Testing, linting, formatting

```sh
just test
just lint
just fmt
```

These closely mirror what CI runs and should be a good indicator if CI will
eventually pass on a changeset..

## License

    recueil: self-hosted webpage bookmarker and archiver
    Copyright © 2026 Mario Finelli

    This program is free software: you can redistribute it and/or modify
    it under the terms of the GNU Affero General Public License as published by
    the Free Software Foundation, either version 3 of the License, or
    (at your option) any later version.

    This program is distributed in the hope that it will be useful,
    but WITHOUT ANY WARRANTY; without even the implied warranty of
    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
    GNU Affero General Public License for more details.

    You should have received a copy of the GNU Affero General Public License
    along with this program. If not, see <https://www.gnu.org/licenses/>.
