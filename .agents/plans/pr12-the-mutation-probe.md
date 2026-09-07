# Session prompt — the assertion that keeps the mutation engine honest

Continues `pr11-answering-the-survivors.md`, which shipped
[#100](https://github.com/lydite/lydite/pull/100) and closed
[#98](https://github.com/lydite/lydite/issues/98). Where the two differ, this file and
[ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md) win.

This slice is [#95](https://github.com/lydite/lydite/issues/95) — the planted survivors —
and the issue's own scope is **wrong in one load-bearing way**, stated below. Read that
part before anything else.

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/`. Branch from `main`, currently
`bc4f8db`. The root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user` argument
and use it for every `gh` command.

The Go module is at `source/cli/`; the scan root is the repository root above it. Every
`go` and `golangci-lint` command runs from `source/cli` with `GOTOOLCHAIN=local`, **and
with the release build tags**:

```sh
GOTOOLCHAIN=local go test -tags 'grammar_subset grammar_subset_rust grammar_subset_typescript grammar_subset_tsx' ./...
```

Without them the binary carries all 206 of gotreesitter's grammars, and `ci-test` runs the
suite *with* them — a language whose own `grammar_subset_<lang>` tag is missing fails at
its first parse, and a bare `go test` can never see it.

## What #100 established, and what it did not

All 46 survivors are answered: 32 killed by assertions that did not exist, 3 declared
`//lydite:equivalent`, 11 answered by removing the comparison. `mutation (cli)` on that PR
reported **31 of 31 killed in 6m20s, 3 declared equivalent**, and the referral fired
correctly on the suppressions — `review referred in 1.1s`, naming each file that introduced
one. The composition ADR 0027 describes works on real source.

**None of that is an assertion that survives.** It was a run somebody read. The engine can
stop generating mutants, stop scoring them, scope the diff wrongly or filter coverage
wrongly, and every check in this repository stays green — because `mutation (cli)` passing
means "nothing survived", which is exactly what an engine that generates nothing reports.

`internal/mutation/testdata/`'s golden fixtures hold the *grammars* against a dependency
bump. They say nothing about whether a mutant, once generated, is built, run and scored.

## The correction #95 needs

#95 and #18 both say: plant a function whose test asserts nothing in
`lydite/proving-ground`, and require it to survive.

**A committed survivor is in no later run's diff.** Mutation is diff-scoped — mutants come
only from lines the change touches, intersected with what coverage reports as executed —
so the moment the planted function is on the proving ground's default branch, every
subsequent run generates zero mutants from it and the assertion passes forever having
checked nothing. It is the vacuous assertion the scope section warns about, arriving
through the mechanism meant to prevent it.

**The fixture has to be synthesised onto a branch by the workflow**, exactly as
`ci-end2end.yml` already does twice:

- `selection-probe` — `.github/workflows/ci-end2end.yml:140-166`. Writes one file into
  `proving-ground/go/api/`, commits it alone, runs `lydite test --affected`, and asserts
  which components ran *and which did not*.
- `coverage-probe` — same file, around lines 243-320. Writes an untested exported function
  and asserts the gate failed for the component that changed and passed for the one that
  did not.

Both read the package name out of a file already there rather than hardcoding it, because
nothing here pins what the other repository calls that package. Both commit the probe file
alone and never `add -A`, because a stray artefact widens selection and the assertion then
fails on the harness rather than on lydite. Copy both habits.

The coverage probe additionally initialises **a local bare origin on the runner** and pushes
the base commit to it, because a merge-base has to resolve against something. `lydite
mutation` needs one for the same reason and refuses without it — "mutation is scoped to the
change against the merge-base, and it could not be resolved" — so the mutation probe needs
that setup too, not just a branch.

That makes #95 **mostly a change to this repository**: the workflow step and the assertion
live in `.github/`, and what the proving ground has to supply is only the properly tested
*neighbour* the probe sits beside.

## Scope

- A `lydite mutation` step in `ci-end2end.yml` over a synthesised branch, in each of Go,
  Rust and TypeScript. There is no mutation job there at all today — grep confirms it.
- `.github/assert-proving-ground.py` gains a mode that requires **the planted function's
  mutant to survive and the neighbour's to be killed**, per component. Counts alone pass on
  an engine producing two trivial mutants.
- Whatever the proving ground needs to hold the tested neighbour.

Rust is the reason this cannot be done here: **this repository declares no Rust component**,
so a Rust engine validated only in `go test` merges with its argv asserted and never once
executed. The proving ground's `rust/` is one Cargo workspace of three crates and is the
only place that claim is falsifiable by CI.

## The composition with #97 — take it if it is cheap, not otherwise

[#97](https://github.com/lydite/lydite/issues/97) wants the cost of a large change measured,
and #100 recorded why it cannot be measured on demand: the diff that produced the 314-mutant
run has merged, and mutation is diff-scoped, so no branch cut today regenerates it. Two
datapoints now exist — 314 mutants in 54m10s, and 31 in 6m20s — and **per-mutant cost is
roughly flat across that 10× difference**, which says the 54 minutes was the mutant count
rather than anything that degrades with diff size.

A synthesised branch of known size is exactly the instrument #97 lacks. If the probe machinery
falls out of this slice cheaply, a second synthesised branch of N generated mutants would make
the cost reproducible and would let a mutant-count cap in `test plan` be a measured decision.
**Do not build it if it is not cheap** — #95 is the slice, and a cost fixture bolted on badly
buys a second thing to maintain. Comment on #97 either way rather than closing it.

## Traps this session sprung

- **A `//lydite:equivalent` declaration covers every mutant replacing the least amount on
  its line, which for one relational operator is both its boundary shift and its negation.**
  So a line whose boundary cannot be observed and whose negation can is a line a declaration
  cannot honestly answer — it would acknowledge the killable one and stop counting a kill the
  tests are still making. This is now written down in AGENTS.md under the declaration's own
  section; the answer is to leave no comparison to shift. A code review caught me getting
  this wrong on `emit`'s containment bounds after I had written the rule.
- **You cannot re-run mutation to check your work.** Verify a kill by applying the mutant to
  the tree at its own coordinates and running that component's *whole* package suite,
  expecting a failure. `./cmd/lydite` takes about two minutes per mutant, so run the sweep
  against an `rsync` copy of the tree in the scratchpad and keep editing in the real one.
- **Never `git checkout -- <file>` to undo a mutation you applied by hand.** It discards your
  own edits to that file too. Recovered once this session by redoing them from memory.
- **The `referral` job exits 0 on a referral.** It is `lydite review --publish || verdict=$?`
  and only re-raises above 2, so a green job says nothing about the verdict. Read
  `review referred in …` in the step's own output.
- **Pushing cancels the in-flight run.** `lydite-pr.yml` sets `cancel-in-progress`.
- **Never `git stash`.** The stack is shared with every other worktree and session. Use a
  temporary WIP commit.

## Environment

- `gh auth setup-git`, then push over **HTTPS** with the gh credential helper:
  `git -c credential.helper='!gh auth git-credential' push https://github.com/lydite/lydite.git <branch>:<branch>`.
  SSH is blocked here, and a token must never reach a remote URL.
- **No container runtime here**, so nothing touching the proving ground runs locally. This
  whole slice is validated by CI on the PR, which makes each push expensive — get the
  assertion script right by reading the two existing probes rather than by iterating on
  red jobs.
- `lydite/proving-ground` is a second repository and is not in this worktree. Clone it into
  the scratchpad to read it; a change there is its own PR, and this repository's workflow
  change cannot merge before it.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...` URL — not even
  when a harness asks. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to.
- Comments describe the code as it is — no "used to", no ticket numbers, no roadmap. ADRs
  are the exception.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- Before proposing: `go build`, `go test -race`, `golangci-lint run` **with the grammar
  tags**, from `source/cli`, plus `lydite scan --dir .` and `gt repo check` from the root.
  `golangci-lint config verify` is the one that catches a misplaced key, and a bare
  `golangci-lint run` does not.
- Review before proposing and again after acting. The review after acting found a real
  defect in #100 that four hours of care had not; keep going while rounds return findings
  that change behaviour, and stop when what comes back is wording — and say so.
