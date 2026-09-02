# lydite — agent guide

Lydite is a Go CLI that unifies code-quality and security scanning — SAST, SCA, linting, and
coverage gates — for Rust, TypeScript, and Go projects. It is the single entry point a developer
runs locally and CI runs identically, so "green locally" and "green in CI" can never drift apart.
It replaces per-repo ad hoc security workflows (CodeQL, standalone cargo-audit jobs, Codecov as a
blocking gate) across `wardnet`, `wardnet-cloud`, and `inforge` with one consistent pipeline.

## Commands

```sh
go build ./...                 # build the binary
go test -race ./...            # run tests
golangci-lint run ./...        # lint — must be clean before a PR
go run ./cmd/lydite            # run the CLI locally
go run ./cmd/lydite review     # referral verdict for the current branch (exit 2 = refer)
go run ./cmd/lydite test --dir ..  # run each declared component's suite and measure its coverage
go run ./cmd/lydite test --dir .. --no-coverage   # the fast path: no instrumentation, no coverage rows

# The Go module is rooted at cli/, so every command above runs from there.

# Release build dry-run (produces dist/):
go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
```

## Layout

```
cli/                           # the Go module (module path lydite/lydite); every go command runs here
cli/cmd/lydite/                # the lydite CLI (scan, test, review, version, update)
cli/internal/ui/               # the output grammar every command renders through, plus --json
cli/internal/referral/         # exemptions, disqualifiers, the referral decision (see Referral below)
cli/internal/clearance/        # the comment surface and the clearance decision (see Clearance below)
cli/internal/forge/            # the hosting platform: commit statuses, permission, comments
internal/component/             # .lydite/components.yml: what a repo builds and tests (see Components below)
internal/orphan/                # source files under no component and no exclude (see The orphan gate)
internal/pathmatch/             # the anchored path-pattern syntax both declarations are written in
internal/runner/                # a runner name to its plain/instrumented/build-only invocations
internal/nodedeps/              # how a JavaScript workspace's dependencies get installed
internal/cargotool/             # pinned cargo subcommands: version parsing and the install
internal/download/              # fetch, checksum-verify and unpack an archive safely
internal/compose/               # the services a component's suite needs (see Services below)
internal/scheduler/             # port locks and the concurrency bound (see The scheduler below)
internal/affected/              # which components a change could have broken (see Affected selection)
internal/gitdiff/               # the changed paths, and the tracked-file listing both gates read
internal/detect/                # ecosystem + TS-package detection (walks for Cargo.toml/package.json/go.mod)
internal/config/                # .lydite/config.yml loading (opt-outs + pipeline shape — see Configuration below)
internal/toolchain/             # ensures the Go/Rust/Node runtime each detected ecosystem needs (see Toolchains below)
internal/rust/                  # clippy, cargo-audit, cargo-deny
internal/typescript/            # pinned Biome, the only TS linter (see Linters)
internal/golang/                # gosec, govulncheck (installed into a version-keyed GOBIN dir)
internal/semgrep/                # pinned Semgrep, installed via pipx
internal/coverage/               # reads a component's coverage report (see Coverage below)
internal/gitstate/               # the base branch, and lydite branch read/write (see Coverage below)
internal/executil/              # shared external-command runner every scanner package uses
assets/                        # the shipped logo set — the action's PR comment embeds
                                #   lydite-mark-64.png by raw URL (see below)
docs/design/                    # tokens, surface specs, and the reference prototypes (see Design)
docs/release-notes/             # _header.md + one <tag>.md per release that needs one (see Release notes)
.lydite/                       # every file that configures lydite: config.yml, components.yml,
                                #   exemptions.yml
.goreleaser.yml                 # build/release config (v2 schema)
.golangci.yml                   # lint config (v2 schema)
.github/workflows/{ci,release}.yml
.github/dependabot.yml
                                # (the composite action lives in lydite/actions, not here)
scripts/install.sh              # curl|sh installer shipped with every release
```

- Module path: `lydite/lydite` (not `github.com/lydite/lydite` — a deliberate deviation from
  the other repos in this org, to be applied there too later; do not "fix" this back).
- `lydite` ships as a single statically-linked binary (`CGO_ENABLED=0`), built for
  linux/darwin × amd64/arm64.

## Status

All five subcommands (`scan`, `test`, `review`, `version`, `update`) are fully implemented — every check
is a real tool invocation (not a stub). Every scanner pins its own tool version and installs it into
a lydite-managed cache directory rather than trusting whatever's already on the machine (see each
`internal/<lang>` package's doc comment for why). `update` follows the same pattern as `inforge`'s
self-update (checksum-verified binary replacement, refuses on dev builds, passive update nudge on
every other command). `lydite test` runs each component's suite, starting the compose services and
running the `setup`/`teardown` commands it declares, runs them concurrently under a port-aware
scheduler, and measures and gates each component's coverage. The whole gate — a baseline miss, the
base tree measured in a throwaway worktree, a regression failed, a patch failure, and the cache hit
on the next run — has been exercised end to end against a real git repository, not only a
hand-written report fixture.

`lydite coverage` is **removed**, and so are `coverage.source`, `coverage.{go,rust}.report`,
`coverage.rust.lcov`, `--source`, `--tests`, `--go-report`, `--rust-report` and
`--rust-lcov-report`. Each is rejected by name rather than ignored — including the command itself,
which answers with what replaced it rather than cobra's unknown-command message. See
[ADR 0019](docs/adr/0019-coverage-per-component-gated-by-lydite-test.md).

## CI

`.github/workflows/ci.yml` runs three jobs on every push/PR to `main`: `lint` (golangci-lint),
`build & test` (`go build`/`go test -race`), and `self-scan` — lydite builds itself and runs
`lydite scan --dir .` against its own repo. `self-scan` is dogfooding, not a formality: it's the
only job that exercises the actual scan/report path end-to-end against a real repo, and it already
caught a real bug once (see the git history around the `go-version: "1.26.5"` pin below).

**Pin the exact Go patch version in workflows (currently `"1.26.6"`), never a bare minor (`"1.26"`).**
`actions/setup-go`'s `go-version: "1.26"` resolves to whatever `1.26.x` patch it has
cached/available, which is not necessarily the version this repo's `go.mod` `toolchain` directive
pins — and critically, `go install`-ing an *external* tool (gosec, govulncheck) does **not** consult
the current module's `go.mod` toolchain directive the way building the module itself does. This bit
us for real: `self-scan`'s `govulncheck` step passed locally (toolchain directive respected) but
failed in CI (setup-go had installed an older, vulnerable patch) until `go-version` was pinned to the
exact `1.26.5`. If `go.mod`'s `toolchain` line is ever bumped, update every `go-version:` in
`ci.yml`/`release.yml` to match in the same change.

`internal/toolchain` (see Toolchains below) now also sets `GOTOOLCHAIN` from the scanned repo's own
`go.mod` before running any Go tooling, which fixes this class of drift for **consumers** — they no
longer need to pin `go-version` on lydite's account. Keep the pins here regardless: this repo's
`lint` and `build & test` jobs invoke `go` directly rather than through lydite, so nothing in that
mechanism covers them, and `self-scan` benefits from the pin holding independently of the feature
it is dogfooding.


## Components: `.lydite/components.yml` and `lydite test`

A repository declares its **components**, and lydite builds, tests, measures and gates each one.
`internal/component` parses the declaration and `internal/runner` turns a runner name into
invocations; `lydite test` runs the suites. See
[ADR 0016](docs/adr/0016-components-and-lydite-run-tests.md).

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

**Unknown keys are rejected**, the same stance `referral.Parse` and `config.validateLinter` take. A
dropped key means a component configured differently from what its author wrote — a suite running
without the environment it declared — while every run still reports a result.

`Load` also validates against the tree: unique names, a `dir` that exists and does not escape the
scan root, `depends_on` that resolves to declared components, and no cycles. A dangling edge is
rejected rather than dropped, because the edge exists to make a dependent run on a change to its
dependency and an edge naming nothing silently stops doing that while the dependent keeps passing.

### Runners: three invocations of one suite

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

- `go-test` — plain `go test`; instrumented adds `-coverprofile` **and `-coverpkg=./...`**;
  build-only is `go build`. Without `-coverpkg` Go instruments only the package under test, so code
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
a foreign toolchain tests the machine it runs on. **Two of the three languages therefore have no
repository here to run against** — `web/` is empty and the only `Cargo.toml` files are pin
manifests, deliberately not components. `ci-end2end.yml`'s `proving ground` job closes that,
running `lydite test` against every component of
[`lydite/proving-ground`](https://github.com/lydite/proving-ground) on a bare checkout — no
`node_modules`, no services started, nothing prepared by the workflow, because a step doing either
would hide the case a consumer actually hits.

Where a runner emits JUnit, the invocation names where it lands: the quality-history ledger
([#26](https://github.com/lydite/lydite/issues/26)) records test counts, which no coverage report
carries.


### Output: captured, not streamed

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

### Services: a compose file, and no schema of lydite's own

`internal/compose` starts what a component's suite needs and stops it again.
lydite owns **no service schema**: images, ports, environment and healthchecks are
compose's job already, and a second description here could only agree redundantly
or drift.

It hard-codes **no container runtime** either. Which implementation is present is a
property of the machine, not of the repository — podman on a laptop, docker on a
runner — so `Probe` tries `docker compose`, `podman compose`, then the standalone
`docker-compose`, and the run says which it chose on stderr. It *runs* each
candidate rather than looking it up on PATH: `docker` exists on a machine whose
daemon is stopped and on one with no compose plugin, and neither can start a
service. A component declaring no services is never probed at all, so a repository
without services runs on a machine with no container engine.

**`wait: healthy` requires a declared healthcheck, and is refused without one**
rather than degrading to `started`. A suite racing a database that is not yet
listening is the flakiest thing a pipeline can contain, and the failure is
attributed to the test rather than to the wait. It is also the default, so a
component that says nothing gets the strict answer. The waiting itself is compose's
`--wait`, not lydite polling `ps`: the healthcheck in the file is what defines
ready, and a second implementation here would be lydite deciding what healthy
means. `started` and `none` produce the same invocation, because `up --detach`
already returns only once every container has started — the distinction is the
repository's statement of intent, and `healthy` is the one that changes the command.

**Teardown runs on every path out**, including a `setup` that failed halfway, which
is when a half-applied migration most needs undoing. It is `down --volumes`: a suite
that truncates and reseeds is deterministic only if it starts from nothing. Both
teardowns get a context of their own, because the run's may already be cancelled and
a cancelled teardown is the leak this exists to prevent. A failing `teardown` command
turns a passing component into a failing one — it has left state the next run
inherits — but never masks a failure that already happened, since the earlier one is
what the reader has to act on.

Each component's stack gets its own compose project (`lydite-<name>`), so two
components cannot adopt each other's containers and teardown removes exactly what
this run started. Published **host ports** are read while the file is open, in both
of compose's port syntaxes: the scheduler serialises two components that publish the
same one, and the conflict is physical — two services called `db` on different ports
do not collide, and a `db` and a `postgres` on the same port do.

`Stack.HostPorts` follows each named service's `depends_on` closure, because
compose does: `compose up app` starts everything `app` depends on, and a `db`
pulled in that way publishes its host port just as surely as one named
directly. A lock computed from `compose.up` alone would leave that port unheld,
and the next component to want it fails mid-run on the bind error the lock
exists to prevent.

`compose.Load` probes for a runtime; `compose.LoadWith` takes a
`RuntimeSource` the caller supplies. It is a function rather than a `Runtime`
so that it is consulted **after** the declaration has been checked: probing
first masks every declaration error behind the state of the machine, so a
`compose.up` naming a service the file does not declare reports an absent
container runtime instead — and `internal/compose`'s own tests then pass or
fail depending on whether the developer running them happens to have docker
installed. `lydite test` loads every selected component's stack before any of them starts,
because a stack is the only thing that knows its component's ports and the scheduler
needs all of them before it can decide what may run together — so the probe happens
once for the run rather than once per component, and compose's own validation lands
before the first container instead of mid-run. A run whose components declare no
services probes nothing at all.

Unknown keys in a compose file are **accepted**, unlike everywhere else lydite parses
YAML. The file is compose's, not lydite's, and rejecting a key lydite has no opinion
about would make lydite's version the ceiling on what a repository may write in a
file lydite does not own.

### The scheduler: ports are the only thing that serialises

Components run concurrently. `internal/scheduler` bounds how many at once and
holds a lock on what a component occupies while it runs: each host port its
compose services publish, and its own directory tree. Two components sharing
either run in sequence — the directory by containment rather than equality,
since a component at the repository root and one rooted at `web/` are writing
into the same files. Both conflicts are physical — the second would fail to bind
the port, and two components rooted at one tree install into, build in and
write their output to it at once, where an `npm ci` removing and recreating a
`node_modules` another is importing from is not a race either suite can report
honestly. `component.validate` enforces unique names, not unique directories,
so a repository may legitimately declare two components over one root. It takes plain data — an item
is a name and a set of ports — and the caller supplies the function that runs one,
so the constraint is testable without a container runtime and the port-conflict
predicate has one implementation rather than one here and another in the planner
that groups shards ([ADR 0017](docs/adr/0017-shards-the-scheduler-and-the-planner.md)).

**A CI job holds a shard — a set of components — not a single component.** Under
one component per job nothing in CI ever contends for a port, so the lock's only
exercise would be a local run somebody has to remember to do. `ci-end2end.yml`'s
proving ground job runs all four components in one process, which is what gives
the lock an automated end-to-end assertion at all.

**`--concurrency` defaults to 4 and is deliberately not derived from `NumCPU`.**
Every runner lydite drives is already internally parallel — `go test` fans out at
`GOMAXPROCS`, `cargo nextest` runs tests concurrently, vitest forks workers — so
one component already tries to use the whole machine and `NumCPU` of them
oversubscribe it quadratically. The symptom is timing-sensitive tests going flaky,
which reads as a bad suite rather than as a bad bound. It is not 1 either: a
scheduler that never runs two components at once passes every assertion about port
locks without once having taken one. `max` is one slot per selected component. It
is a flag and never a key in `.lydite/config.yml`, for the reason a repository
does not state how many cores a machine has.

**`depends_on` is not a scheduling input.** It is an invalidation edge, declared
for affected selection; lydite passes no artifact between components, so ordering
them would cost parallelism to express a claim their author never made — and would
need a policy for what a dependent does when its dependency fails, which is new
surface bought with nothing. `TestDependsOnDoesNotSerialise` holds it.

**An interrupted run fails through the `schedule` row.** The rows for
components that never started are `unmeasured`, which does not vote, so a run
cut short by a CI job timeout would otherwise carry no failing row — and
`--json` would publish `"verdict": "pass"` for a run that tested half the
repository. Anything automated reads that document and never the terminal, so a
truncation visible only in the process exit code is one the PR comment renders
green. The `schedule` row is where it lands, because it is the row about the run
rather than about any component, and `ui.Report.ExitCode` stays the single place
the verdict-to-exit-code mapping lives.

**A failure never cancels the rest.** `lydite test` is a gate, and somebody
clearing one wants every failure in a single run rather than N runs paying the
container startup each time. GitHub Actions already has `fail-fast` on the matrix,
at the layer that can cancel other machines.

**Rows are in declaration order, never completion order.** Workers write into
their own slot and the rows are added after the wait, so two runs of the same
declaration produce the same document; completion order would put this run's
timing into it.

**An interrupt cancels the context rather than killing the process.**
`newTestCmd`'s `RunE` installs `signal.NotifyContext` — **in the command, not in
`main.go`**, because `test` is the only command that reports a cut-short run
honestly. `scan` and `coverage` would render every killed tool as a finding and
publish a document saying every scanner failed, so they keep dying on the signal.
It is scoped here so a component already running reaches its deferred teardown: a
signal that skipped those defers leaves one stack per started component holding
the ports the next run has to bind, and the leak surfaces as an unrelated failure
one run later. The handler unregisters as soon as the first signal lands, so a
second interrupt gets the default disposition and kills a teardown that has
itself hung.

**Nothing about a cancelled run is reported as a result.** The scheduler starts
nothing further once the context is done, and a component it never reached is
`unmeasured`/`not run` rather than dropped — a truncated run that omitted rows
would read as a complete run over fewer components. A component that *had*
started is killed mid-suite and exits non-zero, so it becomes
`unmeasured`/`not completed` too: under cancellation lydite cannot tell a suite
that failed from one that was killed, and four red rows blaming a CI job timeout
on the repository's tests is the worst available answer. Only a component that
had already passed keeps its result, because that one is not in doubt.

**The `schedule` row is how the run says what it actually did.** It carries the
maximum concurrency the bound and the locks allowed, and names each pair that
shared one. That number is the point — every assertion about port locks is
satisfied by a run that never had two components going at once, so a report that
cannot distinguish the two proves nothing. It counts admissions rather than
goroutine entries, because counting when a goroutine actually starts would let a
correct run report 1 and make the CI assertion below flaky; that two components
genuinely overlap is forced in `internal/scheduler`'s own tests, by a barrier
none of them can pass alone. `.github/assert-proving-ground.py` holds the
proving ground to a maximum of at least 2 and to having serialised `tally` and
`api` on 5432 — the components rooted at `rust/` and `go/api/`, which publish it
under services deliberately named `postgres` and `db`, so a lock keyed on
service names fails there. The row names components, never directories or
services, which is the only one of the three that is unique by construction.

### The orphan gate: what makes the declaration trustworthy

A declared list fails open. Nothing breaks when it goes stale, so a directory nobody
declared is tested by nobody and the build stays green — the one failure mode a
declared list has and a discovered one does not, and the reason ADR 0016 can choose
declaration at all. `internal/orphan` closes it: **every source file must fall under
some component's `dir` or an explicit exclude**, and one that falls under neither
fails `lydite test`.

It is a **Gate** in CONTEXT.md's sense and not a **Referral** — the author clears it by
declaring the component, or by writing the exclude that says this code is tested by
nobody and somebody decided that. Both leave a line in the file whose history is the
record of what gets tested.

```yaml
components: [...]
excludes: ["scripts/**", "tools/gen.go"]
```

**It is a path question and nothing else.** It reads no manifest, parses no source and
asks `internal/detect` nothing, which is what lets it catch a whole undeclared
directory holding no manifest yet — the case detection cannot see at all. A generated
file is not special-cased for the same reason: recognising one means reading it, and a
gate that starts reading files has to be right about every language it meets. The
exclude is where a repository says so.

**A file counts only when it is written in a language lydite has a runner for.** The
extension set lives beside the `Lang` constants in `internal/runner`, so the two cannot
come apart — a language that gains a runner and no extensions is one the gate is blind
to, and `TestEveryLangHasSourceExts` refuses it. A `README.md`, a `LICENSE`, a
`Makefile`, an OpenAPI document and a shell script are not code any component could
claim, so requiring an exclude for one is paperwork for a question lydite cannot act on
either way — and a gate that fires on ordinary work is one that gets switched off. The
proving ground is the calibration: six files there sit under no component and exactly
one, `scripts/seed.ts`, must fire.

**A language disabled in `.lydite/config.yml` is still checked.** `rust.enabled: false`
says which checks run over a repository's Rust; it does not say that no component should
test it. Reading it as both would let a repository drop a whole language out of this gate
by changing what its linter looks at — a silent widening of what may go untested, in a
file whose history is not the record of that. The exclude is how a repository says it, in
one line, where a reviewer is already looking. (`floorReport` filters to enabled
ecosystems and is the only gate that does; the reasons differ, and neither generalises to
the other.)

**The file list comes from git** — tracked, plus untracked ones git is not ignoring.
Both halves matter. Tracked alone misses the file just written and not yet staged,
which is the moment its author can most cheaply act on the answer; including ignored
files reports every compiled artefact and every installed dependency as untested
source. A filesystem walk would need its own list of directories to skip
(`node_modules`, `target`, `dist`, `coverage`), which is a second copy of a judgement
`.gitignore` already holds — and the copy that drifts is the one that starts calling
build output source. Outside a git repository, and equally when git lists no source file
at all, the gate reports `unmeasured` and passes: a scan root that is itself ignored — a
vendored checkout, a `--dir` pointed at build output — sits inside a work tree and lists
nothing while exiting zero, which would otherwise render as a green gate that had
examined the whole repository. A gate that could not run must never read as one that did.

**Excludes live in `components.yml`, not `config.yml`.** An exclude states what goes
untested, which is the one thing that file exists to record; splitting the two would
leave each half readable and neither answering the question. They use the same anchored
syntax as the exemption set, so a subtree is spelled `scripts/**` and a bare `scripts`
covers the path of that name alone. An exclude covering no file is named on stderr
rather than failing the build: it is very likely a directory name written where a
pattern was needed, but it is also what an honest exclude becomes the day its file is
deleted, and failing a build over tidying is how a gate earns a reputation for firing on
ordinary work.

**The gate runs before selection and before any component runs.** Whether the
declaration is complete does not depend on which components an invocation chose, or on
there being any — a repository that declares none is exactly the one whose every source
file is orphaned, and a gate it never saw would be the failure it exists to catch. It
takes no baseline and reads the same on the default branch as on a pull request, for
the reason `coverage.floor` does: a file is under a component or it is not.

**It does not catch a forgotten `depends_on` edge**, because there both components exist
and no file is orphaned. Pushes to the default branch running every component is what
covers that.

`internal/pathmatch` holds the matcher, which `internal/referral` also uses. Both decide
something consequential off a path — what may merge unread, and what may go untested —
and two matchers would agree until one learned about a pattern form the other had not,
in a way neither's tests would show.

### Affected selection: `--affected`

A pull request runs only the components a change could have broken; a push to the default
branch runs all of them, which is what makes this an optimisation with a bounded failure
rather than a correctness mechanism. `internal/affected` decides it, from paths alone —
no manifest read, no source parsed, the same stance `internal/orphan` takes. See
[ADR 0018](docs/adr/0018-selection-widens-on-ignorance.md).

A component is affected when the change touches its `dir`, one of its `watch` paths, a
component it `depends_on` **transitively**, or an **invalidator**. `depends_on` is read here
and nowhere else: the scheduler ignores it, so the edge decides what runs at all rather than
in what order.

**A changed path matching nothing selects every component.** The default on ignorance is to
widen, mirroring the way a change matching no exemption is referred. This is the decision the
rest of the feature rests on. Narrowing there would make the invalidator list a *safety*
mechanism — every file family missing from it a change that silently tests nothing — and lists
rot. Widening makes it a *performance* list, where a gap costs a slower run and never a missed
one.

It also yields the invariant the package is built around: **the selected set is empty if and
only if the diff is empty.** Every changed path selects at least one component, so
`0 of N affected` can only mean HEAD has no changes against the merge-base, never a narrowing
that went wrong. `TestSelectedIsEmptyOnlyWhenTheDiffIs` holds it.

**The invalidator set is built in, and its purpose is the inverse of what it looks like.** A
root `go.work`, a top-level `Cargo.lock` and a `rust-toolchain.toml` beside them all match no
component and already select everything, so they need no entry. What the set is for is a file
matching a component's directory *too narrowly*: a repository with one component rooted at `.`
and another at `web/` would otherwise have a change to `.lydite/components.yml` select the root
component alone. It is unexported and has no declarable form — `watch` already says "this
outside file invalidates me" one level down, and a set a repository could empty is not a floor,
the same argument the built-in disqualifiers win. Lockfiles, manifests and toolchain files match
at any depth; `.lydite/**` and the workflow paths stay anchored, because the scan root is the
only place lydite reads configuration from and the only place CI is defined.

`.github/workflows/**` and `.github/actions/**` are on the set for a reason worth stating: **a
component rooted at `.` claims every path, so in that repository nothing ever reaches the
widening rule.** The safety net is switched off by one declaration, and only the invalidator set
protects the other components — a workflow edit would otherwise select the root component alone
and leave every other one unrun. That interaction is a real limit rather than a closed hole:
anything at the root that is not on the set is still absorbed by a `.`-rooted component.

**An exclude does not narrow selection.** `excludes` says no component *tests* a path, not that
nothing depends on it — an excluded file can still be imported into a component, as the proving
ground's own `generated/client.ts` is. Reading one declaration as the answer to two questions is
the mistake the orphan gate refuses to make with `rust.enabled`, and an exclude that narrowed
would let a repository make changes to a path run nothing at all. So an excluded path is
unmatched, and therefore widens.

**Selection is explicit, and never inferred.** `lydite test` with no flags always runs every
component. Every signal for "is this a pull request" is unreliable where lydite runs — a
detached HEAD, a shallow clone, a fork with no upstream fetched, a default branch not called
`main` — and the caller already knows the event, so the workflow passes `--affected` on pull
requests exactly as `action.yml` already passes `--diff-base auto`. `--affected` alongside
`--component` is refused rather than intersected: the planner emits per-shard `--component`
lists that are already the selected set, so the combination is never needed, and an
intersection nobody asked for narrows silently when it is wrong.

**An unresolvable merge-base is an error**, not a fallback in either direction. Falling back to
nothing is the failure this exists to avoid; falling back to everything is safe but makes the
optimisation stop happening with no symptom other than a slow job — the same way a `{}` baseline
left wardnet's coverage gate comparing against nothing for months. A shallow checkout is a
fixable misconfiguration, so lydite names the fix.

**The diff is repository-wide, and mapped onto the scan root.** `internal/gitdiff` reads the
paths — the half `internal/referral` also needs, so there is one implementation of the thing
that must not vary. Its `core.quotePath` / `diff.relative` / `diff.mnemonicPrefix` pins are
load-bearing in both packages, and demonstrably: without `diff.relative=false` a diff taken in a
subdirectory drops everything outside it *and* strips the prefix from what survives. referral
keeps its patch-line parsing, which selection never reads and must not pay for — that parse
fails closed on an over-long line, so a minified bundle in the diff would abort a selection with
no interest in its contents. A path outside the scan root is not dropped: it matches no
component, and therefore widens.

**A deselected component is reported, never dropped.** One `unmeasured` / `not affected` row
each, labelled `test(<name>)` exactly as a suite row is — so every component produces exactly one
such row whether it ran or not, and what separates them is the status. A bare name would share a
namespace with the gate rows, and nothing forbids a component called `watch`, `select`,
`orphans` or `schedule`; a consumer keying rows by label would then silently lose the gate. Rows
are in declaration order, plus a `select` row carrying `N of M affected` and the reason each
selected component was chosen. **On the default branch, `--affected` runs everything.** The merge-base of that branch with
itself is its own head, so a computed selection narrows to nothing — and a consumer wiring the
flag into one workflow would get a permanently green `lydite test` that executed no suite at all.
ADR 0016 requires the default-branch run to be complete, since a forgotten `depends_on` edge is
caught at merge or never, so the rule is enforced in the command rather than left to every caller
to remember. `0 of N` remains reachable, by a commit whose tree matches its base.

Selection does not run at all for a repository that declares no components. Nothing to select
from is not the same as a change that selected nothing, and asking selection which it is produced
a `select` row claiming the diff was empty beside a row saying no components are declared — and
paid a `git fetch` to do it, which on a shallow or fork checkout turned a report that renders into
a hard error before a row was written.

`0 of N` makes the `select` row `unmeasured` rather than `pass`:
nothing was gated, and a gate that did not run must never render as one that did. The
distinction travels as a **status**, so a consumer separates "0 of 4 affected" from "4 of 4
passed" without parsing prose — and the reasons are what make a selection that quietly returned
everything visible to a human reading a log. The verdict is still pass and the exit code 0: a
branch with no commits against its base is correct work, and failing it is how a gate gets
switched off.

### A `watch` pattern that covers no file fails the run

Deliberately asymmetric with the orphan gate's excludes, which only warn. An exclude covering
nothing is fail-safe — it excludes nothing, so the gate stays stricter than declared, and
failing a build over tidying is how a gate earns a reputation for firing on ordinary work. A
`watch` covering nothing is fail-dangerous: the component stops being invalidated by its own
declared input, silently and permanently, while every run stays green. Same syntax, opposite
consequence, so the same treatment would be a false symmetry.

Two checks, in two places. `component.validate` rejects a malformed, empty, absolute or escaping
pattern at parse time, alongside `validateExcludes` — that gap existed for as long as `watch`
has, since only excludes were ever run through `pathmatch.ValidatePattern`. The `watch` row then
holds every pattern against the tree, because the dangerous typo is syntactically perfect:
`Makefil` is a valid pattern, and so is a bare `docs` written where `docs/**` was meant, since
these patterns are anchored and do not float. Outside a git repository — and equally when git
lists no file at all, which is what a scan root that is itself ignored looks like — it reports
`unmeasured` and passes, the shape `orphanRow` already has for both cases. A gate that saw no
files must not fail a declaration that is correct.

The file list is every path git knows about, not only source — a watch legitimately names a
`Makefile`, a `VERSION` file or an OpenAPI document, none of which any component could claim.
`gitdiff.Tracked` is that listing, shared with the orphan gate.

## Configuration

Every file that configures lydite lives in `.lydite/` at the scan root: `config.yml` here,
`components.yml` above, and `exemptions.yml` under Referral below. One directory rather than a
family of dotfiles beside it, because a dotfile next to a dot-directory that both configure the
same tool is an arrangement nobody can predict the shape of from either half.

`.lydite/config.yml` is optional and does one thing, and tuning severity or suppressing individual
findings is not it (that's what a fix-up pass + inline `#nosec`/`nosemgrep` annotations in the
scanned repo are for): it **opts out** of what lydite's zero-config default already does (scan
everything detected, every check enabled), and carries the numeric gate knobs
(`coverage.tolerance`, `coverage.patch.tolerance`, `coverage.floor`).

It used to describe the repository's pipeline as well — `coverage.source` and the
`coverage.{go,rust}` report paths, which located a report some other job produced. lydite now
writes every coverage report itself, at a path derived from the component's runner, so those keys
have nothing left to say and are **rejected by name** rather than ignored (see Coverage above).

See `internal/config/config.go` for the full schema; shape:

```yaml
rust:
  enabled: true          # set false to skip Rust entirely even if a Cargo.toml is detected
  exclude: []            # extra directory names to skip during ecosystem/package detection
typescript:
  enabled: true
  exclude: ["legacy-app"]
  linter: biome          # the only accepted value. The retired "eslint" is rejected with an
                          # error rather than silently run under Biome (see Linters below).
  install: ""            # override the install-command auto-detection a JavaScript component's
                          # runner does from the lockfile, e.g.
                          # "corepack enable && yarn install --immutable"
go:
  enabled: true
  exclude: []
semgrep:
  enabled: true
  config: auto           # override to a custom registry ref/path if needed
toolchain:
  enabled: true          # set false to keep the diagnostics but never download/install
                          # (air-gapped runners, or images that preprovision everything)
  # go/rust/node: deliberately unset. The versions come from the repo's own
  # manifests — see Toolchains below. These keys exist only as a local override.
coverage:
  tolerance: 0.1         # pp a coverage figure may dip below its baseline before the gate fails;
                          # absorbs sub-tenth measurement noise ("86.1% vs baseline 86.1%,
                          # regressed 0.0%"). Compared at display precision (tenths); 0 = fail any
                          # dip the report can show. Must be finite and non-negative — Load
                          # rejects anything else.
  floor: 0               # minimum coverage any single measured component must reach. 0 = off,
                          # which is the default: it is opt-in, so upgrading never starts failing
                          # a repo over a gap it has always had. This is the signal line-weighting
                          # removes — see Coverage above.
  patch:
    tolerance: 0.1       # the patch gate's own dip allowance — deliberately independent, so
                          # loosening the aggregate knob never weakens the untested-new-code check
```

Omitting the file, or omitting a section/key within it, keeps that value at its default — see
`internal/config/config_test.go` for the exact merge semantics.

## Linters: Biome, and only Biome

Biome backs the TypeScript check. `typescript.linter` accepts `biome` and nothing else; the
retired `eslint` value is **rejected** by `config.validateLinter` with an error naming the
removal, rather than accepted and quietly run under Biome. A repo that set that key stated
which rule set it gates on, and switching it silently would change what the scan measures
while every run still reported a pass — the same reason an unknown value is rejected rather
than defaulted. `LinterESLint` stays defined for no purpose but to be recognised and refused.

**Biome is not a replacement for `eslint-plugin-security` — Semgrep is, and it already ran.**
Biome's `security` group is six JSX/eval/secret rules (`noBlankTarget`,
`noDangerouslySetInnerHtml`, `noDangerouslySetInnerHtmlWithChildren`, `noGlobalEval`,
`noScriptUrl`, `noSecrets`), of which only `noGlobalEval` coincided with anything the plugin
had; lydite additionally gates on the **`correctness`** group, so the TypeScript check is not
"security findings only" the way the others are. Measured against a fixture carrying all four
classes, Semgrep at `config: auto` covers `detect-child-process` (with taint — it names the
tainted argument) and `detect-non-literal-fs-filename` (as a path-traversal finding), partly
covers `detect-unsafe-regex` (dynamic patterns via `detect-non-literal-regexp`, but no static
ReDoS analysis of a literal), and does not cover `detect-object-injection` at all. Several of
those Semgrep rules are ports of the ESLint ones and are stronger than the originals. The real
gap is `detect-object-injection` — the plugin's most commonly disabled rule — plus
literal-pattern ReDoS. Do not rebuild either as a Biome GritQL plugin: GritQL matches syntax
against the CST with no dataflow or taint, so it could only produce a weaker copy of what
Semgrep already does. See [ADR 0008](docs/adr/0008-biome-as-the-only-typescript-linter.md),
and [ADR 0005](docs/adr/0005-optional-biome-linter.md) for the opt-in it supersedes.

**Why it went.** ESLint's TypeScript support is a *compiler* dependency, not a linter one:
`@typescript-eslint/parser` declares `typescript` as a peer with an
upper bound (`>=4.8.4 <6.1.0`), so lydite's own pin manifest had to carry a `typescript`
inside that window, and the window moves only when upstream ships support for a new compiler.
`internal/typescript`'s package doc had recorded exactly this hazard — and a Dependabot PR
bumped `typescript` across the ceiling anyway and merged, after which `npm ci` on the
committed lockfile failed with `ERESOLVE` and every consumer with TypeScript got an install
error instead of a lint result. CI stayed green throughout, because lydite's own repo has no
TypeScript to scan: its only `package.json` files are the pin manifests, which `.lydite/config.yml`
excludes by name, so `self-scan` cannot reach that code path. Biome parses TypeScript with its
own Rust parser and depends on no compiler package, so `biome-pin` has no peer range to cross
and the failure class does not exist for it.

Four things about the Biome integration were established against Biome directly rather than
from its docs. The pin is now **2.5.10**, and `biome.json`'s `$schema` URL must be bumped with
it — `TestBiomePinMatchesConfigSchema` fails the build otherwise, the same mirror guard
`internal/golang/pins_test.go` provides for the Go constants. Re-verified on 2.5.10 where
noted:

- **`files.includes` negations work from an out-of-tree config** (re-verified on 2.5.10: an
  `eval` inside `dist/` is not reported). Biome resolves config globs
  "relative to the folder the configuration file is in", and lydite stages `biome.json` in its
  cache directory — so this looked like it would silently ignore nothing. It doesn't: `**`-prefixed
  negations are depth-agnostic and match correctly. But they are doing real work — with them
  removed, Biome lints `dist/` and reports findings inside a minified production bundle.
  `TestBiomeConfigIgnoresMatchesDefaultSkipDirs` guards it.
- **`--config-path` beats the scanned project's own root `biome.json`** (re-verified on 2.5.10).
  A project config setting
  `linter.enabled: false` is ignored, so lydite's verdict doesn't vary with what the target repo
  declares — the same stance `internal/typescript`'s doc comment takes generally.
- **A `biome.json` in a *subdirectory* does NOT abort lydite's run**, and the earlier claim that
  it does was a mis-scoped reproduction. Biome errors with "Found a nested root configuration, but
  there's already a root configuration" **only when it resolves configuration from the tree
  itself**. lydite always passes `--config-path`, and under that flag a nested `biome.json` —
  with `"root": true`, or with no `root` key at all — is simply ignored: Biome exits 0, lints
  normally, and the report is clean. Confirmed on 2.5.8 and 2.5.10 with lydite's exact
  invocation, so this was never version drift.
  The consequence is that `lintDirBiome`'s `biomeNestedRootConfig` branch is unreachable as long
  as `--config-path` is passed. It stays, because it stops being unreachable the moment anyone
  drops that flag — and dropping it is a plausible change, since it is what would let a project's
  own Biome config be honoured. What must not survive is the claim that it fires today.
- **A nested config declaring `"root": false` is *merged* into lydite's config.** Its rules then
  fire in our run. `reportableBiome`'s category allowlist contains that — merged `lint/style/*`
  is dropped. The same merge is a real limitation in the other direction that cannot be closed
  from here: a nested config could set `"security": "off"` and silently narrow what lydite
  checks.

**A check whose findings never stream must set `executil.Result.Detail`.** `executil.Run`
streams every tool's stdout/stderr live, so for gosec, clippy, cargo-audit and Semgrep the
findings are on the terminal and in the log `action.yml` captures before anyone reads the
`Result`. Biome is the exception: its report goes to a file via `--reporter-file` so its own
chatter cannot corrupt the JSON, so nothing streams and `Output` holds no findings.
`cmd/lydite/scan.go`'s `report` prints `Detail` under a failing check — without it a
developer sees a bare `✗ biome(.)` row and has to re-run the pinned toolchain by hand to learn why,
and the PR comment carries nothing at all. Detail lines are **indented**, and that is
load-bearing rather than cosmetic: a finding quotes source, which can contain anything the
source contains — including something shaped like a verdict — and indentation is what stops
it from beginning a line the way a status row does.

`recommended: false` in the bundled `biome.json` is not tidiness — without it Biome's default
preset enables `style` and `suspicious` too, and lydite would start failing PRs over opinions it
never agreed to enforce. The formatter and assist are explicitly disabled for the same reason:
lydite is not a formatter and must never report a formatting diff as a finding.

## Tool version pins

lydite pins every tool it runs, which is the whole premise — and for a long time every one of
those pins was a Go string constant, invisible to Dependabot. A pinned security toolchain that
nothing ever ages out is a scanner that quietly goes stale while still reporting a pass, which
is the worst available failure. Every pin now lives in a real package-manager manifest that
Dependabot watches, colocated with the package that uses it:

| Tool(s) | Manifest | Runtime source |
|---|---|---|
| @biomejs/biome | `internal/typescript/biome-pin/package.json` + lock | the manifest itself (`npm ci`) |
| cargo-audit | `internal/rust/cargo-audit-pin/Cargo.toml` | parsed by `internal/rust/pins.go` |
| cargo-nextest | `internal/runner/cargo-nextest-pin/Cargo.toml` | parsed by `internal/runner/pins.go` |
| cargo-deny | `internal/rust/cargo-deny-pin/Cargo.toml` | parsed by `internal/rust/pins.go` |
| semgrep | `internal/semgrep/requirements.txt` | parsed by `internal/semgrep/pins.go` |
| gosec, govulncheck | `internal/golang/go-pin/go.mod` | **still Go constants** — see below |

The npm toolchains install with `npm ci` from a committed lockfile into a cache directory keyed
by a **hash of that lockfile**. The old key was a hand-maintained string of concatenated version
numbers, so adding a dependency meant remembering to extend the key, and forgetting meant reusing
a cache directory that predated it — precisely how "every .ts file is silently skipped" would have
come back when the TypeScript parser was added. A lockfile hash cannot be forgotten.

**Go is the one exception, deliberately.** `gosecPkg`/`govulncheckPkg` are const expressions that
concatenate the version at compile time, and `go:embed` cannot read files inside a nested module,
so the versions can't be read from `go-pin/go.mod` at runtime. The constants stay and
`internal/golang/pins_test.go` fails CI if a Dependabot bump to `go-pin/go.mod` isn't mirrored into
them. `go-pin` is a **separate module** rather than `tool` directives in lydite's own `go.mod`:
declaring them in the main module works, but drags gosec's entire dependency graph (grpc, protobuf,
`google.golang.org/api`, …) into code lydite never links — measured at go.mod 17→69 lines and
go.sum 17→114 — which would then generate a stream of irrelevant Dependabot PRs.

**Each cargo tool gets its own manifest, and that is not tidiness.** cargo-audit and cargo-deny
cannot resolve in a shared dependency graph — cargo-deny's `krates` pins `petgraph =0.8.1` while
cargo-audit's `cargo-lock` pulls `0.8.2` — and a `[package]` manifest with no `src/lib.rs` doesn't
parse at all. Either way cargo errors, Dependabot's updater fails in a job log nobody reads, and the
pin silently stops being bumped: the exact failure this whole arrangement exists to prevent. `cargo
install` never shares a graph between two tools, so the conflict is invisible in normal use.
`internal/rust`'s `TestPinManifestsAreSeparate` guards against a well-meaning consolidation.

**Adding a new pin means three edits, not one:** the manifest, a `.github/dependabot.yml` entry,
and an exclude in lydite's own `.lydite/config.yml`. The manifests are real `package.json`/`Cargo.toml`/
`go.mod` files, so CI's self-scan would otherwise treat each as a package to lint, a crate to audit
or a module to scan. `internal/config`'s `TestPinDirectoriesAreExcluded` covers only the exclude
half — it never reads `dependabot.yml`, and it discovers pins by a `*-pin` directory-name suffix, so
`internal/semgrep/requirements.txt` falls outside it entirely. Nothing enforces the Dependabot
entry. See [ADR 0006](docs/adr/0006-tool-pins-as-dependabot-manifests.md).

## Toolchains

lydite provisions every tool it *runs* — gosec/govulncheck via `go install` into a version-keyed
cache, cargo-audit/cargo-deny via `cargo install`, Biome via npm, Semgrep via pipx — under the
"pin the exact toolchain, don't reuse ambient installs" principle in `internal/golang`. The one
thing it long did *not* provision was the language toolchain it does that provisioning **with**:
`go`, `cargo` and `node` were simply assumed to be on PATH. `internal/toolchain` closes that.

Nothing was visibly broken before, and that matters for judging changes here: on a GitHub-hosted
runner Go is preinstalled and Go 1.21+ fetches whatever `go.mod` asks for, so the assumption held.
It was a robustness and determinism gap, not an outage — a self-hosted or container runner without
Go fails at `go install`, the version used is whatever the image ships rather than what the repo
declares, and with no shared module cache every run re-downloads.

**Versions come from the repo's own manifests, never from `.lydite/config.yml`.**

| Ecosystem | Version source |
|---|---|
| Go | the `go` and `toolchain` directives in **every** discovered `go.mod`, highest wins |
| Rust | `channel` in `rust-toolchain.toml`, else the legacy bare `rust-toolchain`, per discovered crate |
| TypeScript | `engines.node` in each detected package's `package.json`, else `.nvmrc` (package, then scan root) |

Those files are already authoritative and already enforced by the language's own tooling, so a
second copy in lydite's config could only agree redundantly or drift silently — and a stale
duplicate is worse than none, because it reads as authoritative. `toolchain.{go,rust,node}` in
`.lydite/config.yml` exist purely as a deliberate local **override**, which is a different thing from a
parallel source of truth. `toolchain.enabled: false` keeps the diagnostics and skips the
downloads.

**An ambient toolchain that already satisfies the declared version is used as-is.** Downloading
something already present and correct is pure cost, and on the runners lydite actually runs on
that is the common case — so the common path does no network I/O at all. `internal/toolchain`'s
`satisfied` is the single predicate for that decision; an unpinned requirement (a `stable` rust
channel, an `lts/*` .nvmrc, no declaration at all) is satisfied by anything present, since the repo
named no floor to be below. A toolchain that won't identify itself is treated as too old, because
it cannot be *shown* to satisfy a pin.

Each ecosystem provisions differently, and only one of the three downloads anything:

- **Go** delegates to `GOTOOLCHAIN`. Any Go 1.21+ can fetch another toolchain itself, through the
  module proxy and verified against Go's checksum database — better provenance than lydite could
  hand-roll — so lydite just names the version. Only with no Go at all, or one older than 1.21,
  does it download a tarball from `go.dev`, taking the SHA-256 from the release index.
  **Go is pinned even when the ambient toolchain already satisfies the declaration** — the one
  ecosystem where "satisfied" is not the same as "nothing to do". `GOTOOLCHAIN`'s default (`auto`)
  does not only *upgrade*: `go install <tool>@<version>` run outside a module, which
  `internal/golang` does to fetch gosec and govulncheck, makes the go command consult that
  **tool's** own `go.mod` and switch to whatever minimum it declares. `golang.org/x/vuln@v1.6.0`
  declares `go >= 1.25.0`, so an `auto` runner with Go 1.26 installed builds govulncheck with
  go1.25 — and a govulncheck built by an older Go rejects newer source outright. That is the exact
  failure recorded under CI above, and it reproduces on a runner whose ambient toolchain is
  perfectly correct. `pinAmbientGo` therefore sets `GOTOOLCHAIN=local` in that case.
  `local`, not the declared version: the declaration is a *minimum*, so a `go 1.26` directive
  resolves to `go1.26.0` and pinning it would downgrade a runner already on 1.26.6 — backwards,
  when the newer patch is the one carrying the security fix. lydite's own `ci.yml` sets
  `GOTOOLCHAIN: local` by hand for exactly this reason; consumers now get it automatically.
  The probe reads the ambient version with `GOTOOLCHAIN=local` set for the same reason: `go
  version` inside a module honours that module's `toolchain` directive and reports the version it
  would switch *to*, which is not the one `local` would then select — measure and pin have to come
  from the same place, or lydite concludes "ambient is fine" and lands on an older toolchain than
  it just measured.
- **Rust** delegates to rustup, which already reads the same `rust-toolchain.toml` lydite does.
  This extends rather than contradicts `internal/rust`'s existing stance that the toolchain version
  "is the target repo's responsibility via its own rust-toolchain.toml". What it adds is
  materialising the channel up front with the `clippy` and `rustfmt` components the checks need —
  rustup would otherwise install it lazily in the middle of `cargo clippy`, where a missing
  component reads as a check failure rather than a setup step. With no rustup at all, lydite says
  so and continues rather than installing rustup behind the user's back.
  Installing is not selecting, and the two come apart in exactly one case. rustup picks a toolchain
  by reading `rust-toolchain.toml` from the directory cargo runs in, which covers the normal case
  for free — `internal/rust` runs cargo inside the crate directory and a Rust component's
  instrumented variant runs inside the component's, so the file lydite read is the file rustup
  reads. A version supplied by `toolchain.rust` in
  `.lydite/config.yml` has no such file, so rustup cannot see it and would install the requested channel
  and then go on running the old default. `Requirement.Overridden` marks that case and
  `provisionRust` sets `RUSTUP_TOOLCHAIN` **only** then: applying it whenever a channel came from a
  manifest would override rustup's own per-crate selection — the thing `internal/rust` says not to
  second-guess — and would break a monorepo whose crates pin different channels.
- **Node** is the only one lydite downloads and unpacks itself, from `nodejs.org`, with the digest
  read from that release's `SHASUMS256.txt`. There is no assumable equivalent of GOTOOLCHAIN or
  rustup — nvm/fnm/volta are all optional and mutually exclusive. The `.tar.gz` is taken over the
  `.tar.xz` purely because Go's standard library decompresses gzip and not xz.

**Provisioning failures warn; they do not fail the scan.** This step is preparation, not a gate,
and falling through to "whatever is on PATH" is exactly today's behavior — turning a working scan
into a hard failure over a network blip would be a regression, and if the toolchain really is
absent the next step fails loudly and specifically. What must not happen is failing *silently*, so
every reuse, substitution, skip and failure is named on stderr. Stderr, not stdout, deliberately: stdout carries the
report, and one command emits exactly one report — a warning interleaved into it would break
the grammar and, under `--json`, the document.

**Doing this in lydite rather than in each caller's CI is the point.** gt briefly grew a
`setup-go` step in its lydite stage (`19e4b77`, reverted in `a0ed107`) and it was wrong twice
over: it looked for `go.mod` at exactly one path, so wardnet — whose modules live under `wctl/` and
`sdk/wardnet-go/`, not the scan root — would silently have got nothing, and it put knowledge of Go
toolchains into gt, which would then have needed the same for Rust and TypeScript forever. lydite
already knows which ecosystems it detected and where, so it reads every manifest under the scan
root; and it is the only place that helps wardnet at all, since wardnet calls `wardnet/bulwark@v1`
directly rather than through gt. See [ADR 0004](docs/adr/0004-ensure-language-toolchains.md).

Downloads land in the same version-keyed `~/.cache/lydite` layout every other lydite-managed
install already uses, so a consumer caching that one path (as wardnet's workflow already does)
covers language toolchains too with no key change. Installs stage into a sibling temp directory and
are renamed into place, so an interrupted download can never leave a half-populated toolchain that
the next run mistakes for a complete one.

## Coverage

`lydite test` measures every component's coverage from the instrumented variant its runner
already derives, and `--gate-coverage` compares it against a baseline cached on a dedicated
`lydite` branch (never `main` — bot-owned generated cache data, not source, needs no PR/review and
never pollutes main's history). See
[ADR 0019](docs/adr/0019-coverage-per-component-gated-by-lydite-test.md).

**The component is the unit.** `internal/detect` no longer decides what a coverage unit is — the
declaration does. Three altitudes are reported and all three are gated: per component, per
language, and globally. All three are `Σ covered / Σ total` over subsets of the same stored
per-component entries, so they cannot disagree or drift apart.

**Measuring is local; gating touches the network.** `lydite test` always measures. `--no-coverage`
opts out of instrumentation entirely and emits no coverage row at all. `--gate-coverage` turns on
the baseline read, the comparison and the record. The flag is explicit, exactly as `--affected` is:
folding gating in unconditionally would have a developer's local run push to a shared branch, and
inferring "am I in CI" to avoid that is the thing [ADR 0018](docs/adr/0018-selection-widens-on-ignorance.md)
already refused for selection, for the same reason — every signal is unreliable where lydite runs,
and the caller already knows.

**A run that measured but did not gate renders distinctly from a pass.** The coverage rows are
`context` (`→`), never `pass` (`✓`), and the global row says so in its detail. Without that, a
workflow missing the flag reports exactly the green a gated run reports — the failure
wardnet/wardnet#957 shipped, where patch coverage never ran and the pull-request comment read as
though it had. `--json` carries the status as its own name, so a consumer separates the two without
parsing prose.

Instrumentation is on by default despite costing real time — `cargo llvm-cov` forces a separate
instrumented build, and Go's `-coverpkg=./...` recompiles every package per test binary. The fast
inner loop is `go test` or `cargo nextest` run directly; nothing reaches for lydite to re-run one
package. A gate that is opt-in is a gate that is off where it matters. `--no-coverage` is for the
case that genuinely wants the plain variant, and it emits no rows rather than a row per component
saying `unmeasured` — that would train readers to ignore the tag that exists to be noticed, the
same argument that keeps a types-only TypeScript package from being reported.

### A baseline is per-component counts, keyed by tree

`v3/<tree>.json` on the `lydite` branch, an object of component name to `{covered, total}`.

- **Counts, not percentages.** A percentage cannot be re-weighted: composing a language or global
  figure from components needs each component's size, and so does a run that measured only some of
  them. Storing percentages would make the language figure underivable from the components that
  produce it, leaving two quantities free to disagree.
- **Keyed by the component's name, not its directory.** `component.validate` enforces unique names
  and not unique directories, so the name is the only one of the two unique by construction.
- **No language is stored.** The component declaration already states it, and a second statement
  could only drift.
- **`v3`, so every consumer takes one clean cache miss.** `gitstate.StatePath`'s directory is keyed
  to the metric and to the unit it is measured over; entries recorded under the old per-language
  percentages are a different quantity, and are simply never found.
- **An empty baseline is never cached, and an empty cached baseline is a miss.** Cached, `{}` is
  indistinguishable from a real entry: every later change hits it, reports every component as `new`,
  and the gate enforces nothing — silently, permanently, with no way to self-heal. wardnet's branch
  accumulated nine of them. Treating one as a miss heals the already-written entries with no manual
  purge.

**A run on the default branch records rather than re-measures.** There, HEAD is its own merge-base,
so the tree the run just measured is the tree a baseline would be read for. Reading it would miss on
the first build and measure the whole repository a second time, in a throwaway worktree, to
reproduce numbers already in hand. There is nothing to compare against either — the current commit
*is* the baseline — so the figures render the way an ungated run's do, and the measurement is
recorded.

**No run on the default branch is required.** Keying by tree carries the chain through pull
requests: CI builds `refs/pull/N/merge`, a squash merge lands a commit carrying that same tree, and
the next pull request's merge-base resolves to it — so the number a change measured *is* the
baseline for the commit it becomes. Requiring a main run is a consumer obligation that a repository
running CI only on pull requests satisfies never, and it is strictly more expensive: one
instrumented run per change rather than one on the branch and another on main.

**A cache miss measures, and never substitutes.** `measureBaseTree` checks the base commit out into
a throwaway worktree and runs it through the same path `lydite test` just took — so the compose
services each component declares actually start, each runner's `Prepare` runs, and the report read
back is the one the instrumented variant wrote. Invoking coverage tooling directly instead is what
produced a failed measurement for any suite needing a database, one of the roads to an empty
baseline cached as real. It reads the **base tree's own** `components.yml` and `config.yml`, never
the branch's: a component this change adds did not exist there, and one it renames is a different
component. Gating against the nearest ancestor that happens to have an entry was considered and
rejected — which number a change is judged against would depend on how far back history had one,
which is not reproducible from the change itself.

**A component the run did not *select* carries its baseline entry forward.** Under `--affected` most
components do not run, and recording only what was measured would drop them — every later change
would see them as `new` and gate them against nothing, permanently, since each run would drop them
again.

**Only that one, and the distinction is load-bearing.** A component that ran and failed produced the
same absence and means the opposite thing: its content may be exactly what changed, so its old entry
is a guess — and carrying it renders as a pass, so a language whose only component failed to build
would report that component's last good figure with a `✓` beside it. `measurement.Carryable` is set
in the one place that builds a measurement for a component this invocation never reached, so no path
through a run that did reach one can produce it.

A component the declaration no longer holds is not carried either: its entry dies with it rather
than leaving the baseline a tail of components nobody can measure.

**A composed figure names what it measured.** `72.5% (145/200 lines), 3 of 4 component(s), 1
carried forward`. A figure that does not say how much of it this run measured is indistinguishable
from one that measured everything. The baseline side of a composed comparison sums exactly the
components the current side covers — summing the whole baseline would compare this run's three
components against the base tree's four, so every narrowed run would read as a regression the size
of the component it did not run. A figure whose baseline does not cover every component in it is
reported as `new` rather than compared.

**A tolerated dip does not lower the baseline.** `coverage.tolerance` absorbs sub-tenth measurement
noise; recording a dipped number verbatim would turn it into an unbounded downward ratchet, each
change dipping by up to the tolerance and the next one measured from the lower floor. Within-tolerance
dips are restored to the baseline's own counts, capping total drift at one tolerance. A dip beyond
the tolerance is recorded as measured: it failed visibly on the change that introduced it, so
accepting it is a deliberate reset rather than leakage.

**Only a passing component contributes a measurement.** A report written by a suite that failed,
was killed, or never started describes an unfinished run, and recording it would put a number in
the baseline nothing can be compared against honestly. The rule is enforced over the final rows, so
no path out of `runComponent` can forget it.

**A component declaring a raw `command:` is `unmeasured`**, with the reason said out loud. It has no
instrumented variant to ask for, and there is deliberately no key naming where its coverage lands —
that would be `coverage.{go,rust}.report` returning under a new name one decision after being
deleted. Excluding it instead would drop it from the composed figures silently, leaving a gate that
covered fewer components than the repository has reading as a complete one.

**Generated Go files are excluded from both the aggregate and the patch gate**, matched on Go's
`// Code generated ... DO NOT EDIT.` convention (<https://golang.org/s/generatedcode>) rather than a
filename pattern — the same signal golangci-lint and Codecov use. Without it the gate measures code
generation rather than testing: wardnet's regenerated REST client was 983 of one PR's 1007 changed
Go lines and pinned its SDK module's aggregate at 2%.

### `coverage.floor`: the one gate with no baseline

`coverage.floor` is the minimum any single measured component must reach. It has no baseline and no
comparison against last time — the aggregate asks "is this worse than it was", the floor asks "is
this below the bar", which the repository states once and every component meets or does not. That is
why it gates whether or not the run reads a baseline at all, and why ratcheting it against a prior
value is refused: it would make a component that has never had tests permanently acceptable, which
is the gap it exists to close. It defaults to `0` (off), so upgrading never starts failing a
repository over a gap it has always had.

**The unit is now the component**, coarser than the crate or package it gated before. That is a
change in what is measured rather than a weakening: an untested crate inside a workspace still
contributes its lines as uncovered and still drags its component's figure down in proportion to its
size. What a component-level floor cannot catch is a *small* untested sub-unit — the residual
[ADR 0007](docs/adr/0007-line-weighted-coverage-aggregation.md) identified when it introduced the
floor. A repository wanting crate-level floors declares those crates as components, which is a
statement about what it wants tested made in the file whose history records exactly that.

An unmeasured component is reported as `unmeasured` and never folded into the passing count, and
the summary line reads `N of M component(s)` — the two numbers differ exactly when something went
ungated, so a partial run cannot read as a repository-wide pass.

### Aggregation is line-weighted

A figure is the ratio of its components' **summed line counts**, never the mean of their
percentages. The mean is not a mild approximation: on a nine-package monorepo, one commit that added
a 39-line untested file to the smallest package while adding ~1,250 well-tested lines elsewhere read
as **−2.2** under the mean and **+0.44** by line count — the gate failed a change that improved
coverage, by five times the true magnitude, in the opposite direction. Widening
`coverage.tolerance` is explicitly not the fix: it exists for sub-tenth instrumentation noise, and a
tolerance wide enough to hide a 2.2-point artefact hides a genuine two-point regression too. See
[ADR 0007](docs/adr/0007-line-weighted-coverage-aggregation.md).

The language and global figures blend units that are not quite identical — a Go profile counts
statements where lcov counts lines, and the global figure blends across languages. This is accepted
and stated rather than hidden; the alternative is the mean ADR 0007 rejected.

**A component with no measurable lines is unmeasured, never 0%.** An empty Go profile, a crate
llvm-cov reports zero lines for, an lcov with no `LF` records — each is reported with its reason,
so no 0/0 reaches an aggregate and no floor comparison fails a component no work could clear.

### Patch coverage

Aggregate and patch coverage catch disjoint regression classes: aggregate catches coverage lost in
code the change never touches (a deleted test file — none of those lines are in the diff, so the
aggregate is the only gate that notices); patch coverage catches untested new code even when the
codebase is big enough that it does not move the aggregate. Neither bounds the other, so both run.

**Patch coverage gates per component, against that component's own aggregate baseline**
(`patch% >= baseline% - coverage.patch.tolerance`), from the same instrumented run — no second
execution, and no second artefact. Per component and not repository-wide: a change to a well-tested
component held to the repository's average is held to nothing, and one to a poorly tested component
is failed for reaching the standard it already has. A component with no baseline yet is reported
`new`, not failed. Its tolerance is deliberately independent of `coverage.tolerance`, so loosening
the noisy aggregate knob never weakens the untested-new-code check. It is opt-out per language:

```yaml
coverage:
  patch:
    go:
      enabled: false   # defaults to true
```

**A diff is scoped to the files a component's report could speak for**: under its directory, in a
language its runner implies. Both halves are needed — a repository declaring a Go and a TypeScript
component over one root would otherwise score each against the other's changed files. One
`ChangedLines` call serves every component, partitioned afterwards, since all of them measure the
same range.

**A component whose files the diff touched but which produced no per-line data is `unmeasured`,
never skipped in silence.** A silent skip reads as "patch coverage passed" in the pull-request
comment: wardnet/wardnet#957 shipped a green lydite summary that way while Codecov, fed the very
same lcov export, failed that diff. A stderr warning names the reason. A component the diff did not
touch stays silent — a row per untouched component is the noise that trains readers to skip the
rows that matter.

Changed lines come from a hand-rolled unified-diff hunk parser (`internal/coverage.ChangedLines`,
`git diff --relative --unified=0 <merge-base>..HEAD`) — deliberately not a diff library, since the
format needed is a small, stable subset (hunk headers + `+` lines). `--relative` matters: the
command runs in `--dir`, and every consumer of the changed-line map works in `--dir`-relative paths.
Without it git emits repository-root-relative paths, so with `--dir` pointing at a subdirectory
every changed file fails the prefix match and the patch gate silently measures nothing. The diff
runs through `executil.RunQuiet`: it is data this parses, not output anyone watches, and streamed it
lands in the middle of the report — and under `--json` in the middle of the document.

The parser does no language-aware filtering of comments, blank lines or imports — that happens when
changed lines are intersected with the report, since `PatchPercent` counts only lines the report
actually mentions.

**A Go coverage profile is the exception, and it bit us.** lcov lists only executable lines, so
"absent from the report" safely means "not executable, don't count it". A Go profile records
*blocks*, not statements — every line between a block's braces is in the report, comments and blank
lines included. The dividing line is the *format*, not the tooling: Vitest's default `v8` provider
is range-based exactly like Go's profile, and `llvm-cov`'s own text report does print counts beside
comment lines inside a function. Both still emit clean lcov (v8 maps ranges back onto statements via
`v8-to-istanbul`; `llvm-cov --lcov` only emits `DA:` for lines carrying a coverage segment) —
verified directly against both producers with a comment and a blank line inside an uncovered
function, neither of which appeared in the resulting lcov. So Go needs the filtering below and the
other two genuinely do not. Without it a comment added inside an uncovered function counted as an
uncovered new line, and a comment-only PR scored 0% patch coverage and failed the gate
(`wardnet/inforge#216`, whose entire diff was `nosemgrep` annotations and workflow YAML).
`internal/coverage.ParseGoProfile` therefore reads each profiled source file and drops blank and
`//`-comment lines before they ever reach `LineHits`. It deliberately does **not** track `/* */`
comments (that needs a lexer — `/*` inside a string literal opens nothing) or treat a leading `*` as
a comment continuation (`*p = x` is a pointer assignment): over-counting a rare block comment merely
understates patch coverage, while wrongly dropping a statement would let genuinely untested code
through the gate.

### The base branch

Every gate that measures a change against "before it" resolves one merge-base, through
`gitstate.BaseSHA`: the coverage baseline, `--affected`, `scan --diff-base auto` and
`review --base auto`. They must agree on which branch that is — a scan and a coverage gate
disagreeing about what this change contains is worse than either being wrong alone — so the flag,
the usage string and the resolution live in one place.

Resolution is explicit before discovered:

1. `--base-branch`, which is the caller's own statement.
2. `git symbolic-ref refs/remotes/origin/HEAD` — git's own record. Authoritative where it is set,
   and `actions/checkout` does not set it, so it resolves for a developer's clone and almost never
   in CI.
3. Whichever of `main` and `master` the remote actually has. Exactly one, or it is an error: a
   repository carrying both has not said which is the default, and picking by a hardcoded
   precedence would measure a change against a branch nobody chose.

Every failure names the flag. Falling back to `main` whatever the remote holds is what left a
repository whose default branch is `master` unable to run any of the four, each failing with a
merge-base error naming neither the cause nor the fix. Refs are compared whole rather than by
substring, so a branch called `not-main` is not one of the candidates.

**The remote stays `origin`, deliberately.** A repository with two remotes is real and lydite cannot
guess which one a pull request targets; discovering it would be a second inference with the same
failure mode and no flag to escape it. Naming the limit is better than half-solving it. What
actually fixes stacked pull requests is the action passing `GITHUB_BASE_REF` on `pull_request`
events, which is a change in `lydite/actions`.

## Output grammar and `--json`

Every command renders through `internal/ui`, which implements the specification in
`docs/design/tokens.md`: a row is glyph, space, label, leader dots, value with the value
column at 34 characters; the last line is the command, the verdict and the duration.
`--no-color` drops colour and keeps every glyph, and colour is off automatically when
stdout is not a terminal or `NO_COLOR` is set — so a CI log never fills with escape
sequences nobody configured away.

**Glyph and exit code are different axes, deliberately.** A run has exactly one verdict and
that verdict owns the exit code; a row's glyph only says how much attention the row wants.
`refer`, `unmeasured` and `dropped` all render amber `!`, and only `refer` votes. This is
what lets an unmeasured gate be visibly distinct from a passing one — the wardnet#957
failure — without a path-filtered coverage job starting to fail builds. `ui.Report.ExitCode`
is the single place that mapping lives: `✗` anywhere is 1, else a referral is 2, else 0.

**Anything automated reads `--json`, never the text.** The document carries the same rows as
the terminal, so the two cannot disagree, and statuses travel as their own names rather than
as glyphs. `TestJSONKeysArePartOfTheContract` pins the keys; `ui.jsonRow` stays a separate
type from `ui.Row` for that reason, so a field added for rendering cannot quietly become
part of the published document. `lydite/actions` greps for a
`^\[(PASS|FAIL)\] <name>$` shape this grammar does not produce, and therefore matches nothing
— cutting it over to `--json` is a change in that repository. **Do not reintroduce a bracketed
text mode to accommodate it.** A text-scraping consumer forces every refinement to the human
surface through a synchronised release in another repo, which is the coupling `--json` exists
to remove.

**stdout is the report; everything else goes to stderr.** `internal/gitstate`'s git plumbing
runs through `executil.RunQuiet`, which captures without streaming — `git rev-parse` printing
two SHAs into the middle of a report was how this was found. Scanners still stream live
through `executil.Run`, because their findings are the point; under `--json` the commands
call `executil.StreamTo(os.Stderr)` so the document stays parseable and the findings still
reach the terminal and the CI log. Warnings have always gone to stderr and still do.

Errors are not verdicts. A command that cannot reach an answer returns an `error`, and
`main.go` prints `lydite: <err>` and exits 1; a command that reached one returns
`ui.ExitError`, which `main.go` exits on silently because the report already said what
happened. Every subcommand sets `SilenceUsage`/`SilenceErrors` so cobra does not print a
flag list under a failed gate.

## Referral: `lydite review`

`lydite review` decides whether a change may merge unattended. It runs no check — no
scanner, no test suite — so almost nothing in it is a failure: it emits pass or refer, and a malformed
exemptions file or an unresolvable merge-base is an error, exit 1. See
[ADR 0013](docs/adr/0013-referral-not-approval.md) for the model and
[ADR 0014](docs/adr/0014-evidence-only-referral-matching.md) for what it matches on.

The model is an allowlist and the default is to refer. `.lydite/exemptions.yml` at the scan
root declares shapes of change that merge unattended; with no file, or an empty one, every
change is referred, which is the correct day-one state rather than a broken one.

```yaml
exemptions:
  - name: readme-only
    # reason is required: this file is the entire risk model, and a diff of
    # bare globs is not reviewable.
    reason: >
      Prose in the top-level README changes nothing executable and no build
      step reads it.
    paths: ["README.md"]
disqualifiers:
  # Only ever ADDS to the built-in set. A veto list that can be emptied is
  # not a floor.
  paths: ["infra/**"]
```

Five properties are load-bearing and easy to weaken by accident:

- **The file is read from the merge-base, never from the branch.** A change that widens the
  gate gets no benefit from its own widening — otherwise one pull request declares itself
  exempt. `loadExemptionsAt` does this with `git show <base>:<path>`.
- **All-or-nothing, against a single exemption.** Every changed path must be covered by one
  exemption. Matching on *any* path would let an agent staple a README tweak onto a dangerous
  change; matching on the *union* of two exemptions would mean adding a narrow entry silently
  widens every existing one, destroying `git log .lydite/exemptions.yml` as the readable
  record of widenings that #15 exists to protect.
- **Disqualifiers veto any match, and the built-in set cannot be removed.** A net-new
  suppression annotation, a newly skipped or `.only`-focused test, a removed test — deleted,
  gutted, or renamed out of a test path, since `git mv foo_test.go foo_disabled.go` takes it
  out of the runner's view leaving nothing deleted and no hunk to read — an edit to
  any file under `.lydite/`, an edit to `.github/workflows/`, and an edit to
  `.gitattributes`. The suppression list carries the whole-file and whole-crate forms
  (`#![allow(`, `#[expect(`, `@ts-nocheck`, `//nolint`, `//go:build ignore`) alongside the
  per-line ones, because the broad form is strictly more powerful than the narrow one it would
  otherwise be the only one caught. `.gitattributes` is there because a `-diff` attribute
  replaces a hunk body with `Binary files ... differ`, and git reads that attribute from the
  branch — the diff is passed `--text` so the trick does not work, and the edit is referred
  anyway.
- **Everything matched on is evidence off the diff.** Nothing an author asserts about their
  own change may earn an exemption or clear a disqualifier — ADR 0013's `!` conventional-commit
  marker is deliberately *not* implemented, because nothing detects an undeclared API break and
  a claim-based veto works only for the author who would have declared anyway. The rule for any
  future addition: an author-controlled claim may add a referral, never remove one.
- **A change that edits the exemption set may edit nothing else**, and this is the one thing
  `review` reports as a *failure* rather than a referral: the author clears it by splitting the
  change in two, which is work they can do, and that is exactly what separates a gate from a
  referral. Two properties already protect the file — the merge-base read, so a change gets no
  benefit from its own widening, and the disqualifier, so such a change is always referred —
  and neither closes the realistic attack, which is not a forged exemption but an unremarkable
  one riding along in a large change approved for its other contents. What isolation buys is
  that `git log .lydite/exemptions.yml` becomes the complete, reviewable record of every
  widening. `.lydite/config.yml` deliberately carries no such requirement: report paths change
  alongside code for honest reasons, and a rule that fires on ordinary work gets relaxed later.
- **An absent exemptions file and an unreadable one are different questions**, asked
  separately: `git cat-file -e` answers whether it is there, and only then does `git show` read
  it. Collapsing them would make a broken read indistinguishable from an empty allowlist, which
  is safe only while the allowlist *is* empty and the safe answer happens to coincide.
- **The diff fails closed when it cannot be read.** A patch line longer than the scanner's
  buffer — `--text` renders a binary blob or a minified bundle as one — aborts the run rather
  than yielding an empty set of added lines, which would leave every content veto silent while
  the path list stayed complete.
- **Every git setting that decides what a diff says is pinned** (`core.quotePath`,
  `diff.relative`, `diff.mnemonicPrefix`). Each is settable in a global gitconfig lydite does
  not control, and `diff.relative` alone scopes the diff to `--dir` and strips the prefix from
  what survives — dropping `.github/workflows/` out of a monorepo's paths entirely.
- **The diff covers the whole repository, not just `--dir`.** Unlike `internal/coverage`, which
  scopes its diff with git's `--relative`, referral decides whether a *pull request* needs a
  human, and a workflow edit outside a monorepo's scan root is exactly what must not slip past.
  `--dir` only locates the exemptions file.

Path patterns are **repository-root-relative and anchored**: `README.md` matches the README at
the repository root and nothing else, any-depth matching is spelled `**/README.md`, and a
monorepo scanned with `--dir source` writes `source/README.md` — the diff is not scoped to
`--dir`, so the paths carry no prefix stripped. This parts company with gitignore on
purpose — floating patterns are right for a skip list where over-matching is free, and wrong
for a list that decides what merges without a human. The syntax itself is
`internal/pathmatch`, shared with the orphan gate's excludes; which root a pattern is relative
to belongs to each caller, and only referral's is the repository root. Unknown YAML keys are rejected rather
than ignored, for the reason `config.validateLinter` rejects `linter: eslint`.

The verdict is computed from `<merge-base>..HEAD`, so the local answer and the CI answer come
from identical inputs. A dirty working tree gets its own row saying the uncommitted work was
excluded, because silently deciding on HEAD while the developer is looking at edited files is
the one way this command gives a confidently wrong answer.

## Clearance: `/lydite clear`

A referral is resolved by a person commenting on the pull request, never by the author
pushing more code. `lydite clearance` is that surface and `internal/clearance` is its
decision; `internal/forge` is the only thing that talks to the platform. See
[ADR 0015](docs/adr/0015-clearance-binds-to-a-commit.md).

`review --publish` records the verdict as the **`lydite/referral` commit status** and as
the pull request's standing comment. The status is the whole record: a clearance is a
state change on that context at one commit, and nothing else stores it.

Six properties are load-bearing:

- **A clearance names one commit.** Not the tree, and not the shape of the verdict. Any
  push produces a new head carrying no status, so the clearance evaporates with no state
  of its own — including after a rebase that changes nothing. Both alternatives require
  lydite to hold that two revisions are the same change, and a clearance resting on that
  inference is one the inference can be wrong about.
- **A referral publishes `pending`, never `failure`.** A required check blocks on
  anything but `success`, so this softens nothing; it is the accurate word, and a gate
  fails where a referral does not. `stateFor` is the single place that mapping lives.
- **The status is read before it is written.** Re-running `review` after a clearance
  comment cannot change the answer — a referral re-evaluated is still a referral — so the
  only thing recomputation would buy is telling a referral apart from the isolation gate,
  and the standing status already carries that. Reading it means a `failure` is not
  clearable by comment, and that no pull-request content is fetched at comment time.
- **The commenter must have push permission**, read from the platform rather than from
  the comment. The repository is public, so without it any account could clear anything.
  This is a floor and not the whole trust — whoever holds the credentials satisfies it,
  which is what [#25](https://github.com/lydite/lydite/issues/25) closes.
- **A head that moved after the comment is refused.** A status created after the comment
  cannot be one the person read. Both timestamps are the platform's, so neither is the
  author's to set. `/lydite clear <sha>` names a revision explicitly and overrides the
  ordering, since naming it is the stronger statement.
- **Only the first line of a comment is parsed.** A reply that quotes an earlier comment
  carries its text, and scanning the whole body would let a quoted `/lydite clear` clear
  a change nobody meant to.

`.github/workflows/lydite-clearance.yml` runs on `issue_comment`, so it always executes
the **default branch's** copy and the deciding logic is never the pull request's to edit.
Nothing in it checks out the pull request's code, and nothing in it may start to: the job
holds a writing token. Its job-level `if:` is evaluated before a runner is allocated, so a
comment not addressed to lydite costs nothing rather than costing a short job.

**The status blocks no merge yet.** Making it a required check is one field on a ruleset
`gt` renders and hardcodes to `ci-gate` alone, so it cannot be set from this repository —
see [#34](https://github.com/lydite/lydite/issues/34). Until then the loop is published
and flipped correctly and enforces nothing, which is a temporary state rather than the
design. `/lydite exempt` is likewise held back, in
[#33](https://github.com/lydite/lydite/issues/33), because what it should emit is
undecided rather than unbuilt.

The pull-request comment follows `docs/design/reference/surfaces.dc.html`: verdict badge,
one-sentence headline, a `Check / Head / Base` table, named list sections, and a footer
rule. Which facts fill the columns is lydite's to choose — a referral has no
measurements, so the head column carries what the change contains and the base column
what was read out of the merge-base. The design's footer also claims parity with the
reader's local run; **nothing can establish that yet** (see #27), so it is absent rather
than asserted. `ui.Marker` identifies the standing comment for editing, by a marker in the
body rather than by author, because the author is whoever's token posted it.

## Semgrep: token-bearing vs token-less runs

`internal/semgrep.Check` picks its subcommand from whether `SEMGREP_APP_TOKEN` is set: `semgrep ci`
(diff-aware, applies the org's platform policy, uploads to the AppSec Platform) when it is, plain
`semgrep scan --config <ruleset> --error` when it isn't. Those two modes disagree about **scope**,
and that disagreement was a standing CI defect: GitHub deliberately withholds repo secrets from
`dependabot[bot]` events, so every Dependabot PR arrived with an empty token, silently fell back to
a *whole-repo* scan, and blocked on the consuming repo's pre-existing findings — findings no
token-bearing run had ever reported, in code the PR never touched. Whether a PR was green depended
on who opened it.

`lydite scan --diff-base <ref>` closes that gap: in scan mode it passes Semgrep
`--baseline-commit`, so the fallback blocks on the same thing `semgrep ci` would — what the change
introduces — and nothing else. `--diff-base auto` resolves the merge-base with `origin/main` via the
same `internal/gitstate.BaseSHA` the coverage gate already uses, so a PR's scan and its coverage
agree on what "this change" means. `action.yml` passes `auto` on every `pull_request` event.

Two deliberate choices in `cmd/lydite/scan.go`'s `resolveDiffBase`:

- **A token short-circuits it entirely** — `semgrep ci` already scopes itself to the diff, so
  resolving a merge-base would cost a `git fetch` nothing reads, and would newly demand a
  full-history checkout from token-bearing consumers that don't need one today.
- **An unresolvable `auto` is an error, not a silent full scan.** Falling back would reintroduce
  the exact surprise the flag exists to remove: a scan that quietly widens its own scope. A shallow
  checkout is a fixable misconfiguration (`fetch-depth: 0`), so lydite says so and fails.

Default (`--diff-base` empty) is still a full-repo scan — that's what a local `lydite scan` wants,
and it's what a push to `main` gets.

Restoring `semgrep ci` on Dependabot PRs (for the platform dashboard's sake) is a *consumer-side*
option, not a lydite one: the token has to be added to the repo's separate **Dependabot secrets**
store (`gh secret set SEMGREP_APP_TOKEN --app dependabot`), since Actions secrets are not visible to
Dependabot events. It is not required for CI to be green — the diff-aware fallback above is — and it
does hand an upload token to a workflow that executes the bumped dependency's code, so it's a
per-repo judgment call.

## The `action.yml` composite action

**It still invokes the removed `lydite coverage`, and its output parser does not match lydite's
output.** Both are changes in `lydite/actions`: the coverage gate is now `lydite test
--gate-coverage`, and the action should pass `--base-branch` from `GITHUB_BASE_REF` on
`pull_request` events, which is what actually fixes stacked pull requests.

**Its output parser does not match lydite's output.** `tool_result()` matches
`^\[(PASS|FAIL)\] <name>$`, a shape `internal/ui` does not produce — see "Output grammar and
`--json`" above. It has to read `--json` instead. Reintroducing a bracketed text mode here is
the wrong direction: it is what couples the human surface to a parser in another repository.

Unlike `inforge`'s action (install-only — its invocations vary too much per call site to bake in),
lydite's usage is uniform enough (`.lydite/config.yml` already carries all the config) that the action
owns the whole install → run → report flow: install lydite, run `scan`/`test` (each toggleable
independently), post one sticky PR comment summarizing both (upsert,
not a fresh comment every run — via `marocchino/sticky-pull-request-comment`), and optionally
upload to Codecov (non-blocking, purely for its dashboard/history) and/or switch lydite's own
Semgrep check into `semgrep ci` mode (diff-aware + uploads to the Semgrep AppSec Platform) when a
`SEMGREP_APP_TOKEN`-equivalent input is supplied. The Codecov upload is two `codecov/codecov-action`
invocations sharing the same `codecov-token` gate — one `report_type: coverage`, one
`report_type: test_results` — both relying entirely on that action's own recursive workspace
auto-discovery rather than lydite passing explicit `files:`/`directory:` paths itself. This is
why a consumer's CI only needs to hand lydite a token: lydite owns the whole Codecov
relationship (coverage *and* JUnit test-results), so the calling workflow never has to install a
Codecov CLI or push to Codecov directly itself.

The PR comment's header embeds `assets/lydite-mark-64.png` by **absolute raw URL**
(`raw.githubusercontent.com/lydite/lydite/main/...`), never a repo-relative path — the comment is
posted into the *consuming* repo's PR, where a relative `assets/...` would resolve against that repo
and 404. It's pinned to lydite's default branch, not a release tag, so the image keeps resolving for
consumers pinned to an older lydite version. Renaming or moving that file therefore breaks the logo
in every consumer's PR comment retroactively — treat its path as a public API.

**Never interpolate `${{ inputs.* }}` or `${{ steps.*.outputs.* }}` directly into a `run:` script
body** — pass it via that step's `env:` block instead, and reference the env var name (`"$DIR"`,
not `"${{ inputs.dir }}"`) inside the script. Semgrep's own `yaml.github-actions.security.run-shell-injection`
rule caught this exact mistake once already (see git history) — expression interpolation directly
into a shell script is a real script-injection vector if the interpolated value could ever contain
shell metacharacters, regardless of how trusted the input value looks today. `if:` conditions and
`with:` blocks on a `uses:` step are fine to interpolate directly — only `run:` script bodies are
the risk, since that's the only place text gets spliced into something a shell then executes.

## Design

The brand and design system live here rather than in a `lydite/brand` repository, because
every consumer of them is already in this repository — see
[ADR 0012](docs/adr/0012-design-system-in-the-monorepo.md). The split that matters is
inside the tree, not across repositories:

- `assets/` is **what ships**. Production SVGs, plus `lydite-mark-64.png`, whose path is a
  public API (above).
- `docs/design/tokens.md` is the token set and the surface specifications.
- `docs/design/reference/` is **reference only** — the `.dc.html` prototypes and the logo
  construction proofs. The prototypes use a custom runtime and inline styles, both
  artefacts of the authoring environment: do not port the runtime, and do not copy the
  styles into `web/`.

Two things in `tokens.md` are documented but **not implemented**, and neither should be
mistaken for a description of current behavior:

**The CLI output grammar is implemented**, in `internal/ui`, and every command renders
through it — glyphs, leader dots aligning the value column at 34 characters, `--no-color`,
and a verdict-plus-duration last line. `lydite/actions` parses a
`^\[(PASS|FAIL)\] <name>$` shape this grammar does not produce, and therefore reads nothing;
cutting it over to `--json` is a change in that repository. Do not restore the bracketed text
form to accommodate it — a text-scraping consumer makes every refinement to the human surface
a two-repository release.

**There is no light product theme and no responsive design.** The light token ramp exists
and the PR comment uses it, but no product screen has been drawn light, and nothing below
1240px has been drawn at all. `docs/design/README.md` records both as gaps. Inventing
either from the token table is how a design system starts lying about its own coverage.

## Release notes

`release.yml` assembles the GitHub release body's header from
`docs/release-notes/_header.md` (the invariant install block — it lives there
rather than in `.goreleaser.yml`'s `header:` so the workflow can extend it) plus
`docs/release-notes/<tag>.md` when that file exists, passes it with
`--release-header`, and goreleaser appends its commit-derived changelog beneath.

A missing per-version file is deliberately not an error. Most patch releases are
fully described by their commit subjects, and failing a release over a file it
never needed would make every release depend on remembering a step — the same
failure the major-alias move in that workflow was automated away to prevent.
Write one when the release carries what a commit subject cannot: a change in
what an existing number or verdict *means*, an upgrade step consumers must take,
or a new major. `docs/release-notes/v2.0.0.md` is the worked example, and the
file has to be on `main` before the tag is pushed — the workflow reads it out of
the tagged tree.

## Conventions

- **Version injection:** `cmd/lydite` exposes `var version = "dev"`, overridden at release via
  `-ldflags "-X main.version=<tag>"`. Keep that variable name and package stable.
- **goreleaser & golangci-lint both use the v2 config schema.** In golangci-lint v2, `gosimple` is
  part of `staticcheck` — don't add it as a separate linter (it will error).
- Lint must pass with zero issues; `errcheck` is on, so check returned errors.

## Boundaries

- **Always:** run `go build ./...`, `go test -race ./...`, and `golangci-lint run ./...` before
  proposing a PR.
- **Ask first:** changing the Go version, renaming the binary/`cmd` dir, altering the release
  archive layout, or editing CI.
- **Never:** introduce cgo, commit `dist/` or secrets, or skip the lint/test gates.

## Worktrees

This repo uses a bare-repo + typed-worktree layout managed by the `gt` CLI — one session, one
`gt wt add <type/name>` worktree; never use raw `git worktree` or edit inside `.bare/`.
