# A declined concern renders as declined, not as one that went missing

A consumer who does not want mutation testing on every pull request has no way to say so today.
Skipping the job removes `.lydite-reports/mutation`'s artifact, `lydite publish`'s `buildComment`
finds no document under that name, and the section is simply absent — indistinguishable, in the
one place anybody looks, from the wardnet/wardnet#957 failure this repository's own surface rule
exists to prevent: *"a missing input is a section, never an omission."*

The gap is not in `publish` alone. It has no vocabulary for "the repository decided not to run
this," only for "this ran and passed," "this could not run," and "this ran and needs a human."
This ADR adds the fourth.

## `StatusDeclined` is a new status, not a use of `context`

`worst(rows)` starts at `StatusUnmeasured` and is promoted only by `StatusFail`, `StatusRefer`
and `StatusPass`. A section made entirely of `StatusContext` rows — which is what the existing
per-component `mutation: false` row already renders — never leaves `StatusUnmeasured`, and
`headline()` then says *"everything that ran passed, but no verdict came from mutation."* That
sentence is correct for a concern nobody asked to run at all, and wrong for one the repository
declined on purpose: the reader is being told to go looking for an input that never existed
here by design.

Teaching `worst`/`headline` a carve-out — "a section is not unmeasured if it says it was
declined" — was rejected. It would hide the concept from `--json`, which has no field to carry
it, and from the terminal report, which renders `ui.Row` the same way `publish` does. A
consumer scripting against `--json` has no way to tell "declined" from "unmeasured" apart, which
is exactly the ambiguity this ADR exists to remove.

So: **`StatusDeclined`** joins the seven statuses in `internal/ui/row.go`, non-voting like
`StatusContext` — it never appears in `verdictOf`'s switch — but distinct from it for `worst`
and `counts`:

- `worst()` treats `StatusDeclined` as `StatusPass` already is: it does not leave the
  accumulator at `StatusUnmeasured`, but it also does not outrank a real `StatusFail` or
  `StatusRefer` elsewhere in the same section.
- `counts()` gets its own word — `"declined"` — beside `"not gated"`, so a shut section's
  summary line says which one happened without a reader having to open it.
- `headline()` gains a branch: a section that is `StatusDeclined` is not counted among
  `unmeasured` for the "no verdict came from" sentence.
- The glyph is `→`, the same as every other non-voting status (`StatusContext` and now
  `StatusDeclined` share `default` in `Status.glyph()`) — a declined concern is not something to
  alarm on, and lydite already has four glyphs doing duty for seven statuses. It does not need
  an eighth.

`internal/ui/row.go`'s package doc calls the status set closed: *"rendering a status the grammar
has no glyph for would silently drop it to a context line."* `StatusDeclined` is added to that
set deliberately, in the same package, rather than layered on top of it — the doc's closedness
claim keeps meaning what it says.

## The existing per-component `mutation: false` row stays `StatusContext`

`prepareMutation` already renders a component with `mutation: false` in its declaration as a
`StatusContext` row, with the reasoning recorded in that function: *"the amber tag is for a
gate that could not run, and spending it on a decision the repository stated deliberately is
what teaches a reader to skim past it."* That reasoning does not change here, and neither does
the row.

The two are different things wearing similar language. A per-component opt-out is one row
inside a `mutation` section that still ran for every other component — the section as a whole
has a real verdict, and nothing about *it* went missing. `StatusDeclined` is for the section
never having run at all: no component was ever going to be attempted, because the CI workflow
was told not to try.

This leaves a known gap out of scope for this slice: a repository where *every* component has
`mutation: false` renders its mutation section as `unmeasured` today (every row is `context`,
so `worst` never leaves its starting value), which is arguably the same bug wearing the
per-component costume. It is not fixed here. Unifying it would mean `worst()` inspecting the
component declarations `publish` does not currently read at all — a bigger change than one
concern's CI workflow declining to run — and the two cases are already distinguishable by their
row detail for a reader who opens the section, which the section-level case is not. Left as a
named gap rather than an omission.

## `lydite mutation --declined` writes a report document, not a `publish` flag

Two shapes were on the table for how the run tells `publish` a concern was declined: a
`--declined mutation` flag on `publish` itself, or the command asserting it about itself.

`publish` stays pure. It reads whatever documents its `--reports` directories hold and knows
nothing about why a document says what it says — teaching it a `--declined <concern>` flag
would give the renderer a second source of truth about the run's shape, one that has to agree
with the documents it is also reading, and no equivalent on the terminal report at all: a
developer running `lydite mutation --declined` locally would see a plain mutation document with
no way to ask `publish` to render it as declined without repeating the flag by hand.

Instead, `lydite mutation` gains a boolean `--declined` flag, checked first in its `RunE` before
any component is loaded, planned, scheduled or built. When set, it writes `mutation.json` —
`command: mutation`, `verdict` non-failing, one `Row{Status: StatusDeclined, Label: "mutation",
Value: "declined for this run"}` — and returns. Nothing else in the fold, the artifact naming,
`buildComment`'s directory discovery, or the marker round-trip changes: the document lands
exactly where an ordinary `mutation` run's would, and `publish` renders it exactly as it renders
any other command's document, because it is one.

This mirrors the boolean mode-flags this CLI already has on other commands (`test
--no-coverage`) rather than introducing a subcommand's separate flag surface for a codepath that
is one short-circuit inside an existing command.

## The ledger records nothing for a declined concern

`lydite test record` and whatever else reads a mutation document for the ledger treat a
section whose rows are entirely `StatusDeclined` the same as one that is absent: no ledger row.
Absent is not zero, and a declined run has no measurement to append — recording anything would
give a reader of the ledger's history a data point that means "nobody looked," indistinguishable
from an actual run that found nothing.

## `lydite/actions` writes the declined document instead of running the matrix

The reusable workflow (`lydite.yml`) gains a boolean input, `mutation`, defaulting to `true`.
When `false`, the job that would otherwise run the mutation matrix instead runs a single step:
`lydite mutation --declined --dir <scan root>`, uploading `.lydite-reports/mutation` as
`lydite-reports-mutation` the same as the matrix job would have. `publish/action.yml`'s
nested-versus-flat detection (`:92-122`) keys off whether `lydite-reports-*` subdirectories
exist at all, and this leaves that arithmetic untouched: an artifact exists either way, holding
one document instead of one per shard.

This is [lydite/actions#9](https://github.com/lydite/actions/issues/9)'s slice, gated behind
this repository's own release the same way every change to `lydite/actions`' `@v1` is — see the
handoff's note that A ships nothing until `@v1` moves.

## Consequences

- `internal/ui/row.go` gains `StatusDeclined`; `worst`, `counts`, `headline` and `verdictOf` in
  `cmd/lydite/publish.go` all change to know about it. `--json` gains the new status word.
- `cmd/lydite/mutation.go` gains a `--declined` flag and the short-circuit that writes the
  one-row document; `prepareMutation`'s existing `StatusContext` row for `mutation: false` is
  untouched.
- Nothing in `internal/ledger` changes: a declined section already produces no rows a recorder
  would act on differently from an absent one, once the recorder treats `StatusDeclined` as it
  already treats `StatusUnmeasured` for that purpose.
- The all-components-declined-individually case stays `unmeasured` at the section level. Named
  here as a deliberate gap, not fixed.
- `lydite/actions`' `mutation` input, and the declined-document step, are `lydite/actions#9`'s
  work and ship on its own timeline once a released `lydite` binary carries `--declined`.
