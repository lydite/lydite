package coverage

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/annotation"
)

// parseFixture writes src and parses it with comments, which is what a
// declaration is read out of.
func parseFixture(t *testing.T, src string) (*token.FileSet, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return token.NewFileSet(), path
}

func declaredIn(t *testing.T, src string, gate annotation.Gate) (Excluded, *token.FileSet, error) {
	t.Helper()
	fset, path := parseFixture(t, src)
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DeclaredExclusions(fset, file, "a.go", gate)
	return got, fset, err
}

// A declaration covers the function whose doc comment holds it, and its whole
// span — the signature line included, since that is where a Go profile records
// a function's first block.
func TestADeclarationCoversTheFunctionItDocuments(t *testing.T) {
	t.Parallel()
	src := `package a

// downloadGo fetches a toolchain.
//
// [lydite:exclude_from_coverage][the proving ground exercises this end to end;
// a unit test here would run the machine's own toolchain]
func downloadGo() int {
	return 1
}

func scored() int {
	return 2
}
`
	got, fset, err := declaredIn(t, src, annotation.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Funcs) != 1 {
		t.Fatalf("excluded %d functions, want the one its declaration documents", len(got.Funcs))
	}
	for fn, reason := range got.Funcs {
		if fn.Name.Name != "downloadGo" {
			t.Errorf("excluded %s, want downloadGo", fn.Name.Name)
		}
		if !strings.Contains(reason, "proving ground") {
			t.Errorf("reason = %q, want the wrapped reason joined", reason)
		}
	}
	lines := got.Lines(fset)
	if !lines[7] || !lines[9] {
		t.Errorf("lines = %v, want the whole declaration including its signature", lines)
	}
	if lines[11] {
		t.Error("the exclusion reached the function below it")
	}
}

// A declaration names one gate. A function whose coverage is taken in another
// process has not thereby become unscorable, and the reverse.
func TestADeclarationDoesNotCoverAnotherGate(t *testing.T) {
	t.Parallel()
	src := `package a

// [lydite:exclude_from_coverage][the proving ground exercises this]
func f() int { return 1 }
`
	got, _, err := declaredIn(t, src, annotation.CRAP)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Funcs) != 0 {
		t.Errorf("a coverage declaration excluded a function from the CRAP gate: %v", got.Funcs)
	}
	// And it is not reported as covering nothing either — it covers a
	// function, just not for this gate.
	if len(got.Unused) != 0 {
		t.Errorf("unused = %v, want another gate's declaration passed over in silence", got.Unused)
	}
}

// A declaration that documents no function is named. Its author believes they
// have answered a finding, and nothing they can see says otherwise — the
// commonest cause being a declaration written inside a body, where it reads
// perfectly and does nothing.
func TestADeclarationThatDocumentsNoFunctionIsNamed(t *testing.T) {
	t.Parallel()
	src := `package a

func f() int {
	// [lydite:exclude_from_crap][written inside the body, where it does nothing]
	return 1
}
`
	got, _, err := declaredIn(t, src, annotation.CRAP)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Funcs) != 0 {
		t.Errorf("a declaration inside a body excluded the function: %v", got.Funcs)
	}
	if len(got.Unused) != 1 || got.Unused[0] != 4 {
		t.Errorf("unused = %v, want the line it was written on", got.Unused)
	}
}

// A declaration with no reason is an error naming the line, never a comment
// quietly ignored: the reason is the entire record of why a finding cannot be
// cleared, and an author whose declaration is dropped has a finding they
// believe they have already answered.
func TestADeclarationWithNoReasonIsAnError(t *testing.T) {
	t.Parallel()
	src := `package a

// [lydite:exclude_from_crap][]
func f() int { return 1 }
`
	if _, _, err := declaredIn(t, src, annotation.CRAP); err == nil ||
		!strings.Contains(err.Error(), "a.go") {
		t.Errorf("err = %v, want one naming the file", err)
	}
}

// A declared function's lines leave the coverage report entirely — both sides
// of it. Out of the numerator so nothing claims to have covered them, and out
// of the denominator so the author's own statement is not reported back as a
// hole they have to fill, which is the reading that makes an exclusion worth
// nothing.
func TestADeclaredFunctionLeavesBothSidesOfTheFigure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n\ngo 1.26\n")
	writeFile(t, root, "a.go", `package m

func Kept(n int) int {
	if n > 0 {
		return 1
	}
	return 0
}

// Skipped provisions something.
//
// [lydite:exclude_from_coverage][the proving ground exercises this end to end]
func Skipped(n int) int {
	if n > 0 {
		return 1
	}
	return 0
}
`)
	// A profile in which neither function was executed. Kept's statements are
	// the figure; Skipped's are in neither part of it.
	writeFile(t, root, "cover.out", `mode: set
m/a.go:3.24,4.11 1 1
m/a.go:4.11,6.3 1 0
m/a.go:7.2,7.10 1 0
m/a.go:13.27,14.11 1 0
m/a.go:14.11,16.3 1 0
m/a.go:17.2,17.10 1 0
`)
	src := GoModuleProfile{Profile: filepath.Join(root, "cover.out"), ModuleName: "m"}

	lines, err := goProfileLines(src, root)
	if err != nil {
		t.Fatal(err)
	}
	if lines.Total != 3 {
		t.Errorf("total = %d, want 3 — the declared function is in neither side", lines.Total)
	}
	if lines.Covered != 1 {
		t.Errorf("covered = %d, want 1", lines.Covered)
	}

	hits, err := ParseGoProfile(src, root)
	if err != nil {
		t.Fatal(err)
	}
	for line := 13; line <= 18; line++ {
		if _, ok := hits["a.go"][line]; ok {
			t.Errorf("line %d is still in the per-line hits the patch gate reads", line)
		}
	}
	if _, ok := hits["a.go"][3]; !ok {
		t.Error("the undeclared function left the report too")
	}
}

// writeFile puts a file under root, creating the directories it needs.
func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A coverage declaration answers the coverage gates and no other. Mutation
// bounds its mutants by the lines the report says ran, so reading the same map
// the coverage gates read would let one declaration silence a second gate — and
// a function whose coverage is taken in another process has not thereby become
// unmutable.
func TestACoverageDeclarationLeavesTheExecutedLinesAlone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n\ngo 1.26\n")
	writeFile(t, root, "a.go", `package m

// Provisioned runs somewhere this suite cannot see.
//
// [lydite:exclude_from_coverage][the proving ground exercises this end to end]
func Provisioned(n int) int {
	if n > 0 {
		return 1
	}
	return 0
}
`)
	// Executed, and declared. Both halves matter: a line with no hits would be
	// absent from the executed set anyway, and the assertion would hold for
	// the wrong reason.
	writeFile(t, root, "cover.out", `mode: set
m/a.go:6.27,7.11 1 1
m/a.go:7.11,9.3 1 1
m/a.go:10.2,10.10 1 1
`)
	rep, err := goProfile(GoModuleProfile{Profile: filepath.Join(root, "cover.out"), ModuleName: "m"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Lines.Total != 0 {
		t.Errorf("lines = %+v, want the declared function out of the coverage figure", rep.Lines)
	}
	if len(rep.Hits["a.go"]) != 0 {
		t.Errorf("hits = %v, want the declared lines out of what the coverage gates read", rep.Hits["a.go"])
	}
	if rep.Executed["a.go"][6] == 0 {
		t.Errorf("executed = %v, want the lines that ran kept for the gates a coverage declaration does not answer",
			rep.Executed["a.go"])
	}
}

// go/parser attaches a doc comment to the declaration that follows it, so a
// function written directly beneath an existing declaration takes it. The
// behaviour is pinned rather than defended against: which function a
// declaration names is Go's answer, and a rule of lydite's that disagreed with
// the compiler would be worse than this. What the test protects is that the
// limit is known, and that anyone changing the attachment rule sees it move.
func TestADeclarationFollowsGosOwnAttachment(t *testing.T) {
	t.Parallel()
	src := `package a

// [lydite:exclude_from_coverage][the proving ground exercises this]
func inserted() int { return 1 }

func original() int { return 2 }
`
	got, _, err := declaredIn(t, src, annotation.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Funcs) != 1 {
		t.Fatalf("excluded %d functions, want the one the parser attached it to", len(got.Funcs))
	}
	for fn := range got.Funcs {
		if fn.Name.Name != "inserted" {
			t.Errorf("excluded %s; the declaration belongs to whichever declaration follows it", fn.Name.Name)
		}
	}
	// And it is not reported as covering nothing, because it does cover a
	// function — which is exactly why nothing notices the transfer.
	if len(got.Unused) != 0 {
		t.Errorf("unused = %v, want none: the declaration found a function", got.Unused)
	}
}
