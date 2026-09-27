# `lydite test` Flow migration (#270): per-symbol category table

Working note for tasks 2-6 of `handoff/20260927-flow-test.md`. Every function, method, type,
const and var declared in `cmd/lydite/test.go`, `coverage.go`, `measurements.go` and
`toolchain.go` has exactly one row. Call sites were resolved with `go/types` over the
package's test variant (every `_test.go` in `cmd/lydite` included), so a name in a comment or a
same-named local variable is never counted as a call site.

"Unowned" means any `cmd/lydite` file outside this session's ten: `test.go`, `coverage.go`,
`measurements.go`, `toolchain.go`, `test_test.go`, `coverage_test.go`, `crap_test.go`,
`measurements_test.go`, `history_test.go`, `affected_test.go`.

## Legend

| Cat | Meaning | What happens to the `cmd/lydite` declaration |
|---|---|---|
| **1a** | A type whose unexported field or method an unowned file reaches, or a method of it an unowned file calls | Stays, byte-unchanged |
| **1b** | Bound to a 1a type: constructs its private state, is its field type or implementation, or has a 1a type in its signature with no translation that keeps the same signature | Stays, byte-unchanged |
| **1c** | CLI layer (cobra, flag parsing, rendering) or a package-wide helper that is not test logic | Stays; nothing to move |
| **2** | Called by an unowned file and movable: the real logic moves to `internal/test/*`, a same-signature wrapper stays | Body replaced by a call into the new package |
| **3** | Must exist twice, with a stated drift guard | See "Category 3" below; no owned symbol lands here |
| **4** | Nothing outside `lydite test`'s own command calls it | Deleted in task 6; its logic lives on in the new package or stage it moved to |

Destination shorthands: **M** `internal/test/measure`, **D** `internal/test/measurements`,
**R** `internal/test/run`, **S** `internal/stages/test`, **L** the lowest of these (see
"Import-cycle constraint" below).

"Wrapper adapts" marks a category-2 row whose old signature cannot be the new function's own,
because it carries `*ui.Report`, `*cobra.Command`, or a 1a type. Nothing below the CLI may import
cobra or `ui.Report` (`architecture.md`), so the new function takes an `io.Writer` or returns
rows, and the wrapper in `cmd/lydite` does `rep.Add` / `cmd.ErrOrStderr()` / field extraction.

## Summary

| Category | Count |
|---|---|
| 1a | 8 |
| 1b | 15 |
| 1c | 6 |
| 2 | 53 |
| 3 | 0 owned symbols (4 external items, listed below) |
| 4 | 82 |
| **Total** | **164** |

## `measurement` and `patchPart` are alias-viable: yes

`merge.go` never calls `.scorable()`. Its only scorable check is `scorableLang(langOf(c))` at
`merge.go:284`, two plain functions. Every unowned reach into `measurement` is an exported field
(`Name`/`Dir`/`Lang` in the `mutation.go:595` composite literal, `Unmeasurable` at `merge.go:307`,
`Hits`/`CRAP` in `findings_test.go`) or the type name itself (`merge.go:232,254`,
`findings_test.go:27`). `patchPart` has no methods at all; `merge.go:235` names it and
`patchPartOf` builds it by exported field. No unowned file declares a method on either type.

So `type measurement = measure.Measurement` and `type patchPart = measure.PatchPart` compile
against every unowned file unchanged, and the 1a methods that return them (`asMeasurement`,
`patchPartOf`) stay byte-unchanged. `composedRows` and its neighbours therefore move as real
logic, and none of them is duplicated.

The unexported methods `crapEntry`, `scorable` and `entry` can no longer be called from
`cmd/lydite` once the alias lands. Nothing unowned calls them. Two owned tests do:
`coverage_test.go:1162` (`m.entry()`) and `measurements_test.go:112` (`carried.entry()`).
Whichever task introduces the alias has to edit those two tests in the same change, or the
build breaks.

## Table

### `test.go`

| Symbol | Cat | Dest | Reason and unowned call sites |
|---|---|---|---|
| `newTestCmd` | 1c | — | Cobra command; `root.go:41`. Task 6 rewrites `RunE` in place |
| `testLabel` | 2 | R | `merge.go:97,218`; `merge_test.go:854,865,898,908` |
| `declaresNoSuite` | 2 | R | `merge.go:116,192,299`; `mutation.go:401`; `mutation_merge.go:129`; `plan.go:172,238` |
| `noSuiteReason` (const) | 2 | M | `const noSuiteReason = measure.NoSuiteReason`. `merge.go:300`; `merge_test.go:854,871`; `mutation_test.go:2131`. Goes to M because `noSuiteCoverageRows` (M) reads it |
| `noSuiteTestRow` | 2 | R | `merge.go:98`; `mutation_merge.go:130` |
| `noSuiteFlakyRow` | 2 | R | `merge.go:107`; `merge_test.go:855`. Takes `unexaminedRow` and `flakyLabel` with it |
| `noSuiteCoverageRows` | 2 | M | `merge.go:118`. Wrapper adapts (`*ui.Report`): the new function returns rows |
| `withSuites` | 4 | S | Owned callers only |
| `addTestCoverageRows` | 4 | S | Owned callers only (`test_test.go`) |
| `defaultConcurrency` (const) | 1c | — | The `--concurrency` flag default; `mutation.go:217` |
| `resolveConcurrency` | 1c | — | Flag parsing, and its errors name `--concurrency`; `mutation.go:105`. The rewritten `RunE` keeps calling it and hands the flow an `int` |
| `componentPlan` | 1a | — | `mutation.go` reaches `.c` (401,410,421,456,535,623), `.log` (392,535,623), `.ports` (819), `.row` (405), `.ready` (404); `mutation_test.go:88,91,362,388,469,2033` build it with unexported keys; `plan_test.go:302` reads `.log` |
| `runComponents` | 4 | R | Called only by `measureBaseTree` (moves to S) and owned tests |
| `runComponentsGated` | 4 | R | Called only from `RunE` |
| `itemFor` | 2 | R | `mutation.go:414`; `plan_test.go:303`. Wrapper adapts: unpacks `p.c` and `p.ports` |
| `planComponents` | 2 | R | `mutation.go:390`; `plan_test.go:301`. Wrapper adapts, the hardest seam; see "planComponents" below |
| `scheduleRow` | 2 | R | `mutation.go:431` |
| `runComponent` | 4 | R | Owned callers only |
| `withTestCounts` | 4 | R | Owned callers only |
| `clearReport` | 2 | R | `mutation.go:632`; `mutation_test.go:1373,1385`. Needs a private copy of `ignoreReports` (category 3, trivial) |
| `failure` | 2 | R | `mutation.go:633`. Wrapper adapts: passes `log.Rel` in place of `*componentLog` |
| `componentLog` | 1a | — | `mutation.go:662,707,715` read `.out`; `mutation.go:1058,1266` and `mutation_test.go:28,1429` name it, and `mutation_test.go:1429` builds `&componentLog{}`. `.Rel` (exported) is read in `mutation.go:1069-1270` and `mutation_test.go` |
| `openLog` | 1b | — | Builds `componentLog`'s unexported fields; `mutation_test.go:30`. Stays as the log opener the CLI injects into R |
| `(*componentLog).streamed` | 1b | — | No unowned caller (the handoff says otherwise; see "Premise corrections"). Only `openLog` calls it; it rides with the type |
| `stderrMu` (var) | 1b | — | Implementation of `componentLog`'s stderr mirror |
| `partialLineDelay` (const) | 1b | — | Same |
| `prefixWriter` | 1b | — | The type of `componentLog.mirror`; moving it changes a 1a struct's field type |
| `(*prefixWriter).Write` | 1b | — | Same |
| `(*prefixWriter).arm` | 1b | — | Same |
| `(*prefixWriter).disarm` | 1b | — | Same |
| `(*prefixWriter).partialLineDelay` | 1b | — | Same |
| `(*prefixWriter).Flush` | 1b | — | Same |
| `(*prefixWriter).flush` | 1b | — | Same |
| `(*prefixWriter).emit` | 1b | — | Same |
| `(*componentLog).Close` | 1a | — | `mutation.go:392`; `mutation_test.go:31`; `plan_test.go:302` |
| `tailLines` (const) | 2 | R | Const alias. `publish.go:268`; `publish_test.go:648,649,677,684,685` |
| `tail` | 2 | R | `mutation.go:669`; `reports.go:225` |
| `startServices` | 2 | R | `mutation.go:638`. Wrapper adapts: unpacks `p.stack`, `p.c.Name`, `p.log.Rel` |
| `runCommands` | 2 | R | `mutation.go:644,649`. Wrapper adapts: passes `log.out` and `log.Rel` |
| `prepare` | 2 | R | `mutation.go:584,635`. Wrapper adapts, as `runCommands` does |
| `prepareCommand` | 4 | R | Only `prepare` calls it, so it moves with `prepare` |
| `installsNodeDeps` | 4 | R | Owned callers only |
| `installHint` (const) | 4 | R | Owned callers only |
| `installLabel` | 4 | R | Owned callers only |
| `installNote` | 4 | R | Owned callers only |
| `invocation` | 2 | R | `mutation.go:566`; `mutation_test.go:2024` |
| `invocationFor` | 4 | R | Owned callers only |
| `flakyLabel` | 2 | R | `merge.go:107,218`; `merge_test.go:865` |
| `flakyGate` | 4 | R | The flaky engine (ADR 0062); owned callers only |
| `newFlakyGate` | 4 | R | Owned only. Needs a private `shortSHA` (category 3, trivial) |
| `(*flakyGate).gates` | 4 | R | Owned only |
| `gatedRunner` | 4 | R | Owned only |
| `(*flakyGate).run` | 4 | R | Owned only; takes `*componentLog` today, the injected writer in R |
| `(*flakyGate).examine` | 4 | R | Same |
| `flakyRerunner` | 4 | R | Owned only |
| `flakyNames` | 4 | R | Owned only |
| `flakyFiles` | 4 | R | Owned only |
| `relPackage` | 4 | R | Owned only |
| `(*flakyGate).report` | 4 | R/S | Owned only; takes `*ui.Report`, so the new form returns rows and findings |
| `flakyRow` | 4 | R | Owned only |
| `flakyFinding` | 4 | R | Owned only |
| `flakyName` | 4 | R | Owned only |
| `outcomeOf` | 4 | R | Owned only |
| `flakyGap` | 4 | R | Owned only |
| `componentPaths` | 4 | R | Owned only |
| `unexaminedRow` | 4 | R | Only `noSuiteFlakyRow` and the flaky engine call it; it moves with them |
| `childEnv` | 2 | L | `mutation.go:653,685,710`; `review_apisurface.go:37`; `scan.go:167`; `scan_test.go:957,1073,2521,2540`. Must sit below M, because M's `measure` calls it |
| `splitPath` | 2 | L | `scan.go:358` |
| `env` | 2 | L | `scan.go:358` |
| `componentUnits` | 2 | R | `mutation.go:133`; `review_apisurface.go:33`; `scan_test.go:456` |
| `orphanRow` | 2 | R | `plan.go:104` (`scan.go` names it only in a comment) |
| `renderReport` | 1c | — | Rendering; `merge.go:74`, `mutation.go:90,131,208`, `mutation_merge.go:70`. The rewritten `RunE` keeps calling it |
| `selectAffected` | 4 | S | Owned only |
| `affectedFrom` | 2 | R | `mutation.go:158` |
| `selectRow` | 2 | R | `mutation.go:163` |
| `watchRow` | 4 | S | Owned only |
| `intersect` | 2 | R | `mutation.go:162,165` |

### `coverage.go`

| Symbol | Cat | Dest | Reason and unowned call sites |
|---|---|---|---|
| `measurement` | 2 | M | Alias. `merge.go:232,254`; `mutation.go:595` (literal); `findings_test.go:27`. Fields reached: `Name`/`Dir`/`Lang` (`mutation.go:595`), `Unmeasurable` (`merge.go:307`), `Hits` and `CRAP` (`findings_test.go`) — all exported |
| `measurement.Measured` | 2 | M | Moves with the type; no unowned caller |
| `measurement.Scored` | 2 | M | Same |
| `measurement.crapEntry` | 2 | M | Moves with the type; owned callers only |
| `measurement.scorable` | 2 | M | Same. `merge.go` does not call it |
| `measurement.entry` | 2 | M | Same. Owned tests call it: `coverage_test.go:1162`, `measurements_test.go:112` |
| `scorableLang` | 2 | M | `merge.go:284` |
| `fromEntry` | 4 | M | Owned only |
| `unmeasuredComponent` | 2 | M | `merge.go:306,316` |
| `unmeasurableComponent` | 2 | M | `merge.go:300` |
| `langOf` | 2 | M | `merge.go:284`; `mutation.go:549,972,1279` |
| `measure` | 4 | M | Owned only (`runComponent`, `measureBaseTree`) |
| `score` | 4 | M | Owned only |
| `noComplexitySource` | 4 | M | Owned only |
| `producerOf` | 4 | M | Owned only |
| `coverageOptions` | 4 | S | A CLI options struct, so it may not move below the CLI; its fields become stage `In` fields |
| `addCoverageRows` | 4 | S | Owned only |
| `nameUnusedDeclarations` | 4 | S | Owned only; takes `*cobra.Command` |
| `inDeclarationOrder` | 4 | S | Owned only |
| `ungatedRows` | 4 | S | Owned only |
| `ungatedComponentRows` | 4 | S | Owned only |
| `ungatedComposedRow` | 2 | M | `merge.go:138` |
| `gatedRows` | 4 | S | Owned only |
| `reasonOnly` | 4 | S/D | Owned only; returns `measurementsDoc`, so its moved form builds the new document type |
| `candidateRow` | 4 | S | Owned only; writes `measurementsDoc` |
| `previousTreeBaseline` | 4 | S | Owned only |
| `baselineFor` | 4 | S | Owned only |
| `measureBaseTree` | 4 | S | Owned only (`scan.go` names it only in comments). Calls `runComponents`, so it has to sit above R |
| `baseTreeCRAP` | 4 | S | Owned only |
| `baseTreeBaseline` | 4 | S | Owned only |
| `crapRow` | 2 | M | `findings_test.go:47,50,53,63,83,89,224,441` |
| `crapFindings` | 4 | M | Only `crapRow` calls it |
| `crapValue` | 4 | M | Owned only |
| `worstOffenders` (const) | 2 | M | Const alias. `findings_test.go:441,443,444` |
| `worstFunctions` | 4 | M | Owned only |
| `crapSummaryRow` | 2 | M | `merge.go:156` |
| `crapSummaryOf` | 4 | M | Owned only |
| `carriedScore` | 4 | M | Owned only |
| `componentRow` | 4 | M | Owned only |
| `comparableBase` | 4 | M | Owned only |
| `producerName` | 4 | M | Owned only |
| `producerScope` | 4 | M | Owned only |
| `scopeChangeReason` | 4 | M | Owned only |
| `notCompared` | 4 | M | Owned only |
| `composedRow` | 4 | M | Only `composedRows` and owned code call it |
| `patchRows` | 4 | S | Owned only; takes `*cobra.Command` |
| `patchFindings` | 2 | M | `findings_test.go:176,187,206,348,468,472,490` |
| `composedRows` | 2 | M | `merge.go:132`. Wrapper adapts (`*ui.Report`): the new function returns rows |
| `patchPart` | 2 | M | Alias. `merge.go:235`; built by 1a `patchPartOf` using exported fields only |
| `composedPatchRow` | 4 | M | Only `composedRows` calls it |
| `patchRow` | 4 | M | Owned only |
| `scopeToComponent` | 2 | M | `mutation.go:595` |
| `hasExt` | 4 | M | Owned only |
| `floorRows` | 4 | M | Owned only |
| `floorSummaryRow` | 2 | M | `merge.go:159` |
| `candidateThisTree` | 4 | S | Owned only; returns `measurementsDoc`, so its moved form builds the new document type |
| `crapRecord` | 4 | M | Owned only |
| `recordable` | 2 | M | `merge.go:365`. Calls `record.go`'s `unmeasurableByDeclaration`, so M needs its own copy (category 3) |
| `sameEntries` | 2 | M | `record.go:719`. Generic, so the wrapper keeps the same type parameters |
| `recordingBlockedBy` | 4 | M | Owned only |
| `withToleratedDipsRestored` | 2 | M | `record.go:299` |
| `atPercentOf` | 4 | M | Owned only |
| `regressedBeyond` | 4 | M | Owned only |
| `composed` | 4 | M | Owned only |
| `gateable` | 4 | M | Owned only |
| `everything` | 2 | M | `merge.go:138`, passed as a func value; a wrapper func works |
| `repoLabel` | 2 | M | `merge.go:138,146,206`; `merge_test.go:119,364,470,878` |
| `lineValue` | 4 | M | Owned only |
| `composedValue` | 4 | M | Owned only |
| `unmeasuredRow` | 2 | M | `merge.go:146,168`; `mutation.go:551,591,597,606,664,667,680,687,691,697,718,1061` |
| `firstNonEmpty` | 1c | — | A general helper: `clearance.go:122`, `mergequeue.go:167,262,299`, `status.go:38`, and the 1b `foldMeasurements`. It is not test logic. A new package that needs it keeps a private copy, as `internal/stages/clearance` does with `shortSHA` |
| `newRemovedCoverageCmd` | 1c | — | Cobra command; `root.go:48` |

### `measurements.go`

| Symbol | Cat | Dest | Reason and unowned call sites |
|---|---|---|---|
| `measurementsName` (const) | 2 | D | Const alias `= measurements.Name`, so the filename is declared once. `merge.go:147,170`; `record.go:64,91,128`; `reports.go:177`; `merge_test.go:56,301,471` |
| `measurementsDoc` | 1a | — | `record.go:276` calls unexported `.snapshot()`. Also named by `fold.go:39`, `merge.go:231,273,351`, `record.go:247,328,551,732,957`, `merge_test.go` and `record_test.go` (composite literals) |
| `componentMeasurement` | 1a | — | `merge.go:312` `.asMeasurement(c)` and `merge.go:322` `.patchPartOf(...)`. `merge_test.go` builds it by literal. Its element type is what keeps `measurementsDoc.Components` local |
| `patchCount` | 1b | — | The type of `componentMeasurement.Patch` |
| `measurementsPath` | 4 | D | Only `writeMeasurements` calls it. Needs a private `reportsDir` (category 3, trivial) |
| `writeMeasurements` | 4 | D | Owned only (`candidateRow`, `history_test.go`); writes `measurementsDoc`. See "measurements.json" below |
| `readMeasurements` | 1b | — | Returns `measurementsDoc`; `fold.go:90`, `record.go:771`. The new engine never reads a document, so nothing needs to move |
| `foldMeasurements` | 1b | — | Takes and returns `measurementsDoc`; `merge.go:291`, `record.go:131`. Same reasoning as `readMeasurements` |
| `measurementsDoc.snapshot` | 1a | — | `record.go:276` |
| `measurementsFrom` | 4 | D | Owned only; builds `measurementsDoc` |
| `componentMeasurement.asMeasurement` | 1a | — | `merge.go:312` |
| `componentMeasurement.patchPartOf` | 1a | — | `merge.go:322` |
| `testCounts` | 4 | D/M | Owned only |

### `toolchain.go`

| Symbol | Cat | Dest | Reason and unowned call sites |
|---|---|---|---|
| `ensureToolchains` | 2 | R | `mutation.go:133`; `review_apisurface.go:33`; `scan.go:105`. Wrapper adapts: passes `cmd.ErrOrStderr()` in place of `*cobra.Command` |
| `toolchainOverrides` | 4 | R | Only `ensureToolchains` calls it |

## Category 3 (no owned symbol; four external items)

None of the 164 owned symbols is category 3. Moving category-2 logic out of `package main` still
means four things exist twice, because the moved code reaches helpers declared in *unowned*
files, or because the new engine writes a document whose type is 1a:

1. **The `measurements.json` schema: conditional.** It is category 3 only if a *stage* writes
   the file. In that case `internal/test/measurements` declares its own `Doc`/`Component`/
   `PatchCount` mirroring `measurementsDoc`/`componentMeasurement`/`patchCount` field for field
   and tag for tag. **Drift guard:** the golden fixture task 2 already plans. The new writer
   produces it; the old `readMeasurements` reads it back; re-marshalling through the old type
   must give the same bytes. Add a reflect check that the field names and JSON tags of the two
   type sets are equal. See "measurements.json" for the two designs that need no duplicate.
2. **`unmeasurableByDeclaration`** (`record.go:978`, unowned). `recordable` (2) and
   `candidateThisTree` (4, moving) call it. M needs a copy. **Drift guard:** an owned test in
   `coverage_test.go` asserting `unmeasurableByDeclaration(c) == measure.UnmeasurableByDeclaration(c)`
   for every `runner.Name` × {no args, declared args, a raw `command:`, an unknown runner}.
   This is the one that matters. `merge.go`'s `record` row and `record.go`'s
   `missingFromRecord` answer the same question, and a divergence makes the two disagree about
   what can be recorded.
3. **`ignoreReports` / `reportsDir`** (`reports.go:19,40`, unowned). `clearReport` (R) and
   `measurementsPath`/`writeMeasurements` (D) call them. These are trivial copies. **Drift
   guard:** an owned test asserting both `ignoreReports` implementations write byte-identical
   `.gitignore` files.
4. **`shortSHA`** (`review.go:330`, unowned). `newFlakyGate`, `baselineFor`, `measureBaseTree`,
   `baseTreeBaseline` and `candidateThisTree` call it. This is a trivial private copy; the
   `internal/stages/clearance/reply.go:257` copy is the precedent and has no guard.

## Constraints tasks 2-5 must design around

### Import-cycle constraint (`run` ↔ `measure`)

- `runComponent` (→ R) calls `measure`, `unmeasuredComponent`, `unmeasurableComponent` (→ M),
  so R imports M.
- `measureBaseTree` (→ S) calls `runComponents` (→ R), so it cannot live in M.
- `measure` (→ M) calls `childEnv`, so `childEnv`/`splitPath`/`env` cannot live in R. They go to
  M, or to a leaf package below both (**L** in the table).
- Resulting order: **L/M ← R ← S**, with D beside M. `baselineFor`, `gatedRows`,
  `measureBaseTree`, `candidateThisTree` and `addCoverageRows` go to S, which is where the
  handoff's task 4 already puts "coverage/CRAP gate composition". Nothing merge.go calls
  transitively reaches `measureBaseTree` or R, so every category-2 row function in M stays
  below R.
- Because `measureBaseTree` runs the whole engine inside a throwaway worktree, the injected
  log opener has to reach the coverage stage too, not only the run stage.

### `planComponents` (category 2 through a conversion wrapper)

The body constructs `componentLog` (1a) directly, both through `openLog` and as
`&componentLog{out: io.Discard, name, width}` for a component that declares no suite. The
approach that keeps it category 2:

- `run.PlanComponents(ctx, root, selected, kind, open)` takes an injected opener
  `open(c component.Component, width int, runs bool) run.Output`, where
  `run.Output{W io.Writer; Rel string}`.
- It returns `[]run.Plan{C, Out, Stack, Ports, Row, Ready}`.
- The `cmd/lydite` wrapper's opener calls `openLog` (or builds the discard log) and records each
  `*componentLog` by component name. It then rebuilds `[]componentPlan{c, log: logs[name],
  stack, ports, row, ready}`. Component names are unique within a declaration, so the lookup
  by name is sound.

If task 3 finds this does not hold, `planComponents` becomes the one genuine owned category-3
symbol. Its drift guard would be a test feeding one selection to both and comparing rows,
`ready`, ports and scheduler items.

### measurements.json: three designs, one decision for the coordinator

`measurementsDoc` stays 1a, so no stage can construct it:

- **(a) A stage writes the file** using its own `measurements.Doc`. This is the design the
  handoff assumes, with the schema duplicated as category 3 item 1.
- **(b) The CLI writes the file.** The stage returns the parts (`record`, `measured`, `carried`,
  `scores`, `gatedAgainst`, `gated`, `parts`, `tests`, or a reason), and `RunE` calls the
  existing `measurementsFrom` + `writeMeasurements`, which then become 1b rather than 4. This
  needs no schema duplicate. The handoff's task-4 "saving measurements.json" stage becomes a CLI
  step, the way `renderReport`'s `saveDocument` already is.
- **(c) Single-source the schema** with `type componentMeasurement measurements.Component` and
  `type measurementsDoc measurements.Doc[componentMeasurement]` (a generic `Doc`). Local
  methods and exported fields survive a defined type, so every unowned site still compiles.
  This breaks the handoff's "category 1 byte-unchanged" rule for the 1a declarations, so it is
  listed only to be declined or accepted explicitly.

### Signatures that carry `*ui.Report` / `*cobra.Command`

`noSuiteCoverageRows`, `composedRows`, `ensureToolchains` and `(*flakyGate).report` cannot keep
their parameter list below the CLI. The new functions return `[]ui.Row` (and findings), or take
an `io.Writer`. The `cmd/lydite` wrapper keeps the old signature and adds the rows in order.
`rep.Add` order is the byte-identity invariant.

### Task-boundary issue

`measurement` and `patchPart` are declared in `coverage.go`, not `measurements.go`. The task 2
file list names only `measurements.go` plus the new packages, so introducing the aliases in
task 2 needs `coverage.go` added to its boundary. The same change must also update the two
owned tests that call `measurement.entry()` (`coverage_test.go:1162`,
`measurements_test.go:112`).

`history_test.go` builds fixtures with `writeMeasurements` (4). If design (a) deletes that
function, those fixtures should be written by the new `measurements` writer. That also exercises
the cross-engine path.

## Premise corrections against the handoff and plan draft

- **`componentLog.streamed(...)` has no unowned caller.** In `mutation_test.go:1894-1908`,
  `publish_test.go:198` and `scan_test.go:158,244-254`, "streamed" is a local variable or
  prose. `componentLog` is still 1a, through `log.out` (`mutation.go:662,707,715`) and the
  unexported-key literals in `mutation_test.go`. So the S3/S6 contingency in the handoff cannot
  arise from this table.
- **`mutation_merge.go` does not call `openLog`.** It names it in a comment at line 169. Its
  real calls are `declaresNoSuite` and `noSuiteTestRow`.
- **`scan.go` does not call `orphanRow` or `measureBaseTree`**; both appear only in comments. Its
  real calls are `ensureToolchains`, `childEnv`, `splitPath` and `env`. The last two were not in
  the handoff.
- **`mutants.go` references neither `readMeasurements` nor `measurement`**; both appear only in
  comments. It has its own `readMutants`/`foldMutants`.
- **Unowned callers the handoff did not list** (all category 2 or 1c, so the approach does not
  change): `review_apisurface.go` (`ensureToolchains`, `componentUnits`, `childEnv`),
  `reports.go` (`measurementsName`, `tail`), `publish.go` and `publish_test.go` (`tailLines`),
  `clearance.go`/`mergequeue.go`/`status.go` (`firstNonEmpty`), `root.go` (`newTestCmd`,
  `newRemovedCoverageCmd`), `findings_test.go` (`measurement` fields, `crapRow`,
  `patchFindings`, `worstOffenders`), `record_test.go` (`measurementsDoc`), `scan_test.go`
  (`childEnv`, `componentUnits`).
- **Owned logic depends on unowned helpers** (`unmeasurableByDeclaration`, `ignoreReports`,
  `reportsDir`, `shortSHA`). The plan did not foresee this, and it is the whole of category 3.
