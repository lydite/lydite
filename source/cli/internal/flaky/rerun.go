package flaky

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/runner"
)

// Verdict is what the two runs established about one new test.
type Verdict string

const (
	// Agreed is two runs that reported the same outcome.
	Agreed Verdict = "agreed"
	// Disagreed is two runs that did not, which is the whole of what this
	// gate claims: not that a test is random, but that it answered twice and
	// differently.
	Disagreed Verdict = "disagreed"
	// Skipped is two runs that both skipped it. The outcomes agree and the
	// test's determinism was not examined, which are both true and neither of
	// which is a pass — so it is its own verdict rather than folded into
	// Agreed, where a row would count it among the tests it checked.
	Skipped Verdict = "skipped"
	// Unmeasured is a test one of the two reports does not record. It is
	// decided by absence from the report rather than by a parser guessing at
	// build tags, TestMain, or a package the component's argv excludes — the
	// report is what actually ran, and it answers all of those at once.
	Unmeasured Verdict = "unmeasured"
)

// Result is what the gate established about one new test.
//
// Run1 and Run2 are nil for a run whose report does not record the test, which
// is why they are pointers: an Outcome that could mean either "passed" or
// "nothing ran it" is exactly what would let an unexamined test render as a
// passing one. Command is the rerun's argv, which a finding carries so the
// author can reproduce a disagreement with one command rather than being told
// their test is haunted.
type Result struct {
	Test    Test
	Verdict Verdict
	Run1    *junit.Outcome
	Run2    *junit.Outcome
	// Why names the cause of an Unmeasured verdict, and is empty for every
	// other one. A gate that could not run never renders as one that passed,
	// so the reason travels with the count.
	Why     string
	Command string
}

// Identity is the key space one language's test outcomes are compared in.
//
// It is one value and not a reader per run, so run 1's report and the rerun's
// are always read the same way. Two runs compared across two key spaces agree
// about nothing: every test is absent from one of the maps.
type Identity int

const (
	// ByName is a language where a name alone identifies a test within the
	// scope one process is filtered over. Go is the only one: one `go test`
	// runs one package, and the compiler forbids two functions there with one
	// name.
	ByName Identity = iota
	// ByClassAndName is a language where it does not. cargo nextest runs every
	// binary in a crate in one invocation and vitest every file in a
	// component, and `shared_name` under two nextest binaries is two tests —
	// merged under one key, a flake in one is masked by a pass in the other.
	ByClassAndName
)

// ReadOutcomes reads one report's per-test outcomes in this key space.
func (i Identity) ReadOutcomes(path string) (map[string]junit.Outcome, error) {
	if i == ByClassAndName {
		return junit.ReadOutcomesByClassFile(path)
	}
	return junit.ReadOutcomesFile(path)
}

// key is how a test is looked up in a map this identity produced.
func (i Identity) key(t Test) string {
	if i == ByClassAndName {
		return junit.ClassKey(t.Classname, t.Name)
	}
	return t.Name
}

// Options is what the rerun needs to know about the component whose new tests
// it is examining.
type Options struct {
	// Root is the scan root, which a Test's Scope is relative to.
	Root string
	// Dir is the component's directory relative to Root, which is where the
	// rerun runs — the same directory run 1 ran in, so a relative package
	// pattern and a relative report path mean the same thing to both.
	Dir string
	// Args are the component's declared runner arguments. The rerun copies
	// them minus the coverage flags, so the two runs differ in as little as
	// possible.
	Args []string
	// Env is the environment the rerun's child gets, PATH included. It is the
	// caller's to compose, because the directories a pinned tool needs and
	// the toolchain a component resolved are both things only the caller
	// holds.
	Env []string
	// Run1 is what run 1's report recorded, in Identity's key space. It is read
	// from the report the suite already wrote rather than by running anything
	// again: a run 1 performed here would be a third process, and the outcome
	// being compared against has to be the one the suite row reported.
	Run1 map[string]junit.Outcome
	// Identity is how a test in this language is addressed in both reports.
	Identity Identity
	// Build turns one scope's new tests into the invocation that reruns them.
	// The caller closes over whatever the language needs — Go's relative
	// package pattern, Rust's plain OR-ed filter, TypeScript's file list — so
	// this package stays ignorant of any one runner's argv.
	Build func(scope string, tests []Test) (runner.Invocation, bool)
	// Log is where the rerun's own output goes. A nil one discards it.
	Log io.Writer
}

// Rerun runs each scope's new tests a second time and compares the two
// outcomes, one result per test the gate examines.
//
// One invocation per scope holding a new test — not one per test, which would
// pay for a process per test, and not one for the component where the language
// groups more finely than that, which would rerun the whole suite. A scope with
// no test the rerun can address is never reached, since a filter matching
// nothing reports a pass for a run that ran nothing.
//
// The results are not one per test given. Under ByClassAndName a new name that
// run 1 recorded under two classnames is two tests, examined independently,
// and a declaration whose name no parser could read is carried through as a
// test nothing was established about.
//
// A rerun that exits non-zero is not a failure here. A runner writes a valid
// report for a suite whose tests failed, and a failing new test is the outcome
// this gate most wants to read. What cannot be measured is a report that was
// not written or will not parse, and that is said per test rather than
// returned. The error is for the run being cut short, where nothing was
// established about anything.
func Rerun(ctx context.Context, tests []Test, opts Options) ([]Result, error) {
	if len(tests) == 0 {
		return nil, nil
	}
	dir := filepath.Join(opts.Root, filepath.FromSlash(opts.Dir))
	units := expand(tests, opts)
	out := make([]Result, len(units))
	for i, u := range units {
		if r, decided := undecidable(u, opts); decided {
			out[i] = r
		}
	}
	for _, scope := range scopes(units) {
		runnable := runnableIn(units, scope, opts)
		if len(runnable) == 0 {
			continue
		}
		inv, ok := opts.Build(scope, runnable)
		if !ok {
			return nil, fmt.Errorf("no rerun invocation for %d tests in %s", len(runnable), scope)
		}
		command := strings.Join(append([]string{inv.Name}, inv.Args...), " ")
		outcomes, why := rerunScope(ctx, dir, inv, opts)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		for i, u := range units {
			if u.Scope != scope {
				continue
			}
			if _, decided := undecidable(u, opts); decided {
				continue
			}
			out[i] = compare(u, opts, outcomes, why, command)
		}
	}
	return out, nil
}

// expand is the tests the gate will examine, one per test the rerun can
// address plus one per test it cannot.
//
// Under ByName a test is already its own identity and the list is unchanged.
// Under ByClassAndName a bare name is not: the classnames it ran under are
// read back out of run 1's own report, which is what actually ran, rather than
// computed from the source layout. A name recorded under two classnames is two
// tests — ADR 0041's `shared_name` in `nextestprobe::a` and `nextestprobe::b` —
// and each is examined on its own, so a flake in one binary is not masked by a
// pass in another.
//
// A name run 1 recorded under no classname at all is kept once, with none, and
// is what undecidable answers for.
func expand(tests []Test, opts Options) []Test {
	if opts.Identity != ByClassAndName {
		return tests
	}
	var out []Test
	seen := map[string]bool{}
	for _, t := range tests {
		if t.Unreadable {
			out = append(out, t)
			continue
		}
		classnames := classnamesOf(opts.Run1, t.Name)
		if len(classnames) == 0 {
			out = append(out, t)
			continue
		}
		for _, cn := range classnames {
			u := anchor(tests, t, cn)
			u.Classname = cn
			if seen[key(u)] {
				// One name declared in two files within one scope is one test
				// per classname the report holds, not one per declaration: two
				// units with one identity would be examined twice and make one
				// disagreement two identical findings.
				continue
			}
			seen[key(u)] = true
			out = append(out, u)
		}
	}
	return out
}

// anchor is the declaration a classname's test is reported at.
//
// A vitest classname is the file's own path relative to the component, so the
// declaration in that file is the one an author reads the finding on. Nothing
// ties a nextest binary to a file that way, so a name declared in several
// files there anchors to the first of them — over-reporting the location in
// the direction ADR 0039 already chose, rather than guessing at cargo's target
// naming.
func anchor(tests []Test, t Test, classname string) Test {
	for _, c := range tests {
		if c.Scope == t.Scope && c.Name == t.Name && scopeRelative(c.Scope, c.Path) == classname {
			return c
		}
	}
	return t
}

// scopeRelative names a path the way the directory the rerun runs in does,
// which is the scope's own.
func scopeRelative(scope, p string) string {
	if scope == "" || scope == "." {
		return p
	}
	return strings.TrimPrefix(p, scope+"/")
}

// classnamesOf is every classname run 1 recorded name under, sorted.
//
// Found by the key's own encoding rather than by a second one: junit.ClassKey
// puts a NUL between the halves and no producer can write one into either, so
// a key ending in ClassKey("", name) is that name under some classname and
// nothing else can be.
func classnamesOf(run1 map[string]junit.Outcome, name string) []string {
	if name == "" {
		return nil
	}
	suffix := junit.ClassKey("", name)
	var out []string
	for k := range run1 {
		if strings.HasSuffix(k, suffix) {
			out = append(out, strings.TrimSuffix(k, suffix))
		}
	}
	slices.Sort(out)
	return out
}

// undecidable is the result of a test the rerun cannot address at all, and
// whether it is one.
//
// Neither case reaches an invocation. A test with no name has nothing to
// filter for, and a name run 1 recorded under no classname has no key the
// rerun's report could be read back under — running either would cost a
// process to establish the same nothing.
func undecidable(t Test, opts Options) (Result, bool) {
	switch {
	case t.Unreadable:
		return Result{Test: t, Verdict: Unmeasured,
			Why: "a test is declared here whose name a parser could not read"}, true
	case opts.Identity == ByClassAndName && t.Classname == "":
		return Result{Test: t, Verdict: Unmeasured, Why: "run 1's report does not record it"}, true
	}
	return Result{}, false
}

// runnableIn is the tests of one scope the rerun can address, in order.
func runnableIn(units []Test, scope string, opts Options) []Test {
	var out []Test
	for _, u := range units {
		if u.Scope != scope {
			continue
		}
		if _, decided := undecidable(u, opts); decided {
			continue
		}
		out = append(out, u)
	}
	return out
}

// rerunScope executes one scope's rerun and reads the report it wrote,
// answering the reason there is none instead.
//
// The report path is cleared before the run rather than trusted to be
// overwritten. It is one path for every scope a component reruns, so a scope
// whose rerun wrote nothing would otherwise be measured from the last one that
// did — which is the gate reading one scope's outcomes as another's.
func rerunScope(ctx context.Context, dir string, inv runner.Invocation, opts Options) (map[string]junit.Outcome, string) {
	report := filepath.Join(dir, filepath.FromSlash(inv.JUnitReport))
	if err := os.MkdirAll(filepath.Dir(report), 0o750); err != nil {
		return nil, "the rerun's report directory could not be made: " + err.Error()
	}
	if err := os.Remove(report); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "a report left by an earlier rerun could not be removed: " + err.Error()
	}
	out := opts.Log
	if out == nil {
		out = io.Discard
	}
	executil.RunOutput(ctx, dir, opts.Env, out, inv.Name, inv.Args...)
	outcomes, err := opts.Identity.ReadOutcomes(report)
	if err != nil {
		return nil, "the rerun wrote no report lydite could read: " + err.Error()
	}
	return outcomes, ""
}

// compare is one test's two outcomes and what they establish.
func compare(t Test, opts Options, run2 map[string]junit.Outcome, why, command string) Result {
	r := Result{Test: t, Command: command}
	k := opts.Identity.key(t)
	one, ok1 := opts.Run1[k]
	two, ok2 := run2[k]
	if ok1 {
		r.Run1 = &one
	}
	if ok2 {
		r.Run2 = &two
	}
	switch {
	case !ok2 && why != "":
		r.Verdict, r.Why = Unmeasured, why
	case !ok1 && !ok2:
		r.Verdict, r.Why = Unmeasured, "neither run's report records it"
	case !ok1:
		r.Verdict, r.Why = Unmeasured, "run 1's report does not record it"
	case !ok2:
		r.Verdict, r.Why = Unmeasured, "the rerun's report does not record it"
	case one == junit.Skip && two == junit.Skip:
		r.Verdict = Skipped
	case one == two:
		r.Verdict = Agreed
	default:
		r.Verdict = Disagreed
	}
	return r
}

// scopes is the rerun units the tests sit in, first-seen order, each once. The
// tests arrive sorted by scope, so this is one invocation per scope and never
// two over the same one.
func scopes(tests []Test) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tests {
		if seen[t.Scope] {
			continue
		}
		seen[t.Scope] = true
		out = append(out, t.Scope)
	}
	return out
}

// key identifies a test by its scope, the classname it is reported under where
// its language has one, and its name.
func key(t Test) string { return t.Scope + "\x00" + t.Classname + "\x00" + t.Name }
