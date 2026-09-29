package mutationstages

import (
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/scheduler"
)

// A component declaring compose services runs its mutants one at a time: eight
// suites against one database truncate each other's tables, which surfaces as
// mutants surviving at random and a score that varies run to run.
func TestAComponentPublishingAPortMutatesSerially(t *testing.T) {
	if got := workersFor([]int{5432}, 8); got != 1 {
		t.Errorf("%d workers for a component publishing a port, want 1", got)
	}
	if got := workersFor(nil, 8); got != 8 {
		t.Errorf("%d workers for a component publishing none, want the run's own bound", got)
	}
	// The predicate is the scheduler's own, so a component that publishes
	// nothing holds nothing: a second implementation here would answer
	// differently the day one of them learned about a port syntax.
	if len(scheduler.Conflicts([]scheduler.Item{{Name: "a"}, {Name: "b"}})) != 0 {
		t.Error("two items holding nothing were reported in conflict")
	}
}

// The budget multiplies something this run measured, which is what separates
// it from the invented runtime cap ADR 0027 refuses. Without one, TimedOut is
// an outcome nothing can produce.
func TestTheBudgetIsAMultipleOfTheMeasuredBaseline(t *testing.T) {
	long := 5 * time.Minute
	if got := budget(long, 0); got != long*budgetFactor {
		t.Errorf("budget(%s) = %s, want %s", long, got, long*budgetFactor)
	}
	// A suite too fast to measure would otherwise give every mutant a budget
	// shorter than the compiler takes to start, and every one of them would
	// be reported as a hang.
	if got := budget(40*time.Millisecond, 0); got != minimumBudget {
		t.Errorf("budget for a 40ms baseline = %s, want the %s floor", got, minimumBudget)
	}
	if got := budget(long, 7*time.Second); got != 7*time.Second {
		t.Errorf("--timeout was not honoured: %s", got)
	}
}

// A run says what it is about to cost before it pays it, and the ceiling is
// every mutant taking its whole budget with no worker idle. It is a projection
// and not a cap: nothing reads it to stop a run, which only a --deadline does.
func TestARunProjectsItsCeilingFromTheBudgetAndTheWorkers(t *testing.T) {
	const timeout = 60 * time.Second
	for _, c := range []struct {
		mutants, workers int
		want             time.Duration
	}{
		// Divides exactly: two rounds of four.
		{mutants: 8, workers: 4, want: 2 * timeout},
		// The remainder is a whole round: three mutants over two workers
		// leaves one running alone, and the run waits for it.
		{mutants: 9, workers: 4, want: 3 * timeout},
		// A component publishing a port stages one mutant at a time, so its
		// ceiling is the whole run end to end.
		{mutants: 5, workers: 1, want: 5 * timeout},
		// More workers than mutants buys no round that is not there.
		{mutants: 2, workers: 8, want: timeout},
	} {
		if got := projectedCeiling(c.mutants, c.workers, timeout); got != c.want {
			t.Errorf("%d mutant(s) over %d worker(s) = %s, want %s", c.mutants, c.workers, got, c.want)
		}
	}
}

// The line carries its own derivation, so a reader who thinks the projection is
// wrong can see which term they disagree with rather than only the total.
func TestTheProjectionStatesWhatItIsDerivedFrom(t *testing.T) {
	line := costProjection(9, 4, budget(20*time.Second, 0))
	for _, want := range []string{"9 mutant(s)", "budget 1m0s each", "4 worker(s)", "at most 3m0s"} {
		if !strings.Contains(line, want) {
			t.Errorf("the projection %q does not state %q", line, want)
		}
	}
}

// --timeout is what a mutant's budget becomes, so it is what the projection is
// derived from too: a line stating a ceiling the run is not going to honour is
// worse than none.
func TestTheProjectionIsDerivedFromTheOverriddenTimeout(t *testing.T) {
	line := costProjection(4, 2, budget(5*time.Minute, 30*time.Second))
	if !strings.Contains(line, "budget 30s each") || !strings.Contains(line, "at most 1m0s") {
		t.Errorf("--timeout was not projected from: %q", line)
	}
}

// The memory bound multiplies the component's own measured peak, never the
// machine's memory: a bound that moved with the machine would make a mutant
// killed on a small runner and surviving on a large one.
func TestTheMemoryBoundIsAMultipleOfTheMeasuredBaseline(t *testing.T) {
	const big = 4 << 30
	if got := memoryBudget(big, 0); got != big*memoryFactor {
		t.Errorf("memoryBudget(%d) = %d, want %d", big, got, big*memoryFactor)
	}
	// Four times a small peak is a ceiling a compiler reaches on its own, and
	// every mutant would be reported as one that allocated without stopping.
	if got := memoryBudget(40<<20, 0); got != minimumMemory {
		t.Errorf("the bound for a 40MiB baseline = %d, want the %d floor", got, minimumMemory)
	}
	if got := memoryBudget(big, 7<<30); got != 7<<30 {
		t.Errorf("--memory was not honoured: %d", got)
	}
}

// A ceiling the baseline itself already fills is one every mutant reaches
// whatever its tests do, so the component gates nothing rather than reporting
// a suite that kills everything. Only an override can produce it: the
// derivation is four times that same peak.
func TestABaselineThatWouldNotFitUnderTheBoundGatesNothing(t *testing.T) {
	const peak = 1 << 30
	if !memoryFits(peak, memoryBudget(peak, 0)) {
		t.Error("a derived bound left its own baseline no room")
	}
	if memoryFits(peak, memoryBudget(peak, peak)) {
		t.Error("a bound equal to the baseline's own peak was accepted")
	}
	if memoryFits(peak, memoryBudget(peak, peak*memoryHeadroom-1)) {
		t.Errorf("a bound under %d times the baseline's peak was accepted", memoryHeadroom)
	}
	if !memoryFits(peak, memoryBudget(peak, peak*memoryHeadroom)) {
		t.Errorf("a bound of exactly %d times the baseline's peak was refused", memoryHeadroom)
	}
}
