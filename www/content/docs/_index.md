+++
title = "Overview"
template = "docs-page.html"

[extra]
dek = "A self-hosted webpage bookmarker and archiver — recueil keeps full copies of the pages you save, captured the way your browser actually saw them, on hardware you control."
+++

## What it does

recueil is a personal web archive that runs on hardware you control. Instead of
a pile of bookmarks that quietly rot as pages get edited, paywalled, or taken
down, recueil keeps a full copy of everything you save — the original page, a
cleaned-up readable version, and (optionally) a short summary — searchable from
one dashboard.

## How pages get in

<dl class="intake-list">
  <dt>Browser extension</dt>
  <dd>The primary way in. Click the toolbar icon on any page to save it, and it also picks up anything you've queued from elsewhere.</dd>
  <dt>Share-sheet PWA</dt>
  <dd>Share a link to it from your phone's share sheet, same as any other app.</dd>
  <dt>iOS Shortcut</dt>
  <dd>A one-tap Shortcut for sending the current page without opening an app at all.</dd>
  <dt>CLI</dt>
  <dd><code>recueil enqueue &lt;url&gt;</code> from a terminal.</dd>
</dl>

All four feed the same queue. Nothing archives immediately — see
[Getting Started](@/docs/readers/getting-started.md) for why, and what to expect
the first time.

## How captures get past logins and paywalls

recueil only captures pages from a real, already logged-in browser tab, not a
headless crawler fetching pages cold. That's the whole reason login walls,
paywalls, and CAPTCHAs don't defeat it: whatever you can see in your own browser
is what gets saved.

None of this would be possible without
[SingleFile](https://www.getsinglefile.com), the open-source extension recueil's
own capture is built on. It's the thing actually turning a live tab into a
faithful, self-contained snapshot — we're standing on its shoulders.

## What you can do with your archive

Every capture is searchable by its full extracted text, not just its title. Tags
and nested collections let you organize things your own way. Each page keeps
notes and can be linked to related pages you've saved.

## Ask your archive questions

It's 2026 — we obviously can't _not_ talk about AI. If you'd rather point an AI
client at your own archive than search it by hand, recueil runs a read-only MCP
server: connect a local MCP client with your archive and ask it things like what
you saved about a topic last month. See the MCP Server page for setup.

<div class="cross-links">
{{ cross_link(href="/docs/readers/", title="For Readers", label="Using recueil", desc="Pair a device, capture pages, and work with your archive.") }}
{{ cross_link(href="/docs/operators/", title="For Operators", label="Running an instance", desc="Deployment, configuration, and administration.") }}
</div>
