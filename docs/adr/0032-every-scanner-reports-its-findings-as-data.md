# Every scanner reports its findings as data, through a side channel

[ADR 0030](0030-findings-are-data-in-the-report-document.md) made a finding data,
and [ADR 0031](0031-a-located-finding-is-a-review-thread.md) made a located one a
review thread. Of the seven checks `lydite scan` runs, exactly one — Biome —
turned its tool's report into findings. The rest streamed their output to the
terminal and reported a row, and nothing downstream could anchor to what they
found.

So the standing comment carried the TypeScript findings and none of the others,
and the review carried nothing at all: `scan` never called `finding.Anchored`,
so every claim it made — biome's included — kept the zero anchor and went to
the comment. A repository whose gosec, Semgrep, clippy, cargo-audit and
cargo-deny rows were all red got a comment listing row labels and a review with
nothing on any line, which reads as lydite having found nothing where it found
plenty.

**Every scanner now reports its findings as data, and `scan` anchors them
against the change. Each tool keeps rendering to the terminal exactly as it
did; lydite reads a structured copy purely to populate `Result.Findings`, and a
check whose tool cannot do both at once runs twice. A dependency advisory is
located at the manifest or lockfile line naming the package, and identified by
the advisory's own id rather than by the text of that line.**

## A side channel, not a takeover

[#111](https://github.com/lydite/lydite/issues/111) framed this as lydite
rendering the findings rather than the tool: parse the report, print it
ourselves, and the data falls out. That is the larger change and it is the wrong
one.

A scanner's output is its findings, and the tools are good at printing them.
clippy underlines the span and offers the rewrite; gosec quotes three lines with
the offending one marked; Semgrep prints the matched source under the rule that
matched it. Re-rendering all of that means reimplementing five tools' output and
being judged against the original every time a developer has seen the real thing
in another context. It also means the terminal output changes in the same commit
as the data channel, so a regression in either is attributed to both.

So `Result.Detail` stays **empty** for these tools, which is what its own
documentation already says — *"empty for every tool that prints its own
findings"*. What a reader needs beyond the one-line claim travels per finding, in
`Finding.Detail`: gosec's source excerpt, clippy's rendered diagnostic,
govulncheck's call path, cargo-deny's dependency graph. That is the text a
surface can show beside a claim without reprinting a stream that already reached
the terminal.

Biome remains the exception it already was. Its report goes to a file so its own
chatter cannot corrupt the JSON, which means nothing streams and `Detail` is the
only place its findings exist.

## Three tools run twice, for two different reasons

Only Semgrep and gosec can write a machine-readable report *and* print for a
human in one invocation — `--json-output=<file>` and `-fmt json -out <file>
-stdout -verbose text` respectively. clippy needs no second format at all: every
diagnostic carries `rendered`, the exact text cargo prints.

The other three run twice, and the two reasons are worth separating.

For clippy, cargo-audit and cargo-deny the second pass buys back the **terminal
output**: `--message-format json`, `--json` and `--format json` each replace the
human stream rather than copying it. The cost is small and measured — clippy's
second pass is 0.24s against a first pass of 1.76s, because cargo replays cached
diagnostics rather than recompiling; cargo-audit's is 1.8s against 3.1s.

For govulncheck the second pass buys back the **verdict**, which is a stronger
requirement. It has no output-file flag, and under `-format json` it exits 0
whether or not it found anything while the text run exits 3. A single JSON run
would report every advisory and pass the check that exists to block on them. So
the text pass decides the row and the JSON pass only populates `Findings`. The
second pass costs 4.6s on top of 6.2s, the package-load work being already done.

**The text pass is always the one that decides the row.** lydite adds no verdict
of its own from a parsed report, with two exceptions where the tool's own status
under-reports: gosec states a package that did not compile in the report rather
than in its exit status, and Semgrep exits zero for a run whose rules would not
load. Both are the failure `reportableBiome`'s `parse` and `internalError/io`
categories exist to catch — a scan that read nothing must not render as a clean
pass — and both carry their reason in `Result.Detail`, because that is the only
place a verdict lydite invented can explain itself.

## A dependency advisory is located at the manifest line

cargo-audit, cargo-deny and govulncheck do not make claims about a line someone
wrote. They make claims about a package a project depends on, and the one edit
that clears such a claim is the bump. Editing the call site clears nothing, so
the call site is not where the claim goes: it is located at the `Cargo.lock`
stanza or the `go.mod` require naming that package, and the call trace becomes
`Finding.Detail`.

This follows from what a finding is — *a located claim that one edit clears* —
and it makes the anchor behave correctly at both ends. A change that bumps a
dependency into an advisory touched the manifest, so the claim lands on the line
it touched and becomes a thread there. A change that touched no manifest finds
it in the standing comment as pre-existing debt, which is what it is.

A standard-library advisory is located at the `go` directive, for the same
reason: a toolchain bump is the edit, and that is the line it is written on.

**govulncheck's repeats collapse per advisory *and* module, not per advisory.**
It emits several messages for one advisory at increasing trace depth — 17
messages for 11 advisories in the captured probe — and those are one claim. But
one advisory record routinely names two modules: the standard library and the
`golang.org/x/...` module the same code is vendored from, which is 18 of the
advisories in the fixture's own database. When both are in the build they are
two claims, cleared by two different bumps, on two different manifest lines, at
two different fixed versions. The pair is what `site` already identifies a claim
by, so keying the collapse on anything less drops a claim the fingerprint was
keeping.

**When the line cannot be found, `Line` is 0 and the claim is unanchorable.**
That happens for a transitively-resolved module no manifest names and for a
lockfile lydite could not read. It is never guessed at: since ADR 0031 a guessed
line is a review thread on unrelated code, on a pull request whose author cannot
act on it, and the standing comment is a correct home for a claim that reaches
no line.

### Its identity is the advisory, not the line it sits on

This is a stated departure from [Findings](../../.agents/references/findings.md),
where a scanner's `site` is the rule with the source text it fired on.

A lockfile line reads `name = "time"`. Two advisories against one crate would
share that text, and a site built from it would make them one claim — reported
once, with the second silently dropped by `finding.Set`. The ordinal cannot
save it either: that separates claims by source order within a file, and these
two are genuinely different claims rather than a repeat.

So `site` is the advisory's own identifier with the package and version:
`RUSTSEC-2020-0071␟time 0.1.44`. It is the one place a scanner's identity is not
read from the source it fired on, and it is stable across exactly what should
not re-identify it — a lockfile reordering, a line moving, the advisory being
reworded.

## clippy's double emission is collapsed before numbering

`cargo clippy --all-targets` compiles a crate as a library and again as its own
test harness, and reports every lint in the shared source under both. The two
messages are identical in every field the report carries: target name, target
kind, span, rule, level and rendered text alike.

Nothing downstream can collapse them. `finding.Number` gives two claims alike in
path and site the ordinals 0 and 1 — which is precisely how it keeps two real
occurrences of one rule on one line apart — so both copies hash differently and
both survive `finding.Set`. This is not a gap in ADR 0030's dedup; it is that
dedup working as specified on input that is wrong before it arrives.

So the collapse is in the clippy parser, keyed on file, span and rule, and it
happens **before** `finding.Number` so the ordinals are counted over the real
claims. Left undone, lydite reports double the count and puts two review threads
on one line.

## A scan anchors what it found

Emitting findings is half of it. `finding.Anchored` raises a claim's anchor to
what the changed lines allow, and `scan` was the one producer never calling it —
so `threads.Located`, which takes `AnchorLine` and `AnchorFile`, took nothing
from a scan and the review surface stayed empty however much was found.

`scan` now asks `coverage.ChangedLines` once and anchors in `record`, which is
the single place a check's claims enter the report. It anchors **after**
`labelled` has rebased each path onto the scan root, because the map is keyed
from that root and a claim still named from inside its component would match
nothing in it.

**The base is now resolved whenever `--diff-base` is given**, including for a
run carrying a `SEMGREP_APP_TOKEN` and a run with Semgrep switched off. It used
to be skipped in both cases, and the reason was sound while Semgrep was its only
reader: `semgrep ci` scopes itself, so resolving a merge-base cost a `git fetch`
nothing read. The anchor is a second reader, and a token says nothing about
where gosec's claims belong. The cost is real and is the point — a token-bearing
consumer passing `--diff-base auto` now needs the full-history checkout it
previously did not, and what that buys is a diff-scoped scan whose claims
actually reach the diff.

A scan with no `--diff-base` anchors nothing, which is the honest answer rather
than an omission: it reaches no change at all. That is the shape
`lydite-baseline.yml` runs on `main`.

## Consequences

- Five more gates can reach a line, and a scan can reach one at all, so the
  review surface carries what a security scan found rather than nothing.
- Three checks run their tool twice. The added wall-clock is measured and small,
  and it is paid per component rather than per finding.
- Six report shapes are now lydite's to track. Each is parsed from a captured
  real report rather than a hand-written one, and each parser keeps
  `reportableBiome`'s stance: an unrecognised category, level or code is
  reported rather than dropped, and a report that will not parse falls back to
  the tool's own exit status. A parser that silently drops what it does not
  recognise is how a gate stops gating.
- `cargo fmt` gets no parser. lydite is not a formatter and must never report a
  formatting diff as a finding. That its row still fails a Rust component
  contradicts that and is a separate open question, not settled here.
- A finding count per component, which is what #111 asks for, is now derivable:
  the claims are in `scan.json` under a top-level `findings` key. Carrying them
  into the ledger is its own slice.
