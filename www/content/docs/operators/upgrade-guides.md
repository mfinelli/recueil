+++
title = "Upgrade Guides"
weight = 7
template = "docs-page.html"

[extra]
audience = "operators"
dek = "What a release needs beyond bumping the image tag, one section per release that does."
+++

Most releases need nothing more than what
[Staying updated](@/docs/operators/deploying-recueil.md#staying-updated) already
says: bump the image tag in your Compose file and the module ref in your
Terraform config together, then redeploy. For releases that need extra steps or
are more involved you'll find the specifics here. In general this will be
limited to major versions only as Recueil follows
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## Upgrading from v1 to v2

v2 adds [links](@/docs/readers/links.md) — bookmark-only entries — and,
optionally,
[Wayback Machine submission](@/docs/operators/deploying-recueil.md#wayback-machine-submission-optional).
Links are **on by default** and keep their icons in a directory of their own, so
there's one thing to do before the backend will start.

### Steps

1. **Update Terraform first.** Bump the module ref and `terraform apply`. That
   deploys the new Worker, which is what the extension and the backend talk to
   about links. The module's new `enable_links` variable defaults to on; set it
   to `false` if you want links off.
2. **Give the backend a links directory, or turn links off.** Add `LINKS_DIR`
   and its bind mount to **both** `server` and `agent`, with the same directory
   in both (and the same ownership and permissions as your archive directory,
   and neither inside the other):

   ```yaml
   environment:
     LINKS_DIR: /data/links
   volumes:
     - ./data/links:/data/links
   ```

   If you don't want links, set `ENABLE_LINKS: "false"` instead, matching
   Terraform's `enable_links = false`. If you do neither, `server` and `agent`
   refuse to start, and the message names the directory that's missing. See
   [Running the backend](@/docs/operators/deploying-recueil.md#running-the-backend)
   for the full Compose example.

3. **Bump the image tag and restart.** `server` applies the new Postgres and D1
   migrations as it starts, as it does on every upgrade, and `agent` picks up
   saved links from then on. Nothing for you to run by hand.
4. **Update the browser extension.** Firefox does this on its own. The new
   version adds **Save as link** and a second bookmark sync. An older extension
   keeps working exactly as before; it just doesn't know about links.

{% <callout label="Why Terraform first"> %} The two halves can be upgraded in
either order, but this one is quieter. If the backend is upgraded first, `agent`
logs a warning on every cycle that the Worker doesn't have the new routes yet,
and carries on, until you apply the Terraform. Going the other way round, link
saves from the extension may fail for the short time between the Worker being
upgraded and the backend restarting, because the backend is what adds the new D1
tables, so do the two steps one right after the other. Neither order affects
captures.
{% </callout> %}

### Afterwards

- **Backups.** If you keep links on, add `links_dir` to your
  [backups](@/docs/operators/storage-and-backups.md); it's a separate directory
  from your archive.
- **Wayback Machine submission** is optional and off until you set
  `wayback_access_key` and `wayback_secret_key`. See
  [Deploying recueil](@/docs/operators/deploying-recueil.md#wayback-machine-submission-optional)
  for what to know first.
- **Metrics.** `/metrics` gains `recueil_links_total`, a `link_favicon` storage
  kind, and a `wayback` job type. If you have an alert or a dashboard on
  `recueil_jobs_total` or `recueil_job_oldest_pending_age_seconds` that doesn't
  select a particular `job`, it now includes Wayback submissions too. See
  [Metrics](@/docs/operators/metrics.md).

### If something goes wrong

- **`server` or `agent` won't start and mentions `links_dir`.** The directory
  isn't set, doesn't exist or isn't writable by the container. Fix it, or turn
  links off in both places as in step 2.
- **The Links screen, or the extension's "Save as link", doesn't appear.** Links
  are off, either in the backend (`enable_links`) or in Terraform; they have to
  agree. The extension also remembers the Worker's answer for up to a day, so a
  change can take a while to show; restarting the browser refreshes it.
- **`agent` keeps warning that the Worker has no routes for links.** The backend
  is newer than the Worker. Apply the Terraform; the warnings stop on their own.
