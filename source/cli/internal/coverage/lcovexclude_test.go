package coverage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/runner"
)

// withdrawn rewrites a probe source so its declarations say nothing, leaving
// every line where it was.
//
// The A/B halves have to be the same file at the same line numbers, because
// the report they are both measured against was captured once. Deleting the
// declaration's lines would shift every function below it and compare two
// different files.
func withdrawn(src string) string {
	return strings.ReplaceAll(src, annotation.Marker(annotation.Coverage), "[not:a_declaration]")
}

// measureProbe materialises a captured probe tree, puts one of the captured
// reports beside it, and measures the pair as `lydite test` would.
//
// rewrite, when given, is applied to every source file first — which is how the
// same report is measured with the declarations in force and with them
// withdrawn.
func measureProbe(t *testing.T, probe, report string, lang runner.Lang, rewrite func(string) string) Report {
	t.Helper()
	root := fixture.Tree(t, filepath.Join("testdata", probe))
	if rewrite != nil {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			src, err := os.ReadFile(path) // #nosec G304 -- a file this test just wrote into its own temporary directory
			if err != nil {
				return err
			}
			return os.WriteFile(path, []byte(rewrite(string(src))), 0o600)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join("testdata", report))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lcov.info"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Measure(context.Background(), root, ".", "lcov.info", lang, nil)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// hasLines reports which of lines the map speaks for.
func hasLines(m map[int]int, lines ...int) []int {
	var out []int
	for _, line := range lines {
		if _, ok := m[line]; ok {
			out = append(out, line)
		}
	}
	return out
}

// A declaration in Rust takes its function out of both sides of the
// component's figure and out of the patch gate, and the same report measured
// with the declarations withdrawn puts every line back.
//
// The numbers are read off the captured cargo-llvm-cov report: `provision` is
// lines 12-21 and `Counter::value` is 70-72, together thirteen DA records, all
// of them unhit. So the tool's own LF of 64 becomes 51 and its LH of 48 is
// unchanged — 75% covered rather than 75% of a denominator holding two
// functions the author has said this suite does not measure.
func TestARustDeclarationTakesItsFunctionOutOfTheFigure(t *testing.T) {
	honoured := measureProbe(t, "rustlcovprobe", "rust-lcov.info", runner.Rust, nil)
	if honoured.Lines != (LineCount{Covered: 48, Total: 51}) {
		t.Errorf("Lines = %+v, want {48 51}", honoured.Lines)
	}
	excluded := []int{12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 70, 71, 72}
	if got := hasLines(honoured.Hits["src/lib.rs"], excluded...); got != nil {
		t.Errorf("Hits still speaks for %v", got)
	}
	// Present again in Executed, because a coverage declaration answers the
	// coverage gates and nothing else: a function measured in another process
	// has not thereby become unmutable, and mutation bounds its mutants by
	// this map.
	if got := hasLines(honoured.Executed["src/lib.rs"], excluded...); len(got) != len(excluded) {
		t.Errorf("Executed speaks for %v, want all of %v", got, excluded)
	}

	withheld := measureProbe(t, "rustlcovprobe", "rust-lcov.info", runner.Rust, withdrawn)
	if withheld.Lines != (LineCount{Covered: 48, Total: 64}) {
		t.Errorf("with the declarations withdrawn, Lines = %+v, want {48 64} — the tool's own LF and LH", withheld.Lines)
	}
	if got := hasLines(withheld.Hits["src/lib.rs"], excluded...); len(got) != len(excluded) {
		t.Errorf("with the declarations withdrawn, Hits speaks for %v, want all of %v", got, excluded)
	}

	// The patch gate reads Hits, so a change confined to a declared function
	// has no coverable line to be gated on rather than ten uncovered ones.
	changed := map[string][]int{"src/lib.rs": {12, 13, 14, 15, 16, 17, 18, 19, 20, 21}}
	if hit, total := PatchPercent(changed, honoured.Hits); hit != 0 || total != 0 {
		t.Errorf("PatchPercent = %d/%d, want 0/0", hit, total)
	}
	if hit, total := PatchPercent(changed, withheld.Hits); hit != 0 || total != 10 {
		t.Errorf("with the declarations withdrawn, PatchPercent = %d/%d, want 0/10", hit, total)
	}
}

// The same, in TypeScript, under both coverage providers lydite supports.
//
// `provision` is lines 9-18 and `Counter.read` is 39-41, and lcov lists only
// executable lines: the seven DA records inside those two spans are 10, 11,
// 12, 14, 16, 17 and 40, all unhit. LF 13 becomes 6 and LH stays 6, so a
// package whose every measured line is covered reports as one.
//
// Both providers, because they disagree about what an `FN` record is called
// and a scope read off a parser is answerable to neither.
func TestATypeScriptDeclarationTakesItsFunctionOutOfTheFigure(t *testing.T) {
	for _, report := range []string{"ts-lcov-v8.info", "ts-lcov-istanbul.info"} {
		t.Run(report, func(t *testing.T) {
			honoured := measureProbe(t, "tslcovprobe", report, runner.TypeScript, nil)
			if honoured.Lines != (LineCount{Covered: 6, Total: 6}) {
				t.Errorf("Lines = %+v, want {6 6}", honoured.Lines)
			}
			excluded := []int{10, 11, 12, 14, 16, 17, 40}
			if got := hasLines(honoured.Hits["src/scope.ts"], excluded...); got != nil {
				t.Errorf("Hits still speaks for %v", got)
			}
			if got := hasLines(honoured.Executed["src/scope.ts"], excluded...); len(got) != len(excluded) {
				t.Errorf("Executed speaks for %v, want all of %v", got, excluded)
			}

			withheld := measureProbe(t, "tslcovprobe", report, runner.TypeScript, withdrawn)
			if withheld.Lines != (LineCount{Covered: 6, Total: 13}) {
				t.Errorf("with the declarations withdrawn, Lines = %+v, want {6 13} — the tool's own LF and LH", withheld.Lines)
			}
			if got := hasLines(withheld.Hits["src/scope.ts"], excluded...); len(got) != len(excluded) {
				t.Errorf("with the declarations withdrawn, Hits speaks for %v, want all of %v", got, excluded)
			}
		})
	}
}

// The deduction is per record and not per line. LF and LH count records, which
// is why the captured Rust report reads 64 and 48 over 63 distinct DA lines: a
// generic emits one record per monomorphisation at the same line. Subtracting
// one per excluded line would leave the extra behind, in a denominator the
// gate then compares against a baseline the tool recorded.
func TestTheDeductionIsPerRecordAndNotPerLine(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/lib.rs",
		"// [lydite:exclude_from_coverage][measured elsewhere]\n"+
			"pub fn widest() -> i64 {\n    1\n}\n")
	// Two records for line 2, as a generic's two instantiations produce.
	write(t, root, "lcov.info", "SF:src/lib.rs\n"+
		"DA:2,1\nDA:2,1\nDA:3,1\n"+
		"LF:3\nLH:3\n"+
		"end_of_record\n")

	got, err := Measure(context.Background(), root, ".", "lcov.info", runner.Rust, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lines != (LineCount{}) {
		t.Errorf("Lines = %+v, want {0 0} — every record inside the declared span withdrawn", got.Lines)
	}
}

// A declaration whose next sibling introduces no function reaches the report as
// unused, in both lcov languages, keyed exactly as the hits beside it are.
//
// The same key, because a warning naming a path no gate mentions is one a reader
// cannot open: the component's own directory is on the hits after prefixHits,
// so it is on this too.
func TestAnUnmatchedDeclarationReachesTheLCOVReport(t *testing.T) {
	for _, c := range []struct {
		lang   runner.Lang
		file   string
		source string
	}{
		{runner.Rust, "src/lib.rs", "// [lydite:exclude_from_coverage][measured elsewhere]\n" +
			"pub struct Counter;\n\npub fn f() -> i64 {\n    1\n}\n"},
		{runner.TypeScript, "src/scope.ts", "// [lydite:exclude_from_coverage][measured elsewhere]\n" +
			"export const value = 1;\n\nexport function f(): number {\n  return 1;\n}\n"},
	} {
		t.Run(string(c.lang), func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "pkg/"+c.file, c.source)
			write(t, root, "pkg/lcov.info", "SF:"+c.file+"\nDA:4,1\nDA:5,1\nLF:2\nLH:2\nend_of_record\n")

			got, err := Measure(context.Background(), root, "pkg", "lcov.info", c.lang, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := "pkg/" + c.file + ":1"
			if len(got.Unused) != 1 || got.Unused[0] != want {
				t.Errorf("Unused = %v, want [%s]", got.Unused, want)
			}
			if _, ok := got.Hits["pkg/"+c.file]; !ok {
				t.Errorf("Hits is keyed %v, which the warning above does not name", got.Hits)
			}
			// It excluded nothing, which is what makes it worth naming: the
			// tool's own counts stand.
			if got.Lines != (LineCount{Covered: 2, Total: 2}) {
				t.Errorf("Lines = %+v, want {2 2} — nothing was deducted", got.Lines)
			}
		})
	}
}

// One source named twice is one declaration warned about once. A trace holding
// two records for a file — which cargo-llvm-cov emits for a source compiled
// into two targets — would otherwise report one misplaced declaration twice,
// and a reader who fixed it once would still see it.
func TestASourceNamedTwiceNamesItsDeclarationOnce(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/lib.rs", "// [lydite:exclude_from_coverage][measured elsewhere]\n"+
		"pub struct Counter;\n\npub fn f() -> i64 {\n    1\n}\n")
	write(t, root, "lcov.info",
		"SF:src/lib.rs\nDA:4,1\nLF:1\nLH:1\nend_of_record\n"+
			"SF:src/lib.rs\nDA:5,1\nLF:1\nLH:1\nend_of_record\n")

	got, err := Measure(context.Background(), root, ".", "lcov.info", runner.Rust, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Unused) != 1 {
		t.Errorf("Unused = %v, want one entry for the one declaration", got.Unused)
	}
}

// A declaration that found its function is not named, in either lcov language.
// A warning on every working declaration is one readers learn to skim past.
func TestAMatchedLCOVDeclarationIsNotNamed(t *testing.T) {
	for _, probe := range []struct {
		dir, report string
		lang        runner.Lang
	}{
		{"rustlcovprobe", "rust-lcov.info", runner.Rust},
		{"tslcovprobe", "ts-lcov-v8.info", runner.TypeScript},
	} {
		t.Run(probe.dir, func(t *testing.T) {
			got := measureProbe(t, probe.dir, probe.report, probe.lang, nil)
			if len(got.Unused) != 0 {
				t.Errorf("Unused = %v, want none: every declaration in the probe covers a function", got.Unused)
			}
		})
	}
}

// A source file the report names but the grammar cannot read is no exclusion,
// never a failed measurement. The tree compiled to produce the report being
// read, so failing a coverage figure over a parse would turn it into a syntax
// check.
func TestASourceThatWillNotParseIsNoExclusion(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/lib.rs", "// [lydite:exclude_from_coverage][measured elsewhere]\npub fn f( {\n")
	write(t, root, "lcov.info", "SF:src/lib.rs\nDA:2,0\nLF:1\nLH:0\nend_of_record\n")

	got, err := Measure(context.Background(), root, ".", "lcov.info", runner.Rust, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lines != (LineCount{Covered: 0, Total: 1}) {
		t.Errorf("Lines = %+v, want {0 1} — the tool's own counts, unreduced", got.Lines)
	}
}

// A declaration with no reason is an error on this path too, and never a
// comment quietly disregarded: the reason is the entire risk record for a
// finding nobody else can clear.
func TestADeclarationWithNoReasonFailsTheMeasurement(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/lib.rs", "// [lydite:exclude_from_coverage]\npub fn f() -> i64 {\n    1\n}\n")
	write(t, root, "lcov.info", "SF:src/lib.rs\nDA:2,1\nLF:1\nLH:1\nend_of_record\n")

	_, err := Measure(context.Background(), root, ".", "lcov.info", runner.Rust, nil)
	var noReason annotation.ErrNoReason
	if !errors.As(err, &noReason) {
		t.Fatalf("err = %v, want ErrNoReason", err)
	}
}
