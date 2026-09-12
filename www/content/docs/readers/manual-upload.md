+++
title = "Manual Upload"
weight = 4
template = "docs-page.html"

[extra]
audience = "readers"
dek = "Already have a captured HTML file? Upload it straight from the dashboard."
+++

Every other way of getting a page in — the extension, the PWA, a Shortcut, the
CLI — feeds the same queue and eventually goes through a paired browser. Manual
upload skips all of that: it's for when you already have a captured HTML file in
hand and just want it in your archive.

That covers a few different situations: a page someone emailed you as an
attachment, something captured on a device that doesn't have the extension
installed, or a file someone else handed you directly.

## What you need

- **The page's original URL.** recueil doesn't try to extract this from the file
  itself.
- **A fully inlined HTML file** — the kind
  [SingleFile](https://www.getsinglefile.com) itself produces (recueil's capture
  is built on it too; see the
  [Overview](@/docs/_index.md#how-captures-get-past-logins-and-paywalls)), or
  anything else that saves a page as one self-contained `.html` file with its
  images, styles, and fonts already embedded rather than linked out to the live
  page.
- **A favicon** (optional) — `.svg`, `.png`, or `.ico`.

## Uploading

From your dashboard's **Library** screen, click **Upload**. Fill in the URL,
choose your HTML file (and favicon, if you have one), and submit. It's processed
the same as any other capture — searchable full text, a readable version, a
screenshot, and (if you've configured it) an AI summary all follow shortly
after, exactly like a page the extension captured directly.

{% <callout label="Heads up"> %} There's a size ceiling on the HTML file (100MB
by default) — ask whoever runs your instance if you hit it, since it's a setting
they control (`capture_manual_upload_max_bytes`, see
[Configuration Reference](@/docs/operators/configuration-reference.md)), not a
fixed limit. {% </callout> %}
