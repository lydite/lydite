package reviewdecision

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
)

// reviewScan is what fakeScanReader answers for one report directory.
type reviewScan struct {
	rows []GateRow
	err  error
}

// fakeScanReader answers from a fixed map of report directories, and records
// which directories it was asked for, in order. A directory absent from the
// map holds no scan document.
type fakeScanReader struct {
	scans map[string]reviewScan
	asked []string
}

func (f *fakeScanReader) ReadScan(reports string) ([]GateRow, error) {
	f.asked = append(f.asked, reports)
	scan, ok := f.scans[reports]
	if !ok {
		return nil, fmt.Errorf("%s: %w", reports, ErrNoScanDocument)
	}
	return scan.rows, scan.err
}

// evidenceRoot is a scan root declaring components as given, with every
// directory the declaration names created so the declaration validates. An
// empty declaration writes no components file at all.
func evidenceRoot(t *testing.T, declaration string, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if declaration == "" {
		return root
	}
	p := filepath.Join(root, filepath.FromSlash(component.FileName))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// passing is a row for each label, every one of them passed.
func passing(labels ...string) []GateRow {
	rows := make([]GateRow, 0, len(labels))
	for _, l := range labels {
		rows = append(rows, GateRow{Label: l, Passed: true})
	}
	return rows
}

const (
	goDeclared   = "components:\n  - name: cli\n    dir: cli\n    runner: go-test\n"
	rustDeclared = "components:\n  - name: api\n    dir: api\n    runner: cargo-nextest\n"
	tsDeclared   = "components:\n  - name: web\n    dir: web\n    runner: vitest\n"
)

// Each shape a scan document's rows can take against each declaration: the
// licence gate must have run and passed for every component, the advisory
// check too wherever the component's declared language runs one, or wherever
// the document shows the component running that language's other gate.
func TestScanEvidenceOverEveryRowShape(t *testing.T) {
	for _, tc := range []struct {
		name        string
		declaration string
		dirs        []string
		rows        []GateRow
		want        bool
	}{
		{"undeclared, licence passed", "", nil, passing("licence(cli)"), true},
		{"undeclared, licence failed", "", nil, []GateRow{{Label: "licence(cli)"}}, false},
		{"undeclared, no licence row", "", nil, passing("semgrep(cli)"), false},
		{"a row with no component is not attributed", "", nil, passing("scan", "licence(cli)"), true},
		{"only rows with no component names nothing", "", nil, passing("scan", "(cli)", "licence()"), false},

		// Undeclared, the document's own gosec or clippy row says which
		// advisory check has to be beside it.
		{"gosec without govulncheck", "", nil, passing("gosec(cli)", "licence(cli)"), false},
		{"gosec with govulncheck", "", nil, passing("gosec(cli)", "govulncheck(cli)", "licence(cli)"), true},
		{"gosec with a failed govulncheck", "", nil,
			append(passing("gosec(cli)", "licence(cli)"), GateRow{Label: "govulncheck(cli)"}), false},
		{"clippy without cargo-audit", "", nil, passing("cargo clippy(api)", "licence(api)"), false},
		{"clippy with cargo-audit", "", nil, passing("cargo clippy(api)", "cargo-audit(api)", "licence(api)"), true},

		// Declared, the language says which advisory check is needed, whether
		// or not the document carries any other row for the component.
		{"declared Go, licence alone", goDeclared, []string{"cli"}, passing("licence(cli)"), false},
		{"declared Go, licence and govulncheck", goDeclared, []string{"cli"}, passing("licence(cli)", "govulncheck(cli)"), true},
		{"declared Rust, licence alone", rustDeclared, []string{"api"}, passing("licence(api)"), false},
		{"declared Rust, licence and cargo-audit", rustDeclared, []string{"api"}, passing("licence(api)", "cargo-audit(api)"), true},
		{"declared TypeScript runs no advisory check", tsDeclared, []string{"web"}, passing("licence(web)"), true},
		{"a declared component the document never names", goDeclared, []string{"cli"},
			passing("licence(other)"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := evidenceRoot(t, tc.declaration, tc.dirs...)
			reader := &fakeScanReader{scans: map[string]reviewScan{"reports": {rows: tc.rows}}}
			got, warnings := ScanEvidence(root, []string{"reports"}, reader)
			if got != tc.want {
				t.Errorf("ScanEvidence = %v, want %v", got, tc.want)
			}
			if len(warnings) != 0 {
				t.Errorf("a readable document must warn about nothing, got %q", warnings)
			}
		})
	}
}

// Two documents naming one gate keep the worse answer, in either order: a
// pass a later job's failure overturns is not forgotten because it was seen
// first, and a failure is not overturned by a pass after it.
func TestScanEvidenceKeepsTheWorseOfTwoDocuments(t *testing.T) {
	good := reviewScan{rows: passing("licence(cli)")}
	bad := reviewScan{rows: []GateRow{{Label: "licence(cli)"}}}
	for _, order := range [][]string{{"good", "bad"}, {"bad", "good"}} {
		root := evidenceRoot(t, "")
		reader := &fakeScanReader{scans: map[string]reviewScan{"good": good, "bad": bad}}
		if got, _ := ScanEvidence(root, order, reader); got {
			t.Errorf("ScanEvidence over %v = true, want false", order)
		}
	}
}

// A directory holding no scan document is an ordinary shape and says nothing,
// and one whose document will not be read is warned about; either is skipped,
// and evidence from the others still counts.
func TestScanEvidenceSkipsWhatItCannotRead(t *testing.T) {
	root := evidenceRoot(t, "")
	reader := &fakeScanReader{scans: map[string]reviewScan{
		"broken": {err: errors.New("broken/scan.json: unexpected end of JSON input")},
		"good":   {rows: passing("licence(cli)")},
	}}
	got, warnings := ScanEvidence(root, []string{"absent", "broken", "good"}, reader)
	if !got {
		t.Error("a passing document beside an absent and an unreadable one must still count")
	}
	want := []string{"warning: no licence or SCA evidence came from broken: broken/scan.json: unexpected end of JSON input"}
	if !reflect.DeepEqual(warnings, want) {
		t.Errorf("warnings = %q, want %q", warnings, want)
	}
	if !reflect.DeepEqual(reader.asked, []string{"absent", "broken", "good"}) {
		t.Errorf("asked = %q, want every directory in order", reader.asked)
	}
}

// With no scan document read at all, nothing is evidence, and the condition
// fails: no directory, only directories holding none, or only documents that
// will not be read, each warned about in the order given.
func TestScanEvidenceWithNothingReadIsFalse(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reports  []string
		scans    map[string]reviewScan
		warnings []string
	}{
		{"no directory", nil, nil, nil},
		{"no scan document", []string{"a", "b"}, nil, nil},
		{"none will be read", []string{"a", "b"}, map[string]reviewScan{
			"a": {err: errors.New("first")},
			"b": {err: errors.New("second")},
		}, []string{
			"warning: no licence or SCA evidence came from a: first",
			"warning: no licence or SCA evidence came from b: second",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := evidenceRoot(t, goDeclared, "cli")
			got, warnings := ScanEvidence(root, tc.reports, &fakeScanReader{scans: tc.scans})
			if got {
				t.Error("ScanEvidence = true, want false")
			}
			if !reflect.DeepEqual(warnings, tc.warnings) {
				t.Errorf("warnings = %q, want %q", warnings, tc.warnings)
			}
		})
	}
}

// A component declaration that will not load fails the evidence closed, with
// one warning naming why, and reads no scan document at all.
func TestScanEvidenceFailsClosedOnAnUnreadableDeclaration(t *testing.T) {
	root := evidenceRoot(t, "components:\n  - name: cli\n    dir: cli\n    runner: go-test\n    not_a_real_key: true\n", "cli")
	reader := &fakeScanReader{scans: map[string]reviewScan{"reports": {rows: passing("licence(cli)", "govulncheck(cli)")}}}
	got, warnings := ScanEvidence(root, []string{"reports"}, reader)
	if got {
		t.Error("an unreadable components.yml must fail the condition, not satisfy it")
	}
	if len(warnings) != 1 || !strings.HasPrefix(warnings[0], "warning: no licence or SCA evidence: ") {
		t.Errorf("warnings = %q, want one naming the declaration", warnings)
	}
	if len(reader.asked) != 0 {
		t.Errorf("no scan document is read once the declaration fails, asked %q", reader.asked)
	}
}

// A label is a gate and the component it ran for; a label with no component —
// no "(" at all, or one opening at the very first character, which names an
// empty gate — carries nothing to attribute.
func TestSplitGateLabel(t *testing.T) {
	for _, tc := range []struct {
		label           string
		gate, component string
		ok              bool
	}{
		{"licence(cli)", "licence", "cli", true},
		{"cargo clippy(api)", "cargo clippy", "api", true},
		{"scan", "", "", false},
		{"(cli)", "", "", false},
		{"licence()", "", "", false},
		{"licence(cli", "", "", false},
	} {
		gate, component, ok := splitGateLabel(tc.label)
		if gate != tc.gate || component != tc.component || ok != tc.ok {
			t.Errorf("splitGateLabel(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.label, gate, component, ok, tc.gate, tc.component, tc.ok)
		}
	}
}
