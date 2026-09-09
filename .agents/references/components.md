# Components: `.lydite/components.yml` and `lydite test`

> **The reference for `.lydite/components.yml`, `internal/component`, `internal/runner` and `internal/nodedeps`** — what a repository declares, and how a suite is run and captured.

A repository declares its **components**, and lydite builds, tests, measures and gates each one.
`internal/component` parses the declaration and `internal/runner` turns a runner name into
invocations; `lydite test` runs the suites. See
[ADR 0016](../../docs/adr/0016-components-and-lydite-run-tests.md).

```yaml
components:
  - name: cli                      # unique; names the matrix job and every report row
    dir: cli                       # component root, relative to the scan root
    runner: go-test                # implies the language
    args: ["-race", "./..."]
    watch: ["Makefile", "VERSION"] # paths outside dir that invalidate this component
    depends_on: [sdk]              # declared, because the edge is not always derivable
    env:
      FOO: bar
    compose:
      file: ./docker/compose.yaml  # relative to the component root
      up: [db]                     # default: every service in the file
      wait: healthy                # healthy | started | none
    setup: ["make migrate"]
    teardown: ["rm -rf ./data"]
    mutation: false                # opt-out; the default is true
```

**A component is the unit its build tool treats as a whole** — a Cargo workspace, a Go module, a
JavaScript workspace — not a deployable. Nothing enforces it, because it cannot be read off a
manifest; it is stated in the package doc because it is the rule most likely to be got wrong.
Eleven crates behind one `cargo --workspace` invocation are one component: declared as three, the
workspace compiles three times and provisions three copies of everything the suite needs.

**`lang` is derived from `runner`, never declared.** `cargo-nextest` can only be Rust, and a
second statement of the language could only disagree with the first.

**A component's name may hold only letters, digits, `.`, `_` and `-`.** It is not merely a label: it
is a `--component` value inside a comma-separated list, the name of a CI matrix job, and the suffix of
that job's artifact, so it has to survive all three round trips and the ones it cannot survive fail
late. A component called `a,b` declared beside `a` and `b` makes those two run twice and itself never
run, surfacing only at the fold as duplicated rows; one holding a `/` or a `:` is refused by the
artifact upload, failing a job the composite action promises never to fail. Refused at parse time
instead, where the author is the person who can act on it, and against a set narrower than any of the
three accepts — a floor stated once is worth more than three rules that agree until one changes. A
name is a path segment too, so `.` and `..` are refused: a component's log is written to a directory
named after it, and those two resolve outside the report directory entirely.

**The charset is the one check `LoadHistorical` does not apply.** Every other rule there says whether
a declaration can run at all, which a base tree must satisfy to be measured; this one says what a
*later* run can carry through a flag, a job name and an artifact name. Holding a historical tree to
it would fail the one change that fixes it — the base tree of the pull request renaming an offending
component still carries the old name — which is the migration hazard `LoadHistorical` exists to
refuse.

**Unknown keys are rejected**, the same stance `referral.Parse` and `config.validateLinter` take. A
dropped key means a component configured differently from what its author wrote — a suite running
without the environment it declared — while every run still reports a result.

`Load` also validates against the tree: unique names, a `dir` that exists and does not escape the
scan root, `depends_on` that resolves to declared components, and no cycles. A dangling edge is
rejected rather than dropped, because the edge exists to make a dependent run on a change to its
dependency and an edge naming nothing silently stops doing that while the dependent keeps passing.

## Runners: three invocations of one suite

`internal/runner` maps a runner name onto three variants of the same suite, because lydite needs
all three and they must not disagree about which tests they run:

| variant | why it exists |
|---|---|
| plain | the fast path — mutation runs the suite once per mutant |
| instrumented | the coverage gate, and mutation's baseline |
| build-only | tells an *unviable* mutant from a *killed* one; both exit non-zero |

Deriving them from one declaration is the point. **Instrumentation is not a flag that can be
spliced into an arbitrary command**: `go-test` appends `-coverprofile`, while `cargo-nextest`
replaces the runner outright with `cargo llvm-cov`. That is why a component either names a runner
or supplies a raw `command:`, which opts out of the derived variants entirely.

- `go-test` — plain `go test`; instrumented runs through the pinned **gotestsum**, which writes
  the JUnit report, and adds `-coverprofile` **and `-coverpkg=./...`** to the `go test` beyond
  the `--`; build-only is `go build`. Without `-coverpkg` Go instruments only the package under test, so code
  exercised solely through another package's tests reads as uncovered and a pull request whose new
  code is fully exercised from its caller fails the patch gate on correct work
  ([#36](https://github.com/lydite/lydite/issues/36)).
- `cargo-nextest` — instrumented is `cargo llvm-cov nextest --lcov`. Build-only is `cargo build
  --all-targets`, because `cargo build` alone never compiles the test targets and a test-only
  compilation error is exactly what separates an unviable mutant from a killed one.
- `cargo-llvm-cov-nextest` — the same, with the plain variant already instrumented, for a
  repository that has decided to pay for instrumentation once.
- `vitest` and `jest` — instrumented adds `--coverage` **and names the reporter and the report
  directory**: neither runner emits lcov by default, so a component whose own config says nothing
  would pay for the instrumentation and produce no report either gate can read. Build-only is `tsc
  --noEmit`, since a JavaScript test run has no compile step and a syntactically broken mutant
  would read as a test failure.

**Coverage is written under `.lydite-reports/coverage/`, never at `.lydite-reports/` itself**, and
vitest is additionally told `--coverage.clean=false`. Vitest empties its reports directory before a
run, and for a component rooted at the scan root that directory is where every component's log
lives — including the logs of components running concurrently beside it, whose failing rows then
name a file that no longer exists. Reproduced against vitest 3.2.7. The subdirectory alone fixes it
for every component name but `coverage`; the flag fixes it for all of them.

**A `go-test` component's `dir` must be its module root.** `go list -m` run inside a module answers
with the *enclosing* module, so a component declared at `services/api` in a repository with one
`go.mod` at its root would take the root module's path — and the profile's package-qualified names
would then be stripped of it and re-prefixed with `services/api`, keying every entry
`services/api/services/api/x.go`. Nothing downstream notices: the patch gate finds no overlap and
emits no row at all, indistinguishable from a comments-only diff, and the generated-file check stats
a path that does not exist. `internal/coverage` compares the module's own directory against the
component's and reports the component unmeasured with what is wrong, rather than measuring nonsense.

**A component's report path is cleared before its suite starts** — the directory created, anything
already at the path removed. A suite that passes without writing a report would otherwise be
measured from the previous run's file, which is a coverage number describing code that is no longer
there, supplied by lydite itself.

**One artefact per language, and both gates read it.** Go's profile, Rust's lcov, TypeScript's
lcov. Rust exports the lcov alone: an lcov's summed `LF`/`LH` records give the same covered and
total counts cargo-llvm-cov's `--json` totals carry — verified at 30 of 57 both ways against the
proving ground's three-crate workspace — while the per-line hits the patch gate reads are not
derivable from the JSON, which has no line data at all. Only one of the two is load-bearing, and
asking for both is what produced an invocation naming two exports with one `--output-path`, which
cargo-llvm-cov refuses at argument parsing before anything executes.

The counts come from `LF`/`LH` and never from tallying the `DA` lines. On that same workspace the
two disagree — 57 lines by `LF`, 55 by a `DA` tally — because a line carrying more than one record
is one line to `LF` and two to a tally. A `DA` tally would report a denominator smaller than the
tool's own, against a baseline the same tool recorded.

**A JavaScript component must declare its own coverage provider.** `vitest --coverage`
needs `@vitest/coverage-v8` (or `-istanbul`) in the workspace's own dependencies, and
lydite does not add it: installing a dependency into the repository it is about to
gate would have lydite change what the repository resolves to. A workspace missing it
fails with `Cannot find dependency '@vitest/coverage-v8'`, in the tail under the
component's row.

**A component's runner is pinned and installed, exactly like a scanner.** A test
runner left to whatever a machine happens to carry decides which tests run and what
a failure looks like, so a verdict would vary by runner — and unlike a stale scanner
an absent one is not a degradation but a component that cannot run at all
(`error: no such command: nextest`). `internal/cargotool` holds the version parsing
and the version-keyed install `internal/rust` and `internal/runner` use, so the
rule exists once — and **`cargo-llvm-cov` is now one of them**, which closes the gap
that poisoned a baseline: it was assumed on PATH and never provisioned, so a runner
without it measured nothing, and an empty baseline cached as real makes every later
change a cache hit that gates on nothing. It has no prebuilt path, and that is a
property of the release rather than a choice: cargo-llvm-cov publishes archives but
no checksum beside them, and a digest is read out of band or not at all.

**What a runner installs is read off the built command, never off the variant that
named it.** `cargo-llvm-cov-nextest`'s *plain* variant runs through cargo-llvm-cov,
so a rule keyed on the variant answers "not instrumented" for the one invocation that
needs the instrumentation, and that component fails with `no such command: llvm-cov`
on a machine that has never had it. The invocation stays `cargo nextest run` rather than an absolute
path into the cache, because that is what a reader can re-run from a failure detail;
`Invocation.Env` prepends the pinned binary's directory to PATH, so an older one
already on the machine cannot win.

**The prebuilt release is taken first, and `cargo install` is the fallback.** Building
cargo-nextest from source is around seven minutes; the published archive is three
seconds, and a cold cache end to end is eleven. That is not a speed-up but the
difference between a first run someone waits through and one they abandon. The
digest comes from the release's own `.sha256` and is checked before a byte reaches
a caller — lydite is about to put this binary on PATH and execute it — and the
install stages into a sibling directory and is renamed into place, so an interrupted
download cannot leave a directory the next run reads as complete. A platform nextest
publishes nothing for, a blocked download, or a digest that does not match all fall
back to the source build, saying so on stderr rather than silently making a first run
seven minutes long. Linux takes the **musl** target: a musl-linked static binary runs
on a glibc distribution as well as on Alpine, and the reverse is not true.

`internal/download` holds the fetching, verifying and unpacking that
`internal/toolchain` already had, because the parts that must not vary between the two
callers are the security-relevant ones — a second copy of a path-traversal guard is a
second chance to get it wrong, and only one of the copies would be found. Its
`ExtractTarGz` takes the number of leading components to strip: a toolchain tarball
wraps everything in one directory, and a single-binary release tarball has no wrapper
at all, where stripping would discard the only entry.

Caching `~/.cache/lydite` still matters, and lydite's own `proving ground` job does it,
keyed on the pin manifests so a Dependabot bump misses and installs what it just
pinned.

**A JavaScript component is installed before it is run.** A fresh checkout has
no `node_modules` and every import fails before a single test is collected, so the
`vitest` and `jest` runners carry a `Prepare` step and the others carry none —
`go test` and `cargo` fetch what a build needs on the way past. The rule lives in
`internal/nodedeps` because it is a property of the tree rather than of one
command, and two copies would answer identically until one learned about a package
manager the other had not. The lockfile is the declaration (`package-lock.json` →
npm, `yarn.lock` → yarn, `pnpm-lock.yaml` → pnpm); every detected form is a
*frozen* install, since one that may rewrite the lockfile would have lydite change
what the repository resolves to and then gate the result. A root carrying two
lockfiles resolves to nothing rather than a guessed priority order, and
`typescript.install` is how such a repository says what it means. There is
deliberately **no key naming the package manager**: the lockfile already states it,
and a second statement could only drift.

An install that fails stops the suite, with a row saying so. A JavaScript suite run
without its dependencies fails at import, naming the tests rather than what is
actually missing — the same misattribution a suite run without its database
produces.

Nothing in `internal/runner` executes anything, and its tests assert argv — the same stance
`internal/rust` and `internal/typescript` take, for the same reason: a unit test that shells out to
a foreign toolchain tests the machine it runs on. **Rust therefore has no repository here to run
against** — the only `Cargo.toml` files are pin manifests, deliberately not components, and
`source/web/` is still empty. `source/cloud-services/` closes that half for TypeScript: it is a
real npm workspace under a `vitest` component, so Biome, `internal/nodedeps` and Node toolchain
resolution all run here. `ci-end2end.yml`'s `proving ground` job closes the rest,
running `lydite test` against every component of
[`lydite/proving-ground`](https://github.com/lydite/proving-ground) on a bare checkout — no
`node_modules`, no services started, nothing prepared by the workflow, because a step doing either
would hide the case a consumer actually hits.

**Every runner that can be made to write JUnit is made to**, on the instrumented variant. The
quality-history ledger records test counts, which no coverage report carries and which nothing
can recompute once a commit has been squashed away — and a path named without asking any runner
to write to it is a claim about a file that does not exist. `go-test` and `vitest` write to
`.lydite-reports/junit.xml`; cargo-nextest writes where the profile it runs under puts it,
`target/nextest/default/junit.xml`, because the path is a profile setting rather than a flag.
`go-test` runs through the pinned **gotestsum**; `vitest` names
`--reporter=default --reporter=junit` (junit alone *replaces* the reporter set, and the
component's log would then be empty, so a failing row would have nothing to show); and
`cargo-nextest` gets a `--tool-config-file`, which turns its junit profile on *below* the
repository's own configuration in priority — the opposite of the `--config-path` stance
`internal/typescript` takes with Biome, because a linter's rule set is lydite's verdict to fix
and a repository's test configuration is the repository's. `jest` bundles no JUnit reporter and
lydite will not install `jest-junit` into a workspace it is about to gate, for the reason it
installs no coverage provider — so a jest component contributes no counts and says so. The plain
variant asks for none of it: that is what mutation runs once per mutant.


## Output: captured, not streamed

**`.lydite-reports/` disowns itself**, by holding a `.gitignore` that ignores
everything under it — written when the directory is created, and never
overwriting one already there. lydite writes into the repository it is
measuring, and what it writes must never become part of what it measures: a
committed report directory lands in the diff, where it matches no component and
therefore widens affected selection to everything on every change. Observed on
a fixture whose `select` row named a coverage profile and a test log as the
reason a second component ran. Inside the directory rather than in the
repository's own `.gitignore`, because that file is the repository's to write
and lydite's output is lydite's to disown.

Every component's output goes to `.lydite-reports/<name>/test.log` under the scan
root — the suite, the install, the setup and teardown commands, and the container
lifecycle. A failing row carries the last 40 lines under it and names the log; a
passing row names the log in `--json` and says nothing in the terminal.

**This is the opposite of what the scanners do, and the difference is real.** A
scanner's findings *are* the result, so `executil.Run` streams them live. A test
suite's output is thousands of lines of passing tests, and a CI log carrying all
of it buries the one component that failed under the container lifecycle of the
ones that did not — which is exactly how a `no such command: nextest` ends up
fifty lines above the verdict that reports it. `executil.RunOutput` is the version
that writes to a caller-chosen writer, and the caller is the component's log.

The tail is under the row deliberately, duplicating what the log holds. A reader
looking at a red row should not have to scroll, and a reader who needs more than
40 lines has the path. `ui.Row.Log` carries that path into `--json` — it is not
rendered in the text grammar, since a failing row already names it where the
reader is looking and a passing one would put a path nobody wants on every line
of a clean run. A consumer cannot parse a path back out of prose, which is what
lets a PR comment link the output of the one component that failed.

`--stream` mirrors everything to stderr as well. It exists for the case a captured
file cannot serve: a suite that hangs prints nothing until it is killed. Stderr and
never stdout, because stdout carries the report and, under `--json`, a document a
suite's output would make unparseable.

Each mirrored line is prefixed with its component's name, padded so the separators
align, and whole lines are written under one process-wide lock — components run at
once, so an unlabelled line belongs to nobody and a prefix on half a line is worse
than none. The prefix is on the mirror only: the log stays a faithful copy of what
the suite printed, which is what a CI job uploads and a report links. A line that has not ended in a
newline is shown anyway after half a second, and flushed unconditionally on
close: a suite that prints `running 412 tests...` and then hangs has written no
newline, and watching a hang is the one thing `--stream` is for — so holding
that line until the process is killed withholds exactly the output somebody
turned the flag on to see.

