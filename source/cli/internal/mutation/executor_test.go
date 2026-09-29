package mutation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lydite/lydite/internal/runner"
)

// shell is a command every platform lydite builds for has, so the executor's
// own behaviour can be exercised without a language toolchain. It is the same
// shell `lydite test` already runs a component's setup commands through.
func shell(script string) runner.Invocation {
	return runner.Invocation{Name: "sh", Args: []string{"-c", script}}
}

// fake stages whatever the test says each mutant compiles and runs as.
type fake struct {
	plan func(m Mutant) Staged
	// closeErr and releaseErr are what a worker's own housekeeping fails
	// with, which is the only way into either branch: both are real I/O on a
	// real directory in every backend lydite ships.
	closeErr, releaseErr error

	mu      sync.Mutex
	workers int
	staged  []Mutant
	// live is how many mutants were staged and not yet released at once,
	// which is what says whether a serial component ran serially.
	live, peak int
}

func (f *fake) Worker(context.Context, int) (Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workers++
	return &fakeWorker{f: f}, nil
}

type fakeWorker struct{ f *fake }

func (w *fakeWorker) Stage(m Mutant) (Staged, error) {
	w.f.mu.Lock()
	w.f.staged = append(w.f.staged, m)
	w.f.live++
	if w.f.live > w.f.peak {
		w.f.peak = w.f.live
	}
	w.f.mu.Unlock()
	return w.f.plan(m), nil
}

func (w *fakeWorker) Release() error {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()
	w.f.live--
	return w.f.releaseErr
}

func (w *fakeWorker) Close() error { return w.f.closeErr }

func outcomes(results []Result) []Outcome {
	out := make([]Outcome, len(results))
	for i, r := range results {
		out[i] = r.Outcome
	}
	return out
}

func mutantAt(line int) Mutant {
	return Mutant{Path: "a.go", Line: line, Column: 1, Operator: NegateConditional, Original: "<", Mutated: ">="}
}

func TestAnAcknowledgedMutantIsNeverStaged(t *testing.T) {
	f := &fake{plan: func(Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 0")}}
	}}
	m := mutantAt(1)
	m.Reason = "the branch is unreachable by construction"

	results, err := Execute(t.Context(), f, []Mutant{m}, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := outcomes(results); got[0] != Acknowledged {
		t.Errorf("outcome = %q, want %q", got[0], Acknowledged)
	}
	if len(f.staged) != 0 {
		t.Errorf("an acknowledged mutant was staged: %v", f.staged)
	}
	if results[0].Detail != m.Reason {
		t.Errorf("detail = %q, want the declared reason", results[0].Detail)
	}
}

func TestAMutantThatDoesNotBuildIsUnviableAndItsSuiteNeverRuns(t *testing.T) {
	// The suite would pass, so a build failure that did not stop the run
	// would report this mutant as a survivor; counted as a kill instead it
	// would inflate the score silently and permanently.
	marker := t.TempDir() + "/suite-ran"
	f := &fake{plan: func(Mutant) Staged {
		return Staged{
			Build:  shell("echo 'a.go:3:2: undefined: x' >&2; exit 1"),
			Phases: []runner.Invocation{shell("touch " + marker)},
		}
	}}
	results, err := Execute(t.Context(), f, []Mutant{mutantAt(1)}, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Outcome != Unviable {
		t.Fatalf("outcome = %q, want %q", results[0].Outcome, Unviable)
	}
	if !strings.Contains(results[0].Detail, "undefined: x") {
		t.Errorf("detail = %q, want the compiler's own words", results[0].Detail)
	}
	if fileExists(marker) {
		t.Error("the suite ran against a mutant that did not compile")
	}
}

func TestAKillInTheFirstPhaseLeavesTheSecondUnrun(t *testing.T) {
	// The second phase writes a file; that it does not exist afterwards is
	// what says the phase never ran. Ordering the two is what turns this
	// repository's expensive kill from 76s into 2.36s.
	marker := t.TempDir() + "/closure-ran"
	f := &fake{plan: func(Mutant) Staged {
		return Staged{
			Build: shell("exit 0"),
			Phases: []runner.Invocation{
				shell("exit 1"),
				shell("touch " + marker),
			},
		}
	}}
	results, err := Execute(t.Context(), f, []Mutant{mutantAt(1)}, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Outcome != Killed {
		t.Fatalf("outcome = %q, want %q", results[0].Outcome, Killed)
	}
	if fileExists(marker) {
		t.Error("the closure phase ran for a mutant its own package had already killed")
	}
}

func TestAMutantSurvivesOnlyAfterEveryPhasePassed(t *testing.T) {
	f := &fake{plan: func(Mutant) Staged {
		return Staged{
			Build:  shell("exit 0"),
			Phases: []runner.Invocation{shell("exit 0"), shell("exit 1")},
		}
	}}
	results, err := Execute(t.Context(), f, []Mutant{mutantAt(1)}, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Killed by the closure after surviving its own package, which is the
	// case the second phase exists for: a mutant in a library is routinely
	// killed only by its caller's tests.
	if results[0].Outcome != Killed {
		t.Errorf("outcome = %q, want %q", results[0].Outcome, Killed)
	}
}

func TestASuiteThatHangsTimesOutAndCountsAsKilled(t *testing.T) {
	f := &fake{plan: func(Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("sleep 30")}}
	}}
	results, err := Execute(t.Context(), f, []Mutant{mutantAt(1)}, Options{Workers: 1, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Outcome != TimedOut {
		t.Fatalf("outcome = %q, want %q", results[0].Outcome, TimedOut)
	}
	var s Summary
	s.Add(results[0])
	if k, n := s.Score(); k != 1 || n != 1 {
		t.Errorf("a timeout scored %d of %d, want 1 of 1 — a hang is a behaviour change something noticed", k, n)
	}
}

// allocEnv carries the size, in MiB, the helper child below allocates. Its
// presence is also what tells that child apart from an ordinary run of this
// test binary.
const allocEnv = "LYDITE_MUTATION_ALLOC_MIB"

// TestMutationAllocatingChild is the mutant a memory bound is measured
// against, re-executed out of this test binary so the measurement needs no
// language toolchain to build one.
func TestMutationAllocatingChild(t *testing.T) {
	mib, err := strconv.Atoi(os.Getenv(allocEnv))
	if err != nil {
		t.Skip("not the allocating child")
	}
	// Every byte is written, because a page counts against the bound when it
	// is mapped and against the reported peak only when it is touched — a
	// child that allocates without touching dies at a peak nothing recognises
	// as the bound. The writes go through copy so the race detector
	// instruments a chunk rather than a byte.
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = 1
	}
	held := make([][]byte, 0, mib)
	for range mib {
		b := make([]byte, len(chunk))
		copy(b, chunk)
		held = append(held, b)
	}
	fmt.Printf("allocated %d MiB\n", len(held))
}

// allocating stages one mutant whose suite is the child above, asking it for
// the size the run's own environment carries.
func allocating() *fake {
	return &fake{plan: func(Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{
			{Name: os.Args[0], Args: []string{"-test.run=^TestMutationAllocatingChild$"}},
		}}
	}}
}

// A mutant that allocates without stopping is killed by the bound and counted
// as killed: an allocation that does not stop is a behaviour change something
// noticed, exactly as a hang is. Without the bound the machine's memory goes
// instead, and what dies is the agent running the job rather than the job.
func TestASuiteThatAllocatesWithoutStoppingIsKilledByItsMemoryBound(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_DATA is settable only on linux — darwin's setrlimit refuses it with EINVAL at any value — so this is proven by the linux CI job")
	}
	const limit = 1 << 30
	results, err := Execute(t.Context(), allocating(), []Mutant{mutantAt(1)}, Options{
		Workers: 1, MaxMemory: limit, Env: []string{allocEnv + "=1536"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Outcome != OutOfMemory {
		t.Fatalf("outcome = %q, want %q: %s", results[0].Outcome, OutOfMemory, results[0].Detail)
	}
	if results[0].MemoryUnbounded {
		t.Error("the mutant reports the bound never reached it, on a platform that set one")
	}
	var s Summary
	s.Add(results[0])
	if k, n := s.Score(); k != 1 || n != 1 {
		t.Errorf("a mutant stopped at its memory bound scored %d of %d, want 1 of 1", k, n)
	}
}

// A phase that exits zero while something it started still holds its output
// open is reported alongside its outcome, never silently: the executor's own
// process-group kill is what let the run finish at all, and that is not the
// same thing as nothing having been left running.
func TestAPhaseThatExitsCleanWhileHoldingOutputOpenIsReportedOnTheResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are a unix notion")
	}
	staged := &fake{plan: func(Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{
			shell("sleep 300 & exit 0"),
		}}
	}}
	results, err := Execute(t.Context(), staged, []Mutant{mutantAt(1)}, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Outcome != Survived {
		t.Fatalf("outcome = %q, want %q: %s", results[0].Outcome, Survived, results[0].Detail)
	}
	if !results[0].OutputHeldOpen {
		t.Error("the mutant reports nothing was left running, though its phase's grandchild held output open past the wait delay")
	}
}

// A mutant whose compilation reached the ceiling is unviable rather than
// killed: nothing ran, so nothing observed the change, and what the bound
// caught there is the compiler's appetite.
func TestABuildThatReachesTheMemoryBoundIsUnviable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_DATA is settable only on linux — darwin's setrlimit refuses it with EINVAL at any value — so this is proven by the linux CI job")
	}
	f := &fake{plan: func(Mutant) Staged {
		return Staged{Build: runner.Invocation{
			Name: os.Args[0], Args: []string{"-test.run=^TestMutationAllocatingChild$"},
		}}
	}}
	results, err := Execute(t.Context(), f, []Mutant{mutantAt(1)}, Options{
		Workers: 1, MaxMemory: 1 << 30, Env: []string{allocEnv + "=1536"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Outcome != Unviable {
		t.Fatalf("outcome = %q, want %q: %s", results[0].Outcome, Unviable, results[0].Detail)
	}
	if !strings.Contains(results[0].Detail, "did not compile") {
		t.Errorf("detail = %q, want the bound named as what stopped the compilation", results[0].Detail)
	}
	var s Summary
	s.Add(results[0])
	if s.Denominator() != 0 {
		t.Error("a mutant that was never built was counted as evidence about the suite")
	}
}

// A suite that stays under the bound is untouched by it, so a mutant nothing
// killed still survives — a bound that failed every mutant would report a
// component whose tests are perfect.
func TestASuiteUnderItsMemoryBoundIsUntouched(t *testing.T) {
	results, err := Execute(t.Context(), allocating(), []Mutant{mutantAt(1)}, Options{
		Workers: 1, MaxMemory: 1 << 30, Env: []string{allocEnv + "=16"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Outcome != Survived {
		t.Fatalf("outcome = %q, want %q: %s", results[0].Outcome, Survived, results[0].Detail)
	}
}

// Three states have to be told apart, and a ceiling asked for on a platform
// that has none to set is the one that matters: reported as enforced it would
// render the green of a bound that held.
func TestAMutantSaysWhetherTheBoundReachedIt(t *testing.T) {
	passing := &fake{plan: func(Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 0")}}
	}}
	bounded, err := Execute(t.Context(), passing, []Mutant{mutantAt(1)}, Options{Workers: 1, MaxMemory: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	if want := runtime.GOOS != "linux"; Unbounded(bounded) != want {
		t.Errorf("Unbounded = %v on %s, want %v", Unbounded(bounded), runtime.GOOS, want)
	}

	// A run that asked for no ceiling is not one whose ceiling went missing,
	// and a row saying memory was not bounded on every such run is one nobody
	// reads.
	none, err := Execute(t.Context(), passing, []Mutant{mutantAt(1)}, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if Unbounded(none) {
		t.Error("a run that asked for no memory bound reports one that went missing")
	}
}

func TestAnInterruptedRunReportsEveryMutantItDidNotReach(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := &fake{plan: func(Mutant) Staged {
		cancel()
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 0")}}
	}}
	mutants := []Mutant{mutantAt(1), mutantAt(2), mutantAt(3)}
	results, err := Execute(ctx, f, mutants, Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(mutants) {
		t.Fatalf("%d result(s) for %d mutants — a truncated run that dropped mutants reads as a complete one over fewer", len(results), len(mutants))
	}
	for i, r := range results {
		if r.Outcome == Survived || r.Outcome == Killed {
			t.Errorf("mutant %d reported %q from a cancelled run", i, r.Outcome)
		}
	}
	var s Summary
	for _, r := range results {
		s.Add(r)
	}
	if s.Denominator() != 0 {
		t.Errorf("a cancelled run put %d mutant(s) in the denominator", s.Denominator())
	}
	for i, r := range results {
		if !r.CutShort {
			t.Errorf("mutant %d (%q, %q) is not marked cut short — a resumed run would reuse it as a verdict", i, r.Outcome, r.Detail)
		}
	}
}

// passingBuild stages a mutant that builds and whose suite exits with code.
func passingBuild(code int) *fake {
	return &fake{plan: func(Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell(fmt.Sprintf("exit %d", code))}}
	}}
}

// recorder collects what Options.Record is handed, from every worker at once.
type recorder struct {
	mu   sync.Mutex
	seen []Result
}

func (r *recorder) record(res Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, res)
}

func (r *recorder) known() map[string]Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]Result{}
	for _, res := range r.seen {
		out[MutantID(res.Mutant)] = res
	}
	return out
}

func stagedLines(f *fake) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines := make([]int, 0, len(f.staged))
	for _, m := range f.staged {
		lines = append(lines, m.Line)
	}
	slices.Sort(lines)
	return lines
}

// mutantOn is a mutant whose identity differs by its offset, since MutantID
// leaves the line out: two mutants distinguished only by line are one mutant.
func mutantOn(line int) Mutant {
	m := mutantAt(line)
	m.Offset = line * 10
	m.Length = 1
	return m
}

func TestAMutantWithAKnownVerdictIsNeverDispatched(t *testing.T) {
	f := passingBuild(1)
	mutants := []Mutant{mutantOn(1), mutantOn(2), mutantOn(3)}
	known := map[string]Result{}
	for _, m := range mutants {
		recorded := m
		// A recorded mutant carries whatever line it had when it was
		// recorded; the answer is about the mutant this run was given.
		recorded.Line = 99
		known[MutantID(m)] = Result{Mutant: recorded, Outcome: Survived}
	}
	results, err := Execute(t.Context(), f, mutants, Options{Workers: 4, Known: known})
	if err != nil {
		t.Fatal(err)
	}
	if f.workers != 0 || len(f.staged) != 0 {
		t.Errorf("%d worker(s) opened and %d mutant(s) staged, though every mutant had a verdict", f.workers, len(f.staged))
	}
	for i, r := range results {
		if r.Outcome != Survived {
			t.Errorf("mutant %d = %q, want the known %q", i, r.Outcome, Survived)
		}
		if r.Mutant != mutants[i] {
			t.Errorf("mutant %d is reported as %+v, want the mutant it was given %+v", i, r.Mutant, mutants[i])
		}
	}
}

func TestOnlyTheMutantsWithNoKnownVerdictAreDispatched(t *testing.T) {
	f := passingBuild(1)
	mutants := []Mutant{mutantOn(1), mutantOn(2), mutantOn(3), mutantOn(4)}
	known := map[string]Result{
		MutantID(mutants[0]): {Mutant: mutants[0], Outcome: Survived},
		MutantID(mutants[2]): {Mutant: mutants[2], Outcome: TimedOut},
	}
	results, err := Execute(t.Context(), f, mutants, Options{Workers: 2, Known: known})
	if err != nil {
		t.Fatal(err)
	}
	if got := stagedLines(f); !slices.Equal(got, []int{2, 4}) {
		t.Errorf("staged lines %v, want only the two with no known verdict [2 4]", got)
	}
	if got, want := outcomes(results), []Outcome{Survived, Killed, TimedOut, Killed}; !slices.Equal(got, want) {
		t.Errorf("outcomes = %v, want %v in the order given", got, want)
	}
}

func TestRecordSeesOnlyTheVerdictsThisRunDecided(t *testing.T) {
	f := &fake{plan: func(m Mutant) Staged {
		if m.Line == 3 {
			return Staged{Build: shell("echo 'a.go:3:2: undefined: x' >&2; exit 1")}
		}
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 1")}}
	}}
	acknowledged := mutantOn(1)
	acknowledged.Reason = "the branch is unreachable by construction"
	mutants := []Mutant{acknowledged, mutantOn(2), mutantOn(3), mutantOn(4)}
	known := map[string]Result{MutantID(mutants[3]): {Mutant: mutants[3], Outcome: Survived}}
	var rec recorder
	if _, err := Execute(t.Context(), f, mutants, Options{Workers: 2, Known: known, Record: rec.record}); err != nil {
		t.Fatal(err)
	}
	got := map[int]Outcome{}
	for _, r := range rec.seen {
		got[r.Mutant.Line] = r.Outcome
	}
	// A mutant that would not build is a verdict as much as a kill is: the
	// same tree fails to compile it again.
	want := map[int]Outcome{2: Killed, 3: Unviable}
	if !maps.Equal(got, want) {
		t.Errorf("recorded %v, want only the fresh verdicts %v", got, want)
	}
}

func TestACancelledRunRecordsNothingItCutShortAndAResumeMeasuresExactlyThat(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var stages atomic.Int32
	interrupted := &fake{plan: func(Mutant) Staged {
		// The second mutant staged is in flight when the run ends, and the
		// third is never reached — or reached with the run already over.
		if stages.Add(1) == 2 {
			cancel()
		}
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 1")}}
	}}
	mutants := []Mutant{mutantOn(1), mutantOn(2), mutantOn(3)}
	var rec recorder
	first, err := Execute(ctx, interrupted, mutants, Options{Workers: 1, Record: rec.record})
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Outcome != Killed || first[0].CutShort {
		t.Fatalf("the first mutant = %q (cut short %v), want a kill decided before the run ended", first[0].Outcome, first[0].CutShort)
	}
	for i, r := range first[1:] {
		if !r.CutShort {
			t.Errorf("mutant %d = %q, want it cut short", i+2, r.Outcome)
		}
	}
	if len(rec.seen) != 1 || rec.seen[0].Mutant.Line != 1 {
		t.Fatalf("recorded %v, want only the kill decided before the run ended", rec.seen)
	}

	resumed := passingBuild(1)
	second, err := Execute(t.Context(), resumed, mutants, Options{Workers: 1, Known: rec.known(), Record: rec.record})
	if err != nil {
		t.Fatal(err)
	}
	if got := stagedLines(resumed); !slices.Equal(got, []int{2, 3}) {
		t.Errorf("the resumed run staged lines %v, want exactly the cut-short ones [2 3]", got)
	}
	if got, want := outcomes(second), []Outcome{Killed, Killed, Killed}; !slices.Equal(got, want) {
		t.Errorf("resumed outcomes = %v, want %v", got, want)
	}
	for i, r := range second {
		if r.CutShort {
			t.Errorf("mutant %d is still cut short after a run that finished", i+1)
		}
	}
}

func TestOneWorkerStagesOneMutantAtATime(t *testing.T) {
	// What a component declaring compose services gets: eight mutants against
	// one database truncate each other's tables, and the score then varies
	// run to run.
	f := &fake{plan: func(Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 1")}}
	}}
	mutants := []Mutant{mutantAt(1), mutantAt(2), mutantAt(3), mutantAt(4)}
	if _, err := Execute(t.Context(), f, mutants, Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	if f.peak != 1 {
		t.Errorf("%d mutants were staged at once under Workers: 1", f.peak)
	}
	if f.workers != 1 {
		t.Errorf("%d workers opened under Workers: 1", f.workers)
	}
}

func TestNoMoreWorkersAreOpenedThanThereAreMutantsToStage(t *testing.T) {
	f := &fake{plan: func(Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 1")}}
	}}
	if _, err := Execute(t.Context(), f, []Mutant{mutantAt(1)}, Options{Workers: 8}); err != nil {
		t.Fatal(err)
	}
	if f.workers != 1 {
		t.Errorf("%d workers opened for one mutant — a worker is a tree copy in every language but Go", f.workers)
	}
}

func TestResultsAreInTheOrderTheMutantsWereGiven(t *testing.T) {
	f := &fake{plan: func(m Mutant) Staged {
		// The later mutants finish first, so completion order and input
		// order differ: two runs over one tree must still produce the same
		// document.
		return Staged{
			Build:  shell("exit 0"),
			Phases: []runner.Invocation{shell("sleep 0." + string(rune('0'+4-m.Line)) + "; exit 1")},
		}
	}}
	mutants := []Mutant{mutantAt(1), mutantAt(2), mutantAt(3)}
	results, err := Execute(t.Context(), f, mutants, Options{Workers: 3})
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range results {
		if r.Mutant.Line != mutants[i].Line {
			t.Errorf("result %d is for line %d, want %d", i, r.Mutant.Line, mutants[i].Line)
		}
	}
}

func TestSlotsRefuseToBlockForeverOnACancelledRun(t *testing.T) {
	s := NewSlots(1)
	if !s.acquire(t.Context()) {
		t.Fatal("the first slot was not free")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.acquire(ctx) {
		t.Error("a cancelled run took a slot nothing had released")
	}
	s.release()
}

func TestAnUnboundedRunHasNoSlotsToTake(t *testing.T) {
	// `--concurrency max` asks for no bound at all, and a channel cannot be
	// that large: nothing to acquire is the bound, rather than a buffer
	// nobody could allocate.
	if s := NewSlots(unbounded); s != nil {
		t.Error("an unbounded run allocated a slot buffer")
	}
	var none *Slots
	if !none.acquire(t.Context()) {
		t.Error("an unbounded run could not take a slot")
	}
	none.release()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A component's baseline is a suite execution exactly as a mutant is, and the
// bound counts both. Without that, three components in their baseline beside a
// fourth executing four mutants is seven suites in flight under a bound of
// four — the oversubscription one bound exists to prevent.
func TestSlotsBoundWhateverHoldsThem(t *testing.T) {
	s := NewSlots(2)
	var live, peak, mu = 0, 0, sync.Mutex{}
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Run(t.Context(), func() {
				mu.Lock()
				live++
				if live > peak {
					peak = live
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				live--
				mu.Unlock()
			})
		}()
	}
	wg.Wait()
	if peak > 2 {
		t.Errorf("%d slots were held at once under a bound of 2", peak)
	}
	if peak < 2 {
		t.Errorf("only %d slot was ever held, so the bound was never reached and this proves nothing", peak)
	}
}

// A run cancelled while something waits for a slot never runs it, and says so
// rather than running it late.
func TestARunCancelledWhileWaitingForASlotDoesNotRun(t *testing.T) {
	s := NewSlots(1)
	if !s.acquire(t.Context()) {
		t.Fatal("the first slot was not free")
	}
	defer s.release()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ran := false
	if s.Run(ctx, func() { ran = true }) {
		t.Error("Run reported that it ran on a cancelled context")
	}
	if ran {
		t.Error("the work ran after the run was cancelled")
	}
}

// A component's mutants are dispatched against as many workers as the bound
// and the work allow, and nothing else asserted that more than one is ever
// opened: every other assertion here is satisfied by a run that opened one.
func TestAWorkerIsOpenedForEachMutantTheBoundAllows(t *testing.T) {
	f := &fake{plan: func(Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 1")}}
	}}
	mutants := []Mutant{mutantAt(1), mutantAt(2), mutantAt(3)}
	if _, err := Execute(t.Context(), f, mutants, Options{Workers: 3}); err != nil {
		t.Fatal(err)
	}
	if f.workers != 3 {
		t.Errorf("%d worker(s) opened for 3 mutants under Workers: 3", f.workers)
	}
}

// A bound of zero is no bound, exactly as a bound at or above unbounded is.
// The alternative is a channel with no buffer, which nothing ever releases
// into: the first acquire would block until the run was cancelled, and every
// suite execution in the run would wait behind it.
func TestABoundOfZeroIsNoBoundAtAll(t *testing.T) {
	for _, n := range []int{0, -1} {
		if s := NewSlots(n); s != nil {
			t.Errorf("NewSlots(%d) allocated a slot buffer nothing could release into", n)
		}
	}
}

// A worker holds a directory, and a run that could not put one back is a run
// leaving state behind. Neither failure changes any mutant's outcome — the
// evidence about the tests is already in — so it is said in the log rather
// than scored, and a log that did not say it is the whole of the report.
func TestWhatAWorkerFailsToTidySaysSoInTheLog(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fail  func(*fake)
		about string
	}{
		{"closing", func(f *fake) { f.closeErr = errors.New("the directory is busy") }, "closing the worker"},
		{"releasing", func(f *fake) { f.releaseErr = errors.New("the overlay is gone") }, "releasing the mutant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{plan: func(Mutant) Staged {
				return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 1")}}
			}}
			tc.fail(f)
			var log bytes.Buffer
			if _, err := Execute(t.Context(), f, []Mutant{mutantAt(1)}, Options{Workers: 1, Log: &log}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(log.String(), tc.about) {
				t.Errorf("the log does not say what failed:\n%s", log.String())
			}
		})
	}
}

// A line per mutant, and the compiler's own words under exactly one of them.
// A mutant that did not compile is the one outcome whose cause is nowhere
// else — a survivor is named in its row and a kill needs no explanation — so
// every other outcome's line stands alone.
func TestOnlyAMutantThatDidNotCompileHasItsDetailLogged(t *testing.T) {
	f := &fake{plan: func(m Mutant) Staged {
		if m.Line == 1 {
			return Staged{
				Build:  shell("echo 'a.go:3:2: undefined: x' >&2; exit 1"),
				Phases: []runner.Invocation{shell("exit 0")},
			}
		}
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("echo 'FAIL: TestX'; exit 1")}}
	}}
	var log bytes.Buffer
	results, err := Execute(t.Context(), f, []Mutant{mutantAt(1), mutantAt(2)}, Options{Workers: 1, Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Outcome != Unviable || results[1].Outcome != Killed {
		t.Fatalf("outcomes = %v, want one unviable and one killed", outcomes(results))
	}
	if !strings.Contains(log.String(), "undefined: x") {
		t.Errorf("the compiler's own words are nowhere:\n%s", log.String())
	}
	if strings.Contains(log.String(), "FAIL: TestX") {
		t.Errorf("a killed mutant put its suite's output in the log:\n%s", log.String())
	}
	if n := len(strings.Split(strings.TrimRight(log.String(), "\n"), "\n")); n != 5 {
		t.Errorf("%d log line(s) for two mutants, want a start and a finish line each and one detail:\n%s", n, log.String())
	}
}

// A start line names the mutant before its finish line reports its outcome,
// so a run killed or timed out mid-mutant still leaves a trace of what was in
// flight when it died.
func TestAMutantsStartLineComesBeforeItsFinishLine(t *testing.T) {
	f := &fake{plan: func(m Mutant) Staged {
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("exit 1")}}
	}}
	var log bytes.Buffer
	m := mutantAt(1)
	results, err := Execute(t.Context(), f, []Mutant{m}, Options{Workers: 1, Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Outcome != Killed {
		t.Fatalf("outcome = %v, want Killed", results[0].Outcome)
	}
	start := strings.Index(log.String(), "start "+m.String())
	finish := strings.Index(log.String(), m.String()+": "+string(Killed))
	if start < 0 {
		t.Fatalf("no start line for %s in log:\n%s", m, log.String())
	}
	if finish < 0 {
		t.Fatalf("no finish line for %s in log:\n%s", m, log.String())
	}
	if start >= finish {
		t.Errorf("start line at %d did not come before finish line at %d:\n%s", start, finish, log.String())
	}
}

// The tail is what the compiler puts the error in, and a run whose output is
// exactly as long as the tail keeps all of it.
func TestTheTailIsTheEndOfTheOutputAndNothingIsLostAtItsLength(t *testing.T) {
	line := func(n int) string { return fmt.Sprintf("line %d", n) }
	var all []string
	for i := 1; i <= detailLines+1; i++ {
		all = append(all, line(i))
	}
	if got := lastLines(strings.Join(all[:detailLines], "\n")); got != strings.Join(all[:detailLines], "\n") {
		t.Errorf("output of exactly %d lines was shortened to:\n%s", detailLines, got)
	}
	got := lastLines(strings.Join(all, "\n"))
	if want := strings.Join(all[1:], "\n"); got != want {
		t.Errorf("lastLines kept:\n%s\nwant the last %d lines", got, detailLines)
	}
	if strings.Contains(got, line(1)+"\n") {
		t.Error("the first line of an over-long output survived, so the tail is not the end")
	}
}

// A run that ends while a mutant's suite is running cuts that mutant short as
// it does one still building: the interruption says nothing about the tests, so
// nothing is recorded for it.
func TestARunEndedDuringASuiteCutsThatMutantShortAndRecordsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	f := &fake{plan: func(Mutant) Staged {
		time.AfterFunc(300*time.Millisecond, cancel)
		return Staged{Build: shell("exit 0"), Phases: []runner.Invocation{shell("sleep 30")}}
	}}
	var rec recorder
	results, err := Execute(ctx, f, []Mutant{mutantOn(1)}, Options{Workers: 1, Timeout: time.Minute, Record: rec.record})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].CutShort || results[0].Outcome != Unviable {
		t.Fatalf("results = %+v, want the one mutant cut short", results)
	}
	if !strings.Contains(results[0].Detail, "before this mutant finished") {
		t.Errorf("detail = %q, want it to say the suite was interrupted", results[0].Detail)
	}
	if len(rec.seen) != 0 {
		t.Errorf("recorded %v for a mutant whose suite was interrupted", rec.seen)
	}
}
