---
description: "A stage reads another command's report document through a stage-owned interface and boundary types, never a package main import."
---

# Bridge another command's document through a stage-owned interface and boundary types, never a `package main` import

A stage that folds a report document another command owns — `lydite test`'s measurements,
`lydite mutation`'s mutant counts, `lydite scan`'s reporting — cannot import that document's type
directly: it is declared inside `cmd/lydite`, `package main`, which no `internal/stages/*` package
can import at all, and even where it could, a document's owning type changes on its owner's own
schedule, for its owner's own reasons — a stage typed on it directly would change every time the
owner does, whether or not the fields the stage reads moved. Declare an interface in the
consuming stage's own package (`recordstages.ReportReader` is the model: `ReadMeasurements`,
`ReadScan`, `ReadMutants`, `FoldMeasurements`, `FoldMutants`), typed entirely in boundary structs
the stage package itself declares and that hold only the fields a stage actually reads. The CLI
implements the interface by calling the owning code and converting its answer; every read error
passes through the conversion unchanged, so a missing document's error text reaches a row byte for
byte.

## Applies to

Any stage under `internal/stages/*` that needs to read a report document owned by a different
`cmd/lydite` command's flow.

## Example

```go
// wrong: a stage imports the owning command's own document type
package recordstages

import "lydite/lydite/cmd/lydite" // package main — cannot be imported at all

func ReadReports(ctx context.Context, in ReadReportsIn) (ReadReportsOut, error) {
    var doc main.measurementsDoc // does not compile
    ...
}

// right: a stage-owned interface and stage-owned boundary types
package recordstages

type ReportReader interface {
    ReadMeasurements(dir string) (Measurements, error)
    ReadScan(dir string) (Scan, error)
    ReadMutants(dir string) (Mutants, error)
    FoldMeasurements(dirs []string) (Measurements, error)
    FoldMutants(dirs []string) (Mutants, error)
}
```

Reasoning: [ADR 0069](../../docs/adr/0069-a-recordings-history-is-a-deferred-closure-and-its-inputs-cross-a-boundary-type.md)
and [`agentic/references/architecture.md`](../references/architecture.md#record-one-condition-a-deferred-write-and-a-boundary-type-over-another-commands-document).
