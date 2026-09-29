package mutation

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func stateMutant(offset int) Mutant {
	return Mutant{
		Path: "pkg/a.go", Line: 3, Column: 7, Operator: NegateConditional,
		Offset: offset, Length: 2, Original: "==", Mutated: "!=",
	}
}

func openState(t *testing.T, dir, fingerprint string) *State {
	t.Helper()
	s, err := OpenState(dir, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// Every field a Result carries survives the store, keyed by the mutant's own
// id, and the baseline comes back as it was saved.
func TestAStateRoundTripsItsVerdictsAndBaseline(t *testing.T) {
	dir := t.TempDir()
	s := openState(t, dir, "fp")
	want := []Result{
		{Mutant: stateMutant(10), Outcome: Killed},
		{Mutant: stateMutant(20), Outcome: Unviable, Detail: "does not compile\nat line 3"},
		{Mutant: stateMutant(30), Outcome: OutOfMemory, MemoryUnbounded: true, OutputHeldOpen: true},
		{Mutant: func() Mutant { m := stateMutant(40); m.Reason = "equivalent"; return m }(), Outcome: Acknowledged, Detail: "equivalent"},
	}
	for _, r := range want {
		if err := s.Record(r); err != nil {
			t.Fatal(err)
		}
	}
	base := Baseline{
		Passed: true, Elapsed: 1500 * time.Millisecond, MaxRSS: 123 << 20, MemoryUnbounded: true,
		Executed: map[string]map[int]int{"pkg/a.go": {3: 2, 4: 1}},
	}
	if err := s.SaveBaseline(base); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again := openState(t, dir, "fp")
	got, err := again.Verdicts()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("read back %d verdicts, want %d", len(got), len(want))
	}
	for _, r := range want {
		if g, ok := got[MutantID(r.Mutant)]; !ok || g != r {
			t.Errorf("verdict for %s read back as %+v, want %+v", r.Mutant, g, r)
		}
	}
	b, ok, err := again.Baseline()
	if err != nil || !ok {
		t.Fatalf("baseline: ok=%v err=%v", ok, err)
	}
	if b.Passed != base.Passed || b.Elapsed != base.Elapsed || b.MaxRSS != base.MaxRSS ||
		b.MemoryUnbounded != base.MemoryUnbounded || b.Executed["pkg/a.go"][3] != 2 || b.Executed["pkg/a.go"][4] != 1 {
		t.Errorf("baseline read back as %+v, want %+v", b, base)
	}
}

// A state recorded under another fingerprint measured another tree, and none
// of it may be reused.
func TestAFingerprintMismatchDiscardsTheState(t *testing.T) {
	dir := t.TempDir()
	s := openState(t, dir, "old")
	if err := s.Record(Result{Mutant: stateMutant(1), Outcome: Survived}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveBaseline(Baseline{Passed: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	fresh := openState(t, dir, "new")
	got, err := fresh.Verdicts()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a new fingerprint reused %d verdicts", len(got))
	}
	if _, ok, err := fresh.Baseline(); ok || err != nil {
		t.Errorf("a new fingerprint reused the baseline (ok=%v, err=%v)", ok, err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	// The old fingerprint is gone too: going back to it is a third tree, not
	// the first one resumed.
	back := openState(t, dir, "old")
	if got, _ := back.Verdicts(); len(got) != 0 {
		t.Errorf("returning to the old fingerprint found %d verdicts", len(got))
	}
}

// A run killed mid-write leaves a torn last line. Reading skips it, and the
// next run's verdicts still land on lines of their own.
func TestATornLastLineIsSkipped(t *testing.T) {
	dir := t.TempDir()
	s := openState(t, dir, "fp")
	kept := Result{Mutant: stateMutant(1), Outcome: Killed}
	if err := s.Record(kept); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, stateVerdictsFile), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"path":"pkg/a.go","operator":"negate-condi`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	resumed := openState(t, dir, "fp")
	got, err := resumed.Verdicts()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[MutantID(kept.Mutant)] != kept {
		t.Fatalf("read %+v, want only the whole line", got)
	}
	after := Result{Mutant: stateMutant(2), Outcome: Survived}
	if err := resumed.Record(after); err != nil {
		t.Fatal(err)
	}
	got, err = resumed.Verdicts()
	if err != nil {
		t.Fatal(err)
	}
	if got[MutantID(after.Mutant)] != after {
		t.Errorf("a verdict appended after a torn line was lost: %+v", got)
	}
}

// Every worker records at once, and each verdict lands as a whole line.
func TestConcurrentRecordsEachWriteOneParseableLine(t *testing.T) {
	const n = 64
	dir := t.TempDir()
	s := openState(t, dir, "fp")
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m := stateMutant(i)
			m.Original = strings.Repeat("x", 4096)
			errs <- s.Record(Result{Mutant: m, Outcome: Killed, Detail: fmt.Sprint(i)})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(filepath.Join(dir, stateVerdictsFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	lines := 0
	for sc.Scan() {
		var v verdictLine
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			t.Fatalf("line %d does not parse: %v", lines+1, err)
		}
		lines++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if lines != n {
		t.Errorf("%d records wrote %d lines", n, lines)
	}
}

// A component name is not a path: a `/` stays inside one directory directly
// under the root, and no name climbs out of it or collides with another.
func TestAComponentNameIsEscapedIntoOneDirectory(t *testing.T) {
	root := t.TempDir()
	seen := map[string]string{}
	for _, name := range []string{"web/app", "web_app", "web%2Fapp", "..", ".", ".hidden", "cli.v2", ""} {
		dir := StateDir(root, name)
		if filepath.Dir(dir) != root {
			t.Errorf("%q is kept at %s, not directly under %s", name, dir, root)
		}
		if base := filepath.Base(dir); base == "." || base == ".." || strings.HasPrefix(base, ".") {
			t.Errorf("%q is kept in %q", name, base)
		}
		if other, ok := seen[dir]; ok {
			t.Errorf("%q and %q share %s", name, other, dir)
		}
		seen[dir] = name
	}
	s := openState(t, StateDir(root, "web/app"), "fp")
	if err := s.Record(Result{Mutant: stateMutant(1), Outcome: Killed}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "web")); !os.IsNotExist(err) {
		t.Errorf("a component named web/app created %s", filepath.Join(root, "web"))
	}
}

// The state is a cache, so a directory it cannot be kept in is an error for
// the caller to report and never a panic.
func TestAStateThatCannotBeWrittenReturnsAnError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenState(filepath.Join(file, "state"), "fp"); err == nil {
		_ = s.Close()
		t.Fatal("a state under a regular file opened")
	}

	s := openState(t, t.TempDir(), "fp")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(Result{Mutant: stateMutant(1), Outcome: Killed}); err == nil {
		t.Error("a closed state accepted a verdict")
	}
}

// Every field that tells one mutant from another moves its id, and the
// framing keeps text shifted between Original and Mutated from colliding.
func TestAMutantIDSeparatesEveryIdentifyingField(t *testing.T) {
	base := stateMutant(10)
	variants := []func(*Mutant){
		func(m *Mutant) { m.Path = "pkg/b.go" },
		func(m *Mutant) { m.Operator = ConditionalBoundary },
		func(m *Mutant) { m.Offset = 11 },
		func(m *Mutant) { m.Length = 3 },
		func(m *Mutant) { m.Original = "=" },
		func(m *Mutant) { m.Mutated = "!" },
		func(m *Mutant) { m.Original, m.Mutated = "==!", "=" },
	}
	ids := map[string]bool{MutantID(base): true}
	for i, change := range variants {
		m := base
		change(&m)
		id := MutantID(m)
		if ids[id] {
			t.Errorf("variant %d shares an id with another mutant", i)
		}
		ids[id] = true
	}
	moved := base
	moved.Line, moved.Column, moved.Reason = 99, 1, "equivalent"
	if MutantID(moved) != MutantID(base) {
		t.Error("a mutant's id depends on its line, column or reason")
	}
}
