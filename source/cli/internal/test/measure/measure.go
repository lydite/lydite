// Package measure turns one run of a component's instrumented suite into the
// coverage and CRAP figures every `lydite test` gate reads, and renders the
// rows those gates report.
//
// One implementation serves every caller that composes these rows: the run
// that measured, and `lydite test merge` folding the documents a matrix of
// shards wrote. Two copies that agreed today would come apart the day one
// learned about a case the other had not, and the same tree would then report
// one figure sharded and another unsharded.
//
// It executes no suite and writes no file. A component's suite is run by
// whoever calls Measure, which reads the report that run wrote; every row
// function here is a function of the measurements and baselines it is handed.
package measure

import (
	"context"
	"fmt"
	"path/filepath"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/crap"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
)

// Measurement is one component's coverage outcome for this run.
//
// Every declared component produces exactly one, whether it ran or not. A
// component dropped from the set instead would leave a run that measured half
// the repository reading as a complete one, which is the failure every gate in
// this package is arranged around.
type Measurement struct {
	// Name is the component's, which is what a baseline entry is keyed by:
	// two components may legitimately share a directory, and the name is the
	// only one of the two that is unique by construction.
	Name string
	// Dir is the component's directory relative to the scan root, which is
	// what scopes a diff's changed files to this component.
	Dir string
	// Lang is the language its runner implies, empty for a component that
	// declares a raw command.
	Lang runner.Lang
	// Lines is the measurement, zero when Why is set.
	Lines coverage.LineCount
	// Hits is the per-line data the patch gate reads, nil when Why is set.
	Hits coverage.LineHits
	// Unused names each `[lydite:exclude_from_coverage]` declaration in this
	// component that covers no function, as "file:line". Its author has taken
	// the referral a suppression brings and got none of its effect, which is
	// the one state nothing they can see says anything about.
	Unused []string
	// Producer names what wrote the report Lines was read from, and is what
	// stops a comparison being made across a change to it. Empty for a
	// component lydite could not identify an instrument for, and for one that
	// produced no measurement at all.
	//
	// It travels on the measurement rather than being recomputed when a
	// baseline is written, because a carried-forward entry's producer is the
	// one that measured it — possibly several trees ago — and not this run's.
	Producer string
	// CRAP is the component's complexity score: how many of its functions sit
	// above the threshold, the worst of them, and which they are.
	//
	// It rides on the coverage measurement because it is computed from it —
	// the instrumented run wrote one report, and asking a second question of
	// it costs no second run. Every language lydite walks; a component whose
	// language is unstated carries the zero value and the reason below.
	CRAP crap.Report
	// CRAPWhy says why there is no score, and is empty exactly when there is
	// one. It is separate from Why because the two come apart: a measured
	// component whose source will not parse has a coverage figure and no
	// score, and one whose coverage report describes no function has a figure
	// and nothing to score.
	CRAPWhy string
	// Why says why there is no measurement, and is empty exactly when there
	// is one. It is carried rather than inferred, because "this component was
	// not affected" and "this component's report could not be read" are the
	// same absence and want opposite reactions from a reader.
	Why string
	// Unmeasurable marks a component no run could ever measure: it declares a
	// raw command, or its runner's instrumented variant names no report. Its
	// absence from a baseline is permanent and expected rather than a gap one
	// run created, so it never blocks a recording.
	Unmeasurable bool
	// Carryable marks a component whose absence says nothing about its
	// content: this invocation did not select it, so it is unchanged from the
	// tree the baseline was recorded for and that entry still describes it.
	//
	// A component that ran and failed is not carryable, however tempting the
	// symmetry. Its content may be exactly what changed, so its old entry is a
	// guess — and one that renders as a pass, because a language whose only
	// component failed to build would otherwise report that component's last
	// good figure with a ✓ beside it.
	Carryable bool
	// Unselected marks a component this run never reached, so its absence is
	// not a gap the run left. Completeness is the fold's question, asked once
	// against the declaration of the tree being recorded; a run refuses to
	// establish a candidate only over a component it selected and failed to
	// measure.
	Unselected bool
	// Tests is what became of the component's suite, from the JUnit report its
	// runner wrote, and is nil when there is none.
	//
	// It is deliberately independent of Why: a suite that FAILED has test
	// counts, and they are the counts a quality history most wants — the
	// number of tests and how many of them went red is the whole of what a
	// red build is. Coverage from that same run is refused, because a report
	// written by a suite that stopped early describes an unfinished run; the
	// test counts describe exactly what happened.
	Tests *junit.Counts
	// TestsWhy says why a report that was expected did not arrive, and is
	// empty both when the counts are here and when this runner writes no
	// report at all.
	//
	// Both, deliberately. A runner that writes none is not a component with
	// something missing — nothing was expected, so there is nothing to tell
	// its author, and a line per jest component on every run is how a
	// diagnostic teaches its reader to skim past it. A report that was asked
	// for and is unreadable, or holds no test, is a misconfiguration somebody
	// can fix once they are told, which is why it is named on stderr.
	TestsWhy string
}

// Measured reports whether this component produced a coverage measurement.
func (m Measurement) Measured() bool { return m.Why == "" && m.Lines.Measured() }

// Scored reports whether this component produced a CRAP measurement.
func (m Measurement) Scored() bool { return m.CRAPWhy == "" && m.CRAP.Measured() }

// CRAPEntry is what this measurement records as a CRAP baseline: the two
// scalars, and the instrument that measured the coverage half of them.
func (m Measurement) CRAPEntry() gitstate.CRAPEntry {
	return gitstate.CRAPEntry{Above: m.CRAP.Above(), Worst: m.CRAP.Worst, Producer: m.Producer}
}

// Scorable reports whether CRAP could ever apply to this component, which is a
// property of the language rather than of the run: lydite walks go/ast for Go
// and internal/treesitter's tables for Rust and TypeScript, all in-process. A
// component declaring a raw command states no language at all, so there is
// nothing to walk.
//
// The three are enumerated rather than written as "any language lydite gates",
// so a fourth added without a walk behind it does not become scorable by
// default — it would score zero functions, which reads exactly like a component
// that scored clean.
func (m Measurement) Scorable() bool {
	return ScorableLang(m.Lang)
}

// ScorableLang is the same predicate as Scorable, applied to a bare language
// rather than a measurement — `lydite test merge`'s fold needs it against a component
// declaration, before any measurement exists to ask.
func ScorableLang(lang runner.Lang) bool {
	switch lang {
	case runner.Go, runner.Rust, runner.TypeScript:
		return true
	default:
		return false
	}
}

// Entry is what this measurement records as a baseline: the counts, and what
// produced them. One conversion, so no caller can write counts and forget the
// producer beside them.
func (m Measurement) Entry() gitstate.Entry {
	return gitstate.Entry{LineCount: m.Lines, Producer: m.Producer}
}

// FromEntry is a component whose measurement is a baseline entry carried
// forward rather than taken this run, keeping the producer that measured it.
func FromEntry(m Measurement, e gitstate.Entry) Measurement {
	return Measurement{Name: m.Name, Dir: m.Dir, Lang: m.Lang, Lines: e.LineCount, Producer: e.Producer}
}

// UnmeasuredComponent is a component with no measurement, and the reason.
//
// The reason covers the score as well as the coverage, because CRAP is
// computed from the coverage report: a component that produced none produced
// no score either, and for exactly the same reason.
func UnmeasuredComponent(c component.Component, why string) Measurement {
	return Measurement{Name: c.Name, Dir: c.Dir, Lang: LangOf(c), Why: why, CRAPWhy: why}
}

// UnmeasurableComponent is a component no run could ever measure, as opposed to
// one this run happened not to.
func UnmeasurableComponent(c component.Component, why string) Measurement {
	m := UnmeasuredComponent(c, why)
	m.Unmeasurable = true
	return m
}

// LangOf is the language a component's runner implies. A component declaring a
// raw command has none, which is also why it can have no instrumented variant.
func LangOf(c component.Component) runner.Lang {
	if r, ok := runner.Lookup(c.Runner); ok {
		return r.Lang
	}
	return ""
}

// Measure reads the report the component's instrumented invocation wrote.
//
// A component whose invocation names no coverage report is unmeasured with the
// reason said out loud — a raw `command:` has no instrumented variant to ask
// for, and no key exists to name where its coverage lands. Excluding it
// instead would drop it from the language and global figures silently, leaving
// a gate that covered fewer components than the repository has reading as a
// complete one.
//
// childEnv is the environment the report is read under, composed by the caller
// from tc and the component's declaration exactly as its suite's own was. It
// is handed in rather than composed here because that composition — which
// PATH wins, and that the toolchain's variables go last — is one rule every
// subprocess lydite runs for a component obeys, and a second copy of it here
// is one that could stop agreeing.
func Measure(ctx context.Context, root string, c component.Component, inv runner.Invocation, cfg config.Config, tc *toolchain.Env, instrument bool, childEnv []string) Measurement {
	switch {
	case !instrument:
		return UnmeasuredComponent(c, "coverage is off for this run")
	case len(c.Command) > 0:
		return UnmeasurableComponent(c, "the component declares a raw command, which has no instrumented variant")
	case inv.CoverageReport == "":
		return UnmeasurableComponent(c, "the runner's instrumented variant names no coverage report")
	}
	rep, err := coverage.Measure(ctx, root, c.Dir, inv.CoverageReport, LangOf(c), childEnv)
	if err != nil {
		return UnmeasuredComponent(c, err.Error())
	}
	if !rep.Lines.Measured() {
		// rep.Unused travels even here: a component whose exclusions
		// happen to leave zero net coverable lines can still carry a
		// declaration that covers no function, and the command naming
		// unused declarations reads it off the measurement — a report
		// discarded past this point is a warning nobody sees.
		m := UnmeasuredComponent(c, "the coverage report lists no coverable line")
		m.Unused = rep.Unused
		return m
	}
	m := Measurement{Name: c.Name, Dir: c.Dir, Lang: LangOf(c),
		Lines: rep.Lines, Hits: rep.Hits, Unused: rep.Unused, Producer: producerOf(root, c, cfg, tc)}
	m.CRAP, m.CRAPWhy = Score(root, m)
	return m
}

// Score is the component's CRAP report, from the per-line hits the coverage
// measurement just produced.
//
// No second run and no second artefact, which is the same rule the patch gate
// follows: the instrumented variant wrote one report, and complexity is a walk
// over source lydite can read. A component with no language to walk is not
// scored and says so, rather than being silently absent — a component nobody
// scored and one that scored clean read identically in a count of zero.
//
// It names nothing on stderr. A base tree is measured through this same path
// and its report is discarded, so a declaration warned about here would belong
// to a tree nobody is looking at; the report carries them instead, and the run
// that renders rows says which of them covered no function.
func Score(root string, m Measurement) (crap.Report, string) {
	if !m.Scorable() {
		return crap.Report{}, noComplexitySource(m)
	}
	rep, err := crap.Measure(root, m.Hits)
	if err != nil {
		return crap.Report{}, err.Error()
	}
	switch {
	case rep.Measured():
		return rep, ""
	case rep.Excluded > 0:
		// A component whose every scorable function is excluded is not clean;
		// it is a component nothing was scored in, and it says so. The report
		// travels with the reason, so the declarations it holds are still
		// named.
		return rep, fmt.Sprintf("every function the coverage report describes is excluded (%d)", rep.Excluded)
	case len(rep.Skipped) > 0:
		// Every file the report described was one this walk could not read —
		// a JSX-bearing component, most likely — rather than a component with
		// nothing in it. The reason says so instead of reading the same as an
		// empty one.
		return rep, fmt.Sprintf("every file the coverage report describes could not be walked (%d)", len(rep.Skipped))
	default:
		return rep, "the coverage report describes no function to score"
	}
}

// noComplexitySource says why a component is not scored, for the one reason
// that is a property of the metric rather than of the run: every language
// lydite gates is walked, so the only component left with no complexity source
// is one whose language is never stated.
//
// Built here rather than read off the measurement, because such a component
// also carries whatever stopped its coverage being measured — and "not scored
// — the suite failed" reads as though fixing the suite would produce a score.
func noComplexitySource(m Measurement) string {
	return m.Name + " declares a raw command, so its language is unstated and there is no source to walk"
}

// producerOf names what wrote this component's report.
//
// Asked here rather than at the call site so that a measurement cannot be
// built without one: a component recorded with no producer compares equal
// across every change to its instrument, which is the comparison the field
// exists to prevent, and it would fail silently.
//
// The component's own directory, or the workspace root above it when the
// install hoisted there — a JavaScript workspace's runner and coverage
// provider are its dependencies, and Producer reads them out of the same
// tree Install actually wrote to. cfg.TypeScript.Install is passed through
// for the same reason: an override runs in the component's own directory
// regardless of any lockfile above it, and Producer has to look where the
// install actually ran rather than where one would otherwise be inferred.
//
// The declared args go with them, because a component may narrow the package
// set its coverage is measured over and a figure taken over a narrower tree
// does not compare to one taken over the whole of it.
func producerOf(root string, c component.Component, cfg config.Config, tc *toolchain.Env) string {
	r, ok := runner.Lookup(c.Runner)
	if !ok {
		return ""
	}
	return r.Producer(filepath.Join(root, filepath.FromSlash(c.Dir)), root, cfg.TypeScript.Install, tc.Version(), c.Args...)
}
