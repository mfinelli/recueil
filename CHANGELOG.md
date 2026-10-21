# recueil changelog

This document keeps track of notable changes to `recueil`. The format is loosely
based on the [Keep a Changelog](https://keepachangelog.com/en/1.0.0/) format.

`recueil` adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v2.0.0 — unreleased

This is a major release that adds two new features (bookmark-only links and
Wayback Machine submission). It's a major release because the upgrade process
requires manual intervention.

### Upgrading

Read
[the upgrade guide](https://recueil.app/docs/operators/upgrade-guides/#upgrading-from-v1-to-v2)
before you do. In short:

- **Apply the Terraform first**, then update the backend. The other order works
  but is noisier until the Worker catches up.
- **Links are on by default**, and `server` and `agent` won't start with them on
  unless they have somewhere to keep link icons. Add `LINKS_DIR` and its bind
  mount to both, or turn links off with `ENABLE_LINKS: "false"` (and
  `enable_links = false` in Terraform).
- Update the browser extension; an older one keeps working, but without links.
- If you call the API yourself, some routes were renamed (see **Changed**), and
  rolling back to v1 takes one extra step, reverting a migration that renames a
  table. Both are in the guide.

### Added

- **Links**: bookmark-only entries for things that don't archive well (videos,
  repositories, apps), with a title, icon, notes, tags and collections. Saved
  from the extension (a plain **Save as link** under the capture button,
  including for queued pages) or added by hand in the dashboard, with a Links
  screen, a screen per link, tag and collection links, and a hint when a URL is
  also an archived page. Saved links get their own favicon store (`links_dir`)
  and a second pass in `recueil gc` (`--skip-captures` and `--skip-links` run
  one).
- **Bookmark sync for links**: a second, separate folder named "recueil links"
  in your browser's bookmarks, with its own opt-in. A page or link can be left
  out of its sync from its screen.
- **Wayback Machine submission** (optional): ask the Internet Archive to capture
  a page or link from its screen. Needs an archive.org account's keys
  (`wayback_access_key`, `wayback_secret_key`).
- **MCP**: `search_links` and `get_link` (only when links are on).

### Changed

- **Relationships between items are now named by what they relate**, so "links"
  no longer means two things at once. What was "linked pages" is now **related
  pages**: `/api/pages/{id}/links` is `/api/pages/{id}/related-pages`, the
  `links` field on a page is `related_pages`, `/api/pages/link-candidates` is
  `/api/pages/relation-candidates`, a request body names its target as `page_id`
  rather than `link_page_id`, and the table `page_links` is `page_relations`.
  **This breaks API clients that used the old names**; the dashboard and
  everything else in recueil is updated together, however, so you shouldn't need
  to worry about this.

## v1.0.1 — 2026/09/16

Add `data_collection_permissions` to Firefox manifest definition to permit
distribution on addons.mozilla.org.

## v1.0.0 — 2026/09/14

Initial release. There are still a few items on my TODO list but they're all
cosmetic. I don't expect any more major code/schema changes at this point and
I'd like to get a real install to start doing some more testing with.
