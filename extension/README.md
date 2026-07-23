# recueil browser extension

Pairing, capture (via
[`single-file-core`](https://github.com/gildas-lormeau/single-file-core)), and
upload to a self-hosted recueil instance.

## Build

```sh
pnpm install
pnpm --filter @recueil/extension build
```

Produces `dist/chrome/` and `dist/firefox/` — two independent, complete
extension trees from the same source, per-browser manifest differences merged in
at build time (see `build.js`). Both are gitignored; nothing here is meant to be
committed.

## Internationalization

User-facing strings live in `_locales/<locale>/messages.json`
([WebExtensions i18n](https://developer.chrome.com/docs/extensions/reference/api/i18n)
— native to both Chrome and Firefox, no library). `en` is `default_locale`
(`manifest.base.json`) and is the fallback for any key missing from another
locale.

The browser picks the matching locale from its own UI language automatically —
there's no in-popup language picker, and none is planned until the popup has
other settings worth building a picker alongside.

**Adding a language**: copy `_locales/en/messages.json` to
`_locales/<code>/messages.json` and translate each `message` value (leave
`description` as-is — it's developer-facing only, shown in some browsers' own
translation tooling, never rendered to a user). No code changes needed;
`just build`/`build.js` picks up any directory under `_locales/` automatically.

**Adding a string in code**: add the key to `_locales/en/messages.json` (and
every other locale — a key missing from a non-default locale silently falls back
to `en`, which is fine short-term but shouldn't be left that way), then call
`t("yourKey")` from `src/common/i18n.js` — never `browser.i18n.getMessage()`
directly (see that file's own doc comment for why: centralizing the call site is
what keeps a future manual-locale-override feature from requiring a rewrite of
every call site).

## Try it locally (temporary, not durable)

- **Chrome**: `chrome://extensions` → enable Developer mode → "Load unpacked" →
  select `dist/chrome`.
- **Firefox**: `about:debugging#/runtime/this-firefox` → "Load Temporary
  Add-on…" → select `dist/firefox/manifest.json` (or the packaged `.xpi`, see
  below — either works here).

## Package

```sh
pnpm --filter @recueil/extension package
```

Rebuilds, then produces real package files in `dist/packages/`:

- `recueil-firefox.xpi` — **unsigned**. Release Firefox will not install this
  permanently; it needs signing first (see below).
- `recueil-chrome.crx` + `recueil-chrome.pem` — the `.pem` is the extension's
  private signing key, regenerated fresh if it's ever missing (the ID Chrome
  assigns the extension is derived from this key, so losing it means a new ID on
  next package). Not committed anywhere — if a stable ID ever matters (e.g. for
  an enterprise force-install policy), move this file somewhere persisted and
  treat it like the secret it is.

## Durable installation

Neither package above installs permanently on its own — both browsers require
more than just having the file:

- **Firefox**: sign it via
  [AMO self-distribution](https://extensionworkshop.com/documentation/publish/self-distribution/)
  ("unlisted" — not published, not publicly listed, just signed for you to
  host/distribute yourself). Needs a free AMO developer account; can be
  automated with `web-ext sign` once you have API credentials.
- **Chrome**: as of Chrome 149 (June 2026), sideloaded/dev-mode extensions get
  auto-disabled on update if they're not from the Chrome Web Store. Durable
  options are either publishing to the Web Store with visibility set to
  "Unlisted" (one-time $5 developer fee, still goes through Google's review), or
  an `ExtensionInstallForcelist` enterprise policy pointing at a self-hosted
  update manifest + `.crx` (no store review, more setup — you're hosting your
  own update infrastructure).
