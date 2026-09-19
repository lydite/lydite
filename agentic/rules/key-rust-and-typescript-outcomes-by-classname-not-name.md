# Key a Rust or TypeScript test outcome by classname and name, never by name alone

`go test` scopes one process to one package, where the compiler forbids two functions sharing a
name, so `internal/junit.ReadOutcomes` is safe there. `cargo nextest` runs every binary in a
crate in one invocation and vitest every file in a component, and both write one JUnit report
over the lot — a bare name-keyed map merges two distinct tests that happen to share a name into
one entry holding the worse outcome, which lets a pass in one binary mask a flake in the other, or
reports a test as disagreeing with itself when two different tests actually did. Read a Rust or
TypeScript report with `internal/junit.ReadOutcomesByClass` (keyed by `ClassKey(classname, name)`)
wherever the comparison is per-test rather than a total count.

## Applies to

Any code that reads a `cargo-nextest` or `vitest` JUnit report to compare individual test
outcomes across runs — `internal/flaky` and anything added beside it. Does not apply to the
quality-history ledger's totals, which only need `internal/junit.Counts` and tolerate the merge.

## Example

```go
// wrong: collides `nextestprobe::a shared_name` with `nextestprobe::b shared_name`
outcomes, _ := junit.ReadOutcomesFile(report)
o := outcomes[test.Name]

// right: classname and name together are the test's identity outside Go
outcomes, _ := junit.ReadOutcomesByClassFile(report)
o := outcomes[junit.ClassKey(test.Classname, test.Name)]
```

Reasoning: [`agentic/references/components.md`](../references/components.md) and
[ADR 0041](../../docs/adr/0041-a-new-test-is-rerun-in-rust-and-typescript-too.md).
