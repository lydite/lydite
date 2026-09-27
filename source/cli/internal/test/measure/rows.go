package measure

import (
	"fmt"
	"math"
	"path"
	"slices"
	"strings"

	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/crap"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// UngatedComposedRow renders one language's or the repository's figure for a
// run that measured without gating.
//
// A figure no component contributed to is `unmeasured`, never a 0.0% context
// row. 0/0 renders as 0.0%, which reads as a real measurement of no coverage
// at all — the language whose only component failed would report the worst
// possible number as though it had been measured, and a reader acting on it
// would go looking for missing tests rather than for the failing suite.
// carried names the components whose counts came from the baseline rather than
// from this run. A figure that counted them as measured would claim to have
// measured components nothing ran — and would contradict the floor row beside
// it, which does distinguish the two.
func UngatedComposedRow(label string, ms []Measurement, carried map[string]bool, in func(Measurement) bool) ui.Row {
	lines, measured, carriedN := composed(ms, carried, in)
	total := gateable(ms, in)
	if !lines.Measured() {
		return UnmeasuredRow(label, fmt.Sprintf("none of its %d component(s) produced a Measurement", total))
	}
	return ui.Row{Status: ui.StatusContext, Label: label, Value: composedValue(lines, measured, carriedN, total)}
}

// CRAPRow reports one component's CRAP, and gates it against the baseline's
// count.
//
// The gate is the delta and nothing else: a change may not raise how many
// functions sit above the threshold. Absolute would fail every repository on
// the day it upgrades, over debt it has always had — the rule coverage.floor
// already follows by defaulting to 0 — and scoping to the diff would miss the
// case the gate is for, since a change can push a function over the threshold
// without touching it, by complicating a call site or deleting the test that
// covered it. See docs/adr/0028.
//
// Worst is carried and never gated. A change that takes the worst function
// from 400 to 380 has improved nothing anybody can act on, and one that adds a
// well-tested complex function raises it without adding a thing to fix.
//
// gated says a baseline was read at all, which is not the same as this
// component having an entry in it: an ungated run renders context, and a gated
// run with no entry renders new. Without the distinction a workflow that never
// reads a baseline is indistinguishable from one whose every component is new.
func CRAPRow(m Measurement, baseline gitstate.CRAPBaseline, gated bool) (ui.Row, []finding.Finding) {
	label := "crap(" + m.Name + ")"
	// A component whose language is unstated — a raw `command:`, which names
	// no source to walk — is context and never amber: nothing about this
	// repository could make the row green, so spending the tag that exists to
	// be noticed on it is what teaches a reader to skim past it. It is still a
	// row, because a component silently absent reads as one that scored clean.
	if !m.Scorable() {
		return ui.Row{Status: ui.StatusContext, Label: label, Value: "not scored — " + noComplexitySource(m)}, nil
	}
	if !m.Scored() {
		row := UnmeasuredRow(label, m.CRAPWhy)
		// Named, for the reason a carried coverage figure is: this is the
		// entry the next change is gated against, and a number that says
		// nothing about itself is one a reader takes for a measurement.
		if base, ok := baseline[m.Name]; ok && m.Carryable {
			row.Value += fmt.Sprintf(" — carrying the baseline's %d forward", base.Above)
		}
		return row, nil
	}
	counts := CRAPValue(m.CRAP)
	base, hasBase := baseline[m.Name]
	switch {
	case !gated:
		return ui.Row{Status: ui.StatusContext, Label: label, Value: counts}, nil
	case !hasBase:
		return ui.Row{Status: ui.StatusNew, Label: label, Value: counts + ", no baseline yet"}, nil
	case base.Producer != m.Producer:
		// New, and never regressed, for the reason a coverage comparison
		// refuses the same pair: the coverage half of every score was taken
		// by a different instrument, so the difference is a change of
		// definition rather than debt anybody added.
		if reason, ok := scopeChangeReason(m.Producer, base.Producer); ok {
			return ui.Row{Status: ui.StatusNew, Label: label, Value: counts + ", " + reason}, nil
		}
		return ui.Row{Status: ui.StatusNew, Label: label,
			Value: fmt.Sprintf("%s, not compared — measured by %s, baseline by %s",
				counts, producerName(m.Producer), producerName(base.Producer))}, nil
	case m.CRAP.Above() > base.Above:
		findings := crapFindings(label, m)
		return ui.Row{Status: ui.StatusFail, Label: label,
			Value:  fmt.Sprintf("%s, baseline %d — %d more", counts, base.Above, m.CRAP.Above()-base.Above),
			Detail: worstFunctions(findings, m.CRAP.Above())}, findings
	default:
		return ui.Row{Status: ui.StatusPass, Label: label,
			Value: fmt.Sprintf("%s, baseline %d", counts, base.Above)}, nil
	}
}

// crapFindings is the claim a failing CRAP row makes, one per function over
// the threshold.
//
// A gate emits findings exactly when it makes a claim the author must clear,
// which for CRAP is when the count rose. The functions already above the
// threshold on a passing row are debt this change did not add, and reporting
// them per site would put a claim on every pull request about code nobody
// touched — a gate that fires on ordinary work is one that gets switched off.
//
// The site is the function's own name, which crap.Function already carries
// with its receiver, so the claim survives the function moving down its file.
//
// It names the same worst few the row does, and no more. The gate is cleared
// by bringing any one of them under the threshold, so a longer list is not
// more actionable — and a component carrying hundreds of functions in standing
// debt would otherwise put every one of them into the document on every run
// that regressed by one, which is a document that grows with the debt rather
// than with the change.
func crapFindings(label string, m Measurement) []finding.Finding {
	over := m.CRAP.Over[:min(len(m.CRAP.Over), WorstOffenders)]
	out := make([]finding.Finding, 0, len(over))
	for _, f := range over {
		out = append(out, finding.Finding{
			Gate:      "crap",
			Component: m.Name,
			Row:       label,
			Path:      f.File,
			Line:      f.Line,
			Message: fmt.Sprintf("%s — %.1f (complexity %d, %.1f%% covered)",
				f.Name, f.Value, f.Complexity, f.Lines.Percent()),
			Detail: []string{fmt.Sprintf(
				"Above the CRAP threshold of %d. Testing it or taking it apart clears the gate.",
				crap.Threshold)},
			Site: f.Name,
		})
	}
	finding.Number(out)
	return out
}

// CRAPValue renders a score the way every row shows it.
//
// The excluded count rides on every row that has one, because a repository can
// annotate its way to nothing above the threshold and this is the number that
// makes it visible when one does. Absent when nothing was excluded, since a
// trailing "0 excluded" on every clean row is a clause readers learn to skip.
//
// A skipped file rides on it the same way, for the same reason a gate that
// could not run must never render as one that passed: a TypeScript component
// carrying a JSX-bearing file this walk cannot read has not been fully
// scored, and a row with nothing to say about that would look identical to
// one that scored every file it was handed.
func CRAPValue(rep crap.Report) string {
	value := fmt.Sprintf("%d function(s) above %d, worst %.1f", rep.Above(), crap.Threshold, rep.Worst)
	if rep.Excluded > 0 {
		value += fmt.Sprintf(", %d excluded", rep.Excluded)
	}
	if len(rep.Skipped) > 0 {
		value += fmt.Sprintf(", %d file(s) not walked", len(rep.Skipped))
	}
	return value
}

// WorstOffenders is how many functions a failing row names. Enough to act on,
// and short enough that the row's detail is still read: the whole list of a
// component's debt is a page, and the gate is cleared by testing or splitting
// any one of them.
const WorstOffenders = 5

// worstFunctions names the work a failing row is cleared by.
//
// The worst first, and never "the ones this change added": the baseline stores
// two scalars on purpose, since a list of names cannot be compared across trees
// — a function renamed or moved would read as one fixed and one introduced. So
// the row says what is over the threshold now, and any one of them coming under
// it clears the gate.
func worstFunctions(findings []finding.Finding, over int) []string {
	out := []string{"the count may not rise; testing or splitting any one of these clears it"}
	for _, f := range findings {
		out = append(out, fmt.Sprintf("%s:%d %s", f.Path, f.Line, f.Message))
	}
	if rest := over - len(findings); rest > 0 {
		out = append(out, fmt.Sprintf("and %d more above %d", rest, crap.Threshold))
	}
	return out
}

// CRAPSummaryRow is the two ledger scalars over the repository: how many
// functions sit above the threshold, and the worst of them.
//
// Context, and it gates nothing. Every component's own row already carries the
// gate, and the per-component rule is the stricter one — a change that adds a
// function above the threshold to one component and removes one from another
// fails there and would net to zero here. What this adds is the figure a
// quality ledger records and a reader asks for, which no per-component row is.
//
// The denominator counts the components CRAP could apply to, so a repository
// whose raw-command components cannot be scored is not reported as two thirds
// ungated. It is the same rule `gateable` applies to a composed coverage
// figure.
func CRAPSummaryRow(scorable int, scored, carried []gitstate.CRAPEntry) (ui.Row, bool) {
	// Nothing lydite could score, so there is no figure and no gap. A row here
	// would report a repository that declares no language as ungated, which is
	// a property of the metric rather than of the run.
	if scorable == 0 {
		return ui.Row{}, false
	}
	if len(scored)+len(carried) == 0 {
		return UnmeasuredRow("crap", fmt.Sprintf("none of its %d component(s) produced a score", scorable)), true
	}
	above, worst := 0, 0.0
	for _, e := range slices.Concat(scored, carried) {
		above += e.Above
		worst = math.Max(worst, e.Worst)
	}
	// Carried entries are counted and named, the way a composed coverage
	// figure counts and names them: the figure is over the repository, so a
	// component this run did not reach still has a score and leaving it out
	// would make the number swing with whatever a change happened to touch.
	// Named, because a figure that does not say how much of it this run
	// measured is indistinguishable from one that measured everything — and
	// said nothing about when there is nothing to say, since "0 carried
	// forward" on every complete run is a clause readers learn to skip.
	inherited := ""
	if len(carried) > 0 {
		inherited = fmt.Sprintf(", %d carried forward", len(carried))
	}
	return ui.Row{Status: ui.StatusContext, Label: "crap",
		Value: fmt.Sprintf("%d function(s) above %d across %d of %d component(s)%s, worst %.1f",
			above, crap.Threshold, len(scored)+len(carried), scorable, inherited, worst)}, true
}

// CRAPSummaryOf is that figure over a run's own measurements. `lydite test
// merge` composes the same row from the shards' scalars instead, which is why
// the row above takes those rather than measurements.
//
// carried and anchor are what the gated path already computed for coverage;
// an ungated run has neither and carries nothing.
func CRAPSummaryOf(ms []Measurement, carried map[string]bool, anchor gitstate.CRAPBaseline) (ui.Row, bool) {
	scorable := 0
	var scored, inherited []gitstate.CRAPEntry
	for _, m := range ms {
		if !m.Scorable() {
			continue
		}
		scorable++
		if m.Scored() {
			scored = append(scored, m.CRAPEntry())
			continue
		}
		if e, ok := carriedScore(m, carried, anchor); ok {
			inherited = append(inherited, e)
		}
	}
	return CRAPSummaryRow(scorable, scored, inherited)
}

// carriedScore is the baseline score a component keeps when this run did not
// reach it: only a component affected selection left out, and only when the
// baseline has an entry for it.
//
// One implementation, because both the row a run renders and the document it
// hands `lydite test merge` are counted from it — and two copies that agreed
// today would come apart the day one learned something, leaving the same tree
// reporting one figure sharded and another unsharded.
func carriedScore(m Measurement, carried map[string]bool, anchor gitstate.CRAPBaseline) (gitstate.CRAPEntry, bool) {
	if m.Scored() || !carried[m.Name] {
		return gitstate.CRAPEntry{}, false
	}
	e, ok := anchor[m.Name]
	return e, ok
}

// ComponentRow gates one component against its own baseline entry.
func ComponentRow(m Measurement, baseline gitstate.Baseline, tolerance float64) ui.Row {
	label := "coverage(" + m.Name + ")"
	base, hasBase := baseline[m.Name]
	if !m.Measured() {
		row := UnmeasuredRow(label, m.Why)
		if m.Carryable && hasBase && base.Measured() {
			// Named, because this is the entry the composed figures below are
			// built from. A carried number that says nothing about itself is
			// one a reader takes for a measurement.
			row.Value += fmt.Sprintf(" — carrying the baseline's %.1f%% forward", base.Percent())
		}
		return row
	}
	pct := m.Lines.Percent()
	if !hasBase || !base.Measured() {
		return ui.Row{Status: ui.StatusNew, Label: label,
			Value: LineValue(m.Lines) + ", no baseline yet"}
	}
	// New, and never regressed. The two sides were measured by different
	// instruments, so their difference is a change of definition rather than
	// a change in coverage — reporting it as a regression bills it to whoever
	// bumped the instrument, whose only ways out are to widen the tolerance
	// for every future change or to merge red.
	if base.Producer != m.Producer {
		if reason, ok := scopeChangeReason(m.Producer, base.Producer); ok {
			return ui.Row{Status: ui.StatusNew, Label: label, Value: LineValue(m.Lines) + ", " + reason}
		}
		return ui.Row{Status: ui.StatusNew, Label: label,
			Value: fmt.Sprintf("%s, not compared — measured by %s, baseline by %s",
				LineValue(m.Lines), producerName(m.Producer), producerName(base.Producer))}
	}
	if regressedBeyond(pct, base.Percent(), tolerance) {
		return ui.Row{Status: ui.StatusFail, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%, regressed %.1f%%", LineValue(m.Lines), base.Percent(), base.Percent()-pct)}
	}
	return ui.Row{Status: ui.StatusPass, Label: label,
		Value: fmt.Sprintf("%s, baseline %.1f%%", LineValue(m.Lines), base.Percent())}
}

// ComparableBase is the baseline entry a measurement may be gated against:
// present, measured, and produced by the same instrument this run used.
//
// Anything else answers with the zero entry, which is not Measured — so every
// caller's existing "no baseline" path renders it as new without a second rule
// about what an incomparable baseline means.
func ComparableBase(m Measurement, baseline gitstate.Baseline) (gitstate.Entry, bool) {
	b, ok := baseline[m.Name]
	if !ok || !b.Measured() || b.Producer != m.Producer {
		return gitstate.Entry{}, false
	}
	return b, true
}

// producerName renders a producer for a reader, including the one lydite could
// not identify — which is a real state for a JavaScript component and must not
// render as an empty gap in the middle of a sentence.
func producerName(p string) string {
	if p == "" {
		return "an unidentified instrument"
	}
	return p
}

// producerScope splits a Go producer into its toolchain half and, when the
// component declared one, the package scope Runner.Producer appends after
// ", scope " — the only two things a mismatch needs told apart to say whether
// it is a toolchain bump or a component narrowing what it measures.
//
// No other language's producer carries this suffix, so a non-Go producer
// simply has no scope half, which is exactly what an absent one means here.
func producerScope(p string) (toolchain, scope string) {
	toolchain, scope, ok := strings.Cut(p, ", scope ")
	if !ok {
		return p, ""
	}
	return toolchain, scope
}

// scopeChangeReason says a producer mismatch is a scope change — the same
// toolchain measuring a different package set — rather than a change of
// instrument, so a reader who just narrowed a component's coverage recognizes
// their own edit instead of decoding two producer strings to find it.
//
// A mismatch that also moved the toolchain is not a pure scope change: the
// instrument itself is different, and the existing "measured by X, baseline by
// Y" wording already names that without asking a reader to parse two halves.
func scopeChangeReason(current, base string) (string, bool) {
	curTool, curScope := producerScope(current)
	baseTool, baseScope := producerScope(base)
	if curTool != baseTool || curScope == baseScope {
		return "", false
	}
	describe := func(scope string) string {
		if scope == "" {
			return "the component's default scope"
		}
		return scope
	}
	return fmt.Sprintf("not compared — the measured scope changed, from %s to %s",
		describe(baseScope), describe(curScope)), true
}

// notCompared says why a composed figure has no comparison, naming the
// components rather than only reporting that something is absent: without them
// a reader cannot tell a one-off — a component this very change declared —
// from a baseline that has been incomplete for months.
func notCompared(missing, reinstrumented []string) string {
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "no baseline yet for "+strings.Join(missing, ", "))
	}
	if len(reinstrumented) > 0 {
		parts = append(parts, "a different instrument measured the baseline for "+strings.Join(reinstrumented, ", "))
	}
	return strings.Join(parts, "; ")
}

// ComposedRow gates a language, or the repository, against the same subset of
// the baseline it is composed from.
//
// The baseline side sums exactly the components the current side covers, so a
// run that measured three of four compares like with like. Summing the whole
// baseline instead would compare this run's three components against the base
// tree's four, and every narrowed run would read as a regression the size of
// the component it did not run.
func ComposedRow(label string, current []Measurement, carried map[string]bool, baseline gitstate.Baseline, in func(Measurement) bool, tolerance float64) ui.Row {
	lines, measured, carriedN := composed(current, carried, in)
	total := gateable(current, in)
	if !lines.Measured() {
		return UnmeasuredRow(label, "no component in it produced a Measurement")
	}
	var baseLines coverage.LineCount
	covered := 0
	var missing, reinstrumented []string
	for _, m := range current {
		if !in(m) || !m.Lines.Measured() {
			continue
		}
		b, ok := baseline[m.Name]
		switch {
		case ok && b.Measured() && b.Producer == m.Producer:
			baseLines = baseLines.Add(b.LineCount)
			covered++
		case ok && b.Measured():
			// A baseline exists and is not comparable, which is a different
			// thing from having none and wants different words: one is a
			// component nobody has measured yet, the other a component whose
			// instrument moved under it.
			reinstrumented = append(reinstrumented, m.Name)
		default:
			missing = append(missing, m.Name)
		}
	}
	value := composedValue(lines, measured, carriedN, total)
	// Compared only when the baseline covers every component this figure is
	// composed from. A partial comparison is a different quantity, and
	// rendering one as a comparison is the class of error this package is
	// arranged around: a newly declared component adds its lines to this side
	// and nothing to the other, so the figure would move by the size of the
	// component rather than by anything anyone did to the code.
	//
	// Which components are missing is named, because without it the row says
	// only that something is absent and a reader cannot tell a one-off — a
	// component declared by this very change — from a baseline that has been
	// broken for months.
	if covered != measured+carriedN {
		return ui.Row{Status: ui.StatusNew, Label: label,
			Value: value + ", " + notCompared(missing, reinstrumented)}
	}
	pct, basePct := lines.Percent(), baseLines.Percent()
	if regressedBeyond(pct, basePct, tolerance) {
		return ui.Row{Status: ui.StatusFail, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%, regressed %.1f%%", value, basePct, basePct-pct)}
	}
	return ui.Row{Status: ui.StatusPass, Label: label,
		Value: fmt.Sprintf("%s, baseline %.1f%%", value, basePct)}
}

// PatchFindings is the claim a failing patch row makes, one per contiguous
// stretch of untested new code.
//
// A gate emits findings exactly when it makes a claim the author must clear.
// A component whose patch coverage cleared its own baseline has untested new
// lines too, and putting a claim on each of them would fire on ordinary work.
//
// The site is the stretch's own text rather than where it sits, so inserting
// code above it does not report it as something new. Its two ends stand for
// the whole: they identify the stretch, they survive an edit inside it — the
// same block, still untested — and they keep a claim about three hundred lines
// from carrying three hundred lines of them. Its length is deliberately not an
// ingredient, or adding one untested line to an untested block would orphan
// the claim already made about it.
//
// A stretch whose source cannot be read keeps its claim and loses only what
// tells it from a neighbour, which the ordinal then supplies. The row has
// already gated on it, and dropping the claim to protect its identity would
// hide a failure lydite found.
func PatchFindings(row ui.Row, dir string, m Measurement, scoped map[string][]int) []finding.Finding {
	if row.Status != ui.StatusFail {
		return nil
	}
	var out []finding.Finding
	src := finding.NewSource(dir)
	for _, run := range coverage.Uncovered(scoped, m.Hits) {
		out = append(out, finding.Finding{
			Gate:      "patch",
			Component: m.Name,
			Row:       row.Label,
			Path:      run.File,
			Line:      run.First,
			EndLine:   run.Last,
			Message:   fmt.Sprintf("%d new line(s) here are covered by no test", run.Lines),
			Detail: []string{
				"Patch coverage gates this component against its own aggregate baseline, so a test reaching these lines clears it.",
			},
			Site: src.Line(run.File, run.First) + "\x1f" + src.Line(run.File, run.Last),
		})
	}
	finding.Number(out)
	// Every stretch is made of changed lines by construction, so this
	// establishes what is already true rather than discovering it. It is
	// asked anyway, so that the one rule deciding how a claim reaches a
	// change has one implementation and a producer cannot quietly stop
	// obeying it.
	finding.Anchored(out, scoped)
	return out
}

// ComposedRows renders the two figures over the repository as a whole:
// coverage(repo) and patch(repo).
//
// One implementation with two callers — the unnarrowed local run, and
// `lydite test merge` folding a matrix of shards. Two that agreed today would
// come apart the day one learned about a case the other had not, and nothing
// would show it: the same reason `internal/scheduler` owns the port-conflict
// predicate the planner also reads.
//
// A change that touched no component's measurable lines emits no patch row:
// there is nothing to gate, and a row saying so on every documentation change
// is the noise that trains readers to skip the rows that matter.
//
// The rows come back in the order a report adds them: coverage(repo), then
// patch(repo) when there is one.
func ComposedRows(current []Measurement, carried map[string]bool, baseline gitstate.Baseline, parts []PatchPart, cfg config.Config) []ui.Row {
	rows := []ui.Row{ComposedRow(RepoLabel("coverage"), current, carried, baseline, Everything, cfg.Coverage.Tolerance)}
	if len(parts) > 0 {
		rows = append(rows, ComposedPatchRow(RepoLabel("patch"), parts, cfg.Coverage.Patch.Tolerance))
	}
	return rows
}

// PatchPart is one component's contribution to a composed patch figure: the
// changed lines it had, how many of them the report covers, and the baseline
// that component is held to.
type PatchPart struct {
	Name  string
	Lang  runner.Lang
	Hit   int
	Total int
	Base  coverage.LineCount
}

// ComposedPatchRow gates a language's, or the repository's, changed lines.
//
// It exists because the per-component rows and the aggregate rows between them
// still leave a hole. The aggregate says the repository did not get worse
// overall; the per-component patch rows say each component's new code met that
// component's own standard. Neither answers the question a reviewer actually
// has about a change spanning several components — was the new code in this
// change tested — and a change adding untested code to three components can
// clear every per-component row on tolerance and still be the change that
// should not merge.
//
// Summed over changed lines rather than averaged over components, for the
// reason ADR 0007 gives for the aggregate: a mean of percentages lets a
// two-line component outvote a two-hundred-line one.
//
// The baseline side sums exactly the components the current side covers, and a
// figure whose baseline does not cover all of them is reported rather than
// compared — the same rule ComposedRow follows, for the same reason. A
// component with no baseline contributes its new lines to the numerator and
// nothing to the comparison, which would read as movement nobody caused.
func ComposedPatchRow(label string, parts []PatchPart, tolerance float64) ui.Row {
	var hit, total int
	var base coverage.LineCount
	var missing []string
	for _, p := range parts {
		hit += p.Hit
		total += p.Total
		if p.Base.Measured() {
			base = base.Add(p.Base)
			continue
		}
		missing = append(missing, p.Name)
	}
	pct := float64(hit) / float64(total) * 100
	counts := fmt.Sprintf("%.1f%% (%d/%d new lines), %d component(s)", pct, hit, total, len(parts))
	if len(missing) > 0 {
		return ui.Row{Status: ui.StatusNew, Label: label,
			Value: fmt.Sprintf("%s, no baseline yet for %s", counts, strings.Join(missing, ", "))}
	}
	basePct := base.Percent()
	if regressedBeyond(pct, basePct, tolerance) {
		return ui.Row{Status: ui.StatusFail, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%, below it by %.1f%%", counts, basePct, basePct-pct)}
	}
	return ui.Row{Status: ui.StatusPass, Label: label,
		Value: fmt.Sprintf("%s, baseline %.1f%%", counts, basePct)}
}

// PatchRow renders one component's patch verdict. The gate is that component's
// own aggregate baseline: patch coverage has no baseline of its own, and its
// tolerance is deliberately separate, so loosening the noisy aggregate knob
// never weakens the untested-new-code check.
func PatchRow(label string, hit, total int, base coverage.LineCount, tolerance float64) ui.Row {
	pct := float64(hit) / float64(total) * 100
	counts := fmt.Sprintf("%.1f%% (%d/%d new lines)", pct, hit, total)
	switch {
	case !base.Measured():
		return ui.Row{Status: ui.StatusNew, Label: label, Value: counts + ", no baseline yet"}
	case regressedBeyond(pct, base.Percent(), tolerance):
		return ui.Row{Status: ui.StatusFail, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%", counts, base.Percent())}
	default:
		return ui.Row{Status: ui.StatusPass, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%", counts, base.Percent())}
	}
}

// ScopeToComponent narrows a repository-wide changed-line map to the files
// this component's coverage can speak for: those under its directory, written
// in a language its runner implies.
//
// Both halves are needed. A component's report says nothing about a file
// outside its directory, and a repository declaring a Go and a TypeScript
// component over one root would otherwise score each against the other's
// changed files.
//
// A component rooted at the scan root claims every path under it, which is
// correct here and is not the question affected selection asks: this is
// "whose coverage report could contain this file", not "was this path
// understood".
func ScopeToComponent(changed map[string][]int, m Measurement) map[string][]int {
	exts := runner.SourceExtsFor(m.Lang)
	prefix := path.Clean(m.Dir)
	out := map[string][]int{}
	for file, lines := range changed {
		if !hasExt(file, exts) {
			continue
		}
		if prefix != "." && !strings.HasPrefix(file, prefix+"/") {
			continue
		}
		out[file] = lines
	}
	return out
}

func hasExt(file string, exts []string) bool {
	for _, e := range exts {
		if strings.HasSuffix(file, e) {
			return true
		}
	}
	return false
}

// FloorRows gates every measured component against coverage.floor.
//
// The floor has no baseline and no comparison against last time. The aggregate
// asks "is this worse than it was"; the floor asks "is this below the bar",
// which the repository states once and every component meets or does not.
// Ratcheting it against a prior value would make a component that has never
// had tests permanently acceptable, which is the gap it exists to close — and
// it is why the floor gates whether or not this run reads a baseline at all.
//
// The unit is the component, which is coarser than the crate or package the
// floor gated before. An untested crate inside a workspace still contributes
// its lines as uncovered and still drags its component's figure down in
// proportion to its size; what a component-level floor cannot catch is a
// *small* untested sub-unit. A repository that wants crate-level floors
// declares those crates as components, which is a statement about what it
// wants tested made in the file whose history records exactly that.
// summary says whether to add the row counting how many components cleared
// the floor. It is a figure over every component the declaration holds, so a
// run responsible for part of it emits the per-component rows alone and
// `lydite test merge` counts once.
func FloorRows(ms []Measurement, floor float64, summary bool) []ui.Row {
	if floor <= 0 || len(ms) == 0 {
		return nil
	}
	var rows []ui.Row
	for _, m := range ms {
		if m.Unmeasurable {
			// Named, because a component the floor can never apply to is worth
			// knowing about — but never as a gap this run left.
			rows = append(rows, UnmeasuredRow("floor("+m.Name+")", fmt.Sprintf("the %.1f%% floor cannot apply: %s", floor, m.Why)))
			continue
		}
		if !m.Measured() {
			// Never folded into the passing count, and never a failure: a
			// gate that did not run must be visibly distinct from one that
			// passed and from one that failed.
			rows = append(rows, UnmeasuredRow("floor("+m.Name+")", fmt.Sprintf("the %.1f%% floor was not applied: %s", floor, m.Why)))
			continue
		}
		// Display precision, like every other comparison here: a component
		// printed as meeting the floor must never be failed for a difference
		// the report cannot show.
		if math.Round(m.Lines.Percent()*10) >= math.Round(floor*10) {
			continue
		}
		rows = append(rows, ui.Row{Status: ui.StatusFail, Label: "floor(" + m.Name + ")",
			Value: fmt.Sprintf("%s, floor %.1f%%", LineValue(m.Lines), floor)})
	}
	if !summary {
		return rows
	}
	if row, ok := FloorSummaryRow(ms, floor); ok {
		rows = append(rows, row)
	}
	return rows
}

// FloorSummaryRow counts how many components cleared the floor, and false when
// there is nothing to say: the floor is off, no component is in the set, or one
// of them is below it and has already failed on its own row.
//
// One implementation with two callers — the unnarrowed run, and
// `lydite test merge` counting once over every shard's components. A shard
// counts nothing here: the number is over the whole declaration, and three
// shards publishing three of them under one label is what the responsibility
// set exists to prevent.
func FloorSummaryRow(ms []Measurement, floor float64) (ui.Row, bool) {
	if floor <= 0 || len(ms) == 0 {
		return ui.Row{}, false
	}
	gateable, cleared, below := 0, 0, 0
	for _, m := range ms {
		if m.Unmeasurable {
			continue
		}
		gateable++
		if !m.Measured() {
			continue
		}
		if math.Round(m.Lines.Percent()*10) >= math.Round(floor*10) {
			cleared++
			continue
		}
		below++
	}
	if below > 0 {
		return ui.Row{}, false
	}
	// A floor that cleared nothing examined nothing, whatever the count reads
	// like. Without this a run where every component's report was unreadable
	// renders `✓ floor … 0 of 4 component(s) at or above 80.0%` — a tick on a
	// gate that looked at no component at all, which is the rule this package is
	// arranged around, inverted.
	if cleared == 0 {
		return UnmeasuredRow("floor", fmt.Sprintf("no component was measured, so the %.1f%% floor was applied to none of them", floor)), true
	}
	// "N of M" rather than a bare count: the two differ exactly when some
	// component went ungated, so a partial run cannot read as a
	// repository-wide pass.
	return ui.Row{Status: ui.StatusPass, Label: "floor",
		Value: fmt.Sprintf("%d of %d component(s) at or above %.1f%%", cleared, gateable, floor)}, true
}

// regressedBeyond reports whether cur dipped below base by more than tolerance
// percentage points, compared at the report's display precision (tenths) so
// what is shown and what is gated always agree. A raw float subtraction
// decides an exactly-at-tolerance dip by representation noise, failing one
// "regressed 0.1%" while passing an identical-looking other.
func regressedBeyond(cur, base, tolerance float64) bool {
	return math.Round((base-cur)*10) > math.Round(tolerance*10)
}

// composed sums the counts of every measured component the predicate selects,
// splitting the contributors into those this run measured and those whose
// counts were carried forward from the baseline.
func composed(ms []Measurement, carried map[string]bool, in func(Measurement) bool) (coverage.LineCount, int, int) {
	var lines coverage.LineCount
	fresh, old := 0, 0
	for _, m := range ms {
		if !in(m) || !m.Lines.Measured() {
			continue
		}
		lines = lines.Add(m.Lines)
		if carried[m.Name] {
			old++
			continue
		}
		fresh++
	}
	return lines, fresh, old
}

// gateable counts the components a composed figure could have been composed
// from: every one the predicate selects, minus those nothing could ever
// measure.
//
// The exclusion is what makes the denominator readable. `N of M` exists so a
// partial run cannot read as a repository-wide pass, so an M that counts a
// raw-command component renders a complete run as `1 of 2` — signalling that
// something went ungated when nothing did, and contradicting the floor row
// beside it, which draws the same distinction. A component nothing could ever
// measure contributes to neither side of any comparison; its absence is
// permanent and expected rather than a gap one run created.
func gateable(ms []Measurement, in func(Measurement) bool) int {
	n := 0
	for _, m := range ms {
		if in(m) && !m.Unmeasurable {
			n++
		}
	}
	return n
}

// Everything is the predicate that selects every measurement, for a figure
// composed over the whole repository rather than one language.
func Everything(Measurement) bool { return true }

// RepoLabel names a figure composed over every component.
//
// `coverage(repo)` rather than a bare `coverage`, so every row in the report
// reads as one metric over one unit and a reader pairs them by eye. A bare
// label sat between the component rows looking like a heading for them, which
// is the one thing it is not: it is a peer of theirs measured over a different
// unit.
//
// The unit is the repository, which nothing declares, so a component named
// `repo` would produce a second row under this label. That is the ambiguity
// every gate row already carries — nothing forbids a component called
// `orphans`, `watch` or `schedule` either — and it is the reason a consumer
// keys on the status rather than parsing the name.
func RepoLabel(metric string) string { return metric + "(repo)" }

// LineValue renders a measurement the way every row shows it: the percentage,
// and the counts it came from. The counts are there because they are what the
// baseline stores and what the composed figures are summed from — a reader
// checking a composed number against its parts needs both.
func LineValue(c coverage.LineCount) string {
	return fmt.Sprintf("%.1f%% (%d/%d lines)", c.Percent(), c.Covered, c.Total)
}

// composedValue renders a language or global figure, and says what it is made
// of. A composed figure that does not say how much of it this run measured is
// indistinguishable from one that measured everything.
func composedValue(lines coverage.LineCount, measured, carried, total int) string {
	if carried == 0 {
		return fmt.Sprintf("%s, %d of %d component(s)", LineValue(lines), measured, total)
	}
	return fmt.Sprintf("%s, %d of %d component(s), %d carried forward", LineValue(lines), measured+carried, total, carried)
}

// UnmeasuredRow is the shape every "this did not run" row takes: amber, never
// a vote, and carrying the reason rather than only the absence.
func UnmeasuredRow(label, why string) ui.Row {
	return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "not measured — " + why}
}
