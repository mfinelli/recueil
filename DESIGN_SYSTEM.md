# Recueil — Dashboard Design System

This is a reference, not a history — it describes the dashboard's visual design
system as it stands _right now_, and gets edited in place as decisions change.
`DESIGN.md` covers architecture (why the backend/Worker/D1 are shaped the way
they are); this document is scoped entirely to the dashboard's look and
interaction patterns.

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

## Relationship to the extension and marketing site

|                                      | Extension popup                    | Marketing site                   | Dashboard                                                                               |
| ------------------------------------ | ---------------------------------- | -------------------------------- | --------------------------------------------------------------------------------------- |
| Paper/ink/rule/accent/focus tokens   | ✅ originated here                 | mostly compatible                | ✅ same values                                                                          |
| Brass label accent                   | ❌                                 | ✅ originated here               | ✅ adopted                                                                              |
| Fraunces/IBM Plex Mono               | ad hoc (`ui-serif`/`ui-monospace`) | ✅ via self-hosted `@fontsource` | ✅ via self-hosted `@fontsource`                                                        |
| Stamp motif (rotated bordered badge) | ✅                                 | ✅ (seal)                        | ❌ **not reused** — considered for Queue/job status but dropped; extension-only for now |

## Design tokens

### Color

Defined in `src/styles/_tokens.scss` as CSS custom properties on `:root`, via
two Sass mixins (`light-palette`/`dark-palette`) rather than the values written
out twice. Three states: automatic (`prefers-color-scheme: dark`), or an
explicit `[data-theme="light"|"dark"]` override (higher-specificity attribute
selector, always wins regardless of the OS's preference) driven by
a`Settings`-screen preference.

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
site's `font-face` declarations: 500/600, roman/italic; two IBM Plex Mono
weights: 400/500).

### Breakpoints

Defined in `src/styles/_mixins.scss`, mobile-first (`max-width` queries):

- **`$bp-mobile` (640px)** / **`mix.mobile`** — generic content.
- **`$bp-tablet` (900px)** / **`mix.tablet`** — generic content, wider.
- **`$bp-header` (780px)** / **`mix.header-collapse`** — `AppHeader`'s
  threshold, which is separate from `$bp-mobile`. Six nav links + brand
  - account need more room than generic content does; collapsing at the generic
    mobile breakpoint caused the nav wrap onto multiple lines _before_ the
    toggle kicked in, producing an overlapping account block.

### Base reset

`app.scss` carries a small hand-rolled reset rather than a library — a generic
reset's opinions would mostly need overriding anyway to match this project's
tokens/type roles. Currently covers: `box-sizing: border-box` globally, form
elements (`input`/`button`/`textarea`/`select`) inheriting font/color instead of
the browser's UI font, `textarea` vertical resize, `button:disabled` cursor, and
`img`/`video`/`canvas`/`svg` defaulting to `display: block` + `max-width: 100%`
(avoids the stray inline-gap and keeps media from overflowing on narrow
screens). Grows opportunistically as new element types show up in a screen, not
fleshed out ahead of need.

## Patterns

### Shared mixins (`src/styles/_mixins.scss`)

- **`card-surface`** — `--paper-raised` background + `--rule` border + 3px
  radius. Use for inputs, panels, any raised surface.
- **`dotted-rule`** — `1px dotted var(--rule)` bottom border. Use for list row
  dividers.
- **`focus-ring`** — `2px solid var(--focus)` outline with offset. Apply to
  every interactive element's `:focus-visible`, not just the ones that feel like
  they need it.

Every component's `<style lang="scss">` block pulls these in via
`@use "../styles/mixins" as mix;` (and `@use "../styles/typography" as type;`
for the type mixins) — tokens themselves need no import since they're global CSS
custom properties already loaded once via `app.scss`.

### Icons

`@lucide/svelte` Import individual icons via their documented per-icon subpath.

```svelte
import LogOut from "@lucide/svelte/icons/log-out";
```

App-wide defaults (18px, 2px stroke) are set once via Lucide's context API in
`App.svelte`:

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

### Active nav link

`svelte-spa-router/active`'s `use:active` action. Default usage (no options)
does an exact match against the element's `href`. For a nav item whose section
has drill-down routes (e.g. Library → `/pages/:id`, Collections →
`/collections/*`), pass an explicit `path` regex so the link stays active on
those nested routes too — see `AppHeader.svelte`'s
`libraryActive`/`collectionsActive`/`tagsActive`/`linksActive` for the pattern.
The active class itself needs `:global(.active)` in scoped component styles,
since the class is added by the action at runtime and Svelte's scoped-CSS
analysis can't otherwise see it's used.

### Mobile disclosure (nav collapse)

Where a piece of UI needs a different layout on narrow screens rather than just
reflowing, prefer one DOM tree toggled with a boolean (`AppHeader`'s `navOpen`)
over maintaining a second, duplicated markup tree per breakpoint. Close the
disclosure on navigation (`closeNav()` on every link click), not just on an
explicit toggle click.

### Stamp motif

Rotated, bordered badge treatment (see the extension popup and the marketing
site's seal). **Extension-only for now.** It was explicitly considered for the
dashboard (Queue/job status states are the obvious fit) but dropped.

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

**A favicon is always rendered through `Favicon.svelte`**, wherever it appears:
the page and link lists, the grid cards, the linked-pages list, and the source
row on both detail screens. It takes the image's URL (or `null`) and a size in
px, and owns the rest, so there is one behavior everywhere:

- **Check the data before requesting the image.** `Page.favicon_path` (and a
  link's) is `string | null` from the API — `null` means no favicon was ever
  captured, a known state, not a failure. The caller passes `null` as the `src`
  for it, and the component skips the request entirely instead of always firing
  one and waiting for `onerror`.
- **Fail per image, not per list.** The component remembers which `src` failed,
  so one broken favicon never hides another and there is no list-level set to
  keep in step. A different `src` (another item reusing the instance) starts
  fresh.
- **The fallback is a small bordered `Globe`**, scaled to the size. The one
  place that shows nothing instead is the source row on the detail screens: a
  single image beside a URL has no list to keep aligned, so those pass
  `placeholder={false}`.

Sizes are 20px in list rows, 16px in the secondary list on a detail screen and
in the source row, and 14px in grid cards.

**Thumbnails** are the other image, and have no equivalent field on `Page`, so
they still rely on `onerror` alone. `PageList.svelte` keeps its own
`thumbnailLoadFailed` set for them (grid view only), falling back to the title's
first letter in the display serif. Both fallbacks are "this represents a generic
page" states, not blank placeholder boxes.

### De-emphasizing the common case

`PageDetail`'s capture rows show a source label only for `manual_upload`
captures, not `extension` ones — `extension` is the overwhelming default, so
labeling it on every row is noise, not information. When a field has a
heavily-skewed distribution of values, consider surfacing only the uncommon one
explicitly (with an icon, here `Upload`) rather than labeling every row equally.

### Hiding an action when it would be a no-op

`CaptureReader`'s two regenerate buttons (readability extraction, AI summary)
each hide themselves once the capture's stored version/model already matches
`GET /api/capture-config`'s currently-running one — regenerating would just
reproduce what's already there. The comparison fails open: if `/capture-config`
itself fails to load, both buttons default to showing rather than silently
disappearing, since a missing capability is worse than an
occasionally-unnecessary one. Reach for this same fail-open shape for any future
"hide when redundant" control gated on a second, independent fetch.

### A row with two destinations (`LinkList.svelte`)

A link's row opens its detail screen, but the saved URL itself is one click away
on a trailing icon button. Anchors can't nest, so these are siblings: the `li`
is a two-column grid (`1fr auto`) with the row's link in the first column
(favicon, title, URL and date, laid out as `PageList`'s list rows are) and an
`icon-btn` link in the second. The `li` takes the hover background, so the two
read as one row. Anything inside the row that would otherwise need to be its own
link (the "Also archived" marker, which points at the matching page) is a plain
indicator on its own third line, with `role="img"` and an `aria-label` that says
what it means in full ("This URL is also archived as a page"), and the real link
lives on the detail screen. The icon link needs an accessible name that says
where it goes ("Open {title} in a new tab"), since the glyph alone doesn't, and
it opens with `target="_blank" rel="noreferrer"`.

### A pointer line, and a filter pill (`LinkCountLine.svelte`, the Links page)

A tag's or collection's links aren't a second list on its screen (the page lists
are built around grid and list views of pages, and a link has no thumbnail).
They're one muted line under the heading, `Link` icon, "{n} links have this
tag." and an underlined "View links", which shows only when the count is above
zero so a tag with no links looks exactly as it always did. "View links" opens
the Links page filtered by it, and that page says so with a "Filtered by tag"
row tucked up under the toolbar: the filter's name in a pill with a brass-tinted
border (the same tint as an AI tag's) and a small clear button.

### A typeahead relation list (`RelatedItems.svelte`)

"Related pages" and "Related links" are one component, driven entirely by props
rather than by branching: `kind` says what the list holds (`page` or `link`),
which fixes the row's destination, the favicon route, the picker
(`/{kind}s/relation-candidates`) and the request body (`{ {kind}_id }`), and
`ownerPath` says what it belongs to (`/pages/42`), which fixes where adding and
removing go. `excludeId` is the owner's own id when the list holds the owner's
own kind, so an item isn't offered to itself, and is left out for a list of the
other kind. The compact list is rows of favicon, title and URL with a dotted
rule between; the pencil reveals the remove buttons and a search box whose
dropdown shows a handful of matches. The list lives in the parent, so changes go
back through `onchange` and failures through `onerror`, landing in the screen's
own error banner like every other action on it.

### A status box with one action (`WaybackBlock.svelte`)

The "Internet Archive" block on both detail screens is a `--paper-raised` box
with the state on the left and the one action on the right. The state is a line
of text, with a mono, muted second line for detail (when it was last submitted,
a failure's reason, "reload to see the result"), and the plain underlined link
under it. The action is a `bordered-button` with a small icon; while its work is
pending it is disabled and says so ("Submitting…"), rather than the screen
polling for a result. The block that explains a third party (here, that the URL
is shared and the snapshot public) puts that in a small muted paragraph under
the box, only when the action it warns about is available.

### The same-URL callout (`SameUrlHint.svelte`)

A page and a link can share a normalized URL, and each detail screen says so in
one muted line on a `--paper-raised` card under the source row, naming the other
and linking to it ("This URL is also archived as a page: {title}" on a link's
screen with the `Archive` icon, "This URL is also saved as a link: {title}" on a
page's with `Link`). It's one shared component so the two stay identical. The
title comes in the same API response as the id, and falls back to the URL when
the other entity has none. In a list row the same fact is only a small non-link
marker, since a link can't nest in the row's own link.

### A `title` attribute alone isn't enough for a meaningful icon

`Devices`' per-device type icon (Puzzle/Terminal/Zap/AppWindow, one of four
things a bare glyph can't fully disambiguate on its own) needs `role="img"`

- `aria-label` on its wrapper, not just a `title` attribute -- `title` gives
  sighted mouse users a hover tooltip but isn't reliably exposed to screen
  readers on a plain non-interactive element. Without it, sighted users would
  get the device-type info and screen reader users would get nothing. Any icon
  conveying real information (not just decorating already-present text) needs an
  explicit `role="img"` + `aria-label`, `title` alone doesn't cover
  accessibility even though it looks like it should. Active Sessions'
  device-class icon (Monitor/Smartphone/Tablet, plus a `CircleHelp` fallback
  distinct from Devices' `Smartphone` default) reuses this exact pattern.

### One status-badge vocabulary, reused everywhere a status shows up

`Queue`'s badges (pending/claimed-or-processing/captured-or-done/failed) map
onto existing tokens only — muted (waiting), brass (in progress, the amber slot
in the usual red/amber/green convention), accent-success (done), accent (failed)
— no new colors introduced. The same four-way mapping applies identically to
queue items and all three job kinds, with only the label text differing per
context (`Claimed` vs. `Processing`, `Captured` vs. `Done`). Reach for this
exact mapping on any future status-driven list rather than inventing a new color
scheme per screen.

### Relative time: let `Intl.RelativeTimeFormat` own the whole phrase

`Queue`'s relative timestamps ("2 minutes ago") come from
`Intl.RelativeTimeFormat`, not a hand-rolled formatter — it already localizes
correctly, including word order that differs by language ("2 minutes ago" vs.
"il y a 2 minutes," where the "ago" equivalent comes first). Every composing
message (`queue_item_added`, `queue_job_started`, etc.) pairs a plain verb with
that already-complete phrase ("Added {time}") rather than appending the app's
"ago" after it, which would double up or land in the wrong position depending on
locale. Reuse this shape for any future relative-time display instead of writing
a new `"{n} minutes ago"` template.

## Known limitations

- **Password reset**: CLI-only for now rather than a self-service email flow.
  Login's forgot-password link exists in the markup already, but gated off.
- **Language pill toggle won't scale forever**: `Settings`' language picker is a
  pill toggle (matching Theme, Library's List/Grid, CaptureReader's Sans/Serif),
  fine for the two languages supported today but likely to wrap awkwardly once
  more are added. Revisit with a `<select>` (or a searchable picker) once that
  becomes a real problem, not preemptively — the swap is contained to one
  screen.
- **`Footer.svelte` is built but not currently wired into the app** — a
  brand/copyright/license footer with GitHub/recueil.app links, styled and
  tested, but not rendered from `App.svelte` or any route. Left as-is in case
  the decision to include it changes.
