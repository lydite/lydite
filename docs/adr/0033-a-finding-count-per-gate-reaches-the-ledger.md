# A finding count reaches the ledger per gate, and a root-scoped gate's count sits beside the components

[ADR 0009](0009-quality-history-storage-and-access.md) names a finding count as one of the four
scalars the quality history holds. [ADR 0029](0029-the-ledger-appends-what-cannot-be-recomputed.md)
recorded the other three and said finding counts were out, because only Biome's report was read and
a count meant every scanner emitting one.
[ADR 0032](0032-every-scanner-reports-its-findings-as-data.md) built that, and ended by saying the
count was now derivable and that carrying it into the ledger was its own slice. This is that slice,
and it amends 0029's "finding counts are out".

**A recording appends how many claims each scanner gate made: per gate rather than as one total,
per component where the gate is per component, and beside the components where the gate is
root-scoped. The counts come from `scan.json` and the set of gates that applies comes from the tree
being recorded.**

## Per gate, because absent is not zero and one integer cannot say so

0029's rule is that every metric is optional and absent is not zero. A finding count has a third
state the others do not: a gate can apply and have found nothing, or not apply to this component's
language at all. A single integer collapses those two, and a series that read an absence as a zero
would draw a clean line through a scanner that never ran over the code — which is the exact failure
the rule exists to prevent, in the one metric where it is most likely.

So `ledger.Component` gains `findings`, a map from gate name to count. A key present at `0` is a
gate that ran and found nothing; a key absent is a gate that does not apply. Per gate also answers
what a total cannot — *which* scanner moved — for the same reason the scalars are per component
rather than per repository.

**Scanner gates only.** `crap` is already counted by `CRAP.Above`, and recording it again here would
turn two quantities that must agree into two quantities free to disagree — 0029's own argument
against storing the repository figure beside the components. Patch coverage is likewise already in
the coverage counts. Mutation stays out for the structural reason 0029 gives and is filed as
[#112](https://github.com/lydite/lydite/issues/112).

## The count comes from the document; the gate set comes from the tree

A clean gate reports no findings at all, so the findings alone cannot distinguish a Go component
gosec found nothing in from one gosec never looked at. The declaration and `.lydite/config.yml`
together say which gates each component's language implies, and they are read from the tree being
recorded — the same rule, for the same reason, as the completeness check on a baseline: asking the
document whose completeness is in question would have it answer itself.

Each language package names its own gates (`golang.FindingGates`, `rust.FindingGates`,
`typescript.FindingGates`), so the set cannot drift from the checks that package runs, and the gate's
name is a constant shared with the row it labels rather than two literals that agree until one is
edited. `cargo fmt` is deliberately absent from that set: lydite is not a formatter, and a gate
listed with no parser behind it would record nought on every commit as though it had looked.

**Rejected: reading the gate set from the scan document's rows.** It would answer two cases this
cannot (below), and it costs the thing findings-as-data exists for — a row's label is prose, and
`gosec(cli)` parsed back into a gate and a component is precisely the text-scraping
[ADR 0030](0030-findings-are-data-in-the-report-document.md) removed.

**A recording that read no scan document records no count at all**, not a nought per applicable
gate. A nought says the gate ran and found nothing, and inventing one for a scan that never happened
would draw a clean line through every commit whose scan job died.

Two limits follow and are accepted rather than worked around. A scanner that crashed found no claims
and so records `0` like a clean one — the red scan is in the document's rows and its verdict, and a
ledger holds scalars rather than verdicts. And two components over one directory and environment are
deliberately scanned once under the first one's name, so the other records `0` for gates that ran
for its twin.

## A root-scoped gate's count is not inside any component

Semgrep runs once over the whole scan root, so its claims name no component. Nothing may invent one:
attributing a claim to whichever component happens to contain its path is an ownership question the
declaration does not answer, and a claim outside every component would have nowhere to go at all.

So `ledger.Record` gains `root_findings`, beside `components` rather than inside one, and the daily
projection carries it for the same reason it carries the components — a scalar the rollup drops is
one no chart can draw without walking every partition.

It is **not** a sum over the components and must never be read as one. 0029's rule that the
repository figures are sums nobody stores is about a figure derivable from what is stored; this is a
different measurement, taken once over the whole tree, and storing it is the only way it exists.

## The channel is `scan.json`, and the scan runs on the default branch

`measurements.json` keeps its single writer. `lydite scan` writes its document unconditionally, under
a top-level `findings` key, into the same report directory a workflow already uploads — which is the
argument [findings.md](../../.agents/references/findings.md) makes for the neighbouring case, and
`measurementsDoc` refuses to load without a `Tree` a scan has no business asserting.

`lydite-baseline.yml` therefore grows a `scan` job beside its measure matrix, mirroring
`lydite-pr.yml`'s with **no `--diff-base`**: on the default branch there is no change to scope to, so
it covers the whole repository, every claim lands `AnchorNowhere`, and the count is the repository's
standing total. It is consequently the one lydite job that needs no `fetch-depth: 0`, since 0032
resolves a base whenever `--diff-base` is given.

`record` keeps `if: ${{ !cancelled() }}` and gains `scan` in its `needs`. **A red scan must still
record**: a red scan on the default branch is the most interesting thing a finding history can hold,
and no later run can fill the hole, because the next push is a different tree.

A measurements document is still required and a scan document is not. Only the first names the tree
it describes, and that name is what binds a recording to the checkout; a set of directories holding
scans alone would be a recording filed against a commit the command had to guess at.

## Consequences

- The fourth scalar ADR 0009 named is recorded, and #111 is closed.
- The ledger gains two fields, which costs a series that starts on the day it was added — what an
  append-only ledger is for, rendered by the same machinery that renders a gap.
- A per-finding history is still additive and still unbuilt: the claims carry the identity it would
  be keyed by, and only the counts travel to the branch.
