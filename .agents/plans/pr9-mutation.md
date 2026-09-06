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

## Settled, before any planning

The contradiction this prompt was written to flag is resolved, in
[ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md). Read it first;
it is the decision record for everything below and states what was rejected.

- **lydite owns the engine in all three languages.** ADR 0016's ownership
  section stands. #18's scope is corrected to drop `cargo-mutants` and Stryker.
  The delegation argument is real and is recorded as rejected rather than
  unaddressed: its marginal infrastructure is near zero, but it costs one
  operator taxonomy, one acknowledgement model and one definition of a survivor.
- **The slice ships all three languages** and closes #18 and #19.
- **`lydite mutation` is a top-level command**, not a flag on `lydite test`. It
  runs as its own CI matrix beside the test matrix. ADR 0016 put mutation in the
  test job because those checks "share a compilation"; mutation does not —
  coverage builds the instrumented variant once, mutation builds the plain one
  per mutant. It reuses `lydite test plan`'s matrix and shares one fold
  implementation with `lydite test merge`.
- **Diff-scoped always**, over lines coverage reports as executed. No whole-repo
  mode. Nothing to mutate is `unmeasured`, never a pass.
- **No baseline.** Nothing in `measurements.json`, nothing on the `lydite`
  branch, `lydite test record` untouched.
- **Survivor fails (`✗`, exit 1); a hang counts as killed; an unviable mutant is
  excluded from the denominator and reported separately; a failing baseline
  suite is `unmeasured`.**
- **`//lydite:equivalent <reason>` at the site**, reason required and its absence
  an error. It is a suppression, so it clears the gate and refers the change. One
  token in `suppressionTokens`; no file- or function-level form.
- **#19's five operators, fixed and not configurable.** The accepted cost is the
  equivalent-mutant rate of removed statements and arithmetic operators, which is
  paid by authors rather than by machines. Measure it on the proving ground.
- **Mutants run concurrently, except for a component declaring compose services**,
  which runs them serially — its suites would share a database. Derived from
  `scheduler.Conflicts`, third caller.
- **No runtime budget.** Rows carry mutant count and elapsed time so a budget can
  later be a measured decision. An oversized run dies as a job timeout and the
  fold catches the missing row.
- **The proving ground gains a planted survivor in each language** — a function
  whose test asserts nothing, beside a properly tested neighbour — and the assert
  script requires the first to survive and the second to be killed. This is a
  second cross-repository change and it is the only place the three-language
  claim is falsifiable.

Still open and to be benchmarked: in-process versus rebuild per mutant (#19).
`-overlay` shortens the odds for rebuilding.

## Read before planning

- [ADR 0027](../../docs/adr/0027-mutation-is-its-own-command.md) first, then
  [ADR 0016](../../docs/adr/0016-components-and-lydite-run-tests.md) — its
  ownership section, which stands, and its job-placement section, which 0027
  amends.
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
