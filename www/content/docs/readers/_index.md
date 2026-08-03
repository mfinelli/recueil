+++
title = "Overview"
sort_by = "weight"
template = "docs-page.html"
page_template = "docs-page.html"

[extra]
audience = "readers"
dek = "A self-hosted archive of the pages you actually meant to keep, captured the way your browser saw them."
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
  <dt>CLI</dt>
  <dd><code>recueil enqueue &lt;url&gt;</code> from a terminal — see the CLI Reference.</dd>
  <dt>Share-sheet PWA</dt>
  <dd>Share a link to it from your phone's share sheet, same as any other app.</dd>
  <dt>iOS Shortcut</dt>
  <dd>A one-tap Shortcut for sending the current page without opening an app at all.</dd>
</dl>

All four feed the same queue. Nothing archives immediately — see
[Getting Started](@/docs/readers/getting-started.md) for why, and what to expect
the first time.

## Why captures actually work

recueil only captures pages from a real, already logged-in browser tab, not a
headless crawler fetching pages cold. That's the whole reason login walls,
paywalls, and CAPTCHAs don't defeat it: whatever you can see in your own browser
is what gets saved.

## What you can do with your archive

Every capture is searchable by its full extracted text, not just its title. Tags
and nested collections let you organize things your own way. Each page keeps
notes and can be linked to related pages you've saved. And every device you've
paired shows up in one place, with a live queue so you can see exactly what's
captured, pending, or failed.

## Ask your archive questions

If you'd rather point an AI client at your own archive than search it by hand,
recueil runs a read-only MCP server — connect Claude Desktop or another local
MCP client with a personal API token and ask it things like what you saved about
a topic last month. See the MCP Server page for setup.

<div class="cross-links">
{{ cross_link(href="/docs/readers/getting-started/", title="Getting Started", label="Start here", desc="Pair a device and capture your first page.") }}
{{ cross_link(href="/docs/operators/", title="For Operators", label="Running an instance", desc="Deployment, configuration, and administration.") }}
</div>
