+++
title = "Getting Started"
weight = 1
template = "docs-page.html"

[extra]
audience = "readers"
dek = "Install the extension, pair it with your recueil instance, and capture your first page."
+++

1. **Get your pairing token and worker URL.** Sign in to your recueil dashboard
   and open the _Devices_ screen — both are there.

2. **Install the extension.** Add it from the [Chrome Web Store](#) or
   [Firefox Add-ons](#) <!-- TODO: real store links --> — see
   [Browser Extension](/docs/readers/browser-extension/) for details.

3. **Pair it.** Open the extension and paste in your worker URL and pairing
   token.

4. **Capture something.** Browse to any page and click the toolbar icon —
   recueil takes it from there, and it'll show up in your library shortly.

## Capturing something you queued

If you queued a page instead of capturing it directly — from the share-sheet
PWA, an iOS Shortcut, or another device — the extension shows a notification
badge when something's waiting. Open the popup, click a queued URL to open it,
take care of anything in the way (a CAPTCHA, a login wall), and capture it the
same way you just did.

{% callout(label="Tip") %} No extension installed yet? Queued pages simply wait
— nothing expires. Install it on any browser you use regularly and it'll pick up
the queue automatically. {% end %}

## How this gets past logins and paywalls

recueil only captures pages from a real, already logged-in browser tab, not a
headless crawler fetching pages cold — that's the whole reason login walls,
paywalls, and CAPTCHAs don't defeat it: whatever you can see in your own browser
is what gets saved.
