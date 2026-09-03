# Everything that builds lives under `source/`

[ADR 0010](0010-repo-topology.md) put lydite in its own org as a monorepo plus a
thin actions repo, and described the monorepo as `cli/` (Go), `web/` (the React
dashboard) and later `worker/`. That topology decision stands and nothing here
revises it. What changes is the arrangement inside the monorepo.

**The buildable trees move under one root: `source/cli/` and `source/web/`, with
`source/cloud-services/` landing alongside the relay that needs it. Everything
that configures, documents or ships the repository stays where it is.**

```
source/cli/            the Go module
source/web/            the React dashboard
source/cloud-services/ the Workers, as one npm workspace
docs/  assets/  scripts/  .lydite/  .github/  .gt-repo.yaml  .goreleaser.yml
```

## The root was becoming two lists nobody could tell apart

A reader landing at the repository root met `cli/` and `web/` interleaved with
`docs/`, `assets/`, `scripts/`, `.lydite/`, `.github/` and four dotfiles, with
nothing marking which of them a build ever reads. That was survivable at two
buildable directories. It stops being survivable as the Workers arrive, because
the question a newcomer asks — *where is the code?* — gets a longer answer every
time the repository grows, and no answer that is checkable.

Under `source/`, that question has one answer and the root reads as what it is:
configuration, documentation and shipped assets.

The alternative was to leave the root flat and let it accumulate. It costs
nothing today and cannot be paid off later cheaply: a rename of this shape is
reviewable exactly once, while the repository is small enough that a reviewer can
still confirm every entry is a pure rename. Doing it at four buildable trees
rather than two is strictly worse, and doing it at ten is not done at all.

## Why not one directory per language at the root

`go/`, `web/`, `workers/` at the root was considered and rejected. It puts the
same interleaving back — the root still mixes buildable trees with configuration
— and it names directories after languages, which is a fact the build tool
already states and which stops being true the moment a tree is polyglot. The
grouping that earns its place is *buildable versus not*, because that is the
distinction a reader is actually trying to draw.

## The scan root does not move

`.lydite/` stays at the repository root, and it is still what `--dir` names.
A component's `dir` is relative to the scan root, so the declaration gains a
`source/` prefix and nothing else changes: the component keeps its name, and a
coverage baseline is keyed by component name rather than by directory, so the
history recorded against `cli` before the move is the history compared against
after it.

Putting `.lydite/` under `source/` was rejected. It configures the repository
rather than being built by it, and a scan root below the workflows and the
governance file would make `--dir` name a directory that cannot see either.

## What this costs

Every path reference outside the moved trees has to move with them, and they are
scattered across kinds: eight Dependabot directories in `.gt-repo.yaml`, seven
workflows, `.goreleaser.yml`, the component declaration, `AGENTS.md`, the design
README — and one Go constant, `repoRoot` in `scan_test.go`, which no search of
the configuration would have found. That last one is the argument for making the
move its own change with no behaviour in it: mixed with a feature, a wrong
constant reads as part of the feature.

Older ADRs keep naming `cli/` and `web/`, and are deliberately not edited. They
record decisions taken against the tree as it was, and rewriting them to match a
later layout destroys the only thing they are for. This ADR is where the current
arrangement is recorded.
