# recueil

## install

Step 1: create the cloudflare infrastructure using terraform: see the README in
the terraform directory. This produces the values `worker_url`,
`worker_service_secret`, and the `r2_*`/`cloudflare_*` config values the backend
in step 2 needs.

Step 2: run the self-hosted backend (Postgres, the `recueil server` web process,
the `recueil agent` background-job process, and the headless-Chrome sidecar the
screenshot job needs) via Docker Compose. A full sample, plus the reverse-proxy
config it needs in front of it, is in the docs: see
[Deploying recueil](https://recueil.app/docs/operators/deploying-recueil/).

## backup

recueil doesn't back itself up -- baking `pg_dump` into the application image,
or shelling out to it from the Go binary, is an awkward dependency for an
application binary to carry, and would commit the project to tracking Postgres
version compatibility indefinitely. It's the operator's responsibility, same as
the reverse proxy (see
[Deploying recueil](https://recueil.app/docs/operators/deploying-recueil/)). Two
things, **on the same schedule**:

1. **The Postgres database** -- via `pg_dump`, not a raw copy of the
   `./data/postgres` directory. Postgres's on-disk format isn't safe to copy
   directly while the container is running, bind mount or not (no WAL-aware
   tooling involved), so this has to go through `pg_dump` or an equivalent
   backup tool that actually understands it.
2. **The `./data/archive` directory** -- the captured HTML/screenshots/favicons
   `server` and `agent` both write to. A plain file copy is fine here, since
   these are static files once written.

If the two drift out of sync -- backed up on different schedules, or one
succeeds while the other fails silently -- a restore can leave a `captures` row
pointing at a file that isn't actually in that backup window, or a file with no
row pointing at it. Run them as one job.

A starting point, adapt it to whatever backup tooling you actually use
(`restic`, `rclone`, a managed backup service pointed at `./data`, etc.), this
is just the two commands any of those need to wrap:

```sh
#!/bin/sh
set -eu

backup_dir="./backups/$(date +%Y%m%dT%H%M%S)"
mkdir -p "$backup_dir"

# -Fc: pg_dump's own custom format -- compressed by default, and
# restorable with pg_restore's selective/parallel options later, unlike a
# plain .sql dump. `compose exec`'s own -T disables pseudo-TTY allocation,
# which matters here since the dump is binary output being redirected to
# a file, not something meant to be displayed. Running this inside the
# postgres container itself (rather than pg_dump from the host) also
# means nothing needs 5432 reachable from wherever this script runs.
docker compose exec -T postgres \
  pg_dump -U recueil -Fc recueil > "$backup_dir/postgres.dump"

# ./data/archive is a real host directory (a bind mount, not a named
# Docker volume -- see Deploying recueil in the docs), so this is a plain
# tar, no disposable container needed to reach it.
tar czf "$backup_dir/archive.tar.gz" -C ./data/archive .
```

**What's deliberately _not_ in this list**, since it's an easy thing to get
backwards: R2 and D1 don't need backing up. R2 is documented as a temporary
upload buffer only -- every object is deleted once the backend finishes
ingesting it, so there's nothing durable there to lose. D1 holds device tokens
(and the read-only bookmark-list mirror), not canonical data -- Postgres is the
source of truth for both, and D1 is rebuilt from it, not the other way around.

**Restoring**: bring `postgres` up empty, `pg_restore` the dump into it (or load
the plain-SQL equivalent), and untar the archive backup into a fresh
`./data/archive` directory before starting `server`/`agent`. Then run
`recueil user resync` (see `recueil user --help`) once the database is back --
password changes, new accounts, or pairing-token regenerations made after the
backup was taken won't be reflected in D1's credential mirror otherwise, and
this rebuilds it from the now-restored Postgres state for every account in one
pass.

## clients

Beyond the desktop browser extension, two thin remote-enqueue clients are served
straight off the same Cloudflare Worker the terraform module provisions --
neither needs its own deploy step: a share-sheet PWA (Android, and anywhere else
that supports Web Share Target), and a recipe for building your own iOS
Shortcut. All three are documented for end users at
https://recueil.app/docs/readers/ -- see
[Browser Extension](https://recueil.app/docs/readers/browser-extension/) and
[Mobile Capture](https://recueil.app/docs/readers/mobile-capture/).

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
