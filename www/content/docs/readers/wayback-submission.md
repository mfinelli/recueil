+++
title = "Wayback Machine"
weight = 8
template = "docs-page.html"

[extra]
audience = "readers"
dek = "Ask the Internet Archive to keep its own public copy of a page or link."
+++

The [Wayback Machine](https://web.archive.org) is the Internet Archive's public
record of the web. Besides keeping your own copy of a page, recueil can ask the
Wayback Machine to capture it too — a second, independent copy that doesn't
depend on your server, and one you can point other people to.

This only happens when you ask. Nothing is ever sent to the Internet Archive
automatically, and it's available only if whoever runs your instance has set it
up (see
[Deploying recueil](@/docs/operators/deploying-recueil.md#wayback-machine-submission-optional)).
If they haven't, the dashboard still shows links to the Wayback Machine's public
calendar of snapshots for a URL, just without a button to submit one.

## What gets shared

- **The URL:** recueil sends the address — with the usual tracking parameters
  and any `#fragment` already trimmed off — and the Internet Archive does the
  fetching itself. It visits as a logged-out stranger, so it sees what anyone
  would, **not** necessarily what you saw in your browser: a page behind a login
  or a paywall may fail, or come back as its login screen. The copy you captured
  with the extension is never uploaded.
- **The result is public.** Anyone can look the snapshot up. Don't submit
  private addresses (your home network, an internal tool), pages you wouldn't
  want findable, or URLs that carry a secret token in the query.

## Submitting

Open a page or a link in your dashboard and find the **Internet Archive** block
near the bottom. If it has never been submitted, click **Submit to Wayback
Machine**.

The Internet Archive can take a few minutes to make the capture, and recueil
checks on it every few minutes, so **reload the page a later to see how it
went** (there's no live progress). Then:

- **Submitted to the Wayback Machine**, with when it was last submitted, means
  it worked, and **View snapshots** opens the calendar of everything the
  Internet Archive holds for that URL (not only yours). **Submit again** takes a
  fresh one.
- If it failed, the Internet Archive's explanation is shown (a page that
  couldn't be reached, a URL it won't capture) with a button to try again.
- If the Internet Archive never reports back at all, recueil gives up after an
  hour.

**Check for existing snapshots** is just a link to the same calendar. It works
whether or not you've submitted anything, and it's how you see whether the page
is already there.

## Limits

The Internet Archive only allows so many captures: a handful a minute, and only
a few of the same URL in a day. Asking for a URL that was captured within the
last hour just hands you that recent capture instead of making another. The
limits are shared by everyone on your instance, so a busy moment can
occasionally refuse a request; the message says so, and trying again later is
all that's needed. What you've submitted is private to you, even though the
account behind it is shared.
