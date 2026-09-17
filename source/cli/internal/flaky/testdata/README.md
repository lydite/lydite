# The flaky probe and what two runs of it reported

These are the evidence
[ADR 0039](../../../../../docs/adr/0039-a-new-test-is-rerun-once-in-its-own-process.md)
decides on: a Go module carrying one new test that agrees with itself, one new test that does not,
and one test that is not new — plus the verbatim output of the two runs the gate performs over it.
A test that wants a report reads one of these rather than writing its own, since a hand-written
JUnit matches its author's reading of the format, which is the one thing it must not be evidence
for.

Source files under `flakyprobe/` and `seedprobe/` carry a `.txt` suffix and are materialised by
`internal/fixture`, whose doc comment says why. The captured reports and transcripts at this
directory's top level are data, not source, and carry their own extensions — nothing materialises
them, so nothing strips a suffix from them.

## The probe

`flakyprobe/base/` and `flakyprobe/head/` are one module at two revisions: the same `go.mod` and
the same `probe.go`, and a `probe_test.go` that gains three tests between them. That is what makes
the pair a fixture for the set difference — `base` declares `TestPreExistingStable` alone, and
`head` adds `TestDeterministicNew`, `TestFlakyNew` and `TestNewSubtests`.

`TestFlakyNew` is deterministically flaky. It passes in the process that finds no
`second-run.marker` beside it and leaves one behind, and fails in the next process, which finds
it — a `rand` or a clock here would make the fixture proving the gate the gate's own first false
positive.

`TestNewSubtests` builds its two `t.Run` names from a slice, which is why the reports below record
`TestNewSubtests/one` as well as `TestNewSubtests` and why the ADR makes the top-level function
the unit.

## How the two runs were captured

With go1.26.5 darwin/arm64 and the gotestsum version `internal/runner` pins, from `head/`, with no
marker present and an empty test cache:

```sh
gotestsum --format pkgname --junitfile run1-suite.xml -- \
  -coverprofile=.lydite-reports/coverage/coverage.out -coverpkg=./... -race ./...
gotestsum --format pkgname --junitfile run2-rerun.xml -- \
  -run '^(TestDeterministicNew|TestFlakyNew|TestNewSubtests)$' -count=1 -race .
```

`run1-suite.xml` and `run1-suite.txt` are the first: six test cases, no failure, exit 0.
`run2-rerun.xml` and `run2-rerun.txt` are the second: five test cases and `TestFlakyNew` failed,
exit 1. Between them, `TestFlakyNew` is the one new test whose outcomes disagree, and
`TestDeterministicNew` and `TestNewSubtests` are the ones that agree. `TestPreExistingStable` is
absent from the second entirely, which is what a filter to the new tests means.

The reports carry a `timestamp` and a `go.version`, both of the machine that captured them. A
parser test reads the `<testcase>` elements, as `internal/junit` already does.

## `cached-rerun.txt`

Three invocations over `head/`, the second identical to the first. It answers `(cached)` and runs
nothing, which is why the gate's second run carries `-count=1` — and why that rule is the mirror
image of the one ADR 0027 states for mutation, where the same cache is what makes mutation
affordable.

## `map-iteration-order.txt`

`seedprobe/` prints the order a six-entry `map[int]string` — one hash bucket — iterates in, twice
per process. Over twenty processes it produced six distinct orders, every one a rotation of the
same cycle and six of them ascending; under `-test.count=2`, three of six processes drew the
identical order in both passes.

It is the measurement behind ADR 0039's refusal to fold the rerun into run 1 as `-count=2`: a
second iteration inside one process is not a second sample of anything that process fixed once.
The numbers are of one machine and one Go version and are recorded because the decision rests on
them being measured rather than assumed. `seedprobe` prints and asserts nothing, so it is evidence
rather than a test — an assertion about map order is the flake, not the check for it.

## `nextestprobe/` and `vitestprobe/`

The Rust and TypeScript probes
[ADR 0041](../../../../../docs/adr/0041-a-new-test-is-rerun-in-rust-and-typescript-too.md) decides
on. Each holds the shapes a language's test names can take that the repository's own suites do not:
a name declared in two nextest binaries and a title declared in two vitest files, a nested `mod` and
a nested `describe`, `#[tokio::test]` and `#[ignore]`, a `test.each`, a template-literal title, and
a title made of regex metacharacters.

Their captured reports are in [`../../junit/testdata/`](../../junit/testdata/), whose README says
what each one shows and how it was taken. Sources carry a `.txt` suffix for the reason
`flakyprobe/`'s do.

## `nextest-tool-config-shadowed.txt`

Two runs over `nextestprobe/` given a `.config/nextest.toml` of its own declaring
`[profile.default.junit] path = "repo-owned.xml"`, with lydite's tool config passed beside it. The
repository's file wins for the profile it declares and only that one: the default profile writes
`target/nextest/default/repo-owned.xml` and no `junit.xml`, while `rerun` still writes lydite's
`junit-rerun.xml`. It is why a shadowed profile is a named `unmeasured` reason rather than a report
lydite goes looking for and does not find.
