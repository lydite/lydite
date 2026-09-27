package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/ui"
)

// scanOrderRows is every row a scan over scanOrderRepo reports, as
// "<status> <label>", in the order the report holds them.
//
// The walk is declaration order, and each component contributes its rows
// where it is declared: a scanned component its check rows and then its
// licence row, a raw-command component and an off-by-default shell component
// one row per gate that did not run, and a component sharing an earlier one's
// directory and environment a single row saying so. The root-scoped gates
// follow the whole walk.
var scanOrderRows = []string{
	"fail gosec(order-a)",
	"pass govulncheck(order-a)",
	"context licence(order-a)",
	"unmeasured scan(order-legacy)",
	"unmeasured licence(order-legacy)",
	"unmeasured findings(order-legacy)",
	"unmeasured scan(order-scripts)",
	"unmeasured licence(order-scripts)",
	"unmeasured findings(order-scripts)",
	"fail gosec(order-b)",
	"pass govulncheck(order-b)",
	"context licence(order-b)",
	"unmeasured scan(order-a-again)",
	"fail gitleaks",
}

// scanOrderFindings is every located claim, as "<gate> <component> <path>:<line>
// <row>", in the order the report holds them: each scanned component's in
// declaration order, then the root-scoped gates', which name no component.
var scanOrderFindings = []string{
	"gosec order-a a/main.go:6 gosec(order-a)",
	"gosec order-b b/main.go:6 gosec(order-b)",
	"gitleaks - deploy/config.yml:1 gitleaks",
}

// scanOrderCrashes is every gate whose claims are not a complete answer, as
// "<gate> <component>". A licence gate given a policy and no diff base gates
// nothing, so each scanned Go component names its licence bucket crashed, in
// declaration order.
var scanOrderCrashes = []string{
	"licence order-a",
	"licence order-b",
}

// scanOrderWarnings is lydite's own warnings on stderr, in order: the source no
// component scans, named before any check runs, and then each scanned
// component's declared environment as that component is reached. The
// duplicate declares the same environment as order-a and is never reached as a
// scan, so it warns nothing.
var scanOrderWarnings = []string{
	"warning: 1 go file(s) are under no component that checks them, so nothing scans them (e.g. c/main.go) — declare a component for them, or exclude them in .lydite/components.yml",
	"warning: 1 typescript file(s) are under no component that checks them, so nothing scans them (e.g. web/app.ts) — declare a component for them, or exclude them in .lydite/components.yml",
	"warning: order-a's checks are composed with the environment .lydite/components.yml declares: ORDER_A_FLAG — names only, because a declared value can carry a credential",
	"warning: order-b's checks are composed with the environment .lydite/components.yml declares: ORDER_B_FLAG — names only, because a declared value can carry a credential",
}

// The order a scan reports in is observable output: rows, findings and crashes
// are three append-only lists nothing sorts, and lydite's own warnings are
// written to stderr as they arise. This pins that order over a repository
// exercising every disposition a component can have — scanned, a raw command,
// a language off by default, and a duplicate — with root-scoped gates after
// them, in the text grammar and the JSON document alike.
//
// Real gosec, govulncheck and gitleaks run, as every other scan test here runs
// them; Semgrep is switched off because its registry config is fetched over the
// network. A tool that will not install fails the run, never skips it.
func TestScanOrderIsDeclarationOrderThenRootScoped(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		stdout, stderr, err := scanOrderRun(t, scanOrderRepo(t), "--no-color")
		scanOrderExitsFailing(t, err)

		rows, verdict := scanOrderTextRows(t, stdout)
		if !slices.Equal(rows, scanOrderRows) {
			t.Errorf("text rows:\n%s\nwant:\n%s\nstdout:\n%s", strings.Join(rows, "\n"), strings.Join(scanOrderRows, "\n"), stdout)
		}
		if !regexp.MustCompile(`^scan failed in \d+\.\ds$`).MatchString(verdict) {
			t.Errorf("verdict line = %q, want `scan failed in <duration>s`", verdict)
		}
		scanOrderAssertWarnings(t, stderr)
	})

	t.Run("json", func(t *testing.T) {
		stdout, stderr, err := scanOrderRun(t, scanOrderRepo(t), "--json")
		scanOrderExitsFailing(t, err)

		doc, err := ui.ReadDocument(strings.NewReader(stdout))
		if err != nil {
			t.Fatalf("reading the document: %v\n%s", err, stdout)
		}
		if doc.Verdict != ui.VerdictFail || doc.Exit != 1 {
			t.Errorf("verdict = %q exit %d, want fail and 1", doc.Verdict, doc.Exit)
		}

		rows := make([]string, 0, len(doc.Rows))
		for _, r := range doc.Rows {
			rows = append(rows, string(r.Status)+" "+r.Label)
		}
		if !slices.Equal(rows, scanOrderRows) {
			t.Errorf("json rows:\n%s\nwant:\n%s", strings.Join(rows, "\n"), strings.Join(scanOrderRows, "\n"))
		}

		findings := make([]string, 0, len(doc.Findings))
		for _, f := range doc.Findings {
			findings = append(findings, strings.Join([]string{
				f.Gate, scanOrderOrDash(f.Component), f.Path + ":" + strconv.Itoa(f.Line), f.Row,
			}, " "))
		}
		if !slices.Equal(findings, scanOrderFindings) {
			t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(findings, "\n"), strings.Join(scanOrderFindings, "\n"))
		}

		crashes := make([]string, 0, len(doc.Crashed))
		for _, c := range doc.Crashed {
			crashes = append(crashes, c.Gate+" "+scanOrderOrDash(c.Component))
		}
		if !slices.Equal(crashes, scanOrderCrashes) {
			t.Errorf("crashes:\n%s\nwant:\n%s", strings.Join(crashes, "\n"), strings.Join(scanOrderCrashes, "\n"))
		}
		scanOrderAssertWarnings(t, stderr)
	})
}

// scanOrderRepo builds the repository the order is pinned over, fresh on every
// call: a run writes its logs and its document under the scan root, and a
// second run over the same tree would scan them.
//
// Components, in declaration order:
//   - order-a, a dependency-free Go module whose one gosec claim is G404, with
//     a declared environment
//   - order-legacy, a raw command, which nothing scans
//   - order-scripts, a shell component, whose checks are off by default
//   - order-b, a second dependency-free Go module whose one gosec claim is
//     G304, with a declared environment of its own
//   - order-a-again, over order-a's directory with order-a's environment
//
// Beside them sit a Go module and a TypeScript file no component declares, for
// the unscanned warning, and a credential for gitleaks. The licence policy is
// stated so each Go component's licence gate reads its dependency set.
func scanOrderRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	scanOrderWrite(t, dir, ".lydite/components.yml", `components:
  - name: order-a
    dir: a
    runner: go-test
    env:
      ORDER_A_FLAG: "1"
  - name: order-legacy
    dir: legacy
    command: ["make", "check"]
  - name: order-scripts
    dir: scripts
    lang: shell
  - name: order-b
    dir: b
    runner: go-test
    env:
      ORDER_B_FLAG: "1"
  - name: order-a-again
    dir: a
    runner: go-test
    env:
      ORDER_A_FLAG: "1"
`)
	scanOrderWrite(t, dir, ".lydite/config.yml", `semgrep:
  enabled: false
licence:
  policy:
    allow: [MIT]
`)
	scanOrderWrite(t, dir, "a/go.mod", "module a\n\ngo 1.26\n")
	scanOrderWrite(t, dir, "a/main.go", `package main

import "math/rand"

func main() {
	_ = rand.Intn(10)
}
`)
	scanOrderWrite(t, dir, "legacy/Makefile", "check:\n\t@true\n")
	scanOrderWrite(t, dir, "scripts/run.sh", "#!/bin/sh\necho \"$1\"\n")
	scanOrderWrite(t, dir, "b/go.mod", "module b\n\ngo 1.26\n")
	scanOrderWrite(t, dir, "b/main.go", `package main

import "os"

func main() {
	_, _ = os.ReadFile(os.Args[1])
}
`)
	scanOrderWrite(t, dir, "c/go.mod", "module c\n\ngo 1.26\n")
	scanOrderWrite(t, dir, "c/main.go", "package main\n\nfunc main() {}\n")
	scanOrderWrite(t, dir, "web/app.ts", "export const x = 1;\n")
	// Assembled from halves, so this file does not itself carry the shape
	// gitleaks' generic-api-key rule matches.
	scanOrderWrite(t, dir, "deploy/config.yml", "aws_key: \""+"8a7b6c5d4e3f2a1b0c9d"+"8e7f6a5b4c3d2e1f0a9b"+"\"\n")

	for _, args := range [][]string{{"init", "-b", "main", "."}, {"add", "-A"}} {
		if r := executil.RunQuiet(context.Background(), dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	return dir
}

func scanOrderWrite(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

// scanOrderRun runs `lydite scan` over dir and answers what it wrote to its
// own stdout and stderr, and the error it returned.
func scanOrderRun(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"--dir", dir}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// scanOrderExitsFailing requires the verdict's exit code and nothing else: two
// gosec claims fail the run, and any other error is the scan not having run.
func scanOrderExitsFailing(t *testing.T, err error) {
	t.Helper()
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("scan returned %v, want the failing verdict's exit status 1", err)
	}
}

// scanOrderGlyphs reads a text row's glyph back as the status the JSON
// document names. The text grammar cannot tell unmeasured from refer or
// dropped, and no scan row is either of those.
var scanOrderGlyphs = map[string]string{
	"✓": "pass",
	"✗": "fail",
	"!": "unmeasured",
	"→": "context",
}

// scanOrderTextRows reads the text grammar's rows as "<status> <label>" and
// answers them with the verdict line. A row's head never begins with a space
// and its detail lines always do; a blank line separates the rows from the
// verdict. No scan label carries a space, so the label is the head's second
// field.
func scanOrderTextRows(t *testing.T, stdout string) ([]string, string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	var rows []string
	for i, line := range lines {
		if line == "" {
			if i != len(lines)-2 {
				t.Fatalf("a blank line before the end of the rows:\n%s", stdout)
			}
			return rows, lines[len(lines)-1]
		}
		if strings.HasPrefix(line, " ") {
			continue
		}
		fields := strings.Fields(line)
		status, ok := scanOrderGlyphs[fields[0]]
		if !ok || len(fields) < 2 {
			t.Fatalf("row %q does not begin with a glyph and a label:\n%s", line, stdout)
		}
		rows = append(rows, status+" "+fields[1])
	}
	t.Fatalf("no verdict line:\n%s", stdout)
	return nil, ""
}

// scanOrderAssertWarnings compares lydite's own scan warnings on stderr against
// scanOrderWarnings. Only those: the same stream carries toolchain
// provisioning, whose own warnings depend on the machine, and nothing a tool
// prints reaches it.
func scanOrderAssertWarnings(t *testing.T, stderr string) {
	t.Helper()
	var got []string
	for _, line := range strings.Split(stderr, "\n") {
		if !strings.HasPrefix(line, "warning: ") {
			continue
		}
		if strings.Contains(line, "nothing scans them") ||
			strings.Contains(line, "could not check what no component scans") ||
			strings.Contains(line, "'s checks are composed with the environment") {
			got = append(got, line)
		}
	}
	if !slices.Equal(got, scanOrderWarnings) {
		t.Errorf("warnings:\n%s\nwant:\n%s\nstderr:\n%s", strings.Join(got, "\n"), strings.Join(scanOrderWarnings, "\n"), stderr)
	}
}

// scanOrderOrDash stands a dash in for an empty component, which is what a
// root-scoped gate's claims and crashes carry.
func scanOrderOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
