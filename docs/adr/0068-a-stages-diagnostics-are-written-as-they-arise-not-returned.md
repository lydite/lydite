# A stage writes its own diagnostics as they arise, not as a returned slice

The clearance pilot's stages that have something to say beyond their `Out` say it by returning
it: a field like `Warnings []string`, printed by the CLI once the flow has finished running. The
scan flow's stages — `warn-unscanned` naming source no component scans, `run-checks` naming a
component's declared environment before its checks run, `semgrep` naming a dropped
`.semgrepignore` default — depart from that shape on purpose: each writes to an injected
`io.Writer` at the moment the diagnostic arises, rather than collecting it into the stage's `Out`
for the CLI to print afterwards. This record states why, so a later migration does not read the
two shapes as interchangeable and pick whichever is closer to hand.

## The order a warning is read in has to match the order its check ran in

A scan's own warnings are not the only thing writing to the stream they share stdout or stderr
with — clippy, cargo-deny, gosec and every other check `lydite scan` runs stream their own output
live, as they run. A repository whose declared environment steers `GOVULNDB` for one component and
whose next component crashes needs to read, in order: the warning naming that steering, then that
component's own tool output, then the crash. Collecting every stage's warnings into its `Out` and
printing them after the flow has finished running would still produce that warning — but always
after every check's own streamed output, regardless of which component either one was actually
about. The reader loses the one thing an interleaved stream gives for free: that a warning about
component B appears near component B's own output, not bundled at the end behind component Z's.

Writing at the moment a warning arises, to the same writer a stage's own diagnostics and a
component's declared-environment warning both target, is what keeps that interleaving intact. A
stage like `run-checks` writes `warnDeclaredEnv`'s line immediately before it runs that
component's checks, in the same loop, so the two appear on the stream in the order they actually
happened — exactly as they did before this move, when the whole loop lived in one function inside
`cmd/lydite/scan.go`.

## The pilot's writer carries a tool's output; here it also carries lydite's own prose

`clearance`'s only writer input, `Progress`, exists to carry a tool's own streamed output through
to the caller — there is nothing lydite itself has to say mid-run that a returned `Warnings` slice
could not say just as well afterwards, because nothing else on that flow's path streams anything
of its own to interleave with. The scan flow's `Diagnostics` writer is not that: every scanner
already streams through `executil`, and `Diagnostics` is where a stage's *own* sentences — a
warning naming an unscanned language, a declared environment variable's name, a `.semgrepignore`
that silently replaced Semgrep's defaults — join that same stream, at the point lydite decided to
say them. A returned `Warnings []string` has no way to interleave with output a stage's own
`Out` does not carry at all; only a writer handed to the stage while it runs can put lydite's own
line next to the tool output it is about.

## Consequences

- A later flow's stage should return a diagnostic in its `Out` when nothing else it does streams
  anything of its own to interleave with — the pilot's own shape, which stays correct for a flow
  like `clearance`'s. A stage should write to an injected `Diagnostics` writer instead when, as
  here, ordering against a tool's own streamed output is part of what the diagnostic is for.
  Neither shape is the flow architecture's own default; each flow states which its stages need.
- No scan stage returns a warning in its `Out` for the CLI to print. A stage whose `Out` does carry
  a diagnostic-shaped field going forward is a stage that decided the pilot's shape fit it, not a
  stage that forgot to wire `Diagnostics` through.
- `WarnUnscanned`'s `Out` still carries the `[]orphan.Gap` its warning was built from, for a test to
  assert against — the warning already written to `Diagnostics` is its only production effect, and
  the `Out` field exists for the seam a test needs rather than for a caller to read twice.

See [ADR 0067](0067-scan-runs-as-eleven-single-responsibility-stages-walking-components-in-their-own-body.md)
for the stages this decision applies to, and
[`agentic/references/architecture.md`](../../agentic/references/architecture.md) for where a
stage's injected inputs sit among the four layers.
