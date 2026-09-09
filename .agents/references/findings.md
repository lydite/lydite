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
| a scanner (`biome`, and each tool #111 adds) | the rule with the source text it fired on, read from the tree rather than from the report |
| `crap` | the function's name with its receiver, which `crap.Function.Name` already carries |
| `mutation` | the operator with the text it replaced and the text replacing it |
| `patch` | the stretch's two ends. Its length is deliberately not an ingredient, or adding one untested line to an untested block would orphan the claim already made about it |

**`ordinal` is the disambiguator that is not a line number.** Two identical comparisons on two
lines produce two mutants alike in everything a fingerprint reads; without it they are one claim
and the second survives unreported. It counts identical sites within one file in source order.

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

## The fold carries them through

`readShards` carries every shard's findings into the folded report unchanged. They need no
folding rule of their own: a shard reports exactly the components it was responsible for, so
each claim is made once, and the duplicate a matrix could produce is already caught as a
declared component with two rows.

