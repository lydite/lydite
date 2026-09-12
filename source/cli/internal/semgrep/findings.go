package semgrep

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// report mirrors the subset of Semgrep's JSON output lydite reads.
type report struct {
	Results []result `json:"results"`
	// Errors is what Semgrep could not do — a file it failed to parse, a rule
	// it could not fetch. A scan that parsed nothing reports zero results,
	// which is indistinguishable from a clean pass unless this is read.
	Errors []reportError `json:"errors"`
}

type result struct {
	CheckID string `json:"check_id"`
	Path    string `json:"path"`
	Start   struct {
		Line int `json:"line"`
		Col  int `json:"col"`
	} `json:"start"`
	End struct {
		Line int `json:"line"`
	} `json:"end"`
	Extra struct {
		Severity string `json:"severity"`
		Message  string `json:"message"`
	} `json:"extra"`
}

type reportError struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Path    string `json:"path"`
}

// findings is every result as a located claim.
//
// The site is the rule with the text it fired on, read from the tree through
// finding.Source. Semgrep's own `extra.lines` is not usable for it: on a
// logged-out run the field reads "requires login" rather than the source, so a
// site built from it would identify every claim in the repository alike.
func findings(dir string, rep report) []finding.Finding {
	src := finding.NewSource(dir)
	var out []finding.Finding
	for _, r := range rep.Results {
		path := filepath.ToSlash(strings.TrimPrefix(r.Path, "./"))
		if path == "" || r.Start.Line < 1 {
			continue
		}
		end := 0
		if r.End.Line > r.Start.Line {
			end = r.End.Line
		}
		out = append(out, finding.Finding{
			Gate:     Gate,
			Path:     path,
			Line:     r.Start.Line,
			EndLine:  end,
			Rule:     r.CheckID,
			Severity: strings.ToLower(r.Extra.Severity),
			Message:  r.Extra.Message,
			Site:     r.CheckID + "\x1f" + src.Line(path, r.Start.Line),
		})
	}
	finding.Number(out)
	return out
}

// blocking is whether the report names an error that makes a clean result
// untrustworthy.
//
// Semgrep exits non-zero for a findings failure and, on its own, zero for a
// run whose rules would not load or whose files would not parse — so a scan
// that read nothing reports no findings and passes. This is the same failure
// reportableBiome's `parse` and `internalError/io` categories exist to catch,
// and the answer here is the same: an error level lydite does not recognise
// counts, because the levels are open-ended and every one missed is silent.
func blocking(rep report) []string {
	var out []string
	for _, e := range rep.Errors {
		if strings.EqualFold(e.Level, "warn") || strings.EqualFold(e.Level, "warning") {
			continue
		}
		msg := e.Message
		if e.Path != "" {
			msg = e.Path + ": " + msg
		}
		out = append(out, msg)
	}
	return out
}

// parseReport reads a report, saying whether it could.
//
// A report that will not parse leaves the run entirely to Semgrep's own exit
// status and output, rather than letting a parse error decide the verdict.
func parseReport(data []byte) (report, bool) {
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return report{}, false
	}
	return rep, true
}

// reportArgs adds the report flag to a scan's own arguments.
//
// --json-output writes a *copy*: Semgrep keeps printing its own findings and
// keeps its exit status. It is the flag --json is not, which replaces the
// output entirely — and swapping one for the other would leave a developer
// with a failing row and no finding named anywhere.
func reportArgs(args []string, outPath string) []string {
	return append(append([]string(nil), args...), "--json-output="+outPath)
}

// withFindings runs the scan and reads a JSON copy of its report.
//
// --json-output writes a copy: Semgrep keeps printing its own findings to the
// terminal and keeps its exit status, so this adds Findings and changes
// nothing a developer sees. It is the flag --json is not, which replaces the
// output entirely.
// [lydite:exclude_from_coverage][the self-scan runs Semgrep over the whole
// repository on every run; a unit test here would run the machine's own rather
// than lydite's invocation, which buildArgs and reportArgs state and
// TestBuildArgs and TestReportArgsWritesACopy assert]
func withFindings(ctx context.Context, dir string, args []string) executil.Result {
	out, err := os.CreateTemp("", "lydite-semgrep-*.json")
	if err != nil {
		// Detail as well as Err: report() prints Detail under a failing row
		// and nothing else, so a bare Err renders as `✗ semgrep` with the
		// cause in neither the terminal nor --json.
		return executil.Result{Name: Gate, Err: err, Detail: err.Error()}
	}
	outPath := out.Name()
	_ = out.Close()
	defer func() { _ = os.Remove(outPath) }()

	r := executil.Run(ctx, dir, "semgrep", reportArgs(args, outPath)...)
	r.Name = Gate

	data, readErr := os.ReadFile(outPath) // #nosec G304 -- outPath is our own CreateTemp result, not user input
	if readErr != nil {
		// No report to read: leave Semgrep's own exit status and output as-is
		// rather than inventing a verdict.
		return r
	}
	rep, ok := parseReport(data)
	if !ok {
		return r
	}
	r.Findings = findings(dir, rep)
	if errs := blocking(rep); len(errs) > 0 && r.Ok() {
		r.Err = errors.New("semgrep could not scan everything it was given")
		r.Detail = strings.Join(errs, "\n")
	}
	return r
}
