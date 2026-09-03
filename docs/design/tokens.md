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

Every violet here is sampled from the gem in the mark, so the interface and the
logo cannot drift apart. The greys are the stone's.

| Token | Value | Use |
|---|---|---|
| gradient | `linear-gradient(135deg, #32108F 0%, #6930E8 55%, #A17AFF 100%)` | The gem's own gradient. Marketing accents |
| `violet-500` | `#6930E8` | Primary action, active state, focus ring — **on light** |
| `violet-300` | `#A17AFF` | The same roles **on dark**, and links on dark |
| `violet-700` | `#5924BD` | Pressed |
| `ink` | `#181A1D` | Type on light |
| `ink-dark` | `#D3D6D8` | Type on dark |

**The accent is one value per background, not one value.** No violet clears 4.5:1
on both white and a dark canvas — `#6930E8` is 6.14 on light and 2.87 on `#0d1117`,
`#A17AFF` is the reverse. So a surface picks by its own background, and the two are
the same pair the wordmark uses.

Links on dark: `#A17AFF`, hover `#D2C0FF`. Selection: `rgba(105,48,232,0.4)`.

## Dark surfaces (primary)

| Token | Value |
|---|---|
| `--bg` | `#0D0E10` |
| `--surface` | `#151619` |
| `--raised` | `#1C1E21` |
| `--border` | `#24262A` |
| `--strong` | `#35383E` |
| `--text` | `#D3D6D8` |
| `--muted` | `#9A9DA3` |
| `--faint` | `#767980` |
| `--track` | `#1C1E21` |

## Light surfaces

Defined, and used by the pull-request comment. **No product screen is designed
light** — see the gaps in `README.md`.

| Token | Value |
|---|---|
| canvas | `#F6F7F8` |
| surface | `#FFFFFF` |
| surface-sunken | `#EEF0F1` |
| border | `#E0E2E4` |
| text | `#181A1D` |
| text-muted | `#5A5E63` |
| text-faint | `#787B82` |

## Status and severity

| Token | Value |
|---|---|
| `--pass` | `#16C79A` |
| `--crit` | `#F2426E` |
| `--high` | `#FF7A45` |
| `--med` | `#F0B429` |
| `--low` | `#35C4E8` |
| `--info` | `#9A9DA3` |

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

2px outline at 2px offset on every interactive element — `#6930E8` on light,
`#A17AFF` on dark. Never removed — this is a keyboard-heavy developer tool.

## CLI output grammar

The canonical surface. This is a specification.

| Glyph | Meaning | Colour | Exit |
|---|---|---|---|
| `✓` | pass | `#16C79A` | 0 |
| `!` | referral, unmeasured, or dropped | `#F0B429` | 2 |
| `✗` | fail | `#F2426E` | 1 |
| `→` | context line | `#767980` | never a verdict |
| `$` | a command the reader can run | `#A17AFF` | — |

`$` is specified and unbuilt: no status in `internal/ui` renders it.

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
  (`scan passed in 12.4s`, `review referred in 0.3s`). The `identical to CI #4821`
  suffix belongs to parity runs, which are specified and unbuilt.
- A **referral** prints reason, then cause, then a runnable next step. A scanner failure
  prints the tool's own findings instead, which are the reason; extending the three-part form
  to `scan` and `coverage` is specified but not built.

**This grammar is what the binary emits**, from `internal/ui`, for `scan`,
`coverage` and `review` alike.

Anything automated reads `--json` instead, and never the text — which is what
keeps the human surface free to change without coordinating a release
elsewhere. Detail lines are indented for a reason independent of any parser: a
scanner finding quotes source, which can contain anything the source contains,
including something shaped like a verdict.

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

The mark is a faceted violet gem set into a rounded tile of dark stone — lydite is
a stone, and the gem is what a test suite finds in it.

Clear space on all four sides equals one quarter of the tile's height. Approved
backgrounds: white, `#F6F7F8`, `#0D0E10`, `#0d1117` (GitHub's dark canvas). Never
stretch, recolour, rotate, or add shadows or glows — the mark carries its own
lighting, and a second light source reads as a mistake.

**There is no single-colour form, and this is a property of the mark rather than a
gap.** The gem is legible because of facet shading; flattened to one colour it is a
hexagon inside a black square. A surface with one colour to spend uses the wordmark,
which is flat `#181A1D` on light and `#D3D6D8` on dark. Do not commission a mono
lock-up — a brand kit that promises one it cannot honour is worse than one that
states it has none.

| Asset | File |
|---|---|
| Mark | `assets/lydite-mark.svg` — also the app icon and the favicon |
| Mark, circular | `assets/lydite-avatar.svg`, `assets/lydite-avatar-512.png` (organisation avatar) |
| Mark, 64px raster | `assets/lydite-mark-64.png` — **path is a public API**, see AGENTS.md |
| Wordmark | `assets/lydite-wordmark{,-dark}.svg` |
| Wordmark + tagline | `assets/lydite-tagline{,-dark}.svg` |
| Mark + wordmark | `assets/lydite-logo-horizontal{,-dark}.svg` |
| Mark + wordmark + tagline | `assets/lydite-logo-horizontal-tagline{,-dark}.svg` — the README header |
| Stacked | `assets/lydite-logo-stacked{,-dark}.svg` |

Mark colours, measured from the artwork: stone `#050507` → `#5B5E66` across the
facets, gem `#32108F` → `#6930E8` → `#A17AFF`, crown highlight `#B18AFF`, table
`#7650D6`, seat shadow `#1E084E`. The stone's ramp is where the dark surface tokens
above come from.

### Wordmark construction

Kohinoor Telugu Bold, outlined. Lowercase `lydite` always — the wordmark, the binary
and the prose all agree, so nothing downstream has to decide. The `i` tittle is a
true circle and is the one element in the accent while the rest is ink. Tagline:
the same face in caps, `COVERAGE · QUALITY · CONFIDENCE`, with `QUALITY` and both
separators in the accent and the outer two words in ink.

**Every accented element in one file carries one accent**, and the value differs by
background: `#6930E8` on light, `#A17AFF` on dark. Mixing two violets inside a single
lock-up reads as two brands that nearly agree.

**Everything in `assets/` is outlined**, so no shipped file depends on a font being
installed. The type stays editable in `docs/design/source/lydite-brand.svg`, which is
the only file in the tree carrying live `<text>` — editing it needs Kohinoor Telugu
Bold, which ships with macOS and is absent everywhere else.
