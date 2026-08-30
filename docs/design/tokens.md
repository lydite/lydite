# Design tokens

The values. `docs/design/reference/design-system.dc.html` is the fuller
reference — it carries component states these tables cannot.

Two conventions run through everything:

- **Severity is never carried by colour alone.** A glyph or a label always says it
  too. The CLI's `--no-color` drops colour and keeps every glyph for this reason, and
  the same rule binds the dashboard's pills and the PR comment's lists.
- **Status fills are the status colour at 14% alpha, with text and glyph at full
  strength.**

## Brand

| Token | Value | Use |
|---|---|---|
| gradient | `linear-gradient(135deg, #6E21F3 0%, #251FF7 100%)` | Logo mark, marketing accents |
| `violet-500` | `#6B2CFF` | Primary action, active state, focus ring |
| `indigo-600` | `#3F2BEA` | Pressed |
| `violet-300` | `#A98BFF` | Links on dark |
| `navy-900` | `#10133F` | Ink, logo on light |

Links on dark: `#A98BFF`, hover `#C9B6FF`. Selection: `rgba(107,44,255,0.4)`.

## Dark surfaces (primary)

| Token | Value |
|---|---|
| `--bg` | `#07091E` |
| `--surface` | `#0E1230` |
| `--raised` | `#151A3D` |
| `--border` | `#1E2450` |
| `--strong` | `#2A3168` |
| `--text` | `#EDEEF5` |
| `--muted` | `#9AA0B5` |
| `--faint` | `#6E7594` |
| `--track` | `#151A3D` |

## Light surfaces

Defined, and used by the pull-request comment. **No product screen is designed
light** — see the gaps in `README.md`.

| Token | Value |
|---|---|
| canvas | `#F5F6FA` |
| surface | `#FFFFFF` |
| surface-sunken | `#EEF0F6` |
| border | `#E1E4EF` |
| text | `#10133F` |
| text-muted | `#545B7A` |
| text-faint | `#8A90A8` |

## Status and severity

| Token | Value |
|---|---|
| `--pass` | `#16C79A` |
| `--crit` | `#F2426E` |
| `--high` | `#FF7A45` |
| `--med` | `#F0B429` |
| `--low` | `#35C4E8` |
| `--info` | `#9AA0B5` |

## Typography

Two families only: **Geist** for language, **JetBrains Mono** for anything the machine
produced — paths, commands, hashes, metrics, CLI output. Self-host both.

| Token | Size / line-height | Weight | Use |
|---|---|---|---|
| `display` | 44 / 1.05 | 600 | Marketing hero, page title |
| `h1` | 28 / 1.2 | 600 | Screen heading |
| `h2` | 18 / 1.3 | 600 | Card title, section |
| `body` | 15 / 1.6 | 400 | Prose |
| `ui` | 13.5 / 1.4 | 400 / 500 | Controls, table cells, nav |
| `mono` | 13 / 1.55 | 400 | Paths, commands, metrics, output |
| `EYEBROW` | 11, tracking `0.14em` | 500 | Table headers, labels |

Negative tracking on headings: `-0.02em` at h1/h2, `-0.03em` at display.

## Space, radius, elevation

Spacing: `4` · `8` · `12` · `20` · `32` · `56` · `88`

Radius: `4` chips · `8` buttons, nav, PR card · `12` banners, terminal, panels ·
`14` cards · `999` pills

| Elevation | Value | Use |
|---|---|---|
| flat | border only | Cards, panels — the default |
| raised | `0 6px 20px rgba(0,0,0,0.35)` | Dropdown, popover |
| overlay | `0 24px 60px rgba(0,0,0,0.55)` | Modal, command bar |

Cards do not lift on hover. This is an instrument, not a marketing page.

## Motion

`120ms` micro · `180ms` standard · `260ms` entrance · `900ms` spinner (linear,
infinite). Easing `cubic-bezier(.2,.8,.3,1)` everywhere except the spinner.

**Data values never animate on load.** A coverage number that counts up reads as
decoration and delays the answer.

## Focus

2px `#6B2CFF` outline at 2px offset on every interactive element. Never removed —
this is a keyboard-heavy developer tool.

## CLI output grammar

The canonical surface. This is a specification.

| Glyph | Meaning | Colour | Exit |
|---|---|---|---|
| `✓` | pass | `#16C79A` | 0 |
| `!` | warn or drift | `#F0B429` | 2 |
| `✗` | fail | `#F2426E` | 1 |
| `→` | context line | `#6E7594` | never a verdict |
| `$` | a command the reader can run | `#A98BFF` | — |

**The Exit column describes the verdict, not the row.** A run has exactly one
verdict — it is the last line — and that verdict owns the exit code; a row's
glyph only says how much attention the row wants. The two come apart because
several states want amber while voting differently: a referral exits 2, but an
unmeasured or dropped measurement is amber and votes for nothing. An unmeasured
gate has to be visibly distinct from one that passed, and it has never failed a
build. `internal/ui` is the single implementation of both halves.

- Row shape is glyph, space, label, leader dots, value — **leaders align the value
  column at 34 characters**.
- `--no-color` drops colour and keeps every glyph.
- The last line is always the command, the verdict and the duration
  (`scan passed in 12.4s`, `review referred in 0.3s`). Parity runs do not exist
  yet, so the `identical to CI #4821` suffix is unimplemented.
- A failure prints reason, then cause, then a runnable next step.

**This grammar is what the binary emits**, from `internal/ui`, for `scan`,
`coverage` and `review` alike.

Anything automated reads `--json` instead, and never the text. That separation
is why the human surface is free to change: the grammar sat specified but
unimplemented for as long as its only consumer parsed the terminal output, and
paying that off once was the precondition for adopting it at all. Detail lines
are still indented, for a reason that outlives the regex — a scanner finding
quotes source, which can contain anything the source contains, including
something shaped like a verdict.

## Copy and voice

- **Verdict first, then the reason, then the next command.**
  `gate failed — coverage 71% < 80% — run lydite cover --explain`
- The CLI is the canonical voice. Other surfaces quote it; they do not rephrase it.
- Lowercase `lydite` always, including sentence-initially.
- Name a runnable command wherever the user is stuck. Never "contact support".
- No exclamation marks and no congratulation. A pass is `gate passed in 12.4s`.
- Numbers carry units and comparisons: `87% (gate 80%)`, never a bare `87%`.

## Responsive

Not designed. Layouts assume ≥1240px. The *intended* degradation, unconfirmed: sidebar
collapses to icons at ~1100px, metric row 4→2 columns at ~900px, chart row stacks,
findings table becomes a card list.

## Logo

Clear space on all four sides equals the height of the lowercase `i` stem — roughly
40% of the mark's height. Approved backgrounds: white, `#F5F6FA`, `#10133F`,
`#07091E`. Never stretch, recolour, rotate, or add shadows or glows.

Mark colours, measured from the artwork: slab gradient `#7222F4 → #1A1EF9`, base
gradient `#0A0C38 → #191A5E`, glyphs `#581CF3`, check `#00D397`, lines `#8B89B0`.

### Wordmark construction

Archivo Bold (700), scaled to a 182-unit x-height and stretched **1.244×
horizontally** — the stretch is part of the brand. The `l` has a rounded bottom-left
terminal, radius 48 units, a custom stem rather than an Archivo glyph; its right edge
stays square to the baseline. The `i` tittle is a true circle, radius 30.5 units, and
is the one element allowed in violet (`#661DF2`) while the rest of the wordmark is
ink. Tagline: Archivo caps, tracking 9.31 units, scaled so total ink including the `Q`
descender is 35.8 units; `QUALITY.` violet, `COVERAGE.` and `CONFIDENCE.` muted ink.
