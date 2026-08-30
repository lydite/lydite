# lydite design system

The visual foundation for every surface lydite renders: the CLI's own output, the
pull-request comment, the dashboard in `web/`, and the marketing site.

The organising idea is the product's: **parity between local and CI**. Wherever a
scan result appears, the surface also answers "does this match CI?". That is not
decoration, and it is the one thing a redesign must not lose.

## What is authoritative, and what is not

| Path | Status |
|---|---|
| `assets/*.svg` | **Production.** Use as-is. True vector, not traced. |
| `docs/design/tokens.md` | **Production.** The token values, in one place. |
| `docs/design/reference/*.dc.html` | **Reference.** Prototypes, not code to copy. Runtime not vendored — see below. |
| `docs/design/reference/*.svg` | **Reference.** Logo construction proof — never ship these. |

The `.dc.html` files are design prototypes authored in HTML. They use a custom
runtime (`support.js`) with `{{ }}` holes and `<sc-for>`/`<sc-if>` tags, and every
style is inline. Both are artefacts of the authoring environment. **Do not port the
runtime, and do not copy the inline styles** — rebuild in `web/`'s own idiom against
the tokens.

Read `design-system.dc.html` before building any component; it carries
component states the token table cannot.

### Viewing a prototype

Opened directly, a prototype renders its structure and every style — all of it is
inline — but the `{{ }}` holes stay literal and `<sc-for>` emits one unexpanded row,
because **the `support.js` runtime is deliberately not vendored here.**

That is a scanning decision, not an oversight. It is 69 KB of third-party code this
repository does not maintain and never executes, and Semgrep finds seven real things
in it — prototype pollution, wildcard `postMessage`, origin validation. Carrying it
means either seven suppressions in code nobody here owns, or a `.semgrepignore`, which
**replaces Semgrep's built-in default ignore list rather than extending it** and so
silently starts scanning `*_test.go` across the repository. Neither is worth a viewing
convenience in the repository of a tool whose entire argument is that its own scan is
honest.

To view a prototype fully, copy `support.js` from the design handoff next to the file,
along with the logo set it expects at `assets/logo/`:

```sh
mkdir -p /tmp/lydite-design/assets && cp -r assets /tmp/lydite-design/assets/logo
cp docs/design/reference/*.dc.html /tmp/lydite-design/
cp <handoff>/design/support.js /tmp/lydite-design/
open /tmp/lydite-design/design-system.dc.html
```

## Surfaces

**CLI output** — the canonical surface. Everything else quotes it; nothing rephrases
it. Its grammar is specified in `tokens.md` under "CLI output grammar" and
implemented in `cli/internal/ui`, which every command renders through. Anything
automated reads `--json` and never the text, which is what keeps the human
surface free to change.

**Pull-request comment** — drawn in the *host's* light chrome, not lydite's dark
theme, because GitHub owns that page. Use the light token ramp there.

**Dashboard** — the primary application screen, two columns, dark. `web/` builds it;
see ADR 0009 for where its data comes from and ADR 0010 for why the bundle is
committed.

**Marketing site** — hero and feature band on the dark canvas with the brand gradient
as a radial wash.

## Known gaps

These are unfinished, not decided:

1. **No light theme for product screens.** The light token ramp exists and the PR
   comment uses it, but no product screen has been drawn light. Building one is new
   design work — do not infer it from the token list.
2. **No responsive design below 1240px.** The intended degradation is recorded in
   `tokens.md`, but it is a sketch, not a design.
3. **Product UI icons are not exported.** They are inline in
   `design-system.dc.html`, section 04 — conventional 20px stroke icons at
   1.5px, carrying no brand geometry. Extract or substitute.
