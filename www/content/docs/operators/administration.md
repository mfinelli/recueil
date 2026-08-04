+++
title = "Administration"
weight = 3
template = "docs-page.html"

[extra]
audience = "operators"
dek = "server, agent, gc, user, and device — running an instance day to day."
+++

All five accept `-c`/`--config` for a TOML file, same as any other setting — see
[Configuration Reference](@/docs/operators/configuration-reference.md) for what
each command actually needs.

## server

Runs the backend web server and dashboard API. Applies both Postgres and D1
migrations automatically on every start, and — on first start with no accounts
at all — prints a one-time bootstrap token for creating the first admin account.
Both covered in full on
[Deploying recueil](@/docs/operators/deploying-recueil.md).

## agent

Runs continuously until stopped, on two independent schedules rather than one:

- **Worker-facing** (`agent_worker_poll_interval_seconds`, 30 minutes by
  default): pulling completed captures in from R2/D1 into Postgres and local
  disk, pushing the D1 bookmark-list mirror sync, and — every 12 hours,
  piggybacked on this same cycle rather than its own — sweeping D1's terminal
  rows older than 72 hours.
- **Local-only** (`agent_local_poll_interval_seconds`, 5 minutes by default):
  the screenshot job, the readability job, and AI enrichment (if configured) —
  nothing here touches the Cloudflare Worker, so there's no free-tier budget to
  be careful with, and this schedule can run more often than the Worker-facing
  one.

Both run once immediately on startup rather than waiting for the first tick, so
a freshly-deployed agent doesn't sit idle. Unlike `server`, `agent` never runs
migrations itself — it assumes `server` owns that, and if it happens to start
first against a not-yet-migrated database, its first cycle(s) simply fail, get
logged, and self-heal once `server` catches up. A failed cycle in general
doesn't crash the process; it's logged and tried again next tick.

## gc

```
recueil gc [--dry-run] [--force]
```

Reclaims disk space left behind by deleted pages and captures. Deleting a page
or capture is a pure database operation — the on-disk HTML/thumbnail/favicon
files are left in place rather than deleted inline — so `gc` is the sweep that
actually removes them, along with any capture directory left empty by an
ingestion that failed partway through.

Two safety behaviors worth knowing about:

- Anything modified in the last 15 minutes is left alone regardless — an
  in-flight capture writes to disk before it commits to Postgres, and is
  legitimately (if briefly) unreferenced until it does.
- If more than half of the scanned files come back unreferenced, the run stops
  without deleting anything and just reports what it found — that's far more
  likely to mean something's wrong with the sweep itself than that the archive
  is genuinely half garbage. `--force` proceeds anyway, for the rare case that's
  actually expected.

Safe to run repeatedly; run with `--dry-run` first if you want to see what it
would remove without actually removing anything.

## user

Three subcommands, all direct-to-Postgres operator tools rather than requests
against the dashboard's API — meant to be run on the instance itself, with the
same config `server` uses.

```
recueil user create <username> [--role admin|member]
```

Creates an account directly, prompting for a password (twice, hidden input,
requiring a match — or a single unconfirmed line over stdin for scripted use).
Role defaults to `member`. Prints the new account's pairing token once — the
same kind used with `recueil auth --url ...` and seen on the dashboard's Devices
screen.

```
recueil user reset-password <username>
```

Resets a password directly and invalidates that user's existing sessions — a
stale cookie from before the reset staying valid would defeat the point. The
pairing token is untouched; a password reset isn't a signal that it's
compromised too.

```
recueil user resync
```

Rebuilds the D1 pairing-token mirror for every account in one pass. This is the
repair step after restoring Postgres from a backup: any account created,
password-changed, or pairing-token-regenerated after the backup was taken
wouldn't be reflected in D1 otherwise.

## device

```
recueil device list <username>
recueil device revoke <username> <device-id>
```

Operator-side equivalents of the dashboard's self-scoped Devices screen — not a
replacement for it, an escape hatch for when someone can't reach it themselves
(a lost device, a forgotten password with no session left). `list` prints a
table (ID, device name, type, paired date, last used); `revoke` looks up that
same list first, so a wrong device ID fails with a clear error before anything
happens, and a successful run reports which device it actually revoked by name.
