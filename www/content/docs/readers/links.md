+++
title = "Links"
weight = 3
template = "docs-page.html"

[extra]
audience = "readers"
dek = "Save a page as a bookmark when an archived copy isn't what you want."
+++

A **link** is a saved URL and can have notes or be added to tags and
collections, but no archived copy is saved. It's for the things that don't
archive well: a video, a code repository, an interactive app, a site that's
really a hundred pages. Recueil keeps your link to it, not the thing itself.

That obviously makes it quite different from a
[page](@/docs/readers/browser-extension.md#capturing-a-page). A link doesn't
have its own offline copy, and if the site changes or disappears, so does what
your link points to.

Links are on by default but if whoever runs your instance has turned them off,
you won't see **Links** in the navigation, and the extension won't offer to save
one.

If you use an [MCP client](@/docs/readers/mcp-server.md), it can search and read
your links too.

## Saving a link

- **From the browser extension.** Under the **Save this page** button there's a
  plain **Save as link** link. It works on a page you queued from somewhere else
  too: open it from the queue and choose **Save as link** instead of **Save this
  page**.
- **By hand, in the dashboard.** On the **Links** screen, **Add link** asks for
  a URL, and optionally a title and an icon file (`.svg`, `.png` or `.ico`, up
  to 512 KB).
