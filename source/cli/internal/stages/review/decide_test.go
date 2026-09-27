package reviewstages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/reviewdecision"
)

// readmeExemption covers the README alone, unconditionally.
const readmeExemption = "exemptions:\n  - name: readme-only\n    reason: prose changes nothing executable\n    paths: [\"README.md\"]\n"

// readmeOnCondition covers the README on the condition a version bump needs,
// which only scan evidence can meet.
const readmeOnCondition = "exemptions:\n  - name: readme-on-evidence\n    reason: evidence-gated\n    versions: " +
	referral.VersionsPatchAndMinor + "\n    paths: [\"README.md\"]\n"

// Every source of a warning raises one here: the scan document will not be
// read, the event will not be read, and the commits between the base and HEAD
// will not be read either, because the one between them is gone. Out.Warnings
// holds them in the order they arose — the evidence, then the title, both
// asked for inside the decision, then the decision's own.
func TestDecideReturnsEveryWarningInTheOrderItArose(t *testing.T) {
	dir, shas := checkout(t,
		map[string]string{"README.md": "hello", referral.FileName: readmeExemption},
		map[string]string{"README.md": "middle"},
		map[string]string{"README.md": "again"})
	middle := filepath.Join(dir, ".git", "objects", shas[1][:2], shas[1][2:])
	if err := os.Remove(middle); err != nil {
		t.Fatalf("removing the middle commit: %v", err)
	}
	scan := &reviewScanReader{scans: map[string]reviewScan{"reports": {err: errors.New("unparseable")}}}

	out, err := Decide(context.Background(), DecideIn{
		Dir:       dir,
		Base:      shas[0],
		EventPath: filepath.Join(t.TempDir(), "absent.json"),
		Reports:   []string{"reports"},
		Scan:      scan,
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	want := []string{
		"warning: no licence or SCA evidence came from reports",
		"warning: could not read the event at",
		"warning: could not read the commits in",
	}
	if len(out.Warnings) != len(want) {
		t.Fatalf("Warnings = %q; want %d of them", out.Warnings, len(want))
	}
	for i, prefix := range want {
		if !strings.HasPrefix(out.Warnings[i], prefix) {
			t.Errorf("Warnings[%d] = %q; want it to begin %q", i, out.Warnings[i], prefix)
		}
	}
	if !reflect.DeepEqual(out.Result.Warnings, out.Warnings[2:]) {
		t.Errorf("Result.Warnings = %q; want them last in Warnings %q", out.Result.Warnings, out.Warnings)
	}
	if !reflect.DeepEqual(scan.asked, []string{"reports"}) {
		t.Errorf("scan documents asked for %q; want [reports]", scan.asked)
	}
}

// The title is read from the payload the stage was handed, so a break it
// declares reaches the decision.
func TestDecideReadsTheTitleFromEventPath(t *testing.T) {
	dir, shas := checkout(t,
		map[string]string{"README.md": "hello", referral.FileName: readmeExemption},
		map[string]string{"README.md": "again"})
	event := eventFile(t, `{"number": 7, "pull_request": {"number": 7, "title": "feat!: drop v1", "head": {"sha": "abc123"}}}`)

	out, err := Decide(context.Background(), DecideIn{Dir: dir, Base: shas[0], EventPath: event, Scan: &reviewScanReader{}})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Result.BreakDeclared != "the pull request title" {
		t.Errorf("BreakDeclared = %q; want the pull request title", out.Result.BreakDeclared)
	}
	if out.Result.Verdict != reviewdecision.VerdictRefer {
		t.Errorf("Verdict = %q; want refer for a declared break", out.Result.Verdict)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("Warnings = %q; want none", out.Warnings)
	}
}

// The scan evidence is read from the report directories through the reader
// the stage was handed, and a condition only it can meet is met by it alone.
func TestDecideReadsTheScanEvidenceFromReportsThroughScan(t *testing.T) {
	passing := map[string]reviewScan{"reports": {rows: []reviewdecision.GateRow{{Label: "licence(cli)", Passed: true}}}}
	cases := []struct {
		name    string
		reports []string
		exempt  bool
	}{
		{name: "with evidence", reports: []string{"reports"}, exempt: true},
		{name: "without evidence", reports: nil, exempt: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, shas := checkout(t,
				map[string]string{"README.md": "hello", referral.FileName: readmeOnCondition},
				map[string]string{"README.md": "again"})
			scan := &reviewScanReader{scans: passing}

			out, err := Decide(context.Background(), DecideIn{Dir: dir, Base: shas[0], Reports: tc.reports, Scan: scan})
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if exempt := out.Result.Decision.Exemption == "readme-on-evidence" && !out.Result.Decision.Referred; exempt != tc.exempt {
				t.Errorf("exempt = %t; want %t (decision %+v)", exempt, tc.exempt, out.Result.Decision)
			}
			if !reflect.DeepEqual(scan.asked, tc.reports) {
				t.Errorf("scan documents asked for %q; want %q", scan.asked, tc.reports)
			}
		})
	}
}

func TestDecidePropagatesAnError(t *testing.T) {
	dir, _ := checkout(t, map[string]string{"README.md": "hello"})
	if _, err := Decide(context.Background(), DecideIn{Dir: dir, Base: strings.Repeat("0", 40), Scan: &reviewScanReader{}}); err == nil {
		t.Error("Decide against a base that does not exist succeeded")
	}
}

func TestCheckDirty(t *testing.T) {
	dir, _ := checkout(t, map[string]string{"README.md": "hello"})
	out, err := CheckDirty(context.Background(), CheckDirtyIn{Dir: dir})
	if err != nil {
		t.Fatalf("CheckDirty: %v", err)
	}
	if out.Dirty {
		t.Error("a checkout with nothing uncommitted reads as dirty")
	}

	writeFiles(t, dir, map[string]string{"README.md": "edited, not committed"})
	out, err = CheckDirty(context.Background(), CheckDirtyIn{Dir: dir})
	if err != nil {
		t.Fatalf("CheckDirty: %v", err)
	}
	if !out.Dirty {
		t.Error("a checkout with an uncommitted edit reads as clean")
	}
}
