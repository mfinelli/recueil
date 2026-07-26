# Recueil — Dashboard Design System

This is a reference, not a history — it describes the dashboard's visual design
system as it stands _right now_, and gets edited in place as decisions change.
For the story of how a decision was reached (bugs found, options considered,
reversals), see `IMPLEMENTATION.md`'s phase entries; this document just tells
you what to actually do when building or fixing a screen. `DESIGN.md` covers
architecture (why the backend/Worker/D1 are shaped the way they are); this
document is scoped entirely to the dashboard's look and interaction patterns.

Scope: this covers the **dashboard** (`src/`) only. The browser extension popup
(`extension/src/popup/`) has its own, separately-arrived-at visual identity that
this system was reconciled against but doesn't govern — see "Relationship to the
extension and marketing site" below for where they agree and where they don't.

## Philosophy

The dashboard's identity is a library/ledger one — card catalogs, ledgers, ink
stamps — arrived at independently on two other surfaces before the dashboard
ever had a real design pass:

- The **extension popup** (`extension/src/popup/popup.css`) already had a full
  paper/ink surface with a serif heading and monospace data treatment.
- The **marketing site** (`www/sass/style.scss`) already had a darker ledger
  backdrop, brass label accents, and a stamp-red seal.

The dashboard's palette turned out to need very little reconciling: its accent
color and the marketing site's stamp-red were already nearly identical. What it
actually lacked was a label/eyebrow accent (brass) and any typographic role
beyond plain system sans. Those are the two things this pass added.

**Self-hosted-first stance:** the dashboard is the authenticated half of a
self-hosted tool. Fonts are vendored via `@fontsource` rather than pulled from a
CDN (Google Fonts, etc.) — no external request happens on every authenticated
page load. The marketing site is a public "coming soon" page and is exempt from
this; it currently uses the Google Fonts CDN and may move to `@fontsource`
later, but that's tracked separately.

## Relationship to the extension and marketing site

|                                      | Extension popup                    | Marketing site          | Dashboard                                                                               |
| ------------------------------------ | ---------------------------------- | ----------------------- | --------------------------------------------------------------------------------------- |
| Paper/ink/rule/accent/focus tokens   | ✅ originated here                 | mostly compatible       | ✅ same values                                                                          |
| Brass label accent                   | ❌                                 | ✅ originated here      | ✅ adopted                                                                              |
| Fraunces/IBM Plex Mono               | ad hoc (`ui-serif`/`ui-monospace`) | ✅ via Google Fonts CDN | ✅ via self-hosted `@fontsource`                                                        |
| Stamp motif (rotated bordered badge) | ✅                                 | ✅ (seal)               | ❌ **not reused** — considered for Queue/job status but dropped; extension-only for now |

## Design tokens

### Color

Defined in `src/styles/_tokens.scss` as CSS custom properties on `:root`, with a
`prefers-color-scheme: dark` override block. No manual light/dark toggle exists
yet — see "Open items" below.

| Token              | Light     | Dark      | Use for                              |
| ------------------ | --------- | --------- | ------------------------------------ |
| `--ink`            | `#2b2924` | `#ece5d4` | primary text                         |
| `--ink-muted`      | `#746c5c` | `#a89c86` | secondary text, nav links, usernames |
| `--paper`          | `#f2ede1` | `#1d1b17` | page background                      |
| `--paper-raised`   | `#fdfbf6` | `#29261f` | inputs, panels, raised surfaces      |
| `--rule`           | `#ddd2b8` | `#40392d` | borders, dividers                    |
| `--accent`         | `#8a3223` | `#d17b64` | active/primary actions, errors       |
| `--accent-success` | `#3c5c3a` | `#8bb385` | success states                       |
| `--focus`          | `#4a6b8a` | `#7fa8c9` | focus rings only                     |
| `--brass`          | `#b68a2e` | `#d9ae52` | eyebrow labels, `dt` terms           |

### Typography

Defined in `src/styles/_typography.scss`. Three roles, each with a purpose —
don't introduce a fourth without a real reason:

- **`$font-display`** (Fraunces, italic 600) — page/section headings only. Use
  the `heading` mixin.
- **`$font-mono`** (IBM Plex Mono) — URLs, dates, byte sizes, IDs, and eyebrow
  labels. Use the `data-mono` mixin for data, `eyebrow` for uppercase
  section/nav labels.
- **`$font-body`** (system sans) — everything else. This was already the
  dashboard's font before this pass; unchanged.

Self-hosted via `@fontsource/fraunces` and `@fontsource/ibm-plex-mono`, imported
once in `src/main.ts` (four Fraunces weight/style files matching the marketing
site's own `font-face` declarations: 500/600, roman/italic; two IBM Plex Mono
weights: 400/500).

### Breakpoints

Defined in `src/styles/_mixins.scss`, mobile-first (`max-width` queries):

- **`$bp-mobile` (640px)** / **`mix.mobile`** — generic content.
- **`$bp-tablet` (900px)** / **`mix.tablet`** — generic content, wider.
- **`$bp-header` (780px)** / **`mix.header-collapse`** — `AppHeader`'s own
  threshold, which is separate from `$bp-mobile`. Six nav links + brand
  - account need more room than generic content does; collapsing at the generic
    mobile breakpoint caused the nav wrap onto multiple lines _before_ the
    toggle kicked in, producing an overlapping account block.

**Takeaway for new screens:** don't assume `$bp-mobile` is the right threshold
for every component. If a component's own content needs more or less room than
generic mobile content, give it its own breakpoint variable the way `AppHeader`
did, rather than force-fitting it to an existing one or adding ad hoc `@media`
queries with hardcoded widths.

### Base reset

`app.scss` carries a small hand-rolled reset rather than a library — a generic
reset's opinions would mostly need overriding anyway to match this project's own
tokens/type roles. Currently covers: `box-sizing: border-box` globally, form
elements (`input`/`button`/`textarea`/`select`) inheriting font/color instead of
the browser's UI font, `textarea` vertical resize, `button:disabled` cursor, and
`img`/`video`/`canvas`/`svg` defaulting to `display: block` + `max-width: 100%`
(avoids the stray inline-gap and keeps media from overflowing on narrow
screens). Grows opportunistically as new element types actually show up in a
screen, not fleshed out ahead of need.

## Patterns

### Shared mixins (`src/styles/_mixins.scss`)

- **`card-surface`** — `--paper-raised` background + `--rule` border + 3px
  radius. Use for inputs, panels, any raised surface.
- **`dotted-rule`** — `1px dotted var(--rule)` bottom border. Use for list row
  dividers.
- **`focus-ring`** — `2px solid var(--focus)` outline with offset. Apply to
  every interactive element's `:focus-visible`, not just the ones that feel like
  they need it.

Every component's own `<style lang="scss">` block pulls these in via
`@use "../styles/mixins" as mix;` (and `@use "../styles/typography" as type;`
for the type mixins) — tokens themselves need no import since they're global CSS
custom properties already loaded once via `app.scss`.

### Icons

`@lucide/svelte` Import individual icons via their documented per-icon subpath,
which is what actually guarantees tree-shaking:

```svelte
import LogOut from "@lucide/svelte/icons/log-out";
```

App-wide defaults (18px, 2px stroke) are set once via Lucide's own context API
in `App.svelte`:

```svelte
import { setLucideProps } from "@lucide/svelte";
setLucideProps({ size: 18, strokeWidth: 2 });
```

Override `size`/`strokeWidth` per instance when a specific icon needs to differ
(e.g. `AppHeader`'s smaller 16px sign-out icon).

**Decorative by default:** Lucide's base icon component already sets
`aria-hidden="true"` automatically unless you pass it an `aria-*`, `role`, or
`title` prop — nothing to add yourself for the common case. The established
pattern on this dashboard is to put the accessible name on the _enclosing_
interactive element (a button's `aria-label`) and leave the icon itself
decorative, rather than labelling the icon directly.

**One exception:** `AppHeader`'s nav-toggle button is a hand-built three-bar
hamburger (plain `<span>`s + CSS transforms), not a Lucide icon — it's the one
place on the dashboard that needs the classic animated morph into an X, which
requires both states to share the same three shapes so they can animate into
each other. Two separate icon glyphs can only cross-fade as a pair, not morph
line-by-line. Every other icon still goes through `@lucide/svelte`; don't reach
for a hand-built icon elsewhere without a similarly concrete animation reason.

### Active nav link

`svelte-spa-router/active`'s `use:active` action. Default usage (no options)
does an exact match against the element's own `href`. For a nav item whose
section has drill-down routes (e.g. Library → `/pages/:id`, Collections →
`/collections/*`), pass an explicit `path` regex so the link stays active on
those nested routes too — see `AppHeader.svelte`'s
`libraryActive`/`collectionsActive`/`tagsActive` for the pattern. The active
class itself needs `:global(.active)` in scoped component styles, since the
class is added by the action at runtime and Svelte's scoped-CSS analysis can't
otherwise see it's used.

### Mobile disclosure (nav collapse)

Where a piece of UI needs a different layout on narrow screens rather than just
reflowing, prefer one DOM tree toggled with a boolean (`AppHeader`'s `navOpen`)
over maintaining a second, duplicated markup tree per breakpoint. Close the
disclosure on navigation (`closeNav()` on every link click), not just on an
explicit toggle click.

### Stamp motif

Rotated, bordered badge treatment (see the extension popup and the marketing
site's seal). **Extension-only for now.** It was explicitly considered for the
dashboard (Queue/job status states are the obvious fit) but dropped. Revisit
if/when the Queue screen's own design comes up, don't reach for it by default
just because it exists elsewhere in the product.

### Password fields

Every password field (Login, Register ×2, Setup ×2) goes through the shared
`src/components/PasswordInput.svelte` — bindable `value`, its own show/hide
toggle (`Eye`/`EyeOff` from `@lucide/svelte`), consistent `aria-label` swap
between the two states.

### Empty and error states

A centered icon above the message, not just text — `Archive` (muted,
`--rule`-colored) for "nothing here yet" states, `AlertCircle` (`--accent`,
dimmed) for load failures. Same icon Login/Register/Setup already use inline
next to their error text, just larger (28px) and stacked above the message here
since these states get a full block of space rather than sitting next to a form
field.

### Image fallbacks (favicon/thumbnail)

Two independent rules, not one:

- **Check the data before requesting the image.** `Page.favicon_path` is
  `string | null` from the API — `null` means no favicon was ever captured, a
  known state, not a failure. `PageList.svelte`'s `showFavicon()` checks this
  before rendering the `<img>` at all, so a page with no favicon skips the
  request entirely instead of always firing one and waiting for `onerror`.
  Thumbnails don't have an equivalent field on `Page`, so they still rely on
  `onerror` alone — there's nothing to check ahead of time there.
- **Track each image type's failures independently.** A single id-keyed set
  covering both favicon and thumbnail state (Phase 6's original shape) silently
  breaks the moment one screen renders both images for the same page — grid view
  showing a favicon alongside the thumbnail was what surfaced this.
  `PageList.svelte` keeps `faviconLoadFailed` and `thumbnailLoadFailed` as two
  separate `SvelteSet`s for exactly this reason.

Fallback content: a small bordered `Globe` icon for a missing/broken favicon
(list and grid both), the title's first letter in the display serif for a
missing/broken thumbnail (grid only) — both are deliberate "this represents a
generic page" states, not blank placeholder boxes.

## Open items

- **Dark mode toggle**: currently automatic via `prefers-color-scheme` only, no
  manual override. Leaning toward a `Settings`-screen preference
  (system/light/dark) following the exact shape of the existing `language`
  preference in `user_settings` rather than a header toggle. Whichever surface
  lands it, applying the resolved theme needs to happen before first paint (the
  same place `App.svelte` currently resolves the locale, after `sessionReady`)
  to avoid a flash of the wrong theme.
- **Password reset**: CLI-only for now rather than a self-service email flow.
  Login's forgot-password link exists in the markup already, but gated off.

## Screen status

| Screen                         | Status                                                                          |
| ------------------------------ | ------------------------------------------------------------------------------- |
| `AppHeader` (shared)           | Done — active link, mobile disclosure, animated hamburger toggle, icon sign-out |
| `Footer` (shared)              | Done — on every page via `App.svelte`, real version/commit from `GET /info`     |
| Login / Register / Setup       | Done — shared `PasswordInput` toggle                                            |
| Library                        | Done — also styles `PageList` (shared with Tag/CollectionDetail, not yet built) |
| PageDetail                     | Not started                                                                     |
| Collections / CollectionDetail | Not started                                                                     |
| Tags / TagDetail               | Not started                                                                     |
| Devices                        | Not started                                                                     |
| Queue                          | Not started                                                                     |
| Settings                       | Not started                                                                     |
