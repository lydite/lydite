package mutation

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/runner"
)

// Backend is one language's answer to the two questions the executor cannot
// ask for itself: how a mutant's source reaches the compiler without the
// component's own tree being edited, and what the mutant's own compilation
// unit is.
//
// Mutating the component's tree in place is faster than either answer and is
// rejected: an interrupt leaves mutated source in the tree lydite is
// measuring, which is a repository somebody then commits.
type Backend interface {
	// Worker opens an isolated place to stage mutants in, numbered so a
	// language needing a directory can name one per concurrency slot.
	//
	// It takes the run's context because opening one is real work: a
	// JavaScript worker installs the workspace's dependencies, and an
	// interrupt that could not reach that would leave the run waiting on an
	// `npm ci` for a component it has already stopped mutating.
	//
	// One per slot and never one per mutant. A tree copy — and for
	// TypeScript a dependency install — is not affordable per mutant, so a
	// worker is reused with the mutated file restored between them. Go
	// needs no directory at all, since an overlay names the mutated file
	// wherever it is written, and the same interface covers both.
	Worker(ctx context.Context, n int) (Worker, error)
}

// Worker presents one mutant at a time.
type Worker interface {
	// Stage presents a mutant to the toolchain and returns the commands
	// that compile and run against it. It stays presented until Release.
	Stage(m Mutant) (Staged, error)
	// Release withdraws the staged mutant, leaving the worker ready for the
	// next one.
	Release() error
	// Close releases everything the worker holds.
	Close() error
}

// Staged is one presented mutant: where its commands run, and what they are.
type Staged struct {
	// Dir is where each command runs — the component's own directory for a
	// language whose toolchain can be told to read one file from elsewhere,
	// and a worker directory for one that cannot.
	Dir string
	// Build compiles without running a test. It is the only thing that
	// separates an unviable mutant from a killed one, since both exit
	// non-zero, and it is why a runner derives a build-only variant at all.
	Build runner.Invocation
	// Phases are the suite runs, cheapest first, and a kill in any of them
	// is final.
	//
	// Two, for a language with a compilation unit smaller than the
	// component: a mutant its own package's tests kill is killed, and
	// nothing a wider run could say would change that. A mutant that
	// survives them has to be held against every test that could kill it,
	// because a mutant in a library is routinely killed only by its
	// caller's tests. Ordering the two costs a survivor nothing — the
	// second run reads the first's cached result for the package they share
	// — and turns the expensive kill from a whole module into one package.
	//
	// One phase, for a language with no unit cheaper than the component.
	// The executor reads the length rather than a flag, so a backend that
	// narrows nothing needs no branch here.
	Phases []runner.Invocation
}

// Options is everything a run of the executor needs that is not the mutants.
type Options struct {
	// Env is the environment every command runs with, already composed by
	// the caller — the component's toolchain, its declared variables, and
	// the pinned directories its runner needs on PATH.
	Env []string
	// Timeout bounds one suite execution.
	//
	// A mutant that hangs is killed, because an infinite loop is a
	// behaviour change something noticed, and this is how that is observed:
	// without a timeout TimedOut is an outcome nothing can produce. It
	// bounds each phase separately rather than the mutant as a whole, since
	// what it is watching for is one suite that stopped making progress.
	Timeout time.Duration
	// Workers is how many mutants are staged at once. One, for a component
	// whose suites would share a running service: eight mutants against one
	// database truncate each other's tables, which surfaces as mutants
	// surviving at random and a score that varies run to run.
	Workers int
	// Slots bounds suite executions in flight across the whole run, and is
	// shared by every component in it. Nil is unbounded.
	Slots *Slots
	// Log is where each mutant's line goes, and the compiler output of one
	// that did not build. Nil discards.
	Log io.Writer
}

// Slots bounds how many suite executions are in flight.
//
// One for the whole run and never one per component: --concurrency means
// suite executions in flight, exactly as it does in `lydite test`, and two
// bounds would multiply into components times mutants — the quadratic
// oversubscription defaultConcurrency is a constant rather than NumCPU to
// avoid.
type Slots struct{ ch chan struct{} }

// NewSlots bounds a run at n suite executions. A bound at or above unbounded
// is no bound at all, which is what `--concurrency max` asks for: a channel
// cannot be that large, and a caller that wants no bound is better served by
// nothing to acquire than by a buffer nobody could allocate.
func NewSlots(n int) *Slots {
	if n <= 0 || n >= unbounded {
		return nil
	}
	return &Slots{ch: make(chan struct{}, n)}
}

// unbounded is the bound above which there is no bound. Well past any machine
// that could run that many suites, and small enough to allocate.
const unbounded = 1 << 20

// acquire takes a slot, or reports that the run was cancelled while waiting.
func (s *Slots) acquire(ctx context.Context) bool {
	if s == nil {
		return ctx.Err() == nil
	}
	select {
	case s.ch <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Slots) release() {
	if s != nil {
		<-s.ch
	}
}

// Execute runs every mutant and reports what became of each, in the order
// they were given.
//
// Input order and never completion order, for the reason `lydite test` orders
// its rows by the declaration: two runs over one tree must produce the same
// document, and ordering by whichever finished first puts this run's timing
// into it.
//
// An acknowledged mutant is never staged, never built and never run. The
// declaration is the answer, and running it would only reproduce the survival
// its author already claimed.
//
// A cancelled run reports every mutant it did not reach as unviable with the
// reason said out loud, rather than dropping it: a truncated run that omitted
// mutants reads as a complete run over fewer of them, and the denominator
// would shrink to whatever the run got through.
func Execute(ctx context.Context, b Backend, mutants []Mutant, opts Options) ([]Result, error) {
	results := make([]Result, len(mutants))
	var pending []int
	for i, m := range mutants {
		if m.Acknowledged() {
			results[i] = Result{Mutant: m, Outcome: Acknowledged, Detail: m.Reason}
			continue
		}
		results[i] = Result{Mutant: m, Outcome: Unviable, Detail: "the run ended before this mutant was built"}
		pending = append(pending, i)
	}
	if len(pending) == 0 {
		return results, nil
	}

	workers := opts.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > len(pending) {
		workers = len(pending)
	}

	queue := make(chan int)
	var wg sync.WaitGroup
	// Every worker is opened before any of them runs, so a backend that
	// cannot open one — no space for a worker directory, a tree it cannot
	// copy — fails the component rather than reporting the mutants nobody
	// could stage as evidence about its tests.
	opened := make([]Worker, 0, workers)
	defer func() {
		for _, w := range opened {
			if err := w.Close(); err != nil {
				_, _ = fmt.Fprintf(logOf(opts), "closing the worker: %v\n", err)
			}
		}
	}()
	for n := range workers {
		w, err := b.Worker(ctx, n)
		if err != nil {
			return nil, err
		}
		opened = append(opened, w)
	}

	for _, w := range opened {
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			for i := range queue {
				results[i] = run(ctx, w, mutants[i], opts)
			}
		}(w)
	}
	for _, i := range pending {
		// A cancelled run stops handing out mutants and waits for what is
		// already staged, which is what leaves each worker able to release
		// what it put in place.
		if ctx.Err() != nil {
			break
		}
		queue <- i
	}
	close(queue)
	wg.Wait()
	return results, nil
}

// run builds one mutant and, if it built, runs it against each phase in turn.
func run(ctx context.Context, w Worker, m Mutant, opts Options) Result {
	started := time.Now()
	staged, err := w.Stage(m)
	if err != nil {
		// Evidence about the generator rather than about the tests, which is
		// what Unviable already means: a mutant whose source moved under it
		// was never built, so counting it as a kill or as a survivor would
		// put the engine's own defect into the score.
		return log(opts, m, Result{Mutant: m, Outcome: Unviable, Detail: err.Error()}, started)
	}
	defer func() {
		if err := w.Release(); err != nil {
			_, _ = fmt.Fprintf(logOf(opts), "%s: releasing the mutant: %v\n", m, err)
		}
	}()

	switch res, v := execute(ctx, staged.Dir, staged.Build, opts); v {
	case cutShort:
		// An interrupted build is not a mutant that would not compile, and
		// reporting it as one blames the generator for a CI job timeout.
		return log(opts, m, Result{Mutant: m, Outcome: Unviable,
			Detail: "the run was interrupted before this mutant was built"}, started)
	case exceeded:
		// A compilation that outran the budget says nothing about the tests
		// either: nothing was run, so nothing observed the change.
		return log(opts, m, Result{Mutant: m, Outcome: Unviable,
			Detail: fmt.Sprintf("the mutant did not compile within %s", opts.Timeout)}, started)
	case failed:
		return log(opts, m, Result{Mutant: m, Outcome: Unviable, Detail: lastLines(res.Output)}, started)
	}

	for _, phase := range staged.Phases {
		switch _, v := execute(ctx, staged.Dir, phase, opts); v {
		case passed:
			continue
		case exceeded:
			// Killed by the clock, and counted as killed: an infinite loop
			// is a behaviour change something noticed. It keeps its own name
			// because a suite full of timeouts is worth seeing even when
			// every one of them scores correctly.
			return log(opts, m, Result{Mutant: m, Outcome: TimedOut,
				Detail: fmt.Sprintf("no result within %s", opts.Timeout)}, started)
		case cutShort:
			return log(opts, m, Result{Mutant: m, Outcome: Unviable,
				Detail: "the run was interrupted before this mutant finished"}, started)
		default:
			return log(opts, m, Result{Mutant: m, Outcome: Killed}, started)
		}
	}
	return log(opts, m, Result{Mutant: m, Outcome: Survived}, started)
}

// verdict is what running one command established, and it is deliberately not
// read off the command's error.
//
// A process killed for exceeding its budget and one killed because the run was
// interrupted both exit through a signal and report the same thing, and one of
// the two is a killed mutant while the other is a run that established
// nothing. What separates them is which context expired, which the caller of
// the command is the only thing that knows.
type verdict int

const (
	// passed is an exit status of zero.
	passed verdict = iota
	// failed is any non-zero exit the run itself caused.
	failed
	// exceeded is the per-suite budget expiring.
	exceeded
	// cutShort is the whole run being cancelled.
	cutShort
)

// execute runs one invocation under the per-suite timeout.
//
// The timeout gets a context of its own so that a suite that hangs is the only
// thing it cancels: derived from the run's, so an interrupt still reaches the
// child, and separate from it, so a mutant that exceeded its budget stays
// distinguishable from a run that was cut short.
func execute(ctx context.Context, dir string, inv runner.Invocation, opts Options) (executil.Result, verdict) {
	// Held around the command and never around the staging, so a worker
	// waiting for a slot is not also holding one: the bound is on suite
	// executions in flight, which is what a machine actually runs out of.
	if !opts.Slots.acquire(ctx) {
		return executil.Result{Err: ctx.Err()}, cutShort
	}
	defer opts.Slots.release()

	within := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		within, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	// RunOutput and not RunQuiet, because the words that matter here are on
	// stderr: a compiler writes its errors there, and an unviable mutant's
	// detail is the only place they reach a reader. It merges the two
	// streams for that reason, and discards them as they arrive — a mutant's
	// output is worth keeping only when it turns out not to have compiled,
	// and forty copies of a passing suite is what buries the one that did
	// not.
	res := executil.RunOutput(within, dir, opts.Env, io.Discard, inv.Name, inv.Args...)
	switch {
	case ctx.Err() != nil:
		// The run's own context, asked first: a cancellation that lands
		// inside the budget expires both, and the wider one is the truth
		// about what happened.
		return res, cutShort
	case within.Err() != nil:
		return res, exceeded
	case res.Ok():
		return res, passed
	default:
		return res, failed
	}
}

// log records one mutant's outcome, and the compiler's own words for one that
// would not build.
//
// A line per mutant rather than a suite's output per mutant: a component's log
// is read by somebody looking for the mutant that survived, and forty copies
// of a passing suite is what buries it. The exception is a mutant that did not
// compile, which is the one outcome whose cause is nowhere else — a survivor
// is named in its row, and a kill needs no explanation.
func log(opts Options, m Mutant, r Result, started time.Time) Result {
	out := logOf(opts)
	_, _ = fmt.Fprintf(out, "%s: %s in %s\n", m, r.Outcome, time.Since(started).Round(time.Millisecond))
	if r.Outcome == Unviable && r.Detail != "" {
		_, _ = fmt.Fprintln(out, r.Detail)
	}
	return r
}

func logOf(opts Options) io.Writer {
	if opts.Log == nil {
		return io.Discard
	}
	return opts.Log
}

// detailLines is how much of a failed compilation goes under a row.
//
// A mutant that does not compile fails on one line and the compiler says so in
// a handful; a mutant that broke a whole package's build says so in hundreds,
// and a report carrying all of them for each of forty mutants is one nobody
// reads. The whole of it is in the component's log.
const detailLines = 20

// lastLines keeps the end of a command's output, which is where a compiler
// puts the error rather than the invocation that led to it.
func lastLines(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > detailLines {
		lines = lines[len(lines)-detailLines:]
	}
	return strings.Join(lines, "\n")
}
