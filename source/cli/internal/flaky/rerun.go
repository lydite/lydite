package flaky

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
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

// Options is what the rerun needs to know about the component whose new tests
// it is examining.
type Options struct {
	// Root is the scan root, which a Test's Package is relative to.
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
	// Run1 is what run 1's report recorded, by test name. It is read from the
	// report the suite already wrote rather than by running anything again:
	// a run 1 performed here would be a third process, and the outcome being
	// compared against has to be the one the suite row reported.
	Run1 map[string]junit.Outcome
	// Log is where the rerun's own output goes. A nil one discards it.
	Log io.Writer
}

// Rerun runs each package's new tests a second time and compares the two
// outcomes, one result per test in the order they were given.
//
// One invocation per package holding a new test — not one per test, which
// would pay for a process per test, and not one for the component, which would
// rerun the whole suite. A package with no new test is never reached, since a
// `go test -run` matching nothing prints `ok` and reports a pass for a filter
// that ran nothing.
//
// A rerun that exits non-zero is not a failure here. `go test` writes a valid
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
	byTest := map[string]Result{}
	for _, pkg := range packages(tests) {
		names := namesIn(tests, pkg)
		pattern, err := relPackage(opts.Dir, pkg)
		if err != nil {
			return nil, err
		}
		inv, ok := runner.GoRerun(opts.Args, pattern, names)
		if !ok {
			return nil, fmt.Errorf("no rerun invocation for %d tests in %s", len(names), pkg)
		}
		command := strings.Join(append([]string{inv.Name}, inv.Args...), " ")
		outcomes, why := rerunPackage(ctx, dir, inv, opts)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		for _, t := range tests {
			if t.Package != pkg {
				continue
			}
			byTest[key(t)] = compare(t, opts.Run1, outcomes, why, command)
		}
	}
	out := make([]Result, 0, len(tests))
	for _, t := range tests {
		out = append(out, byTest[key(t)])
	}
	return out, nil
}

// rerunPackage executes one package's rerun and reads the report it wrote,
// answering the reason there is none instead.
//
// The report path is cleared before the run rather than trusted to be
// overwritten. It is one path for every package a component reruns, so a
// package whose rerun wrote nothing would otherwise be measured from the last
// one that did — which is the gate reading one package's outcomes as another's.
func rerunPackage(ctx context.Context, dir string, inv runner.Invocation, opts Options) (map[string]junit.Outcome, string) {
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
	outcomes, err := junit.ReadOutcomesFile(report)
	if err != nil {
		return nil, "the rerun wrote no report lydite could read: " + err.Error()
	}
	return outcomes, ""
}

// compare is one test's two outcomes and what they establish.
func compare(t Test, run1 map[string]junit.Outcome, run2 map[string]junit.Outcome, why, command string) Result {
	r := Result{Test: t, Command: command}
	one, ok1 := run1[t.Name]
	two, ok2 := run2[t.Name]
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

// packages is the package directories the tests sit in, first-seen order, each
// once. The tests arrive sorted by package, so this is one invocation per
// package and never two over the same one.
func packages(tests []Test) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tests {
		if seen[t.Package] {
			continue
		}
		seen[t.Package] = true
		out = append(out, t.Package)
	}
	return out
}

// namesIn is the test names one package contributes, which become the rerun's
// -run pattern.
func namesIn(tests []Test, pkg string) []string {
	var out []string
	for _, t := range tests {
		if t.Package == pkg {
			out = append(out, t.Name)
		}
	}
	return out
}

// key identifies a result by the identity ADR 0039 gives a test: its package
// directory and its name.
func key(t Test) string { return t.Package + "\x00" + t.Name }

// relPackage turns a package directory relative to the scan root into the
// pattern `go test` takes in the component's own directory.
//
// A pattern and not an import path, because deriving one would need `go list`
// over a module the gate has not otherwise had to load, and a relative pattern
// names the same package for a component whose module path lydite never reads.
// It is spelled with a leading "./" for the reason cmd/go requires one: a bare
// `pkg` is a path in the module cache, and only `./pkg` is a directory here.
func relPackage(dir, pkg string) (string, error) {
	rel, err := filepath.Rel(filepath.FromSlash(dir), filepath.FromSlash(pkg))
	if err != nil {
		return "", fmt.Errorf("locating %s inside the component at %s: %w", pkg, dir, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "../") {
		return rel, nil
	}
	return "./" + path.Clean(rel), nil
}
