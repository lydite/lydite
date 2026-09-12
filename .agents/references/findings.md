# Findings: the located claims, as data

> **The reference for `internal/finding`** — the located claims, their fingerprints and their anchors.

Every gate names the exact place it is complaining about, and `internal/finding` is that place
as data rather than as the prose a row renders — **a located claim about the code that one edit
clears**, travelling in a top-level `findings` key of each command's report document, beside the
rows and never inside one. See
[ADR 0030](../../docs/adr/0030-findings-are-data-in-the-report-document.md).

**The definition decides who may emit one.** CRAP's functions over the threshold, mutation's
survivors, a scanner's findings, and patch coverage's untested new code. **Per line is not a
finding**: a three-hundred-line untested addition is one thing to do rather than three hundred,
and neighbouring uncovered lines have nothing to tell them apart — so `coverage.Uncovered`
groups them into contiguous **stretches**, ending where cover resumes or where the change does.
A stretch counts the lines the report speaks for and not its span: a comment written inside an
untested block lies between the two ends and counts towards neither side of `PatchPercent`, so a
claim measured by the span would say more lines are untested than the gate ever counted.
It consults no function boundary, and that is a limit rather than an oversight: lydite parses Go
and reads lcov for the other two, so a rule asking which function a line is in would answer for
one language and guess for the rest. **An orphaned file is not a finding** either — the one edit
that clears it is to `.lydite/components.yml`, not to the file a thread would sit on.

**A gate emits findings exactly where it makes a claim the author must clear**, which is where
its row fails. The CRAP gate can name every function above the threshold on a *passing* row too,
and those are debt the change did not add; a claim on each of them would fire on every pull
request about code nobody touched, which is how a gate earns a reputation for firing on ordinary
work.

**A row's `Detail` is rendered from its findings**, not beside them. Two derivations of one
answer are free to disagree, and the one nobody looks at is the one that drifts — the same
argument that keeps `review`'s verdict reaching the comment through its document rather than
being rendered twice. **Findings never vote**: the rows hold the verdict, and a gate that
already failed a row would otherwise be counted twice.

**A count of them is a scalar the quality history holds**, per gate and per component, read out of
`scan.json` by `lydite test record` — see [quality-history.md](quality-history.md) and
[ADR 0033](../../docs/adr/0033-a-finding-count-per-gate-reaches-the-ledger.md). Nothing else about a
finding reaches the branch: the per-finding history that would need is additive and unbuilt, and
every claim already carries the identity it would be keyed by.

**The channel is a document key and deliberately not a sibling file.** `measurements.json` is
the obvious precedent and it does not carry over: it has one writer, `lydite test`, while
findings have three — `scan`, `test` and `mutation` — and a local run writes all three into one
`.lydite-reports/`, where the third would clobber the first. Nor do findings hang off their row:
`ui.Row` and `ui.jsonRow` are converted by direct struct conversion, so the rendering type would
grow a field it never renders, and the grouping buys nothing because a finding carries its own
gate and component regardless — the alternative being to parse `gosec(cli)` back out of a label,
which is the text-scraping the channel exists to remove. `ui.Document` accepts keys it does not
know, and that tolerance was written for exactly this.

## A fingerprint contains no line number

`v1:sha256(gate │ component │ path │ site │ ordinal)[:16]`, derived centrally in
`internal/finding` for the reason `internal/pathmatch` holds one matcher — a second copy agrees
until one learns something, and the disagreement surfaces as a duplicate anchor rather than as a
failing test.

An edit anywhere above a finding moves its line while changing nothing about the claim, so a
line-keyed identity reports the same finding as new on every push. `site` is content, per gate:

| Gate | `site` |
|---|---|
| a scanner over source (`biome`, `gosec`, `semgrep`, `cargo clippy`) | the rule with the source text it fired on, read from the tree rather than from the report |
| a scanner over dependencies (`cargo-audit`, `cargo-deny`, `govulncheck`) | the advisory's own identifier with the package and version — **not** the line's text. See below |
| `crap` | the function's name with its receiver, which `crap.Function.Name` already carries |
| `mutation` | the operator with the text it replaced and the text replacing it |
| `patch` | the stretch's two ends. Its length is deliberately not an ingredient, or adding one untested line to an untested block would orphan the claim already made about it |

**`ordinal` is the disambiguator that is not a line number.** Two identical comparisons on two
lines produce two mutants alike in everything a fingerprint reads; without it they are one claim
and the second survives unreported. It counts identical sites within one file in source order.

### A dependency advisory is identified by the advisory, not by its line

**This is the one place a scanner's `site` is not read from the source it fired on.**
`cargo-audit`, `cargo-deny` and `govulncheck` claim things about a package rather than about a
line someone wrote, and they are located at the `Cargo.lock` stanza or the `go.mod` require
naming that package — because the one edit that clears such a claim is the bump, and editing the
call site clears nothing. A standard-library advisory is located at the `go` directive, since a
toolchain bump is the edit.

A lockfile line reads `name = "time"`. Two advisories against one crate would share that text, so
a site read from the line would make them one claim and `finding.Set` would drop the second; the
ordinal cannot save it, because that separates a repeat in source order and these are two
different claims. So the site is `RUSTSEC-2020-0071␟time 0.1.44` — stable across exactly what
should not re-identify it: a lockfile reordering, a line moving, the advisory being reworded.

**A line that cannot be found is `Line: 0` and unanchorable**, never a guess. That is a module no
manifest names because it was resolved transitively, or a lockfile lydite could not read. Since
[ADR 0031](../../docs/adr/0031-a-located-finding-is-a-review-thread.md) a guessed line is a
thread on unrelated code, and the standing comment is a correct home for a claim reaching no
line.

**One advisory against two modules is two claims.** A record routinely names the standard
library and the `golang.org/x/...` module the same code is vendored from; when both are in the
build, two bumps on two manifest lines clear them. The site is the pair, so a producer
collapsing its tool's repeats must collapse on the pair too — `govulncheck` emits several
messages per advisory at increasing trace depth, and keying that collapse on the id alone drops
a claim the fingerprint was keeping.

See [ADR 0032](../../docs/adr/0032-every-scanner-reports-its-findings-as-data.md).

**Message and severity are excluded** — a tool that rewords or reclassifies its own diagnostic
has not found something different, and orphaning every anchor on a tool upgrade is the failure
this exists to avoid. **Path is included**, so moving a file re-identifies everything in it:
the alternative is two functions of one name in one component sharing an identity, and a claim
attached to the wrong code is worse than one attached afresh.

**`v1` is load-bearing.** Changing the formula orphans everything derived from the old one, so
the version makes that visible rather than silent — a reader keeps understanding every version
ever emitted while a writer emits only the current one.

`finding.Source` reads the code a site is identified by, **through an `os.Root`**: a path
reaching it was reported by a tool reading a repository lydite does not own, and the text it
names becomes a `site` that travels in the report document — so a path climbing out of the tree
would put a line of somebody else's file into lydite's own output. Confined at the syscall
rather than checked lexically, for the reason `internal/mutation` opens a worker's root before
the copy rather than after. It reads once per file, and is one
implementation because two gates need it: a second copy would resolve a path against a different
root or normalise it by a different rule, and a claim identified differently by two producers is
a claim reported twice. A stretch whose source cannot be read keeps its claim and loses only
what tells it from a neighbour — the row has already gated on it, and dropping the claim to
protect its identity would hide a failure lydite found.

## Anchoring is decided where the finding is made

A finding carries whether it can be attached to a line, to a file, or to neither. The gate
decides it from the `coverage.ChangedLines` map it already holds, and never whatever posts it:
the renderer of the standing comment is pure — no network, no git — so a question it cannot ask
has to be answered before it reads the document.

That map is `--unified=0`, so it is the change's added lines with no context. It is therefore
**narrower** than any diff a hosting platform shows, which matters in one direction only: a
finding lydite calls anchorable is one such a platform accepts, and the error case is a claim
that loses its precise anchor rather than one a platform refuses.

**The CRAP gate produces unanchorable findings on purpose.** A function crosses the threshold
because a call site grew or its test was deleted, which is the blind spot ADR 0028 says the gate
exists to cover, and neither edit is in the function.

**A scanner's findings are rebased onto the scan root** where its component is labelled, in
`labelled`, because a check runs inside its component and reports paths relative to there while
every other producer names a file from the root. One file named from two roots is two claims,
and only one of them can be anchored.

## A finding names its row, and that decides which surface renders it

`Finding.Row` is the label of the row that made the claim, written by the producer that already
holds both. It is a label rather than a nesting, and it is carried rather than parsed: a finding
still carries its own gate and component, so nothing has to take `gosec(cli)` apart. It takes no
part in the fingerprint — relabelling a row is not finding something new.

What reads it is the standing comment. A failing row renders its detail from that row's
**unanchored** findings plus a line counting the located ones, and a row with no findings quotes
`Row.Detail` exactly as before. The located ones are threads on their own lines (see Surface),
and **no finding appears in both** — a partition on `Anchor`, which is already in the document,
so the two renderers need no knowledge of each other. The narrowing is unconditional: a
developer running `lydite publish` locally reads exactly the comment a reviewer sees.

The cost is that a row's non-finding asides — mutation's `3 did not compile` — leave the comment
with the rest of that row's `Detail`. They stay on the terminal and in the log the row names.
This partly reverses [ADR 0030](../../docs/adr/0030-findings-are-data-in-the-report-document.md)'s
removal of the row link, which
[ADR 0031](../../docs/adr/0031-a-located-finding-is-a-review-thread.md) states as an amendment.

## A report holds each claim once

`ui.Report.AddFindings` collects on the fingerprint and keeps the first under each, through
`finding.Set` — the one implementation of the rule, which `lydite threads` reaches too. Every
producer already goes through `AddFindings`, so the fold, the patch gate, the mutation run and
the scan are all covered by the one gate, and a consumer of the document never sees a claim
twice.

The drop is silent. A duplicate is lydite's plumbing rather than a gate result, and a row
announcing one would sit among rows about the code; the document holding each claim once is
what a reader can observe. `lydite threads` is the exception that names its drops on stderr,
because it consumes documents a local run wrote with no fold at all and a duplicate there is
the operator's evidence that two commands wrote into one directory.

**A producer that receives one claim twice must collapse it itself.** `cargo clippy
--all-targets` compiles a crate as a library and again as its own test harness and reports every
lint under both, identical in target, span, rule and rendered text alike. Neither the fingerprint
nor `finding.Set` can help: `finding.Number` gives the two copies ordinals 0 and 1 — which is
exactly how it keeps two real occurrences of one rule on one line apart — so they hash
differently and both survive. The clippy parser therefore collapses on file, span and rule
**before** numbering, so the ordinals are counted over the real claims. Uncollapsed it is double
the count and two review threads on one line.

**The ordinal is what makes this safe.** `finding.Number` runs per producer, before the claims
reach a report, and `Ordinal` is a fingerprint ingredient — so two genuinely distinct claims
alike in gate, component, path and site (two identical comparisons on two lines) carry ordinals
0 and 1, hash differently, and both survive. Only a repeat of one claim collapses. A producer
that means to state one claim twice cannot, and none does.

Dedup happens at `AddFindings` and not earlier because `scan`'s `labelled` sets `Component` and
rebases `Path` after its parser numbered the findings, and both are ingredients: a fingerprint
taken before labelling is not the fingerprint the document carries.

`readShards` needs no folding rule beyond this. A shard reports exactly the components it was
responsible for, so each claim is meant to be made once; what `AddFindings` catches is a report
directory holding several documents, a re-run job, and a component two shards both measured.

