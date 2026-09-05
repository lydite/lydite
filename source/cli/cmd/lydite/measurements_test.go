package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
)

// producing builds a measured entry attributed to one instrument.
func producing(covered, total int, producer string) gitstate.Entry {
	return gitstate.Entry{LineCount: coverage.LineCount{Covered: covered, Total: total}, Producer: producer}
}

// A coverage figure is comparable only to one the same instrument produced. A
// runner or provider bump changes what a line is — vitest 3.2.7 to 4.1.11 took
// one workspace from 345 lines to 152 over an identical tree — and reporting
// that fall as a regression bills it to whoever bumped the tool, whose only
// ways out are widening the tolerance for every future change or merging red.
func TestAChangedProducerIsNewRatherThanRegressed(t *testing.T) {
	m := measured("web", "typescript", 142, 152)
	m.Producer = "vitest 4.1.11, @vitest/coverage-v8 4.1.11"
	baseline := gitstate.Baseline{"web": producing(327, 345, "vitest 3.2.7, @vitest/coverage-v8 3.2.7")}

	row := componentRow(m, baseline, 0.1)
	if row.Status != "new" {
		t.Errorf("coverage(web) = %+v, want new — the two sides were measured by different instruments", row)
	}
	if !strings.Contains(row.Value, "vitest 4.1.11") || !strings.Contains(row.Value, "vitest 3.2.7") {
		t.Errorf("row = %q, want both instruments named so a reader can act on it", row.Value)
	}
	if strings.Contains(row.Value, "regressed") {
		t.Errorf("row = %q, want no regression claimed across a change of instrument", row.Value)
	}
}

// The same producer gates normally. Without this the rule above is satisfied by
// an implementation that never compares anything.
func TestAnUnchangedProducerStillGates(t *testing.T) {
	m := measured("web", "typescript", 50, 100)
	m.Producer = "vitest 4.1.11, @vitest/coverage-v8 4.1.11"
	baseline := gitstate.Baseline{"web": producing(80, 100, m.Producer)}

	if row := componentRow(m, baseline, 0.1); row.Status != "fail" {
		t.Errorf("coverage(web) = %+v, want a failure — same instrument, real regression", row)
	}
}

// An unidentified instrument compares equal to an unidentified one, so a
// component lydite cannot introspect — a Yarn PnP workspace, an install that
// wrote no node_modules — is still compared rather than permanently reported
// new. Refusing to compare there would silently stop gating a whole
// repository.
func TestAnUnidentifiedProducerGatesAsBefore(t *testing.T) {
	m := measured("web", "typescript", 50, 100)
	baseline := gitstate.Baseline{"web": producing(80, 100, "")}

	if row := componentRow(m, baseline, 0.1); row.Status != "fail" {
		t.Errorf("coverage(web) = %+v, want the comparison to happen when neither side names an instrument", row)
	}
}

// A composed figure refuses to compare when any component in it was measured by
// a different instrument, and says which — the same rule it already applies to a
// component with no baseline, with words that tell the two apart. A reader
// cannot act on "no baseline yet" for a component that has had one for months.
func TestAComposedFigureNamesAReinstrumentedComponent(t *testing.T) {
	api := measured("api", "go", 50, 100)
	api.Producer = "go 1.26.6"
	web := measured("web", "typescript", 80, 100)
	web.Producer = "vitest 4.1.11, @vitest/coverage-v8 4.1.11"
	baseline := gitstate.Baseline{
		"api": producing(50, 100, "go 1.26.6"),
		"web": producing(80, 100, "vitest 3.2.7, @vitest/coverage-v8 3.2.7"),
	}

	row := composedRow(repoLabel("coverage"), []measurement{api, web}, nil, baseline, everything, 0.1)
	if row.Status != "new" {
		t.Errorf("coverage(repo) = %+v, want new — its baseline does not cover every component", row)
	}
	if !strings.Contains(row.Value, "different instrument") || !strings.Contains(row.Value, "web") {
		t.Errorf("row = %q, want the reinstrumented component named", row.Value)
	}
	if strings.Contains(row.Value, "no baseline yet") {
		t.Errorf("row = %q, want a baseline that exists not described as absent", row.Value)
	}
}

// A carried entry keeps the producer that measured it, not this run's. Under
// affected selection an entry rides forward across many trees, and for Go and
// Rust the instrument is lydite's own pinned tool — so a consumer upgrading
// lydite changes it with nothing in the repository's diff to signal it.
func TestACarriedEntryKeepsItsOwnProducer(t *testing.T) {
	m := measurement{Name: "web", Dir: "web", Lang: "typescript", Why: "not affected"}
	carried := fromEntry(m, producing(80, 100, "vitest 3.2.7, @vitest/coverage-v8 3.2.7"))

	if carried.Producer != "vitest 3.2.7, @vitest/coverage-v8 3.2.7" {
		t.Errorf("producer = %q, want the one that measured the entry", carried.Producer)
	}
	if carried.entry().Producer != carried.Producer {
		t.Error("the recorded entry lost the producer it was carried with")
	}
}

// Shards fold into one candidate, and a measured entry beats a carried one
// whichever order the documents arrive in. Each shard carries forward every
// component it did not run, so most components appear in every document and
// only one copy came from a suite that executed — taking the last would record
// the base tree's number for a component this change rewrote.
func TestFoldingPrefersAMeasuredEntryOverACarriedOne(t *testing.T) {
	carriedWeb := measurementsDoc{Tree: "t1", Components: map[string]componentMeasurement{
		"api": {Entry: producing(50, 100, "go 1.26.6")},
		"web": {Entry: producing(10, 100, "vitest 4.1.11"), Carried: true},
	}}
	measuredWeb := measurementsDoc{Tree: "t1", Components: map[string]componentMeasurement{
		"api": {Entry: producing(50, 100, "go 1.26.6"), Carried: true},
		"web": {Entry: producing(90, 100, "vitest 4.1.11")},
	}}

	for _, order := range [][]measurementsDoc{{carriedWeb, measuredWeb}, {measuredWeb, carriedWeb}} {
		got, err := foldMeasurements(order)
		if err != nil {
			t.Fatalf("foldMeasurements: %v", err)
		}
		if got.Components["web"].Covered != 90 {
			t.Errorf("web = %+v, want the shard that measured it to win", got.Components["web"])
		}
		if got.Components["api"].Covered != 50 {
			t.Errorf("api = %+v, want the shard that measured it to win", got.Components["api"])
		}
	}
}

// Shards of different trees are not shards of one run. Folding them would
// record a baseline no tree ever had: each number right, the entry wrong.
func TestFoldingRefusesCandidatesOfDifferentTrees(t *testing.T) {
	_, err := foldMeasurements([]measurementsDoc{{Tree: "aaaaaaaaaaaa"}, {Tree: "bbbbbbbbbbbb"}})
	if err == nil {
		t.Fatal("foldMeasurements accepted documents describing different trees")
	}
	if !strings.Contains(err.Error(), "different trees") {
		t.Errorf("error = %q, want it to say the trees disagree", err)
	}
}

// A candidate naming no tree cannot be bound to a checkout, so it is refused
// rather than defaulted — the reason ReadDocument refuses a document with no
// command. It is not a newer shape; it is not a candidate.
func TestACandidateWithoutATreeIsRefused(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite-reports/"+measurementsName, `{"components":{"api":{"covered":1,"total":2}}}`)
	if _, err := readMeasurements(root + "/.lydite-reports"); err == nil {
		t.Fatal("readMeasurements accepted a document naming no tree")
	}
}

// The binding. A candidate names the tree it measured, and recording it
// anywhere else is refused: without this a mis-wired workflow lands one tree's
// numbers under another tree's key, silently, and that entry then gates every
// later change whose merge-base is that tree.
func TestRecordRefusesACandidateMeasuredOnAnotherTree(t *testing.T) {
	root := gateRepo(t)
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}
	// The tree moves after the measurement, exactly as it does when a job
	// records from a checkout that is not the one it measured.
	write(t, root, "svc/extra.go", "package svc\n\n// Extra moves the tree.\nfunc Extra() int { return 3 }\n")
	if r := executil.RunQuiet(context.Background(), root, "git", "add", "-A"); !r.Ok() {
		t.Fatal(r.Err)
	}
	if r := executil.RunQuiet(context.Background(), root, "git", "-c", "user.email=t@t", "-c", "user.name=t",
		"commit", "-m", "move the tree"); !r.Ok() {
		t.Fatal(r.Err)
	}

	out, _, err := runRecordCmd(t, root, "--json")
	if err == nil {
		t.Fatalf("record accepted a candidate measured on another tree\n%s", out)
	}
	row := jsonRows(t, out)["record"]
	if row.Status != "fail" || !strings.Contains(row.Value, "is checked out") {
		t.Errorf("record = %+v, want a refusal naming both trees", row)
	}
}

// A baseline missing a declared component gates on nothing: ReadBaseline reports
// a hit for any non-empty entry, so the missing component reads as new on every
// later change, and every composed figure it belongs to stops comparing too.
// Recording nothing leaves the next change a clean miss, which is slower and
// correct — and the fold is where a sharded run's completeness can be judged at
// all, since each shard is missing most components until they are put together.
func TestRecordRefusesAFoldMissingADeclaredComponent(t *testing.T) {
	root := gateRepo(t)
	decl, err := component.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	full := measurementsDoc{Components: map[string]componentMeasurement{"svc": {Entry: producing(2, 4, "go 1.26")}}}
	if gap, blocked := missingFromRecord(decl, full); blocked {
		t.Errorf("a complete fold was blocked by %q", gap)
	}
	empty := measurementsDoc{Components: map[string]componentMeasurement{}}
	gap, blocked := missingFromRecord(decl, empty)
	if !blocked || gap != "svc" {
		t.Errorf("missingFromRecord = (%q, %v), want svc named as the gap", gap, blocked)
	}
}

// A component nothing could ever measure is not a gap. Its absence is permanent
// and expected rather than something one run created, so it must not block every
// recording forever — the same rule `lydite test` applies, asked here from the
// declaration alone because this command runs none of it.
func TestRecordIsNotBlockedByAComponentNothingCanMeasure(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "docs", Dir: "docs", Command: []string{"make", "docs"}},
	}}
	if gap, blocked := missingFromRecord(decl, measurementsDoc{}); blocked {
		t.Errorf("a component declaring a raw command blocked recording as %q", gap)
	}
}

// Recording the same measurement twice does not push twice. The branch is
// shared and busy, so a run that would rewrite an entry byte for byte is a
// wasted fetch-commit-push against a ref other jobs are reading.
func TestRecordingATreeTwiceIsIdempotent(t *testing.T) {
	root := gateRepo(t)
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}
	if out, errOut, err := runRecordCmd(t, root, "--json"); err != nil {
		t.Fatalf("first recording: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	out, errOut, err := runRecordCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("second recording: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	if got := jsonRows(t, out)["record"]; !strings.Contains(got.Value, "already holds") {
		t.Errorf("record = %q, want the second recording to find its own entry", got.Value)
	}
}

// A directory holding no candidate is named and skipped rather than failing the
// command, since a --no-coverage run writes none and a caller may reasonably
// pass `record` the same directory list it passes `publish`. With no candidate
// anywhere there is nothing to do, and that is an error: the command was asked
// to record and could not reach an answer.
func TestRecordSaysWhenNoDirectoryHoldsACandidate(t *testing.T) {
	root := gateRepo(t)
	if err := os.MkdirAll(filepath.Join(root, runner.ReportDir), 0o750); err != nil {
		t.Fatal(err)
	}
	out, _, err := runRecordCmd(t, root)
	if err == nil {
		t.Fatalf("record succeeded with no candidate anywhere\n%s", out)
	}
	if !strings.Contains(err.Error(), measurementsName) {
		t.Errorf("error = %q, want the missing document named", err)
	}
}

// A run that measured nothing establishes no candidate, and says which of the
// reasons it was. A document that simply omitted its components would be
// indistinguishable from one describing a repository that declares none.
func TestACandidateSaysWhyItIsEmpty(t *testing.T) {
	cmd := newRootCmd()
	decl := component.File{Components: []component.Component{{Name: "api", Dir: "api", Runner: "go-test"}}}
	ms := []measurement{unmeasuredComponent(decl.Components[0], "the suite failed")}

	doc, value := candidateThisTree(context.Background(), cmd, t.TempDir(), decl, ms, nil, nil, true, nil, 0.1)
	if len(doc.Components) != 0 || doc.Reason == "" {
		t.Errorf("doc = %+v, want no components and a reason", doc)
	}
	if !strings.Contains(value, "nothing to record") {
		t.Errorf("row = %q, want it to say nothing was established", value)
	}
}

// A component the declaration no longer holds does not survive in the entry.
// Otherwise a baseline accumulates a tail of components nobody can measure, and
// each one blocks nothing while quietly widening what a composed figure sums.
func TestRecordingDropsAComponentTheDeclarationLost(t *testing.T) {
	decl := component.File{Components: []component.Component{{Name: "svc", Dir: "svc", Runner: "go-test"}}}
	if !declares(decl, "svc") {
		t.Error("declares says a declared component is absent")
	}
	if declares(decl, "gone") {
		t.Error("declares says an undeclared component is present")
	}
}

// Folding nothing is an error rather than an empty baseline. An empty entry,
// once written, reads as a hit for every later change and gates nothing.
func TestFoldingNothingIsAnError(t *testing.T) {
	if _, err := foldMeasurements(nil); err == nil {
		t.Fatal("foldMeasurements accepted an empty set")
	}
}

// A fold in which no shard established anything carries a shard's own reason
// forward, so the row says why rather than reporting an absence.
func TestFoldingKeepsAShardsReason(t *testing.T) {
	got, err := foldMeasurements([]measurementsDoc{
		{Tree: "aaaaaaaaaaaa", Reason: "the suite failed"},
		{Tree: "aaaaaaaaaaaa"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "the suite failed" {
		t.Errorf("reason = %q, want the shard's own reason kept", got.Reason)
	}
}

// A component declaring a raw command has no producer, because lydite does not
// know what such a run would invoke, let alone what wrote a report.
func TestAComponentWithARawCommandHasNoProducer(t *testing.T) {
	c := component.Component{Name: "docs", Dir: "docs", Command: []string{"make", "docs"}}
	if got := producerOf(t.TempDir(), c, nil); got != "" {
		t.Errorf("producer = %q, want nothing for a component lydite does not invoke", got)
	}
}

// The patch gate holds new code to the component's own aggregate baseline
// percentage, so it is as incomparable across a change of instrument as the
// aggregate is — and refuses for the same reason. Without this the producer
// rule holds at two altitudes and silently lapses at the third.
func TestThePatchGateWillNotCompareAcrossAChangeOfInstrument(t *testing.T) {
	m := measured("web", "typescript", 90, 100)
	m.Producer = "vitest 4.1.11, @vitest/coverage-v8 4.1.11"
	baseline := gitstate.Baseline{"web": producing(80, 100, "vitest 3.2.7, @vitest/coverage-v8 3.2.7")}

	base, ok := comparableBase(m, baseline)
	if ok || base.Measured() {
		t.Fatalf("comparableBase = (%+v, %v), want no baseline to gate against", base, ok)
	}
	if row := patchRow("patch(web)", 5, 10, base.LineCount, 0.1); row.Status != "new" {
		t.Errorf("patch(web) = %+v, want new — the baseline percentage came from another instrument", row)
	}

	// And still gates when the instrument is unchanged, so the rule above is
	// not satisfied by a patch gate that compares against nothing.
	same := gitstate.Baseline{"web": producing(80, 100, m.Producer)}
	base, ok = comparableBase(m, same)
	if !ok {
		t.Fatal("comparableBase refused a baseline taken by the same instrument")
	}
	if row := patchRow("patch(web)", 5, 10, base.LineCount, 0.1); row.Status != "fail" {
		t.Errorf("patch(web) = %+v, want a failure — 50%% of new lines against an 80%% baseline", row)
	}
}

// A candidate that exists but cannot be parsed is refused with the path named.
// It is not the same as an absent one: something wrote a document there, and a
// reader has to be told which one it could not use.
func TestAMalformedCandidateNamesThePathItCameFrom(t *testing.T) {
	root := t.TempDir()
	write(t, root, runner.ReportDir+"/"+measurementsName, `{"tree": "abc",`)
	_, err := readMeasurements(filepath.Join(root, runner.ReportDir))
	if err == nil {
		t.Fatal("readMeasurements accepted a truncated document")
	}
	if !strings.Contains(err.Error(), measurementsName) {
		t.Errorf("error = %q, want the path named", err)
	}
}

// Re-recording a tree does not lower the entry it already holds.
//
// The anchor is against what this tree already holds, not only against what
// the measuring run compared with: the same tree is the same content, so a
// difference between two measurements of it is the measurement noise the
// tolerance exists for. Without it a second recording replaces an anchored
// high-water entry with a raw dipped one, and the next change gates against
// the lowered number — a per-merge ratchet, one recording at a time.
//
// Driven through the command rather than through withToleratedDipsRestored,
// because the helper being correct says nothing about whether anything calls
// it: the anchoring lived in the measuring path before recording became a
// command of its own, and a unit test of the helper passed either way.
func TestRecordingATreeAgainDoesNotLowerItsAnchoredEntry(t *testing.T) {
	root := gateRepo(t)
	// A tolerance wide enough that the fixture's coarse profile can dip
	// inside it. The fixture measures two statements, so its only movements
	// are 50 points and 100; a realistic tolerance cannot absorb either, and
	// what is under test is the anchoring rather than the threshold.
	write(t, root, config.FileName, "coverage:\n  tolerance: 60\n")
	if r := executil.RunQuiet(context.Background(), root, "git", "add", "-A"); !r.Ok() {
		t.Fatal(r.Err)
	}
	if r := executil.RunQuiet(context.Background(), root, "git", "-c", "user.email=t@t", "-c", "user.name=t",
		"commit", "-m", "widen the tolerance"); !r.Ok() {
		t.Fatal(r.Err)
	}
	if _, errOut, err := runTestCmdStreams(t, root, "--gate-coverage", "--json"); err != nil {
		t.Fatalf("measuring: %v\n%s", err, errOut)
	}

	// What the run measured is in the candidate, not on the branch: the
	// measuring command writes nothing there, which is the split this whole
	// change is about.
	candidate, err := readMeasurements(filepath.Join(root, runner.ReportDir))
	if err != nil {
		t.Fatal(err)
	}
	measured := candidate.Components["svc"]
	if !measured.Measured() {
		t.Fatalf("the run measured nothing for svc: %+v", candidate)
	}

	// An earlier recording of this same tree that measured higher. Written
	// directly, because reproducing it needs a second suite the fixture does
	// not have — what is under test is what happens to it, not how it arose.
	higher := gitstate.Entry{
		LineCount: coverage.LineCount{Covered: measured.Total, Total: measured.Total},
		Producer:  measured.Producer,
	}
	tree := candidate.Tree
	if err := gitstate.WriteBaseline(context.Background(), root, tree, gitstate.Baseline{"svc": higher}); err != nil {
		t.Fatal(err)
	}

	if out, errOut, err := runRecordCmd(t, root); err != nil {
		t.Fatalf("recording again: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	got := previousOrCurrentBaseline(t, root, "HEAD")["svc"]
	if got.Percent() != higher.Percent() {
		t.Errorf("svc = %+v (%.1f%%), want the anchored %+v (%.1f%%) kept — a tolerated dip must not lower the entry",
			got, got.Percent(), higher, higher.Percent())
	}
}

// `patch(repo)` is composed straight from the stored baseline counts, so an
// entry a different instrument produced would have the fold compare across a
// change of instrument — the comparison the shard's own patch row refused.
func TestMeasurementsStoreOnlyAComparableBaseline(t *testing.T) {
	record := gitstate.Baseline{
		"same":  {LineCount: coverage.LineCount{Covered: 1, Total: 2}, Producer: "go 1.26.5"},
		"moved": {LineCount: coverage.LineCount{Covered: 1, Total: 2}, Producer: "vitest 4.1.11"},
	}
	baseline := gitstate.Baseline{
		"same":  {LineCount: coverage.LineCount{Covered: 2, Total: 2}, Producer: "go 1.26.5"},
		"moved": {LineCount: coverage.LineCount{Covered: 2, Total: 2}, Producer: "vitest 3.2.7"},
	}
	doc := measurementsFrom("tree", record, record, nil, baseline, true, nil)
	if doc.Components["same"].Base == nil {
		t.Error("a baseline the same instrument produced was not stored, so the fold has nothing to compare against")
	}
	if got := doc.Components["moved"].Base; got != nil {
		t.Errorf("moved carries a baseline from another instrument: %+v", got)
	}
}

// A run responsible for part of the declaration establishes part of a
// candidate, which is expected — completeness is the fold's question. A row
// saying only how many entries it holds announces a recording `lydite test
// record` may then refuse, so it says how many the tree declares as well.
func TestTheRecordRowSaysHowMuchOfTheDeclarationItCovers(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: "go-test"},
		{Name: "web", Dir: "web", Runner: "vitest"},
	}}
	cmd := newRootCmd()
	// A real tree, because the row names the one it would be recorded for.
	root := gitRepo(t, map[string]string{"api/x.go": "package api\n"})
	gitIn(t, root, "-c", "user.email=t@e.com", "-c", "user.name=t", "commit", "--quiet", "-m", "base")
	// One component measured, the other never reached: the shape a shard, and
	// an --affected run over a newly declared component, both produce.
	unrun := unmeasuredComponent(decl.Components[1], "the component was not selected for this run")
	unrun.Unselected = true
	_, value := candidateThisTree(context.Background(), cmd, root, decl,
		[]measurement{measured("api", runner.Go, 1, 2), unrun}, nil, nil, true, nil, 0.1)
	if !strings.HasPrefix(value, "1 of 2 component(s)") {
		t.Errorf("record row = %q, want it to name the declaration's count as well as its own", value)
	}
}

// The anchored entry and what was measured are different quantities, and the
// fold needs both: `record` lands the anchored one, because a dipped number
// recorded verbatim turns the tolerance into an unbounded downward ratchet,
// and `coverage(repo)` sums what was measured, because that is what an
// unsharded run composes. Composing from the anchor gives a sharded run
// headroom it did not earn, and prints a total nobody can reach by adding up
// the rows above it.
func TestMeasurementsKeepWhatWasMeasuredBesideWhatIsRecorded(t *testing.T) {
	record := gitstate.Baseline{"api": producing(90, 100, "go")}
	measured := gitstate.Baseline{"api": producing(89, 100, "go")}
	doc := measurementsFrom("tree", record, measured, nil, nil, true, nil)

	e := doc.Components["api"]
	if e.LineCount != (coverage.LineCount{Covered: 90, Total: 100}) {
		t.Errorf("the recorded entry is %+v, want the anchored counts", e.LineCount)
	}
	if e.Unanchored == nil || *e.Unanchored != (coverage.LineCount{Covered: 89, Total: 100}) {
		t.Fatalf("unanchored = %+v, want the counts the run measured", e.Unanchored)
	}
	if got := e.asMeasurement(component.Component{Name: "api", Dir: "api", Runner: "go-test"}); got.Lines != *e.Unanchored {
		t.Errorf("the composition reads %+v, want the measured counts", got.Lines)
	}
	// An entry that never dipped carries nothing extra: the two agree, and a
	// second copy of one number is one that can disagree with the first.
	plain := measurementsFrom("tree", record, record, nil, nil, true, nil)
	if plain.Components["api"].Unanchored != nil {
		t.Error("an entry that was not anchored still carries a second copy of its counts")
	}
}

// A partial candidate is worse than none: any non-empty entry reads as a cache
// hit, so the missing component is new on every later change and every composed
// figure it belongs to stops comparing, silently. What enforces that is the
// fold, against the declaration of the tree being recorded — never the run,
// which sees only its own slice.
//
// So a run that could not measure one component still hands on every component
// it did measure, and says why it must not be landed. The document is the only
// channel by which a shard's counts reach `lydite test merge`, and emptying it
// loses them for good: one unreadable report would drop every other component
// of that shard out of `coverage(repo)`, which is then composed over a strict
// subset and rendered as a pass.
func TestABlockedRunHandsOnItsMeasurementsAndTheFoldRefusesThem(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetErr(io.Discard)
	root := gitRepo(t, map[string]string{"api/x.go": "package api\n"})
	gitIn(t, root, "-c", "user.email=t@e.com", "-c", "user.name=t", "commit", "--quiet", "-m", "base")
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: "go-test"},
		{Name: "web", Dir: "web", Runner: "vitest"},
	}}
	// api measured; web ran and its report could not be read, which is what
	// blocks the recording.
	ms := []measurement{
		measured("api", runner.Go, 9, 10),
		unmeasuredComponent(decl.Components[1], "the coverage report lists no coverable line"),
	}

	doc, value := candidateThisTree(context.Background(), cmd, root, decl, ms, nil, nil, true, nil, 0.1)

	if _, ok := doc.Components["api"]; !ok {
		t.Errorf("the run discarded the component it measured: %+v", doc)
	}
	if doc.Reason == "" || !strings.Contains(doc.Reason, "web") {
		t.Errorf("doc.Reason = %q, want the blocked component named", doc.Reason)
	}
	if !strings.Contains(value, "not recorded") {
		t.Errorf("row = %q, want it to say the tree was not recorded", value)
	}
	// And the fold refuses it, which is where the refusal belongs.
	if gap, blocked := missingFromRecord(decl, doc); !blocked || !strings.Contains(gap, "web") {
		t.Errorf("missingFromRecord = (%q, %v), want the fold to refuse the partial document", gap, blocked)
	}
}
