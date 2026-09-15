package rust

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// capturedRun is a fixture read as the run that produced it: the report the
// tool wrote, and the status it exited with, taken from the fixture's sibling
// `.exit` file.
//
// The pair is what a fixture has to be. A report alone cannot say whether the
// gate passed — cargo-audit exits zero over a crate whose only advisories are
// warnings, and clippy exits non-zero over a crate that will not compile — and
// the exit status is the verdict.
func capturedRun(t *testing.T, name string) (report string, err error) {
	t.Helper()
	data, readErr := os.ReadFile(filepath.Join("testdata", name))
	if readErr != nil {
		t.Fatalf("reading fixture: %v", readErr)
	}
	exit := strings.TrimSuffix(name, filepath.Ext(name)) + ".exit"
	status, readErr := os.ReadFile(filepath.Join("testdata", exit))
	if readErr != nil {
		t.Fatalf("reading the fixture's exit status: %v", readErr)
	}
	code, convErr := strconv.Atoi(strings.TrimSpace(string(status)))
	if convErr != nil {
		t.Fatalf("exit status of %s: %v", name, convErr)
	}
	if code == 0 {
		return string(data), nil
	}
	// The wording os/exec gives a non-zero exit, so a Detail built from it
	// reads as it does in a real run.
	return string(data), fmt.Errorf("exit status %d", code)
}

// stdoutRun is a captured run of a tool that writes its report to stdout.
func stdoutRun(t *testing.T, name string) executil.Result {
	t.Helper()
	report, err := capturedRun(t, name)
	return executil.Result{Output: report, Err: err}
}

// stderrRun is a captured run of cargo-deny, whose NDJSON arrives on stderr
// while stdout stays empty.
func stderrRun(t *testing.T, name string) executil.Result {
	t.Helper()
	report, err := capturedRun(t, name)
	return executil.Result{Stderr: report, Err: err}
}

// fingerprints is each claim's identity, in the order the claims were made.
func fingerprints(findings []finding.Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Fingerprint())
	}
	return out
}

func TestFindingsDetailIndentsEachClaimsOwnDetailBeneathIt(t *testing.T) {
	// The claim line locates and names what was found; the lines under it are
	// the tool's own account of it — a rendered diagnostic, an advisory URL, a
	// dependency path — and the indent is what keeps the two readable as one
	// entry when twenty are listed together.
	got := findingsDetail([]finding.Finding{
		{Path: "Cargo.lock", Line: 12, Rule: "RUSTSEC-2020-0071", Message: "a claim",
			Detail: []string{"https://rustsec.org/advisories/RUSTSEC-2020-0071"}},
		{Path: "src/lib.rs", Line: 1, Rule: "clippy::ptr_arg", Message: "another"},
	})
	want := "Cargo.lock:12  RUSTSEC-2020-0071  a claim\n" +
		"  https://rustsec.org/advisories/RUSTSEC-2020-0071\n" +
		"src/lib.rs:1  clippy::ptr_arg  another\n"
	if got != want {
		t.Errorf("detail =\n%q\nwant\n%q", got, want)
	}
}

func TestFindingsDetailIsEmptyWithoutClaims(t *testing.T) {
	// Empty is what lets each gate tell a failure its claims explain from one
	// it has to account for some other way.
	if got := findingsDetail(nil); got != "" {
		t.Errorf("detail = %q, want none", got)
	}
}

func TestUnreadableNamesTheGateAndTheExitStatus(t *testing.T) {
	// cargo-deny's status is a bitmask of which checks failed, so the number
	// itself is the diagnosis and has to survive into the row.
	got := unreadable(GateDeny, fmt.Errorf("exit status 6"))
	if !strings.Contains(got, GateDeny) || !strings.Contains(got, "exit status 6") {
		t.Errorf("detail = %q, want the gate and the status it exited with", got)
	}
}

func TestOneRunPerGateKeepsEveryCapturedClaimsFingerprint(t *testing.T) {
	// A fingerprint is a claim's identity across runs, and one that moves
	// re-posts every review thread as new. These are the identities the
	// captured reports produce, pinned as values rather than recomputed from
	// the parsers the assertion is about — which is what makes this an
	// assertion about the claims and not about the code that derives them.
	cases := []struct {
		fixture string
		got     []finding.Finding
		want    []string
	}{
		{
			fixture: "clippy.ndjson",
			got:     clippyResult(crateUnderClippy(t), stdoutRun(t, "clippy.ndjson")).Findings,
			want:    []string{"v1:2b29d02dfbd6f459", "v1:252090a87ba4bf83"},
		},
		{
			fixture: "audit.json",
			got:     auditResult(crateDir(t), stdoutRun(t, "audit.json")).Findings,
			want:    []string{"v1:b0a0d130fc48e514", "v1:75f9033b9f8296b3"},
		},
		{
			fixture: "deny.ndjson",
			got:     denyResult(crateDir(t), stderrRun(t, "deny.ndjson")).Findings,
			want:    []string{"v1:95f7b979e3558348", "v1:c3164fde9059f99c", "v1:76b3346873f20bfb"},
		},
		{
			fixture: "deny-transitive.ndjson",
			got:     denyResult(crateDir(t), stderrRun(t, "deny-transitive.ndjson")).Findings,
			want:    []string{"v1:28cf4c0066a801a5"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			if got := fingerprints(tc.got); !slices.Equal(got, tc.want) {
				t.Errorf("fingerprints = %q, want %q", got, tc.want)
			}
		})
	}
}
