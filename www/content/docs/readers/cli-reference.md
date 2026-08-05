+++
title = "CLI Reference"
weight = 4
template = "docs-page.html"

[extra]
audience = "readers"
dek = "Enqueue URLs from your terminal."
+++

This is for enqueuing pages from a terminal — useful for scripting something, or
working from a machine without the browser extension installed. For everything
else, the [extension](@/docs/readers/browser-extension.md) or
[mobile capture](@/docs/readers/mobile-capture.md) cover most people better.

## Installing

Prebuilt binaries are published for macOS (Apple Silicon) and Linux (amd64 and
arm64) — grab the latest from
[GitHub Releases](https://github.com/mfinelli/recueil/releases). Anything else —
Windows, Intel Macs, anything 32-bit — clone the repository and build from
source; see the
[README](https://github.com/mfinelli/recueil/blob/master/README.md) for build
instructions.

## Pairing a device

```
recueil auth --url <worker-url> [--name <device-name>]
```

Pairs the machine you run this on with your recueil instance, exchanging a
pairing token for a device credential that `enqueue` (see below) can then use.

- **`--url`** — your instance's worker URL. Required. Both this and your pairing
  token are on the dashboard's _Devices_ screen.
- **`--name`** — a label for this device, shown on that same Devices screen so
  you can tell your paired devices apart. Defaults to the machine's hostname.

You'll be prompted to paste your pairing token, with the terminal's echo
disabled — or pipe it in for scripted use:

```
echo "$TOKEN" | recueil auth --url https://your-worker.example.workers.dev
```

Running `auth` again on a machine that's already paired registers a new,
additional device rather than replacing the existing one — the old pairing stays
active until you revoke it separately from the dashboard.

## Sending pages

```
recueil enqueue <url> [<url> ...]
```

Requires that this device has already been paired. Each URL is enqueued
independently — a malformed one, or a single failed request, gets reported and
doesn't stop the rest from going through. If any of them failed, the command
exits non-zero once every URL's been attempted, not on the first failure.

```
$ recueil enqueue https://example.com/article https://example.org/post
enqueued https://example.com/article
enqueued https://example.org/post
```

Enqueuing doesn't archive anything immediately — same as every other way of
adding to the queue, it waits for a paired browser to pick it up. See
[Browser Extension](@/docs/readers/browser-extension.md) for that part.

## Where the credential lives

`auth` writes to `$XDG_CONFIG_HOME/recueil/credentials.json` (falling back to
`$HOME/.config/recueil/credentials.json` if `$XDG_CONFIG_HOME` is unset), and
`enqueue` reads from the same file. Deleting it stops this machine from
enqueuing until you run `auth` again — but it's local only. The device itself
stays listed and active on the dashboard until you revoke it from there
yourself.

## Everything else

The binary also runs and administers a recueil instance itself — `server`,
`agent`, `gc`, `user`, `device` — configured via a TOML file or environment
variables rather than anything `auth` sets up. Those are for whoever's actually
running an instance, not day-to-day use, and are covered in
[For Operators](@/docs/operators/_index.md).
