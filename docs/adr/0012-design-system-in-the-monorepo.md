# The design system lives in the monorepo, not a brand repository

lydite has a brand and a design system: 22 production SVGs, a token set, component
specifications, a dashboard design, and a grammar for the CLI's own output. The
obvious home is a dedicated `lydite/brand` or `lydite/design` repository, on the
reasoning that brand outlives any one product.

It goes in `lydite/lydite` instead.

## The bar a third repository has to clear

ADR 0010 rejected a `lydite/go` mirror because there was no consumer a monorepo could
not serve, and a published surface would cost sync automation and a second tag
namespace to go stale in. That bar is the right one here, and a brand repository does
not clear it — every consumer of this material is already inside `lydite/lydite`.

## The handoff is three things, not one

Treating it as a single "design system" is what makes a separate repository look
right. It is three bodies of work with three different consumers, and only one of them
is design in the usual sense.

**The logo files are already a published interface.** The pull-request comment embeds
`assets/lydite-mark-64.png` by absolute raw URL against this repository's default
branch, because the comment renders in the *consuming* repository where a relative
path would resolve against their tree. Every historical PR comment in every consuming
repository dereferences that URL on every page view. Moving those files to another
repository is not a migration; it is a retroactive break of every comment lydite has
ever posted. A brand repository would have to keep serving the old path anyway.

**The tokens are consumed by `web/`, which is in this repository.** ADR 0010 committed
the built dashboard bundle specifically so `go build` needs no Node and contributors
without a Node toolchain can build. Putting tokens behind a repository boundary means
publishing a package, versioning it, and reintroducing a cross-repository build
dependency into the one thing that ADR deliberately made self-contained.

**The CLI output grammar is not a design asset at all.** It is a specification for
what the Go binary prints — glyphs, leader dots aligning at 34 characters, exit codes,
verdict-first phrasing. It is coupled to code on both sides: `lydite/actions`'s
`tool_result()` parses lydite's stdout with a pattern anchored at both ends, which is
why detail lines are indented today. A grammar living in a brand repository, away from
both the emitter and the parser, drifts from them silently — and the failure mode is a
PR comment that loses its verdict while the scan still reports success.

So the split worth making is inside the repository: `assets/` for what ships,
`docs/design/` for what is reference, and the output grammar recorded next to the code
that has to implement it.

## The trip-wire

This is a decision about today's consumers, not a claim that brand belongs to a CLI.
It should be revisited when a second product needs the brand without needing the CLI —
concretely, when lydite.org becomes its own repository. At that point the logo has two
consumers that share no build, and publishing tokens as a package earns its cost. The
trigger is a second independent consumer, not the design system growing.

## What is deliberately not done here

Importing the material does not adopt it. Two things are recorded and left alone:

The **CLI output grammar is documented, not implemented.** Adopting it is a breaking
change to an interface another repository parses, so it has to land with the matching
change to the action in the same release, or every consumer's PR comment breaks. That
is its own change with its own blast radius.

The **prototype runtime is not vendored.** The `.dc.html` references need a 69 KB
third-party script to interpolate their templates, and Semgrep finds seven real issues
in it. Suppressing them means annotating code this repository does not maintain;
excluding them with a `.semgrepignore` is worse, because that file *replaces* Semgrep's
built-in default ignore list rather than extending it — adding one silently starts
scanning every `*_test.go` in the repository, which surfaced two unrelated findings the
moment it was tried. A scanner's own repository is the last place to accept either, so
the prototypes render statically and `docs/design/README.md` says how to view one
fully.

The **light theme and responsive behaviour are gaps, not defaults.** The token ramp
for light surfaces exists and the PR comment uses it, but no product screen has been
designed light, and nothing below 1240px has been designed at all. Both are recorded
as gaps in `docs/design/README.md` so they are not mistaken for decisions already
taken — inventing either from the token list is how a design system starts lying about
what it covers.
