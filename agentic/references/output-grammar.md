# Output grammar and `--json`

> **The reference for `internal/ui`** — rows, statuses, verdicts and the `--json` document.

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

**`StatusDeclined` shares the default glyph with `StatusNew` and `StatusContext`, not the amber
`!`.** A concern the repository chose not to run at all is a decision stated on purpose, not a
gate that failed to run, so it does not wear the same attention `StatusUnmeasured` does. Telling
the two apart in a fold — a gap nobody asked about versus one the repository declared — is
`cmd/lydite/publish.go`'s `worst()`, `counts()` and `headline()`, not a second glyph here; see
[`surface.md`](surface.md) for the section-level rule and [ADR
0044](../../docs/adr/0044-a-declined-concern-renders-as-declined.md) for why it is its own
status rather than a use of `StatusContext`.

**Anything automated reads `--json`, never the text.** The document carries the same rows as
the terminal, so the two cannot disagree, and statuses travel as their own names rather than
as glyphs. `TestJSONKeysArePartOfTheContract` pins the keys; `ui.jsonRow` stays a separate
type from `ui.Row` for that reason, so a field added for rendering cannot quietly become
part of the published document. `lydite/actions` parses no output at all: it reads the
documents a run wrote, which is what a consumer should do. **Do not reintroduce a bracketed
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

