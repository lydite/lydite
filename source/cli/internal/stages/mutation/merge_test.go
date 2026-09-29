package mutationstages

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/shard"
)

// mergeLogName is the log name the fold's tests read projections from.
const mergeLogName = "mutation.log"

// mergeDeclaration writes a declaration naming each component under its own
// directory, and returns the root.
func mergeDeclaration(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	body := "components:"
	if len(names) == 0 {
		body += " []"
	}
	body += "\n"
	for _, name := range names {
		body += "  - name: " + name + "\n    dir: mod" + name + "\n    runner: go-test\n"
		if err := os.MkdirAll(filepath.Join(root, "mod"+name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".lydite"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lydite", "components.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// mergeCounts writes a counts document into a fresh shard directory.
func mergeCounts(t *testing.T, doc mutation.CountsDocument) string {
	t.Helper()
	dir := t.TempDir()
	if err := mutation.WriteCounts(dir, doc); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mergeLog writes a component's mutation log where a run writes it: under a
// directory named for the component, inside the shard's report directory.
func mergeLog(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o750); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, name, mergeLogName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The fold reads the declaration and nothing else: no configuration, since
// it executes nothing from the repository.
func TestLoadComponentsReadsTheDeclaration(t *testing.T) {
	out, err := LoadComponents(t.Context(), LoadComponentsIn{Dir: mergeDeclaration(t, "a", "b")})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Declared || len(out.File.Components) != 2 ||
		out.File.Components[0].Name != "a" || out.File.Components[1].Name != "b" {
		t.Errorf("loaded %+v, want a and b in declaration order, declared", out)
	}
}

// A declaration naming no component is not an error: the stage says so as
// data, and what that means is the caller's to decide.
func TestLoadComponentsReportsAnEmptyDeclarationAsData(t *testing.T) {
	out, err := LoadComponents(t.Context(), LoadComponentsIn{Dir: mergeDeclaration(t)})
	if err != nil {
		t.Fatalf("an empty declaration failed the stage: %v", err)
	}
	if out.Declared {
		t.Error("an empty declaration was read as naming a component")
	}
}

// A declaration that cannot be read fails the stage with the loader's own
// error.
func TestLoadComponentsFailsOnAnUnreadableDeclaration(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lydite"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".lydite", "components.yml"), []byte("components: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, want := component.Load(root)
	if want == nil {
		t.Fatal("the fixture declaration parsed")
	}
	if _, err := LoadComponents(t.Context(), LoadComponentsIn{Dir: root}); err == nil || err.Error() != want.Error() {
		t.Errorf("err = %v, want the loader's own %v", err, want)
	}
}

// A shard that wrote no counts is an older lydite, whose counts come back out
// of its prose: neither read nor an error. A document that is there and will
// not parse is that shard's error, so the fold can say so on its row.
func TestReadShardCountsSortsEachShardIntoReadAbsentOrUnreadable(t *testing.T) {
	doc := mutation.CountsDocument{Tree: "abc", Components: map[string]mutation.ComponentCounts{"a": {Killed: 3}}}
	good := mergeCounts(t, doc)
	absent := t.TempDir()
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, mutation.CountsFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := ReadShardCounts(t.Context(), ReadShardCountsIn{Shards: []shard.Shard{
		{Dir: good, Read: true},
		{Dir: absent, Read: true},
		{Dir: broken, Read: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Shards) != 3 {
		t.Fatalf("%d shard(s) of counts, want one per shard", len(out.Shards))
	}
	if s := out.Shards[0]; s.Dir != good || !s.Read || s.Err != nil || !reflect.DeepEqual(s.Counts, doc) {
		t.Errorf("the readable shard = %+v, want its document read", s)
	}
	if s := out.Shards[1]; s.Dir != absent || s.Read || s.Err != nil {
		t.Errorf("the shard that wrote none = %+v, want neither read nor an error", s)
	}
	if s := out.Shards[2]; s.Dir != broken || s.Read || s.Err == nil ||
		!strings.Contains(s.Err.Error(), filepath.Join(broken, mutation.CountsFileName)) {
		t.Errorf("the unparseable shard = %+v, want an error naming its document", s)
	}
}

// A shard whose report was not read has no row for its counts to be said on,
// so they are never read, however readable they are.
func TestReadShardCountsLeavesAnUnreadShardAlone(t *testing.T) {
	dir := mergeCounts(t, mutation.CountsDocument{Tree: "abc"})
	out, err := ReadShardCounts(t.Context(), ReadShardCountsIn{Shards: []shard.Shard{{Dir: dir}}})
	if err != nil {
		t.Fatal(err)
	}
	if s := out.Shards[0]; s.Dir != dir || s.Read || s.Err != nil {
		t.Errorf("an unread shard's counts = %+v, want nothing read", s)
	}
}

// Every shard that wrote counts folds into one document, and a shard that
// wrote none contributes nothing rather than failing the fold.
func TestFoldShardCountsFoldsTheShardsThatWroteCounts(t *testing.T) {
	out, err := FoldShardCounts(t.Context(), FoldShardCountsIn{Shards: []ShardCounts{
		{Dir: "one", Read: true, Counts: mutation.CountsDocument{Tree: "abc",
			Components: map[string]mutation.ComponentCounts{"a": {Killed: 3}}}},
		{Dir: "none"},
		{Dir: "two", Read: true, Counts: mutation.CountsDocument{Tree: "abc",
			Components: map[string]mutation.ComponentCounts{"b": {Killed: 1, Survived: 1}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Err != nil {
		t.Fatalf("the shards did not fold: %v", out.Err)
	}
	want := mutation.CountsDocument{Tree: "abc", Components: map[string]mutation.ComponentCounts{
		"a": {Killed: 3}, "b": {Killed: 1, Survived: 1},
	}}
	if !reflect.DeepEqual(out.Counts, want) {
		t.Errorf("folded %+v, want %+v", out.Counts, want)
	}
}

// Shards that mutated different trees are not one run. That is part of what
// the fold reports, so it is returned as data beside no counts, and the stage
// itself does not fail.
func TestFoldShardCountsReturnsADisagreementAboutTheTreeAsData(t *testing.T) {
	out, err := FoldShardCounts(t.Context(), FoldShardCountsIn{Shards: []ShardCounts{
		{Dir: "one", Read: true, Counts: mutation.CountsDocument{Tree: "aaaa"}},
		{Dir: "two", Read: true, Counts: mutation.CountsDocument{Tree: "bbbb"}},
	}})
	if err != nil {
		t.Fatalf("a disagreement failed the stage: %v", err)
	}
	if out.Err == nil || !strings.Contains(out.Err.Error(), "different trees") {
		t.Errorf("Err = %v, want the disagreement named", out.Err)
	}
	if !reflect.DeepEqual(out.Counts, mutation.CountsDocument{}) {
		t.Errorf("counts = %+v beside a disagreement, want none", out.Counts)
	}
}

// No shard wrote counts — every shard's lydite predates the document — which
// is nothing to fold and nothing wrong.
func TestFoldShardCountsWithNothingToFoldAnswersNothing(t *testing.T) {
	out, err := FoldShardCounts(t.Context(), FoldShardCountsIn{Shards: []ShardCounts{{Dir: "none"}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Err != nil || !reflect.DeepEqual(out.Counts, mutation.CountsDocument{}) {
		t.Errorf("folded %+v, want neither counts nor an error", out)
	}
}

// Each declared component's projection is the first shard's, in the order the
// shards were named, whose log carries one; a component no log carries one for
// is absent.
func TestReadProjectionsTakesTheFirstShardWhoseLogCarriesOne(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	early := costProjection(9, 4, budget(20*time.Second, 0))
	late := costProjection(412, 4, budget(30*time.Second, 0))
	mergeLog(t, first, "a", "running the baseline suite")
	mergeLog(t, second, "a", "running the baseline suite", late)
	mergeLog(t, first, "b", early)
	mergeLog(t, second, "b", late)

	out, err := ReadProjections(t.Context(), ReadProjectionsIn{
		Reports: []string{first, second},
		File:    component.File{Components: []component.Component{{Name: "a"}, {Name: "b"}, {Name: "c"}}},
		LogName: mergeLogName,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Projection{
		"a": {Dir: second, Line: late},
		"b": {Dir: first, Line: early},
	}
	if !reflect.DeepEqual(out.Projections, want) {
		t.Errorf("projections = %+v, want %+v", out.Projections, want)
	}
}

// The log is read under the name the caller gives it and no other.
func TestReadProjectionsReadsTheLogTheCallerNames(t *testing.T) {
	dir := t.TempDir()
	mergeLog(t, dir, "a", costProjection(9, 4, budget(20*time.Second, 0)))

	out, err := ReadProjections(t.Context(), ReadProjectionsIn{
		Reports: []string{dir},
		File:    component.File{Components: []component.Component{{Name: "a"}}},
		LogName: "test.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Projections) != 0 {
		t.Errorf("projections = %+v from a log the caller did not name", out.Projections)
	}
}

// The projection is written through one format string and read back through the
// same one, so a reader cannot hold a copy of the wording that drifts from the
// writer's.
func TestTheProjectionIsReadBackThroughTheFormatItIsWrittenWith(t *testing.T) {
	line := costProjection(37, 3, budget(time.Minute, 0))
	got, ok := costProjectionIn(line)
	if !ok || got != line {
		t.Errorf("costProjectionIn(%q) = %q, %v; want the line back", line, got, ok)
	}
	for _, other := range []string{
		"running 412 tests",
		"",
		"9 mutant(s), budget 1m0s each, 4 worker(s): at most",
		"app | " + line,
	} {
		if got, ok := costProjectionIn(other); ok || got != "" {
			t.Errorf("%q was read as the projection %q, %v; want no line at all", other, got, ok)
		}
	}
}

// Every failure of the search answers with no line at all, and not with text a
// caller taking the line without its flag would go on to quote as something a
// run said: a component with no log and a log that never reached the projection
// are both nothing to say.
func TestAShardWithNoProjectionToQuoteAnswersWithNoLine(t *testing.T) {
	dir := t.TempDir()

	if line, ok := shardProjection(dir, "b", mergeLogName); ok || line != "" {
		t.Errorf("a component with no log answered %q, %v; want no line at all", line, ok)
	}

	mergeLog(t, dir, "b", "running the baseline suite", "ok fixture/b 1.2s")
	if line, ok := shardProjection(dir, "b", mergeLogName); ok || line != "" {
		t.Errorf("a log that never reached the projection answered %q, %v; want no line at all", line, ok)
	}
}

// A suite writes whatever it likes into the log the projection shares — a
// fixture dumped whole, a payload in a panic — and such a line is far longer
// than the limit a scanner reads with by default. The projection sits below
// those lines rather than above them, so a run that wrote one is read past it.
func TestTheProjectionIsFoundBelowALineLongerThanTheDefaultLimit(t *testing.T) {
	dir := t.TempDir()
	line := costProjection(412, 4, budget(30*time.Second, 0))
	mergeLog(t, dir, "b", strings.Repeat("x", 512*1024), line)

	got, ok := shardProjection(dir, "b", mergeLogName)
	if !ok || got != line {
		t.Errorf("the projection below a long line read back as %q, %v; want %q", got, ok, line)
	}
}

// The first projection line a log carries is the one quoted, not the last: a
// run that wrote a second one — a worker restarting the count mid-run — does
// not retroactively change what the fold already said about this component.
func TestShardProjectionTakesTheFirstProjectionLineInOneLog(t *testing.T) {
	dir := t.TempDir()
	first := costProjection(9, 2, budget(time.Minute, 0))
	second := costProjection(41, 4, budget(2*time.Minute, 0))
	mergeLog(t, dir, "b", first, second)

	got, ok := shardProjection(dir, "b", mergeLogName)
	if !ok || got != first {
		t.Errorf("shardProjection with two projection lines answered %q, %v; want the first, %q", got, ok, first)
	}
}

// scanLog's own return distinguishes a visit that stopped itself from one
// that ran the log to its end: a caller that reads scanLog's return value
// alongside its own state — shardProgress guards a partial read with it —
// needs the two told apart.
func TestScanLogReportsThatItStoppedWhereVisitDid(t *testing.T) {
	dir := t.TempDir()
	mergeLog(t, dir, "b", "first", "second", "third")

	var seen []string
	ok := scanLog(dir, "b", mergeLogName, func(line string) bool {
		seen = append(seen, line)
		return line != "second"
	})
	if !ok {
		t.Errorf("scanLog whose visit stopped itself answered %v, want true", ok)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("visit saw %v, want %v — the scan kept going past where it stopped", seen, want)
	}
}

// The scanner's ceiling is a mebibyte, and a line past it ends the scan where
// it stands: a projection below such a line is not found, and the answer is
// no line at all rather than an error.
func TestAProjectionBelowALinePastTheCeilingIsNotFound(t *testing.T) {
	dir := t.TempDir()
	mergeLog(t, dir, "b", strings.Repeat("x", 1024*1024+1), costProjection(412, 4, budget(30*time.Second, 0)))

	if got, ok := shardProjection(dir, "b", mergeLogName); ok || got != "" {
		t.Errorf("the projection below an over-long line read back as %q, %v; want no line at all", got, ok)
	}
}

// progressMutant is a mutant spelled the way the executor spells it in a log,
// through mutation.Mutant's own String, so a reader holding its own copy of the
// spelling cannot pass here while the executor writes something else.
func progressMutant(line int) string {
	return mutation.Mutant{Path: "api/a.go", Line: line, Column: 7,
		Operator: mutation.NegateConditional, Original: "<", Mutated: ">="}.String()
}

// finished is the line the executor writes once a mutant has an outcome.
func finished(m string, o mutation.Outcome) string {
	return m + ": " + string(o) + " in 1.2s"
}

// A mutant the log names as started and records no outcome for was running when
// the run stopped. Every mutant named that way is returned, in the order it
// started — several are in flight at once over more than one worker — against
// the number the log records an outcome for and the number its projection
// names.
func TestReadProgressNamesEveryMutantStartedAndNeverFinished(t *testing.T) {
	dir := t.TempDir()
	one, two, three, four := progressMutant(1), progressMutant(2), progressMutant(3), progressMutant(4)
	mergeLog(t, dir, "b",
		"running the baseline suite",
		costProjection(412, 4, budget(30*time.Second, 0)),
		"start "+one, "start "+two,
		finished(one, mutation.Killed),
		one+": releasing the mutant: busy",
		"start "+three, "start "+four,
		finished(four, mutation.Unviable),
		"api/a.go:9:1: undefined: x")

	out, err := ReadProgress(t.Context(), ReadProgressIn{
		Shards:  []shard.Shard{{Dir: dir, Err: os.ErrNotExist}},
		File:    component.File{Components: []component.Component{{Name: "a"}, {Name: "b"}}},
		LogName: mergeLogName,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []ShardProgress{{Dir: dir, Components: []Progress{{
		Component: "b", Planned: 412, Projected: true, Started: true, Finished: 2,
		InFlight: []string{two, three},
	}}}}
	if !reflect.DeepEqual(out.Shards, want) {
		t.Errorf("progress = %+v\nwant %+v", out.Shards, want)
	}
}

// A log that names no mutant at all is a run that stopped before the first one:
// a build, an install or a baseline that never returned leaves exactly this.
func TestReadProgressReadsALogNamingNoMutantAsNotStarted(t *testing.T) {
	dir := t.TempDir()
	mergeLog(t, dir, "b", "running the baseline suite", "start the fixture server")

	p, ok := shardProgress(dir, "b", mergeLogName)
	if !ok {
		t.Fatal("a log that reads through was not read")
	}
	want := Progress{Component: "b"}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("progress = %+v, want %+v: a suite's own `start` line is not a mutant", p, want)
	}
}

// Only a shard whose report was not read is asked about, and only for a
// component it left a log for. A log that does not read through is no answer
// at all, because a mutant whose outcome lies past where the read stopped would
// be named as in flight.
func TestReadProgressAsksOnlyAnUnreadShardAndOnlyALogThatReadsThrough(t *testing.T) {
	read, unread := t.TempDir(), t.TempDir()
	mergeLog(t, read, "a", "start "+progressMutant(1))
	mergeLog(t, unread, "a", "start "+progressMutant(1), strings.Repeat("x", 1024*1024+1), finished(progressMutant(1), mutation.Survived))

	out, err := ReadProgress(t.Context(), ReadProgressIn{
		Shards:  []shard.Shard{{Dir: read, Read: true}, {Dir: unread, Err: os.ErrNotExist}},
		File:    component.File{Components: []component.Component{{Name: "a"}, {Name: "b"}}},
		LogName: mergeLogName,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []ShardProgress{{Dir: unread}}
	if !reflect.DeepEqual(out.Shards, want) {
		t.Errorf("progress = %+v, want only the unread shard, with nothing read from it", out.Shards)
	}
}

// Every outcome the executor can write ends a mutant, and the projection's
// count is read through the same format the projection is written with.
func TestShardProgressCountsEveryOutcomeAsFinished(t *testing.T) {
	outcomes := []mutation.Outcome{mutation.Killed, mutation.TimedOut, mutation.OutOfMemory,
		mutation.Survived, mutation.Unviable, mutation.Acknowledged}
	lines := []string{costProjection(9, 4, budget(20*time.Second, 0))}
	for i, o := range outcomes {
		m := progressMutant(i + 1)
		lines = append(lines, "start "+m, finished(m, o))
	}
	dir := t.TempDir()
	mergeLog(t, dir, "b", lines...)

	p, ok := shardProgress(dir, "b", mergeLogName)
	if !ok || p.Finished != len(outcomes) || len(p.InFlight) != 0 || !p.Started || !p.Projected || p.Planned != 9 {
		t.Errorf("progress = %+v, %v; want all %d finished of 9 projected, none in flight", p, ok, len(outcomes))
	}
}
