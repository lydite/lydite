# Design

> **The reference for `assets/` and `docs/design/`.**

The brand and design system live here rather than in a `lydite/brand` repository, because
every consumer of them is already in this repository — see
[ADR 0012](../../docs/adr/0012-design-system-in-the-monorepo.md). The split that matters is
inside the tree, not across repositories:

- `assets/` is **what ships**. Production SVGs, plus `lydite-mark-64.png` and
  `lydite-avatar-512.png`, which is the organisation avatar. Nothing lydite renders embeds the
  raster mark today — the pull-request comment dropped it when the App became the identity — so
  its path is no longer load-bearing for a consumer, and the rule that made it so is recorded
  with the comment in [actions.md](actions.md).
- `docs/design/source/` is where the brand is **authored**. `lydite-brand.svg` is the only
  file in the tree carrying live `<text>`; everything in `assets/` is outlined, so nothing
  shipped depends on a font being installed. Editing it needs **Kohinoor Telugu Bold**, which
  ships with macOS and is absent everywhere else — a machine without it silently substitutes
  and the wordmark redraws wrong.
- `docs/design/tokens.md` is the token set and the surface specifications.
- `docs/design/reference/` is **reference only** — the `.dc.html` prototypes. They use a
  custom runtime and inline styles, both artefacts of the authoring environment: do not
  port the runtime, and do not copy the styles into `source/web/`. The construction proofs that
  lived here measured the retired "L" mark against a supplied raster; the current mark is
  authored as vector, so there is nothing to prove and they are gone rather than restated.

**The mark is a violet gemstone set in dark stone, and it carries no single-colour form.**
Its legibility is facet shading, so one flat colour renders a hexagon inside a black square.
`lydite-icon-mono.svg`, `lydite-icon-flat.svg`, `lydite-logo-mono.svg` and
`lydite-logo-stacked-mono.svg` are therefore **retired rather than redrawn** — a brand kit
that promises a mono lock-up it cannot honour is worse than one that says it has none. A
surface needing one flat colour uses the wordmark, which is flat `#181a1d` already.

**The accent is one value per file, and the two differ by theme.** `#6930e8` on light,
`#a17aff` on dark — both sampled from the gem's own gradient, so the accent cannot drift from
the mark. Every accented element in a file (the `i` dot, the tagline separators, `QUALITY`)
carries the same one. The dark value is not a preference: `#6930e8` on GitHub's `#0d1117`
canvas is 2.87:1, under the 3:1 floor, and the tagline separators shipped that way until they
were brought in line.

Two things in `tokens.md` are documented but **not implemented**, and neither should be
mistaken for a description of current behavior:

**The CLI output grammar is implemented**, in `internal/ui`, and every command renders
through it — glyphs, leader dots aligning the value column at 34 characters, `--no-color`,
and a verdict-plus-duration last line. Nothing parses it: `lydite/actions` reads the
documents a run wrote. Do not restore a bracketed text
form for a consumer to scrape — that makes every refinement to the human surface
a two-repository release.

**There is no light product theme and no responsive design.** The light token ramp exists
and the PR comment uses it, but no product screen has been drawn light, and nothing below
1240px has been drawn at all. `docs/design/README.md` records both as gaps. Inventing
either from the token table is how a design system starts lying about its own coverage.

