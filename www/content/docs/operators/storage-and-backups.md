+++
title = "Storage & Backups"
weight = 4
template = "docs-page.html"

[extra]
audience = "operators"
dek = "How captures live on disk, reclaiming space, and backing it all up."
+++

## How captures are stored

Local disk (`archive_dir`) is canonical — R2 is a temporary buffer only, used to
get a capture's blobs from wherever they're captured to the backend, and deleted
from R2 once pulled and stored locally. There's nothing durable there to back
up.

Every capture gets its own directory — compressed HTML, a thumbnail, and a
favicon, all together in one place. The directory name is a backend-generated
id, sharded three levels deep off its own trailing characters, so one directory
never ends up holding hundreds of thousands of entries as an archive grows.
Concretely, a capture might live at something like:

```
archive_dir/6a/d3/019fce71-a3bd-7ad7-b4d6-0238b3d96ad3/
```

You shouldn't ever need to touch these paths directly, but it's useful to know
the shape if you're poking around during a backup restore or just curious what's
actually on disk.

## Reclaiming space

Deleting a page or capture from the dashboard is deliberately a pure database
operation — the files above are left in place rather than deleted inline.
`recueil gc` is the sweep that actually reclaims that space; see
[Administration](@/docs/operators/administration.md#gc) for the command itself.
It needs a working `DATABASE_URL`, so against the Docker Compose deployment, run
it through the same image and environment `server` already uses:

```
docker compose run --rm server recueil gc --dry-run
```

Safe to run whenever, on whatever schedule suits you — nothing else depends on
it running promptly.

## Backup

recueil doesn't back itself up and deliberately doesn't have any built-in backup
functionality — it's the operator's responsibility. A backup consists of two
things which must be run **on the same schedule**:

1. **The Postgres database** — via `pg_dump`, not a raw copy of the
   `./data/postgres` directory.
2. **The archive directory** — the captures directory described above. A plain
   file copy is fine here, since these are static files once written.

If the two drift out of sync — backed up on different schedules, or one succeeds
while the other fails silently — a restore can leave a `captures` row pointing
at a file that isn't actually in that backup window, or a file with no row
pointing at it. Run them as one job.

Here's a starting point — adapt it to whatever backup tooling you actually use
(`restic`, `rclone`, a managed backup service pointed at `./data`, etc.), this
is just the two commands any of those need to wrap:

```shell
#!/bin/sh
set -eu

backup_dir="./backups/$(date +%Y%m%dT%H%M%S)"
mkdir -p "$backup_dir"

# -Fc: pg_dump's custom format -- compressed by default, and restorable
# with pg_restore's selective/parallel options later, unlike a plain
# .sql dump. `compose exec`'s -T disables pseudo-TTY allocation, which
# matters here since the dump is binary output being redirected to
# a file, not something meant to be displayed. Running this inside the
# postgres container itself (rather than pg_dump from the host) also
# means nothing needs 5432 reachable from wherever this script runs.
docker compose exec -T postgres \
  pg_dump -U recueil -Fc recueil > "$backup_dir/postgres.dump"

# ./data/archive is a real host directory (a bind mount, not a named
# Docker volume), so this is a plain tar (the files are already compressed),
# and no disposable container is needed.
tar czf "$backup_dir/archive.tar.gz" -C ./data/archive .
```

**Intentionally not in this list**, since it's an easy thing to get backwards:
R2 and D1 don't need to be backed up. R2 is a temporary upload buffer only,
covered above. D1 holds device tokens (and the read-only bookmark-list mirror),
not canonical data — Postgres is the source of truth for both, and D1 is rebuilt
from it, not the other way around.

### Restoring

Bring `postgres` up empty, `pg_restore` the dump into it (or load the plain-SQL
equivalent), and untar the archive backup into a fresh `./data/archive`
directory before starting `server`/`agent`. Then run `recueil user resync` (see
[Administration](@/docs/operators/administration.md#user)) once the database is
back — password changes, new accounts, or pairing-token regenerations made after
the backup was taken won't be reflected in D1's credential mirror otherwise, and
this rebuilds it from the now-restored Postgres state for every account in one
pass.
