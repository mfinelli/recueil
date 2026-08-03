+++
title = "Getting Started"
weight = 1
template = "docs-page.html"

[extra]
audience = "readers"
dek = "Pair a device with your recueil instance and send your first page to be archived."
+++

{% callout(label="Before you start") %} You'll need a **worker URL** and login
credentials from whoever runs your recueil instance. Once you're signed in,
generate your own **pairing token** from the dashboard's Devices screen — that
part is self-service and doesn't need any further help from your administrator.
{% end %}

1. **Get your pairing token.** Sign in to your recueil dashboard and open the
   _Devices_ screen. Copy the pairing token shown there — it identifies your
   account and is safe to reuse across every device you pair.

2. **Install the CLI** (optional — skip this if you'll only be using the browser
   extension). Grab the latest release binary for your platform from GitHub.

3. **Pair this device.**

   ```
   $ recueil auth --url https://your-worker.example.workers.dev
   # paste your pairing token when prompted
   ```

4. **Send your first page.**

   ```
   $ recueil enqueue https://example.com/some-article
   enqueued https://example.com/some-article
   ```

## What happens next

Enqueuing a page doesn't archive it right away. recueil only captures pages from
a real, already logged-in browser tab — so login walls, paywalls, and CAPTCHAs
don't defeat it the way they would a headless crawler. That means your page sits
in the queue until a paired browser picks it up: open the extension's toolbar
popup, and it'll offer to capture anything waiting for you.

{% callout(label="Tip") %} No paired browser yet? Enqueued pages will simply
wait — nothing expires. Install the extension on any device you browse from
regularly and it'll pick up the queue automatically. {% end %}
