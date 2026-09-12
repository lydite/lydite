package golang

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// govulncheckMessage is one object in govulncheck's JSON stream.
//
// The stream is a concatenation of single-key objects rather than an array or
// NDJSON, so it is decoded with a streaming decoder and every key lydite does
// not read is left alone. Only `finding` and `osv` are read: `config`, `SBOM`
// and `progress` say how the run went, which the text pass already reported.
type govulncheckMessage struct {
	OSV     *govulncheckOSV     `json:"osv,omitempty"`
	Finding *govulncheckFinding `json:"finding,omitempty"`
}

// govulncheckOSV is the advisory record, which carries the prose a finding
// message only references by id.
type govulncheckOSV struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
	Details string `json:"details"`
}

type govulncheckFinding struct {
	OSV          string             `json:"osv"`
	FixedVersion string             `json:"fixed_version"`
	Trace        []govulncheckFrame `json:"trace"`
}

// govulncheckFrame is one step of the path from the scanned module to the
// vulnerable symbol. The last frame is the scanned module's own code.
type govulncheckFrame struct {
	Module   string `json:"module"`
	Version  string `json:"version"`
	Package  string `json:"package"`
	Function string `json:"function"`
	Receiver string `json:"receiver"`
	Position *struct {
		Filename string `json:"filename"`
		Line     int    `json:"line"`
		Column   int    `json:"column"`
	} `json:"position"`
}

// govulncheckFindings is one claim per advisory, located at the manifest line
// naming the vulnerable module.
//
// govulncheck emits several `finding` messages for one advisory, at increasing
// trace depth — a module, then a package, then the call path that reaches the
// vulnerable symbol — and they are one claim about one dependency, cleared by
// one bump. Reporting each message would triple-count the advisory and put
// three review threads on the manifest line. The richest trace is the one
// kept, because it is the only one that says how the code actually reaches the
// vulnerability, and that is what a reader needs in order to judge it.
//
// **The collapse is keyed on the advisory *and* the module**, not on the
// advisory alone. One record routinely names two — the standard library and
// the `golang.org/x/...` module the same code is vendored from, which is 18 of
// the advisories in this package's own fixture — and when both are in the
// build they are two claims cleared by two different bumps, on two different
// manifest lines, at two different fixed versions. Keyed on the id alone one
// of them never reaches the document and the survivor names the other's
// module. It is the pair `Site` already identifies a claim by, so the two
// fingerprints differ and both are meant to survive.
//
// A standard-library advisory arrives with a depth-one trace naming `stdlib`,
// and is located at the `go` directive rather than at a require: the edit that
// clears it is a toolchain bump.
func govulncheckFindings(dir string, messages []govulncheckMessage) []finding.Finding {
	mod := readGoMod(dir)
	summaries := map[string]*govulncheckOSV{}
	richest := map[[2]string]*govulncheckFinding{}
	var order [][2]string
	for i := range messages {
		if osv := messages[i].OSV; osv != nil && osv.ID != "" {
			summaries[osv.ID] = osv
		}
		f := messages[i].Finding
		if f == nil || f.OSV == "" {
			continue
		}
		key := [2]string{f.OSV, vulnerableModule(f)}
		if kept, seen := richest[key]; !seen {
			order = append(order, key)
			richest[key] = f
		} else if len(f.Trace) > len(kept.Trace) {
			richest[key] = f
		}
	}

	out := make([]finding.Finding, 0, len(order))
	for _, key := range order {
		id, module := key[0], key[1]
		f := richest[key]
		out = append(out, finding.Finding{
			Gate:     GateGovulncheck,
			Path:     goModFile,
			Line:     mod.Line(module),
			Rule:     id,
			Severity: "vulnerability",
			Message:  govulncheckMessageText(id, module, f, summaries[id]),
			Detail:   govulncheckTrace(f),
			// The advisory with the module it is against, and not the text of
			// the manifest line: a manifest line names a module and cannot
			// tell two advisories against one module apart. See ADR 0032.
			Site: id + "\x1f" + module,
		})
	}
	finding.Number(out) // [lydite:exclude_from_mutation][the collapse above is keyed on the advisory and the module, which is exactly what a site is made of, so no two findings here share one and every ordinal is already zero; the call stays because numbering is the contract every parser keeps and the collapse's key is free to change]
	return out
}

// vulnerableModule is the module an advisory is against.
//
// The trace's first frame is the vulnerable module itself; later frames walk
// back towards the scanned code. A finding with no trace at all is the
// standard library, which is how govulncheck reports a toolchain advisory it
// traced no call into.
func vulnerableModule(f *govulncheckFinding) string {
	if len(f.Trace) > 0 {
		return f.Trace[0].Module
	}
	return "stdlib"
}

// govulncheckMessageText is the one-line claim: what is wrong, in which
// module, and what clears it.
func govulncheckMessageText(id, module string, f *govulncheckFinding, osv *govulncheckOSV) string {
	summary := id
	if osv != nil && osv.Summary != "" {
		summary = osv.Summary
	}
	msg := summary + " in " + module
	if len(f.Trace) > 0 && f.Trace[0].Version != "" {
		msg += " " + f.Trace[0].Version
	}
	if f.FixedVersion != "" {
		msg += " (fixed in " + f.FixedVersion + ")"
	}
	return msg
}

// govulncheckTrace is the call path as the lines a reader sees, from the
// scanned module's own code inwards to the vulnerable symbol.
//
// The order is reversed from the report's, which starts at the vulnerability.
// A reader is looking for the line in their own code to start from, and that
// is the trace's last frame.
func govulncheckTrace(f *govulncheckFinding) []string {
	out := make([]string, 0, len(f.Trace))
	// slices.Backward rather than a counting loop. A mutant that drops the
	// decrement from `for i := len(x) - 1; i >= 0; i--` turns an appending
	// loop unbounded, and the runner dies of memory rather than of its
	// timeout — which is lydite/lydite#109, and is reported as an interrupted
	// job rather than as a survivor. A range has no counter to drop.
	for _, frame := range slices.Backward(f.Trace) {
		symbol := frame.Package
		if frame.Function != "" {
			symbol = strings.TrimPrefix(frame.Package+"."+frame.Receiver+"."+frame.Function, ".")
			symbol = strings.ReplaceAll(symbol, "..", ".")
		}
		if symbol == "" {
			// A frame naming only a module says the module is in the build
			// and no call into it was traced, which is the whole of what a
			// depth-one finding reports.
			symbol = frame.Module
			if frame.Version != "" {
				symbol += " " + frame.Version
			}
		}
		if frame.Position != nil {
			symbol += fmt.Sprintf(" (%s:%d)", frame.Position.Filename, frame.Position.Line)
		}
		out = append(out, symbol)
	}
	return out
}

// decodeGovulncheck reads the whole JSON stream.
//
// A stream that stops parsing part-way keeps what it had read: govulncheck
// writes each message complete, so a truncated stream is a run that was
// killed, and the advisories it did report are true.
func decodeGovulncheck(r io.Reader) []govulncheckMessage {
	dec := json.NewDecoder(r)
	var out []govulncheckMessage
	for {
		var m govulncheckMessage
		if err := dec.Decode(&m); err != nil {
			return out
		}
		out = append(out, m)
	}
}

// govulncheckArgv is one of the two passes, as argv.
//
// The text pass carries no format flag and is the one that decides the row,
// because under `-format json` govulncheck exits 0 whether or not it found
// anything while the text run exits 3. Adding a flag to the first pass, or
// dropping the second, silently turns the gate off.
func govulncheckArgv(asJSON bool) []string {
	if asJSON {
		return []string{"-format", "json", "./..."}
	}
	return []string{"./..."}
}

// runGovulncheck runs govulncheck twice: once for the terminal and the
// verdict, once for the data.
//
// The second run is not a choice about rendering. govulncheck has no
// output-file flag and no way to emit both formats at once, and under
// -format json it exits 0 whether or not it found anything — so a single JSON
// run would report the advisories and silently pass the check that exists to
// block on them. The text run is therefore the one that decides the row, and
// the JSON run only populates Findings.
//
// It is cheap because it is second: the package-load work is already in the
// build cache, measured at 4.6s on top of a 6.2s first pass over lydite's own
// module.
// [lydite:exclude_from_coverage][the self-scan runs govulncheck over
// source/cli on every run; a unit test here would run the machine's own rather
// than lydite's invocation, which govulncheckArgv states and
// TestGovulncheckArgvKeepsTheVerdictOnTheTextPass asserts]
func runGovulncheck(ctx context.Context, dir string, env []string, bin string) executil.Result {
	r := executil.RunEnv(ctx, dir, env, bin, govulncheckArgv(false)...)
	r.Name = GateGovulncheck

	// RunQuiet, because this pass is data: streaming it would print the whole
	// report a second time under the one the developer just read.
	data := executil.RunQuietEnv(ctx, dir, env, bin, govulncheckArgv(true)...)
	return govulncheckResult(r, dir, data.Output)
}

// govulncheckResult is the text pass's result with the data pass's stream read
// into it.
//
// Split from the invocation so it can be tested against a captured stream:
// which pass decides the row is the decision here, and a test that had to run
// govulncheck twice to reach it would be testing the machine's govulncheck.
func govulncheckResult(r executil.Result, dir, stream string) executil.Result {
	if stream == "" {
		// Nothing to parse: leave the text pass's verdict and output as-is
		// rather than inventing one.
		return r
	}
	r.Findings = govulncheckFindings(dir, decodeGovulncheck(strings.NewReader(stream)))
	return r
}
