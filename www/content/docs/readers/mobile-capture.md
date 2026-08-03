+++
title = "Mobile Capture"
weight = 3
template = "docs-page.html"

[extra]
audience = "readers"
dek = "Send pages from your phone — a share-sheet PWA for Android, or a build-your-own iOS Shortcut."
+++

Both of these only add to your queue — neither captures anything itself. That
happens later, when a paired browser picks the item up; see
[Browser Extension](@/docs/readers/browser-extension.md) for that part.

## Share-sheet PWA (Android, and anywhere else with Web Share Target)

Visit your worker URL on your phone's browser and add it to your home screen.
The first launch shows a pairing form — paste in the pairing token from your
dashboard's _Devices_ screen and give the device a name. There's no worker URL
field to fill in here: the page is served by the same worker it talks to, so it
already knows.

Once paired, sharing a page to it from any app's share sheet enqueues the URL —
you'll see a brief "Saving…" while it sends, then confirmation, or a "Try again"
if it fails. Opening the app directly (not via a share) shows a manual **Add a
URL** field for the same thing, plus a **Disconnect this device** option if you
ever want to unpair it.

## iOS Shortcut

Apple Shortcuts aren't plain-text source — they're built and exported through
the Shortcuts app itself, so there's no file to install directly. This is the
recipe for building one by hand.

### Get a bearer token

A Shortcut has no way to run the pairing exchange on its own, so it needs a
bearer token handed to it directly instead of a pairing token. Visit
`<your worker URL>/token.html`, paste in your pairing token and a name for the
device, and it exchanges it for a bearer token shown once — copy it before you
navigate away, since the page itself never saves it. It shows up afterward on
the dashboard's Devices screen like any other paired device, so it can be
revoked independently later.

### Build the shortcut

1. Create a new shortcut and give it a name you'll recognize, like **Save to
   recueil**.
2. Add **Current Date**, then **Format Date** taking that as its input — format
   ISO 8601, with "Include ISO 8601 time value" turned on.
3. Add **Random Number**, 1 to 100000.
4. Combine the two into one **Text** step, something like
   `Save-{Formatted Date}-{Random Number}`. `POST /queue` needs a
   client-generated, reasonably unique `id` for each enqueue — this only needs
   to not collide with another enqueue in the same second, not be a real UUID.
5. Add **Get Contents of URL**:
   - **URL**: `<your worker URL>/queue`
   - **Method**: POST
   - **Headers**: `Authorization: Bearer <the token from above>`, and
     `User-Agent: recueil/1.0`.
   - **Request Body**: JSON, with two fields — `id` set to the combined text
     from step 4, and `url` set to **Shortcut Input** (whatever was shared to
     it).
6. Tap the ⓘ button at the bottom and turn on **Show in Share Sheet**.
7. While you're there, set **If There's No Input** to ask for input rather than
   fail, and consider limiting **Share Sheet Types** to Safari web pages, URLs,
   and articles, so it only shows up for things it can actually use.
8. Optional: add a **Show Notification** step at the end so you get a
   confirmation each time it runs.

{% callout(label="Tip") %} The token lives in plain text inside the shortcut's
saved configuration — viewable if you open the shortcut to edit it, the same
exposure any other client's stored credential has. Don't export or share this
particular shortcut with anyone. {% end %}

{% callout(label="Heads up") %} Shortcuts treats **Get Contents of URL** as
successful once it gets any HTTP response at all — a 401 from a revoked or
expired token, a 500 for an internal error, or any other error response won't
show up as a failure on its own. If enqueues silently stop landing, you might
want to add a **Show** step to show the results from the request to help you
diagnose the problem. {% end %}
