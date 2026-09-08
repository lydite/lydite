# A finding is data in the report document, identified by what it is about

Every gate lydite has can name the exact place it is complaining about, and
none of them could say so to anything but a person. `crap.Function` carries a
file and a line, `mutation.Mutant` carries a path, a line and a column, and
patch coverage computes the uncovered line and throws it away. All of it
reached the surface as `Row.Detail` — a `[]string` of rendered prose.

So a consumer wanting to anchor a finding to the line it is about would have to
parse that prose, which AGENTS.md forbids in terms: *"Do not reintroduce a
bracketed text mode to accommodate it. A text-scraping consumer forces every
refinement to the human surface through a synchronised release."*

**A finding is a located claim about the code that one edit clears. Findings
travel as data in a top-level `findings` key of each command's own report
document, beside the rows and never inside one. A finding is identified by a
content-derived fingerprint that contains no line number, and the prose a row
renders is derived from the findings rather than beside them.**

## Three consumers, and a channel invented three times would be three shapes

[#111](https://github.com/lydite/lydite/issues/111) wants a count per component
in the quality-history ledger. [#114](https://github.com/lydite/lydite/issues/114)
wants a finding that names a file and a line to arrive as a review thread on
that line. [ADR 0009](0009-quality-history-storage-and-access.md) defers
per-finding fingerprints as *"additive later"*, so that "when did this finding
first appear?" can be answered one day.

None of the three can start without the same missing piece, and each would have
invented it differently. So it is designed once, here, and the other two consume
it.

## What may be a finding, and what may not

The definition does the work. *One edit clears it* is what a review thread needs
in order to be resolvable, what a count needs in order to mean anything, and
what a fingerprint needs in order to exist at all.

It admits the CRAP gate's functions, mutation's survivors, a scanner's findings,
and patch coverage's untested new code. It excludes two shapes on purpose:

**Per line is not a finding.** A three-hundred-line untested addition is not
three hundred claims — there is no per-line action, the action is to test the
function — and neighbouring uncovered lines have nothing to tell them apart, so
no per-line identity is derivable. Patch coverage therefore emits one claim per
contiguous **stretch** of untested new code. A stretch ends where cover resumes
or where the change does; it consults no function boundary, because lydite
parses Go and reads lcov for the other two, so a rule asking which function a
line is in would answer for one language and guess for the rest. Breaking at
unchanged code is most of what a function boundary would have bought.

**An orphaned file is not a finding.** The orphan gate names a path, but the one
edit that clears it is to `.lydite/components.yml`, not to the file a reader
would be pointed at. A claim whose location is not where its fix goes is a
claim pointing at the wrong code.

## The channel is a document key, and the two alternatives lose

**A sibling `findings.json`** would follow the `measurements.json` precedent —
a document beside the report carrying what rendered rows cannot — and it does
not work. `measurements.json` has exactly one writer, `lydite test`. Findings
have three: `scan`, `test` and `mutation`. A local run writes all three into one
`.lydite-reports/`, so the third would clobber the first. Fixing that means
`scan-findings.json`, `test-findings.json` and `mutation-findings.json`, which
is three files sitting exactly parallel to the three documents that already
exist and are already carried through the shard, artifact, fold and publish
pipeline.

**Hanging findings off their row** has a real precedent: `Row.Log` is a field
the terminal deliberately never renders, added because a consumer cannot parse a
path back out of prose. But `ui.Row` and `ui.jsonRow` are converted by direct
struct conversion, so the rendering type would grow a field it never renders and
`sameRow` would need a merge rule for it. And the grouping buys nothing: a
finding has to carry its own gate and component regardless, or whatever anchors
it is parsing `gosec(cli)` back out of a label — which is the text-scraping this
whole channel exists to remove.

`ui.Document` accepts keys it does not know, and that tolerance was written for
exactly this kind of growth: an older binary rendering a newer document ignores
the key and renders the rows, which is the property a workflow pinning two
versions depends on.

## A fingerprint contains no line number

A line number is not an identity. An edit anywhere above a finding moves it
while changing nothing about the claim, so a line-keyed identity reports the same
finding as a new one on every push — and the consumer that anchors it posts a
duplicate beside the original.

```
v1 : sha256( gate │ component │ path │ site │ ordinal )[:16]
```

`site` is the gate's own identity ingredient, and every one of them is content:
a rule with the source text it fired on, a function's name with its receiver, an
operator with the text it replaced and the text replacing it, a stretch's two
ends and its length. It is derived centrally rather than by each gate, for the
reason `internal/pathmatch` holds one matcher and `internal/scheduler` holds one
port-conflict predicate — a second copy agrees until one of them learns
something, and the disagreement surfaces as a duplicate anchor rather than as a
failing test.

**`ordinal` is the disambiguator that is not a line number.** Two identical
comparisons on two lines produce two mutants alike in everything a fingerprint
reads. Without an ordinal they are one claim, reported once, and the second
survives unreported. It counts identical sites within one file in source order,
so it is stable under every edit except adding or removing an identical sibling.

**Message and severity are excluded.** A tool that rewords its own diagnostic,
or reclassifies it, has not found something different, and orphaning every
anchor on a tool upgrade is the failure this is built to avoid.

**Path is included**, so moving a file re-identifies everything in it. That is
the right way round: the alternative is two functions of one name in one
component sharing an identity, and a claim attached to the wrong code is worse
than one attached afresh. A rename has genuinely relocated the anchor anyway.

**`v1` is load-bearing.** Changing the formula orphans everything derived from
the old one, so the version makes such a change visible instead of silent: a
reader keeps understanding every version ever emitted while a writer emits only
the current one, which is what lets an old anchor be recognised as stale rather
than mistaken for a different finding.

## A gate emits findings exactly where it makes a claim

Not wherever it has data. The CRAP gate can name every function above the
threshold on a passing row too, and those are debt the change did not add;
patch coverage has untested new lines in a component that cleared its own
baseline. Emitting claims there would put one on every pull request about code
nobody touched, and a gate that fires on ordinary work is one that gets switched
off — the argument `coverage.floor` already wins by defaulting to `0` and
[ADR 0028](0028-crap-gates-the-delta-above-the-threshold.md) wins by gating the
delta.

So a gate emits findings when its row fails, and the row's `Detail` is rendered
**from** those findings. One derivation produces both the prose a human reads
and the data a consumer anchors. Two independent renderings of one answer would
be free to disagree, and the one nobody looks at is the one that would drift —
which is the argument [ADR 0023](0023-one-standing-comment-rendered-by-the-cli.md)
makes for the referral verdict reaching the comment through its document rather
than being rendered twice.

**Findings never vote.** The rows hold the verdict; a finding is descriptive. A
gate that already failed a row would otherwise be counted twice.

## Anchoring is decided where the finding is made

A finding carries how precisely it can be attached to the change: to a line, to
a file, or to neither. That is decided by the gate, from the changed-line map it
already holds, and never by whatever posts it — because the renderer of the
standing comment is pure, with no network and no git, and a question it cannot
ask has to be answered before it reads the document.

The set it is decided against is `coverage.ChangedLines`, which runs
`--unified=0` and so holds the change's added lines with no context around them.
That matters in one direction only: it is narrower than any diff a hosting
platform shows, so a finding lydite calls anchorable is one such a platform will
accept, and the error case is a claim that loses its precise anchor rather than
one a platform refuses.

**The CRAP gate produces unanchorable findings on purpose.** A function crosses
the threshold because a call site grew or its test was deleted — which is the
blind spot ADR 0028 says the gate exists to cover — and neither edit is in the
function. Those claims are real and cannot be attached to the diff, which is
what the third anchor state exists to say out loud.

## The costs, stated

A finding's `site` is read from the tree, so a gate that emits findings reads the
source files it is complaining about. That is new I/O on a failing path only,
cached per file.

**Those reads are confined rather than checked.** A path reaching `finding.Source`
was reported by a tool reading a repository lydite does not own, and the text it
names becomes a `site` that travels in the report document — so a path climbing
out of the tree would put a line of somebody else's file into lydite's own
output. `os.Root` refuses that at the syscall and leaves no window between
resolving a path and reading it, which a lexical check does; the same reason
`internal/mutation` opens a worker's root before copying into it rather than
after.

A stretch whose source cannot be read keeps its claim and loses only what tells
it from a neighbour, which the ordinal then supplies. The row has already gated
on it, and dropping the claim to protect its identity would hide a failure
lydite had already found.

`Site` and `Ordinal` travel in the document although the fingerprint is derived
from them and could replace both. A fingerprint nobody can check is a hash
somebody has to trust; carrying the ingredients is what lets a reader see why
two claims are the same one, or why they are not.
