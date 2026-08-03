+++
title = "Browser Extension"
weight = 2
template = "docs-page.html"

[extra]
audience = "readers"
dek = "Capturing, working through your queue, and bookmark sync."
+++

If you haven't already, [Getting Started](@/docs/readers/getting-started.md)
walks through installing and pairing in full. The short version: add it from the
[Chrome Web Store](#) or [Firefox Add-ons](#), then open it and paste in your
worker URL and pairing token from the dashboard's _Devices_ screen.
<!-- TODO: real store links -->

## Capturing a page

Browse to any page and click **Save this page**. That's a direct capture —
recueil takes it from there, and it'll show up in your library once it's
finished processing.

## Working through your queue

Pages queued from somewhere else — the share-sheet PWA, an iOS Shortcut, or
another paired device — show up under **Queue** in the popup, listed by URL.
Click one to claim it: this opens it in a new tab so you can deal with
whatever's in the way (if necessary) — a CAPTCHA or a login wall, for example —
on your own time. Once you're ready, click **Save this page** the same as any
direct capture.

A few things to keep in mind about how this works:

- **A claim only holds for 15 minutes.** If you open a queued item and don't get
  around to capturing it, it just goes back in the queue — where the same
  browser, or another one of yours, is now free to pick it up.
- **The tab closes itself after a successful capture** — but only for tabs the
  queue opened for you. A page you already had open and captured directly is
  yours; recueil never closes a tab you opened yourself.
- **↻ Refresh** checks the queue immediately, if you know something's waiting
  and don't want to wait for the extension's own periodic check.

## Syncing to your browser's bookmarks

Off by default — turn it on from the checkbox in the popup, which will ask for
an extra permission the first time. Once enabled, recueil creates a folder named
**recueil** in your bookmarks and keeps it in sync with your archive, adding,
renaming, and removing entries to match.

Note that this is one-directional and so there are a couple of caveats: it's a
mirror of your archive, not a general bookmarks folder. Adding your own
bookmarks inside it, or renaming or moving the ones recueil created, won't be
preserved — it gets overwritten on the next sync, not just when you disable the
feature. Bookmarking something there also doesn't queue or capture it; the sync
only ever flows one way: archive to bookmarks.

To leave a specific page out of the synced folder without turning off sync
entirely, you can uncheck **Sync with my browser's bookmarks** on that page's
detail view in the dashboard.

## Forgetting a device

**Forget this device**, at the bottom of the popup, signs this browser out — it
clears everything stored locally and takes you back to the pairing form. It
doesn't revoke anything server-side: the device stays listed on the dashboard's
Devices screen afterward, still valid, until you revoke it from there yourself.

## Permissions

Pairing asks for permission to access every site, not just your recueil
instance. This is because capturing a page means fetching everything on it —
images, stylesheets, or any other assets that the page links to — and uploading
the result, not just talking to your instance's URL. Turning on bookmark sync
asks for a separate, narrower permission covering only your bookmarks.

All of recueil, extension included, is open source — see the repository on
[GitHub](https://github.com/mfinelli/recueil) if you'd prefer verify any of
these claims yourself rather than take our word for it.
