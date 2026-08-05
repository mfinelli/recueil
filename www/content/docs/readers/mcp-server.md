+++
title = "MCP Server"
weight = 5
template = "docs-page.html"

[extra]
audience = "readers"
dek = "Point an AI client at your archive — search, browse, and read pages without leaving the conversation."
+++

recueil runs a read-only [MCP](https://modelcontextprotocol.io) server — your
dashboard's backend can also speak directly to an AI client, not just a browser.
It can search and read your archive; it can't add, edit, or delete anything.
Nothing it does gets around what you can already see in the dashboard yourself.

## Get an API token

On the dashboard's _Devices_ screen, find the **API tokens** section (below your
paired devices) and click **Create token**. Give it a name that says what's
using it and copy the token immediately: it's shown once, and there's no way to
see it again afterward. Losing it just means creating a new one and revoking the
old, the same as any other credential here.

## Connect a client

The server itself lives at your dashboard's normal URL with `/mcp` on the end —
something like `https://your-instance.example.com/mcp`. Any MCP client that
supports a remote server with a bearer token can use it: send
`Authorization: Bearer <your API token>` with every request.

{% callout(label="Reachability") %} This only works from wherever your dashboard
itself is already reachable — the same network or VPN, nothing more open than
that. If your instance is LAN- or Tailscale-only, the machine running your MCP
client needs to be on it too. {% end %}

## What it can do

- Search your archive by keyword, and list your most recently captured pages.
- List your tags and collections, and browse the pages filed under any one of
  them.
- Read a specific page in full — its notes, tags, collections, and the actual
  extracted text or summary from a capture, not just its title and URL.

That's the whole surface. There's no tool for archiving something new, changing
a tag, or deleting a page — for any of that, use the dashboard itself, the same
as always.
