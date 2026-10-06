# Compa Brand System

## Brand idea

**Compa** is Mexican Spanish slang for pal: the friend who helps when you ask,
knows the work, and keeps things calm. The identity draws that as Compa's own
C, open on one side and holding a distinct square core. The C is Compa; the
core is your stuff. It stays inside the C, on your computer, with you.

The opening keeps the form approachable rather than sealed. The core stays
visible and separate from the C: Compa holds your things for you; it does not
take them over.

The system should feel friendly, capable, and calm: a pal who knows the work
and does not make a show of it. It should never look mystical,
cloud-dependent, or self-directing. Compa helps when you ask, and you stay in
charge.

## Audience

Compa speaks to:

- everyday people who want a personal AI assistant that runs on their own
  computer and explains itself in plain words;
- developers who embed its Go runtime in their own applications and keep the
  host application in control.

Both should always see where control sits: which model and provider answer,
which tools run, and what waits for their approval. Write everyday copy for
people without technical training. Developer material can assume Go, but not
familiarity with this repository or its compatibility identifiers.

## Positioning

Approved positioning:

> A personal AI assistant that runs on your computer.

Approved descriptor:

> Personal AI assistant and embeddable Go runtime.

Approved full descriptor:

> A personal AI assistant that runs on your computer, with a web app for
> everyday use and an embeddable Go runtime for developers.

Product truth is bounded: Compa is a personal AI assistant that runs on the
person's own computer, with a web app and an embeddable in-process Go runtime.
Human authority is central: the person stays in charge. Product communication
may describe its model, provider, sign-in, tool, skill, session, and module
surfaces. Compa keeps its state, such as sessions, settings, and credentials,
in `~/.compa` on that computer; messages go to the model provider the person
chooses. Do not describe Compa as hosted SaaS, promise autonomous outcomes,
claim an operating-system sandbox, claim that nothing leaves the computer, or
claim that other products already embed Compa.

## Naming and compatibility

| Context | Name | Rule |
|---|---|---|
| Product and public prose | **Compa** | Capital C. Use for the product identity. |
| Visual wordmark | **compa** | Lowercase. Use as artwork, not as sentence-case prose. |
| Meaning | Mexican Spanish slang for **pal** | Explain when useful; do not turn it into a second tagline. |
| Repository | `compa` | Already matches the name. Keep verbatim. |
| Go module | `github.com/xibodev/compa/v2` | Already matches the name. Keep verbatim. |
| Headed binary | `compa` | Already matches the name. Keep verbatim. |
| Runtime binary | `compa-kernel` | Already matches the name. Keep verbatim. |
| Local state | `~/.compa` | Already matches the name. Keep verbatim. |

The compatibility identifiers already are `compa`, so adopting this kit
migrates nothing. Changing any of them is a separately approved code and state
migration, never part of a brand change.

## Mark

### Idea and construction

The mark is a fixed open C, Compa's C, enclosing a compact core. It uses
`viewBox="0 0 100 100"`. Do not redraw, round, stroke, rotate, skew, or close the
opening.

Outer open C:

```text
M72 20 H36 C27.16 20 20 27.16 20 36 V64 C20 72.84 27.16 80 36 80 H72 V68 H38 C34.69 68 32 65.31 32 62 V38 C32 34.69 34.69 32 38 32 H72 Z
```

Inner form, always at `opacity=".48"`:

```text
M66 38 H46 C41.58 38 38 41.58 38 46 V54 C38 58.42 41.58 62 46 62 H66 V53 H48 C46.9 53 46 52.1 46 51 V49 C46 47.9 46.9 47 48 47 H66 Z
```

Core:

```text
rect x="72" y="44" width="12" height="12" rx="2.5"
```

No variant may use gradients, filters, glows, shadows, or extra geometry inside
the mark.

### Variants

- **Primary mark**: Cobalt outer form, Deep inner form, Deep core; use on Canvas
  or Surface.
- **Inverse mark**: Dark-surface Text outer form, Soft inner form, Soft core; use
  on Core Night.
- **Primary lockup**: primary mark with the lowercase Ink wordmark.
- **Inverse lockup**: inverse mark with the lowercase Soft wordmark.
- **Wordmark**: lowercase `compa`, without an attached descriptor.
- **Monochrome**: all forms use black or white; the inner form retains `.48`
  opacity so the construction remains legible.
- **App icons**: inverse mark on a Core Night tile. Maskable artwork uses a
  full-bleed tile and keeps the mark within the central safe zone.

Do not place the primary mark directly on Cobalt or Deep. Do not recolor
individual forms beyond the supplied variants.

### Clear space and minimum sizes

Use the core width, `12%` of the master viewBox, as the minimum clear space on
all sides of a mark or lockup. Nothing visually active may enter that zone.

| Asset | Minimum digital size | Minimum print size |
|---|---:|---:|
| Standalone mark | 16 px | 5 mm |
| App icon | 32 px | 8 mm |
| Horizontal lockup | 112 px wide | 28 mm wide |
| Standalone wordmark | 72 px wide | 20 mm wide |

At 16 px, use the supplied SVG without adding detail or sharpening effects. If
the inner form cannot reproduce cleanly in a constrained production process,
use the supplied monochrome mark at a larger size rather than simplifying it.

Where the background is unknown or can be dark, such as browser tabs,
taskbars, and system trays, use the favicon tile (`icons/favicon.svg`) rather
than the bare mark: the inverse mark on Core Night reads the same on light and
dark surroundings.

## Color

### Palette

| Token | Hex | Role |
|---|---|---|
| Cobalt | `#3D63D8` | Primary identity and selected actions |
| Deep | `#2749AD` | Strong accent, core, and primary hover state |
| Dark-surface Text | `#9CABFF` | Brand and readable secondary text on Core Night |
| Soft | `#E8ECFF` | Inverse wordmark and high-emphasis content on Core Night |
| Core Night | `#111215` | Dark canvas and icon tile |
| Canvas | `#F7F5EF` | Warm primary light canvas |
| Surface | `#FFFFFF` | Raised light surface |
| Ink | `#171719` | Primary text on light surfaces |
| Muted | `#666976` | Secondary text on light surfaces |
| Line | `#D9D7D0` | Light-surface borders and dividers |

Use Core Night, Canvas, and Surface as fields. Cobalt is an accent, not a page
background by default. Dark-surface Text is specifically tuned for Core Night;
do not substitute Cobalt for small text on dark surfaces.

### Verified contrast pairs

Ratios use WCAG relative luminance and are rounded to two decimals.

| Foreground / background | Ratio | Text use |
|---|---:|---|
| Ink / Canvas | 16.42:1 | AAA normal text |
| Ink / Surface | 17.90:1 | AAA normal text |
| Muted / Canvas | 5.01:1 | AA normal text |
| Muted / Surface | 5.46:1 | AA normal text |
| Deep / Surface | 7.95:1 | AAA normal text |
| Cobalt / Surface | 5.28:1 | AA normal text |
| Surface / Cobalt | 5.28:1 | AA normal text |
| Dark-surface Text / Core Night | 8.64:1 | AAA normal text |
| Soft / Core Night | 15.94:1 | AAA normal text |

Contrast approval applies to the listed pairs, not to arbitrary opacity,
blending, or nearby colors. The `.48` inner mark is decorative geometry and is
not a text color.

## Typography

Use Inter when already available and a system sans fallback otherwise. The
brand kit includes no font binaries and requires no remote resource.

```css
Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif
```

Use a platform monospace stack for identifiers, commands, paths, model names,
and runtime values:

```css
ui-monospace, "SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace
```

Use weight 650-700 for concise display headings, 600 for labels, and 400-500 for
prose. Prefer sentence case. Keep measures compact and spacing deliberate; do
not imitate a terminal for general marketing copy.

The SVG wordmark contains live text with the approved fallback stack. Convert
that text to outlines only for a controlled export where typography must be
frozen; retain the editable text master in this kit.

## Voice

Compa sounds like a friendly, capable, calm pal: plain words, short sentences,
no hype. Lead with what the person can do, name where authority resides, and
distinguish current behavior from plans. Warmth comes from being clear and
helpful, not from slang, jokes, or pretending to be a person.

Approved examples:

- "Choose a model, then tell Compa what you need."
- "Your approval is required before the tool continues."
- "Embed the runtime in-process and keep the host application in control."
- "A personal AI assistant that runs on your computer."
- "Go programs can embed the runtime and keep the host in control."

Prohibited examples:

- "Set it loose and let it run your business." This promises unsupervised
  autonomy.
- "Securely sandboxed by default." This claims a sandbox without evidence.
- "The cloud agent platform for every team." This misstates the product that
  runs on your computer as hosted SaaS.
- "Nothing you type ever leaves your computer." This is false once a hosted
  model provider answers.
- "Already powering products you know." This claims consumption nobody has
  shown.
- "Your AI best friend who truly understands you." This is anthropomorphic and
  implies sentience.
- "Magic intelligence at the heart of everything." This is vague and
  anthropomorphic.

Avoid superlatives, inevitability, sentience, and guarantees. Prefer "can" or a
specific present-tense fact over "always," "never fails," or "fully autonomous."

## Legal and attribution boundary

The Compa name, mark, wordmark, lockups, icons, preview, and social artwork are
Compa brand artwork governed by `brand/LICENSES.md`. Software licensing and
upstream attribution are separate. That attribution remains in the
root `NOTICE`, alongside the root `LICENSE`; do not copy, abbreviate, or replace
that software notice with brand language.

Brand applications must not imply affiliation, endorsement, or ownership of an
upstream project. Legal-entity wording is outside this kit unless separately
approved.

## Imagery and motion

Use quiet, structural imagery: close material detail, layered interfaces,
precise crops, and real product captures. Favor warm neutral fields with one
cobalt focal point. Do not use generic humanoid robots, glowing brains, cosmic
clouds, fake terminal noise, or decorative network meshes.

Motion should explain the relationship between the C and its core:

- reveal the open C before the core, or keep the core steady while the
  interface around it changes;
- use 160-240 ms transitions for interface applications;
- prefer opacity and short position shifts with standard easing;
- honor reduced-motion preferences;
- do not pulse continuously, spin the mark, add glow trails, or imply that the
  product acts without a person.

## Do and don't

| Do | Don't |
|---|---|
| Use the supplied fixed geometry. | Reconstruct the C with a font glyph. |
| Keep the core visually distinct. | Merge the core into the outer C. |
| Use approved contrast pairs. | Set Cobalt body text on Core Night. |
| Pair warm Canvas with crisp Surface. | Fill every surface with brand blue. |
| Describe human review and control. | Promise autonomous guarantees. |
| Label planned applications as illustrative. | Present mockups as shipped UI. |
| Preserve compatibility identifiers verbatim. | Rename binaries or state in a brand change. |
| Keep exports flat and unfiltered. | Add gradients, glows, bevels, or shadows to the mark. |

## Touchpoints

| Touchpoint | Preferred asset | Color mode | Status |
|---|---|---|---|
| App header | Mark and wordmark, drawn by `web/frontend/src/components/brand-logo.tsx` | Primary on light, inverse on dark | Adopted |
| App header on narrow screens | Mark only | Match application theme | Adopted |
| App browser tab | `icons/favicon.svg` and its ICO export | Core Night tile | Adopted |
| App install icons and manifest | `icons/icon-192.svg`, `icons/icon-512.svg`, `icons/maskable.svg` | Core Night tile | Adopted; the maskable entry reuses the 512 tile |
| App color theme | `tokens.json`, `tokens.css` | Match application theme | Planned; the app keeps its own accent |
| Desktop tray icon | `icons/favicon.svg` for small sizes, `icons/app-icon.svg` for large | Core Night tile | Adopted |
| Public website header | `logos/mark.svg`, or `logos/lockup.svg` | Canvas or Surface | Mark file adopted; page not restyled |
| Public website dark footer | `logos/lockup-inverse.svg` | Core Night | Planned |
| Website metadata | `icons/favicon.svg`, `og/og-default.png` | Supplied | Planned |
| Repository and package surfaces | `logos/lockup.svg`, `icons/app-icon.svg` | Surface | Planned |
| Release and social cards | `og/og-default.png` | Core Night | Planned |
| Single-color print or engraving | `logos/mono-black.svg` or `logos/mono-white.svg` | One ink | Ready artwork |

## Adoption note

This kit was drawn for an earlier working name and relabelled Compa. Its
geometry, palette, and rules did not change; its name, wordmark, and copy did.
`provenance.json` records every adopted consumer file.

The app header, browser and install icons, tray icon, manifest colors, and the
website's mark files use the kit. The app's color theme, the website page,
repository and release surfaces, and social metadata have not adopted it yet.
Map each of those deliberately, through its normal review path, and verify the
result in the product rather than treating this kit as runtime evidence. The
mockups in `preview.html` remain illustrative.

Rebuild every raster and consumer copy with `node brand/export.mjs`, then run
`node brand/check.mjs`; `README.md` describes both.
