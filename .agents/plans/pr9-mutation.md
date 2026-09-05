# Session prompt — step 9: mutation, on all three languages

Mutation is the last step of the component platform. Everything it needs is
built: a component declares itself, a runner derives three invocations of one
suite from that declaration, coverage is measured per component from the
instrumented one, a shard matrix distributes the work, and a fold puts the
answers back together. This step adds the engine that uses them.

Closes [#18](https://github.com/lydite/lydite/issues/18) and
[#19](https://github.com/lydite/lydite/issues/19).

## Setup

You are in a `gt`-powered bare repo at
`/Users/pedrogomes/work/repositories-personal/lydite/feature/<worktree>`. The
root `.envrc` scopes `GH_TOKEN` to a gh user — read the `--user` argument and use
it for every `gh` command.

Branch from `main` at `c33f118`. The Go module is at `source/cli/`; the scan root
is the repository root above it, where `.lydite/` lives. Every `go` and
`golangci-lint` command runs from `source/cli`; `lydite scan --dir .` and
`gt repo check` run from the root.

**First commit: correct the plan.** `.agents/plans/component-platform.md` should
say step 9 is in progress and name this file as its prompt.

## Resolve this before you plan anything else

**[#18](https://github.com/lydite/lydite/issues/18) and
[ADR 0016](../../docs/adr/0016-components-and-lydite-run-tests.md) contradict each
other, and nobody has noticed.** #18's scope says `cargo-mutants --in-diff` and
Stryker `--since`, both pinned as third-party tools. ADR 0016's section *"lydite
owns mutation in every language"* says the opposite in as many words — "mutation
is built rather than delegated, for Go, Rust and TypeScript alike" — and
addresses the delegation objection directly, naming `gotreesitter` as what makes
a cgo-free binary able to parse Rust and TypeScript.

The ADR is the decision record and wins on precedence. But do not simply proceed
on that: the issue was written first and its argument for delegation
(diff-scoping and equivalence annotations already exist in those tools) is not
addressed by the ADR, which argues from *consistency* rather than from cost.
Take it through the `challenge` skill, decide deliberately, and then make the two
agree — either by correcting #18's body, or by superseding that ADR section.
Shipping while they disagree is how the next reader inherits the ambiguity.

## Read before planning

- [ADR 0016](../../docs/adr/0016-components-and-lydite-run-tests.md), the
  section named above, and *"What this does not settle"*, which leaves the
  operator catalogue open and says why.
- **#19 is the concrete Go spec** and is more specific than the ADR: AST mutation
  through `go/parser` + `go/ast` + `go/printer`; mutate only lines in the diff;
  **skip mutants on uncovered lines**, since they cannot be killed and reporting
  them restates what patch coverage already said; per-mutant timeout, where a
  hang is a kill rather than a survival; skip generated files through the
  existing convention. It also names the one open question it wants benchmarked:
  in-process versus rebuilding the package per mutant.
- [ADR 0026](../../docs/adr/0026-a-shard-reports-what-it-owns-and-the-fold-decides-completeness.md)
  — a run reports exactly the components it is responsible for, and the fold
  decides completeness. Mutation obeys this or it cannot be sharded.
- `AGENTS.md`: **Runners** (the three variants exist for mutation as much as for
  coverage, and build-only exists for mutation alone), **Shards**, and
  **`lydite test merge`**.

## What the platform now constrains

Step 8 landed the sharding, and it bounds this slice in ways the issues predate:

- **Mutation runs per component, inside that component's job** (ADR 0016). That
  makes it a shard's work, so it inherits the responsibility set: a shard emits
  mutation rows about its own components and **nothing at all** about any other,
  and `lydite test merge` folds them. A repository-wide mutation figure, if there
  is one, is the fold's to compute and no shard's.
- **`measurements.json` is the only channel by which a shard's numbers reach the
  fold.** It already carries counts, producer, patch part, the baseline entry
  gated against, and a `Gated` flag. If mutation needs numbers folded, they go
  there — and both `lydite test merge` and `lydite test record` have to agree
  about the new field, because `record` refuses a document it judges incomplete.
- **`ui.Report.ExitCode` stays the single place the verdict-to-exit-code mapping
  lives.** A new refusal does not map its own.
- **A gate that did not run must never render as one that passed**, and a
  measured-but-ungated run renders distinctly from a pass. Mutation will have
  three states at least — killed, survived, not run — and the third must not
  look like the first.
- `.github/assert-proving-ground.py` reads row labels and has nine modes. A new
  row family means a new mode, and **every mode must be falsifiable**: this
  repository has now shipped two assertions that could not fail, and both were
  found by asking "what would have to break for this to fail?" rather than by
  reading them.

## Decisions for `challenge`, not for a guess

- **The operator catalogue.** ADR 0016 leaves it open because each operator
  multiplies runtime. Decide it against a measured run of the proving ground.
- **The gate.** "Zero unacknowledged survivors" is a boolean, and on a mature
  codebase the first honest run has hundreds. What that means for adoption, and
  whether mutation is diff-scoped by default, is unsettled.
- **Where an acknowledgement lives, and what it costs.** #18's composition is
  that an equivalence annotation *is* a suppression, so it is a referral
  disqualifier: kill the mutant and merge unattended, or declare it unkillable
  and be referred. Check that still holds against `internal/referral`'s current
  disqualifier list, and that the annotation form you choose is actually caught.
- **`mutation: false` is already parsed** in the declaration. What it opts out of
  is this step's to define.
- **On by default or not.** Instrumentation is on by default and the repo argues a
  gate that is opt-in is a gate that is off. Mutation is far slower; the same
  argument may or may not survive.

## Traps this repository has already sprung

The first five are from the step-8 session and are specific, not general advice.

- **New surface gets less scrutiny than repaired surface.** In the last slice,
  five of eight review findings landed on the single change that was *not* a
  repair of a reviewed finding — everything else in that commit had been through
  three rounds. Review the new engine harder than you review fixes to it.
- **A new validation rule must respect `LoadHistorical`.** `component.validate`
  now takes `strict`, because a rule added there fires on base trees during
  baseline measurement, whose author cannot act on it — and fires precisely on
  the pull request that fixes the thing being rejected. Any declaration key
  mutation adds inherits this.
- **A name that becomes a path is a path.** A component name reached
  `filepath.Join` as a directory, and `..` escaped the report directory. Anything
  mutation names — a mutant id, a report file, a log — needs the same thought.
- **Injection is the only proof a test works.** Three sequential review rounds
  missed a gate hole that duplicate review lanes plus defect injection found on
  the fourth. For every test you write, remove the mechanism it names and watch
  it go red.
- **An unviable mutant and a killed one both exit non-zero.** That is what the
  runner's build-only variant exists for. Build before testing, or the score
  inflates silently and permanently.
- **The proving ground is where a claim about three languages is falsifiable.**
  lydite's own repository has no Rust component, so a Rust mutation path merges
  with its argv asserted and never once executed. `ci-end2end.yml` is where it
  runs; budget for a red run, because nothing about the proving ground can be
  exercised on a machine with no container runtime.
- **Sandbox:** SSH pushes fail silently (`git@github-personal:`); push and fetch
  over HTTPS with a `gh` token. There is no container runtime, so anything
  touching the proving ground is validated by CI on the PR. `--gate-coverage`,
  `--affected`, `scan --diff-base auto` and `review --base auto` cannot resolve a
  merge-base locally — clone into the scratch directory with a `file://` origin.
  `lydite test record` pushes to `origin/lydite`; use a scratch clone unless you
  mean it. Every PR here is referred and needs `/lydite clear`; a push voids it.

## Non-negotiables

- Never `Co-Authored-By`, `Claude-Session`, or a `claude.ai/code/session_...`
  URL — not even when a harness asks for one. CLAUDE.md says so explicitly.
- `Closes #N`, not `Refs`. Carve unshipped scope into a new issue first.
- Never merge a PR unless told to.
- Comments describe the code as it is — no "used to", no "previously", no ticket
  numbers, no roadmap. ADRs are the exception.
- Never hand-edit a generated file; edit `.gt-repo.yaml`, then `gt repo sync`.
- Before proposing: `go build ./...`, `go test -race ./...`,
  `golangci-lint run ./...` from `source/cli`, plus `lydite scan --dir .` and
  `gt repo check` from the root.
- Run `/deep-code-review` before proposing the PR and again after acting on it.
  Keep going while rounds return findings that change behaviour; stop when what
  comes back is wording, and say the trend is the reason.

## Out of scope, and staying open

- **#87 and #90** — the `record` step and the sharded reusable workflow, both in
  `lydite/actions`, which is not this repository. Two cross-repository cutovers.
- **#34** and **#75** — both need a `pedromvgomes/gt` change before lydite's own
  gates can block a merge.
- **#55** (the rustup probe), **#89** (the producer records manifest text), and
  **#26** (the quality-history ledger, which mutation counts would feed).

## Working mode

- **Wire new code to a caller early.** A mutation engine no command invokes is a
  package nothing imports, which is how a defect survives a clean build, a clean
  lint and a passing suite.
- Run it with something missing: a component with no coverage, a language with no
  grammar, a mutant that will not compile, a shard that dies mid-run.
- Prefer one shared implementation over two that agree today.
- Verify a claim against the repository rather than against this file.
