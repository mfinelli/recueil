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

### API changes

If you only use the dashboard, there's nothing to do. If you call the API
yourself, the routes that relate pages to each other were renamed, so that
"links" now means only the saved-link entity. The old routes return `404`.

| Before                             | Now                                        |
| ---------------------------------- | ------------------------------------------ |
| `POST /api/pages/{id}/links`       | `POST /api/pages/{id}/related-pages`       |
| `DELETE /api/pages/{id}/links/{n}` | `DELETE /api/pages/{id}/related-pages/{n}` |
| `GET /api/pages/link-candidates`   | `GET /api/pages/relation-candidates`       |
| `links` on a page's detail         | `related_pages`                            |
| request body `{"link_page_id": n}` | `{"page_id": n}`                           |

### Rolling back

Most of v2's migrations only add tables, which a v1 backend doesn't care about,
so going back to v1 is normally just pointing your Compose file at the v1 image.
v2 has two exceptions that change tables v1 uses: migration `00016` renames the
`page_links` table to `page_relations`, so a v1 backend would fail to load a
page's related pages, and migration `00017` adds a `user_id` to the tables that
tie pages to tags and collections, so a v1 backend's inserts into them would
fail. Before starting v1 again, revert them with
[goose](https://github.com/pressly/goose), using the `migrations/` directory
from the v2 release you ran:

```sh
goose -dir migrations -table schema_migrations postgres "$DATABASE_URL" down-to 15
```

`down-to 15` undoes everything after the last migration that only added tables,
which is just those two. It restores the old table name and removes the added
columns and constraints, and nothing else: your links and any Wayback records
stay where they are, ignored by v1, and are there again if you upgrade later.
For now recueil cannot revert migrations itself.

### If something goes wrong

- **`server` or `agent` won't start and mentions `links_dir`.** The directory
  isn't set, doesn't exist or isn't writable by the container. Fix it, or turn
  links off in both places as in step 2.
- **The Links screen, or the extension's "Save as link", doesn't appear.** Links
  are off, either in the backend (`enable_links`) or in Terraform; they have to
  agree. The extension also remembers the Worker's answer for up to a day, so a
  change can take a while to show; restarting the browser refreshes it.
- **The upgrade stops with "rows in … join … owned by different users".**
  Migration `00017` makes the database enforce that every tag, collection and
  relation joins rows of one user, and first checks your data. It names the
  table and how many rows break the rule. They should never exist, since the app
  has always checked, so this is a sign of data that was changed outside
  recueil. This is only a preflight check so nothing has been changed yet. Find
  the rows with the matching query below, decide whether to delete or fix them,
  and start again. (For `page_collections` and the `link_*` tables, swap in the
  matching tables.)

  ```sql
  -- page_tags: a page and a tag owned by different users
  SELECT pt.page_id, pt.tag_id
  FROM page_tags pt
  JOIN pages p ON p.id = pt.page_id
  JOIN tags t ON t.id = pt.tag_id
  WHERE p.user_id <> t.user_id;

  -- page_relations: two pages owned by different users
  SELECT r.page_id_a, r.page_id_b
  FROM page_relations r
  JOIN pages a ON a.id = r.page_id_a
  JOIN pages b ON b.id = r.page_id_b
  WHERE a.user_id <> b.user_id;

  -- a collection whose parent belongs to someone else
  SELECT c.id, c.parent_id
  FROM collections c
  JOIN collections parent ON parent.id = c.parent_id
  WHERE c.user_id <> parent.user_id;
  ```

- **`agent` keeps warning that the Worker has no routes for links.** The backend
  is newer than the Worker. Apply the Terraform; the warnings stop on their own.
