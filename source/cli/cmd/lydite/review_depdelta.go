package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/reviewdecision"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/ui"
)

// gateDependencies labels each manifest's verdict in the report.
const gateDependencies = "dependencies"

// addDependencyRows renders each manifest that was compared and found to add
// nothing.
//
// A manifest that added a package, or could not be measured, is already a
// disqualification in the decision reviewdecision.Decide returned, and reaches
// the report through it. A manifest that was compared and added nothing says
// so under its own name: a manifest missing from the report is
// indistinguishable from one nothing was measured over.
func addDependencyRows(report *ui.Report, deltas []reviewdecision.ManifestDelta, base string) {
	for _, m := range deltas {
		if m.Unmeasured != "" || len(m.Delta.Added) > 0 {
			continue
		}
		report.Add(ui.Row{
			Status: ui.StatusPass,
			Label:  gateDependencies + "(" + m.Path + ")",
			Value:  "no package added against " + shortSHA(base),
		})
	}
}

// scaGateForLang names the advisory check a declared component's language
// runs. A language absent from this map, TypeScript among them, runs no
// advisory check at all, so its dependency evidence is its licence row
// alone — requiring one would make the condition unsatisfiable for every
// repository that has a component in that language.
//
// Keyed by declaration rather than by a gate in the document, so a component
// whose language was switched off in .lydite/config.yml — and which therefore
// carries no row of any kind — is still known to need one: nothing in the
// document could have told this apart from a component that never existed.
var scaGateForLang = map[runner.Lang]string{
	runner.Go:   golang.GateGovulncheck,
	runner.Rust: rust.GateAudit,
}

// scaGateFor is the same pairing, keyed by a gate the document itself
// reports rather than by a declaration in .lydite/components.yml.
//
// A component the document shows running `gosec` for is a Go component
// whether or not it is declared, and a component with no declaration behind
// it at all — every fixture in this repository's own tests, and any caller
// that runs review with no components.yml — still has to hold to the rule:
// a `govulncheck` row missing from beside a `gosec` one is the gap this
// closes where scaGateForLang has no declaration to read a language from.
var scaGateFor = map[string]string{
	golang.GateGosec: golang.GateGovulncheck,
	rust.GateClippy:  rust.GateAudit,
}

// dependencyGatesPassed reports whether the scan documents in the given report
// directories show the licence gate and the advisory check passing for every
// declared component.
//
// Evidence about advisories is evidence about advisories and nothing else: the
// bump that introduces a copyleft dependency is precisely a lockfile-only
// change with a clean SCA run, which is why the licence gate has to have run
// and passed rather than merely not failed. A component whose licence row is
// missing, unmeasured, not configured or failing takes the whole condition
// with it — and so does a component the document carries no row for at all,
// which is what a language switched off in .lydite/config.yml, or a scan that
// did not run, looks like. Checking only the components a document happens to
// name would let either read as clean.
//
// No directory, no scan document in the ones given, or a directory that will
// not be read are all false. The flag can only ever supply evidence, so the
// absence of evidence is the absence of the exemption, which is the verdict a
// run without it already reaches.
func dependencyGatesPassed(dir string, dirs []string, warn io.Writer) bool {
	file, err := component.Load(dir)
	if err != nil {
		_, _ = fmt.Fprintf(warn, "warning: no licence or SCA evidence: %v\n", err)
		return false
	}

	// component -> gate -> status.
	byComponent := map[string]map[string]ui.Status{}
	read := false
	for _, d := range dirs {
		doc, err := readDocument(documentPath(d, "scan"))
		switch {
		case err == nil:
		// A directory holding no scan document is an ordinary shape — a job
		// that ran the suite and not the scan writes one of the others — and
		// says nothing a reader must act on. A document that is there and
		// will not parse is the opposite.
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			_, _ = fmt.Fprintf(warn, "warning: no licence or SCA evidence came from %s: %v\n", d, err)
			continue
		}
		read = true
		for _, row := range doc.Rows {
			gate, name, ok := splitGateLabel(row.Label)
			if !ok {
				continue
			}
			gates := byComponent[name]
			if gates == nil {
				gates = map[string]ui.Status{}
				byComponent[name] = gates
			}
			// Two documents naming one component keep the worse of the two
			// answers: a gate that passed in one job and failed in another
			// failed.
			if prev, seen := gates[gate]; !seen || prev == ui.StatusPass {
				gates[gate] = row.Status
			}
		}
	}
	if !read {
		return false
	}

	// The names to check are the union of what was declared and what the
	// document named: a declared component absent from the document entirely
	// — the disabled-language case — has to be checked against a nil gate
	// map rather than skipped, and a component the document names without
	// any declaration behind it (every fixture in this repository's own
	// tests, and any caller that runs review with no .lydite/components.yml
	// at all) still has to hold to the rule.
	declaredLang := map[string]runner.Lang{}
	names := map[string]bool{}
	for _, c := range file.Components {
		declaredLang[c.Name] = c.Lang()
		names[c.Name] = true
	}
	for name := range byComponent {
		names[name] = true
	}
	if len(names) == 0 {
		return false
	}
	for name := range names {
		gates := byComponent[name]
		if gates[licence.Gate] != ui.StatusPass {
			return false
		}
		if sca, ok := scaGateForLang[declaredLang[name]]; ok && gates[sca] != ui.StatusPass {
			return false
		}
		for gate := range gates {
			if sca, ok := scaGateFor[gate]; ok && gates[sca] != ui.StatusPass {
				return false
			}
		}
	}
	return true
}

// splitGateLabel takes a row label apart into the gate and the component it
// ran for — `licence(cli)` is the licence gate over the component named cli.
//
// A row that carries no component, like the scan's own summary, is not one
// this attributes: a gate with no component behind it answers for nothing in
// particular, and folding it under an empty name would invent a component the
// declaration never had.
func splitGateLabel(label string) (gate, component string, ok bool) {
	open := strings.LastIndex(label, "(")
	if open <= 0 || !strings.HasSuffix(label, ")") {
		return "", "", false
	}
	component = label[open+1 : len(label)-1]
	if component == "" {
		return "", "", false
	}
	return label[:open], component, true
}
