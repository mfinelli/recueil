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
Storage layout, reclaiming disk space, and backup/restore guidance are all in
the docs too: see
[Storage & Backups](https://recueil.app/docs/operators/storage-and-backups/).

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
