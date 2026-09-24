# A component states its language only where no runner implies one, and a component with no suite is scanned and not tested

[#240](https://github.com/lydite/lydite/issues/240) asks for shell to be scanned without gaining a
runner, and names the question to settle first: a component cannot say "this is shell, scan it, it
has no suite". A component declares exactly one of `runner:` or `command:`
(`validateInvocation`, `internal/component/component.go:483-497`), and its language is read off the
runner alone (`Component.Lang`, `component.go:180-185`; `langOf`, `cmd/lydite/coverage.go:200-205`).
A raw `command:` therefore implies no language, and a component that implies none is exactly what
`unscannedRows` (`cmd/lydite/scan.go:435-445`) renders as three `unmeasured` rows — `scan`,
`licence`, `findings`. There is no third thing a component can be.

The issue offers two shapes: a `lang:` the declaration may state directly, or a distinct "scanned but
not tested" component shape. This record adopts one field, `lang:`, and lets "scanned but not
tested" follow from which invocation a component declares rather than from a kind of its own. It
also separates the two things `Component.Lang` answers today, because the field cannot be added
safely without doing so.

What is not reopened here: a shell runner stays refused ([#187](https://github.com/lydite/lydite/issues/187)'s
comment) — a `Shell` entry in the runner registry flips `runner.Runs(Shell)` true
(`internal/runner/runner.go:132-141`) and `crap.skipped` then reports every `.sh` as skipped
forever, since there is no shell grammar. Nothing below touches the registry. And `scannedLang`
(`scan.go:412-419`) stays enumerated independently of `runner.Runs`, so a language gains a scanner
without gaining a runner and the reverse.

## `lang:` is declared, and only where no runner states one

A component may carry `lang:`, naming a language in `runner`'s `sourceExts` table
(`runner.go:117-123`). Its three valid shapes are then:

| Declares | Language | Tested | Scanned as |
|---|---|---|---|
| `runner:` | implied by the runner; `lang:` refused | the runner's derived variants | the runner's language |
| `command:`, optionally `lang:` | `lang:` if stated, otherwise none | the command, as today | `lang:` if stated, otherwise not scanned |
| `lang:` alone | `lang:` | not tested — the component declares no suite | `lang:` |

**`lang:` beside `runner:` is refused**, and for the reason [ADR 0016](0016-components-and-lydite-run-tests.md)
and `components.md` already give: `cargo-nextest` can only be Rust, and a second statement of the
language could only disagree with the first. That argument holds exactly where a runner exists and
nowhere else — a `command:` component and a component with no invocation have no first statement
for a second one to disagree with. "The language is never declared" becomes "the language is never
declared twice".

**`lang:` beside `command:` is allowed.** Refusing it would make scanning conditional on having no
suite: a repository that adds a `bats` harness through `command:` to a `lang: shell` component would
lose shellcheck the moment it gained a test, which inverts what either declaration means. It also
lets a Go module tested by `make test` state that it is Go and be scanned as Go, rather than taking
three `unmeasured` rows for having a Makefile. The command component's test side is unchanged:
everything a runner derives — the instrumented, build-only and flaky variants — is still absent,
and still reported so.

**A component declaring neither `runner:` nor `command:` must declare `lang:`**, and is the
"scanned but not tested" component. `validateInvocation` relaxes from "exactly one of runner or
command" to "at most one of runner or command, and at least one of runner, command or lang". A
component with none of the three is refused as it is today: it names nothing to run and nothing to
scan.

An unknown `lang:` value is refused at parse time, naming the recognised languages, as an unknown
runner is. A language `lang:` names but lydite has no scanner for — shell today, or Python — is not
refused: the component is still declared, and the scan says what it could not do (below).

## A distinct component kind was rejected

The alternative is a second shape of declaration: a `kind: scanned` discriminator, or a top-level
`scanned:` list beside `components:`.

**A second list** is the more separable of the two, and is rejected on what reads `components:`.
Every consumer of the declaration iterates `File.Components` — the scan loop (`scan.go:126`),
`scanUnits` (`scan.go:381-391`), `findingCounts` (`cmd/lydite/record.go:742-760`), the fold's
scorable count (`cmd/lydite/merge.go:249-253`), `orphan.Find` and `orphan.Unscanned`
(`internal/orphan/orphan.go:182-222`), `internal/affected`, `test plan`'s `planItems`
(`cmd/lydite/plan.go:210`), and `File.Select`, which is how `--component` resolves a name
(`component.go:639-660`). A second list is invisible to each of them until each learns to read it,
and the ones not updated are blind to it in silence — a component the orphan gate does not see is a
component whose files are orphans, and one `--component` cannot name is one a CI shard cannot run.
Names would need to be unique across both lists, `depends_on` would need to resolve across both, and
a component that gains a suite would move from one list to the other, changing its history in a file
whose history is the record of what gets tested (`File.Excludes`' doc comment, `component.go:190-201`).

**A `kind:` discriminator** keeps one list but restates what the keys already say. Whether a
component is tested is already legible from whether it declares an invocation; a `kind: scanned`
component that also declares a `command:` is a contradiction the parser would have to refuse, which
is the same "a second statement could only disagree" objection that rules out `lang:` beside
`runner:`. And it cannot express the `command:` plus `lang:` case above without a third kind.

Both alternatives also leave the scanned-and-tested-by-command component — the case that motivates
allowing `lang:` beside `command:` — without a shape at all. One field that composes with the
invocation keys covers every row of the table; a kind covers one row.

## The two things `Component.Lang` answers are separated

Today `Component.Lang` and `langOf` answer one question that is really two, and they coincide only
because a runner is the one source of a language:

- **the language the suite runs in** — which toolchain `lydite test` provisions, which coverage
  report is read, whether CRAP and mutation can walk the source, whether a flaky rerun exists;
- **the language the source is scanned as** — which linters, vulnerability and licence checks run,
  and which files the component answers for in `orphan.Unscanned`.

A declared `lang:` must reach the second and not the first. `langOf` has readers across the test
side that assume a non-empty language came from a runner, and a `lang: go` on a `command:` component
reaching them would be wrong in ways no row reports:

- `scorableLang(langOf(c))` (`merge.go:250`) would count it as a component the fold could have
  scored, so a folded report expects a CRAP measurement a command component never produces;
- `componentUnits` (`cmd/lydite/test.go:1896-1906`) would provision a Go toolchain for a suite that
  never asked for one;
- the flaky gate's reason (`test.go:1767-1772`) would read "no second run for a  suite", with the
  runner name empty;
- `validateAPISurface` (`component.go:511-521`) would accept `api_surface` on a command component,
  whose comparison `review_apisurface.go` then runs against a component with no derived build.

So `Component.Lang` keeps its meaning exactly — the runner-implied language, empty without a runner
— and every test-side reader keeps reading it. A second accessor, `ScanLang`, answers the runner's
language when there is a runner and the declared `lang:` otherwise, and only the scan path reads it.
A reader that is not listed below as moving stays on `Lang`; a reader that silently moves is the
defect this split exists to prevent.

## What changes, and where

This record is design only. The changes below land in their own slice.

**`internal/component`.** `Component` gains `Lang runner.Lang \`yaml:"lang,omitempty"\``.
`validateInvocation` takes the relaxed rule above and refuses `lang:` beside `runner:`. `Lang()` is
unchanged; `ScanLang()` is added. A component with no invocation refuses every key that configures a
suite, because each would be a declaration nothing reads — the same stance `Parse` takes on unknown
keys (`component.go:258-266`): `args`, `compose`, `setup`, `teardown`, `occupies`, `mutation`,
`api_surface`, and its own `watch` and `depends_on`, which exist to decide when a suite reruns. `env`
stays allowed, because it reaches the scan's checks ([ADR 0046](0046-a-components-declared-environment-is-named-in-the-scan.md)).
Another component may name a no-suite component in its own `depends_on`: a change to a script a
suite invokes is a real invalidation edge, and nothing about the target needing no suite makes it
less of one. The package doc and `components.md`'s "`lang` is derived from `runner`, never declared"
are restated to the rule above.

**`internal/runner`.** Nothing. No runner is added, `runner.Runs` stays derived from the registry,
and `Shell` stays a `Lang` no runner implies. The one thing the package supplies is the set of names
`lang:` validates against, which `sourceExts` already is.

**The scan path (`cmd/lydite/scan.go`).** The loop at `scan.go:126-208`, `scanUnits`, and
`anyLanguageDeclared` read `ScanLang` instead of `langOf`. `toolchain.Requirements` already skips a
language it has no toolchain for (`internal/toolchain/require.go:135`), so a `lang: shell` unit
provisions nothing. The order in the loop — `scannedLang` asked before `langEnabled` — stays, and is
what makes a declared language lydite has no scanner for render as `unmeasured` rather than as an
opt-out. `findingCounts` (`record.go:749`) reads `ScanLang`, so a scanned `lang:` component records
its gates' counts.

**`internal/orphan`.** `coveredByLanguage` (`orphan.go:267-296`) matches a component on its scan
language rather than on `runner.Lookup(c.Runner)`, so a `lang: shell` component covers shell files
and not the Go files beside them. `Unscanned` skips a language on `!runner.Runs(lang)`
(`orphan.go:207`), which is the wrong predicate once a language is scanned without a runner: it
becomes "lydite has a scanner for this language", which means `scannedLang`'s enumeration moves to a
package both `cmd/lydite` and `internal/orphan` can read — one list, not a second copy.

**`lydite test`, the planner and the fold.** A no-suite component is not a work item. It runs
nothing, so it takes no toolchain, no services and no scheduler lock — a lock on its directory
would serialise the components rooted beside it for nothing — and `test plan` places it in no
shard. Its rows are produced from the declaration rather than from a run, as the fold's scorable
count already is (`merge.go:246-253`): `lydite test` emits them directly when unsharded, and
`test merge` emits them when folding shards.

## What a `lang:` component reports

Every row a gate could not produce stays `unmeasured` with that gate's own reason. None is dropped
and none passes by default ([the rule](../../agentic/rules/a-gate-that-could-not-run-never-renders-as-one-that-passed.md)).

**A no-suite component**, on the test side: its test row, coverage, complexity (CRAP), mutation and
flaky are each `unmeasured`, with the reason that the component declares no suite. That is a
different sentence from a raw command's — "which has no instrumented variant" — because the
declaration an author would change differs.

**Any `lang:` component**, on the scan side, depends on whether lydite scans that language:

- **No scanner** (shell before its scanner lands; Python): `unscannedRows` as today, whose
  `unscannedReason` already names the language rather than the raw command (`scan.go:451-456`).
  The shape can therefore land before any scanner does, and says truthfully what it gets.
- **A scanner**: `scan(<name>)` and `findings(<name>)` are measured by that language's checks.

**`licence(<name>)` stays `unmeasured` for a language with no dependency set, and says so.** Shell
has no manifest and no lockfile, so there is nothing to read a licence from. Today the licence
dispatch after a component's checks (`scan.go:200-207`) is a `switch` with no `default`, so a
language that passes `scannedLang` and has no case there would produce no licence row at all — a
gate absent from the document, which reads exactly like one that ran and found nothing. The scanner
slice adds an explicit case for each scanned language without a dependency set, rendering
`licence(<name>)` `unmeasured` with the reason that the language declares no dependency set, and
makes the switch's `default` a panic: a language reaching it has passed `scannedLang`, so an
unhandled one is a lydite defect rather than an input a repository can supply — the stance
[the per-language switch rule](../../agentic/rules/a-per-language-counting-switch-panics-on-an-unhandled-grammar.md)
takes.

**The orphan gate.** A no-suite component claims only the files of its declared language under its
directory, not every file there. A component that runs no suite and claimed by containment would let
`lang: shell` at `dir: .` clear the orphan gate for every file in the repository while testing none
of them. A `command:` component keeps claiming by containment, as today: it runs a suite over its
directory, whatever that suite reaches. For a shell script, the no-suite component replaces the
exclude an author writes today (`scripts/install.sh`'s): both are reviewable lines in
`components.yml`, and the component's rows say out loud that nothing tests it.

## Not decided here

- **Which scanner, and its config key.** Nothing above assumes shellcheck. The scanner slice picks
  the tool, pins it with its own Dependabot-watched manifest, adds its case to `scannedLang` and its
  checks to the scan dispatch, and adds a `.lydite/config.yml` key for the language —
  `langEnabled` answers false for a language it has no key for (`scan.go:134-138`), so a scanner with
  no key would be skipped as though the repository had switched it off.
- **`shfmt`**, which #240 names as a formatting question rather than a security one.

## Consequences

- A component's language is stated at most once. The runner states it where one exists; `lang:`
  states it everywhere else.
- A repository can declare shell — and any later language lydite scans without a runner — as a
  component without inventing a suite for it, and the three scan rows a raw command takes become,
  once a scanner exists, a scan.
- A `command:` component can opt into its language's scanners with one line, and loses nothing it
  has today on the test side.
- Every test-side reader of a component's language keeps reading the runner-implied one. The cost is
  two accessors where there was one, and a reviewer of any future reader has to ask which question
  it is answering.
- A no-suite component is not scheduled, not sharded, and takes no lock, so its rows come from the
  declaration in both the unsharded run and the fold — a second producer of per-component rows beside
  a run, which the fold already has one of.
- An older lydite reading a declaration with `lang:` refuses it as an unknown key, as it would any
  new key; the declaration and the binary reading it move together.
