package mutationstages

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"time"

	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
)

// backendFor is the isolation strategy one language's mutants are run under.
//
// A language with no backend is unmeasured with the reason said out loud,
// never skipped: a component silently absent from a mutation report reads as
// one whose suite killed everything.
func backendFor(lang runner.Lang, root, dir string, build, suite runner.Invocation, files []string, prep func(context.Context, string) error) (mutation.Backend, error) {
	switch lang {
	case runner.Go:
		// Go needs no worker directory at all: an overlay names the mutated
		// file wherever it is written, so every other path resolves in the
		// component's own tree and nothing is copied.
		return mutation.Go{Dir: filepath.Join(root, filepath.FromSlash(dir)), Build: build, Suite: suite}, nil
	case runner.Rust, runner.TypeScript, runner.Python:
		// A scan root whose files git lists none of has nothing to copy, and
		// a worker holding an empty tree would report every mutant unviable
		// with a compiler error nobody could act on.
		//
		// It asks about the scan root rather than about this component's own
		// subtree, and the weaker question is the one worth asking: a mutant
		// exists only for a file in the change, which is therefore tracked,
		// therefore listed and therefore copied — so a worker whose component
		// directory holds nothing is not reachable from a run that has a
		// mutant to stage.
		if len(files) == 0 {
			return nil, errors.New("git lists no file under the scan root, so there is nothing to copy into a worker directory")
		}
		// The scan root, with the component's commands run at its own
		// directory inside the copy: a component's build routinely reads a
		// file above itself, and a worker holding the component alone makes
		// every one of its mutants unviable.
		return mutation.Tree{Root: root, Component: path.Clean(dir), Files: files, Build: build, Suite: suite, Prepare: prep}, nil
	default:
		return nil, fmt.Errorf("lydite has no mutation backend for %s yet", lang)
	}
}

// workersFor is how many of a component's mutants are staged at once.
//
// One, for a component declaring compose services. ADR 0016 rejects sharing a
// running service between concurrent suites — two suites against one database
// truncate each other's tables — and eight mutants against one component's
// stack is that exactly: it would surface as mutants surviving at random, so
// the score would vary run to run, which is worse than a slow one.
//
// The question is asked of scheduler.Conflicts with two of this component's
// mutants as items rather than of the port list directly, so the predicate
// that decides what may run beside what has one implementation. They carry the
// component's published ports, so they conflict exactly when it publishes one;
// they carry no directory, because a mutant is not a second tree.
func workersFor(ports []int, limit int) int {
	pair := []scheduler.Item{{Name: "mutant-a", Ports: ports}, {Name: "mutant-b", Ports: ports}}
	if len(scheduler.Conflicts(pair)) > 0 {
		return 1
	}
	return limit
}

// budget is how long one mutant's suite may run before it counts as killed.
//
// A multiple of what this run measured, never a number nobody measured: ADR
// 0027 refuses a runtime budget because every way of exceeding an invented one
// is bad, and a timeout that multiplies the component's own observed baseline
// is not that. Without one, TimedOut is an outcome nothing can produce.
//
// The floor is for a suite too fast to measure. A component whose baseline is
// forty milliseconds would otherwise give every mutant a budget shorter than
// the compiler takes to start, and every one of them would be reported as a
// hang.
func budget(baseline, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	// The floor as a clamp: a conditional whose boundary returns what the
	// other arm returns is a branch nothing can be asked about.
	return max(baseline*budgetFactor, minimumBudget)
}

// costProjection is what a run says it is about to cost, before it pays it.
//
// The basis is in the line and not only the number, because every term in it is
// a decision lydite made for this component — how many mutants the change
// yielded, what its own baseline bought each of them, and how many run at once
// — and a reader who can see the derivation can argue with it.
//
// It is a worst case: every mutant running to the whole of its budget, with no
// worker ever idle. A killed mutant costs a fraction of that, so a real run
// lands well under. Stating the ceiling is not capping it — ADR 0027 refuses a
// runtime budget, and nothing here stops a run.
func costProjection(mutants, workers int, timeout time.Duration) string {
	return fmt.Sprintf(costProjectionFormat,
		mutants, timeout.Round(time.Second), staged(mutants, workers),
		projectedCeiling(mutants, workers, timeout).Round(time.Second))
}

// costProjectionFormat is the projection's one spelling, written through
// Sprintf here and read back through Sscanf by the fold, which finds the line
// in a log a shard uploaded without a document. A reader holding its own copy
// of the wording agrees with the writer until either is edited, and the
// disagreement is silent: the fold simply stops finding the line.
const costProjectionFormat = "%d mutant(s), budget %s each, %d worker(s): at most %s"

// projectedCeiling is the longest a run of this many mutants can take: as many
// rounds as it takes to stage them all, each round costing a whole budget.
func projectedCeiling(mutants, workers int, timeout time.Duration) time.Duration {
	w := staged(mutants, workers)
	rounds := (mutants + w - 1) / w
	return time.Duration(rounds) * timeout
}

// staged is how many mutants actually run at once, which is the executor's own
// clamp: never fewer than one, and never more than there are mutants to stage.
// A projection over an unclamped count states a parallelism the run does not
// have, which is the direction that understates the cost.
func staged(mutants, workers int) int { return min(max(workers, 1), max(mutants, 1)) }

// memoryBudget is how much memory one mutant's suite may hold before it counts
// as killed.
//
// A multiple of the component's own measured baseline, never a share of the
// machine's memory: a bound that moved with the machine would make a mutant
// killed on a small runner and surviving on a large one, so the verdict would
// stop meaning one thing. The machine is what sets the floor's value instead.
//
// The floor is for a suite whose own peak is small. Four times a forty-megabyte
// baseline is a ceiling a compiler reaches on its own, and every mutant would
// be reported as one that allocated without stopping.
func memoryBudget(baseline, override int64) int64 {
	if override > 0 {
		return override
	}
	// The floor as a clamp, for the reason budget's is: a conditional whose
	// boundary returns what the other arm returns is a branch nothing can be
	// asked about.
	return max(baseline*memoryFactor, minimumMemory)
}

// memoryFactor is how much more than the baseline's own peak a mutant may hold.
//
// Four rather than the timeout's three because memory is the less elastic of
// the two: a suite is routinely slower under a mutation and is rarely four
// times larger. The error that matters is one-sided — a bound too tight kills a
// mutant nothing about the tests killed, and an inflated score is permanent and
// silent where a false survivor is an author's afternoon.
const memoryFactor = 4

// minimumMemory is the floor under that multiple. Against defaultConcurrency's
// four slots it is 8GiB of a 16GB runner, with the agent, the toolchains and
// the page cache in the rest; --concurrency is what bounds the sum.
const minimumMemory = 2 << 30

// memoryHeadroom is how much of the derived bound the baseline itself may hold
// and the run still mean something.
//
// A mutant runs the plain variant of the suite the baseline ran instrumented,
// so a ceiling the baseline alone already fills is one every mutant reaches
// whatever its tests do — the run would report a component whose suite kills
// everything, from a bound nothing about the code justifies. Only an override
// can produce it: the derivation is four times that same peak.
const memoryHeadroom = 2

// memoryFits reports whether a component's baseline leaves room under the bound
// its mutants run at.
func memoryFits(peak, bound int64) bool { return peak*memoryHeadroom <= bound }

// budgetFactor is how much longer than the baseline a mutant may take.
//
// The baseline is the *instrumented* variant, which is the slower of the two —
// Go's -coverpkg=./... recompiles every package per test binary — so three
// times it is generous against the plain variant a mutant actually runs. What
// it has to separate is a suite that is merely slower under a mutation from
// one that is not going to finish.
const budgetFactor = 3

// minimumBudget is the floor under that multiple.
const minimumBudget = 60 * time.Second
