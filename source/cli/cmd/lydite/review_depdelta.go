package main

import (
	"fmt"
	"io"

	"lydite/lydite/internal/reviewdecision"
	"lydite/lydite/internal/ui"
)

// gateDependencies labels each manifest's verdict in the report.
const gateDependencies = "dependencies"

// dependencyRow renders a manifest that was compared and found to add nothing.
//
// A manifest that added a package, or could not be measured, is a
// disqualification reviewdecision.Decide already returned, and reaches the
// report as one. A manifest that was compared and added nothing says so under
// its own name: a manifest missing from the report is indistinguishable from
// one nothing was measured over.
func dependencyRow(o reviewdecision.Outcome, status ui.Status) ui.Row {
	return ui.Row{
		Status: status,
		Label:  gateDependencies + "(" + o.Manifest.Path + ")",
		Value:  "no package added against " + shortSHA(o.Base),
	}
}

// dependencyGatesPassed is reviewdecision.ScanEvidence over the scan documents
// this command reads, with its warnings written to warn as they are returned.
func dependencyGatesPassed(dir string, dirs []string, warn io.Writer) bool {
	passed, warnings := reviewdecision.ScanEvidence(dir, dirs, commandScanReader{})
	for _, warning := range warnings {
		_, _ = fmt.Fprintln(warn, warning)
	}
	return passed
}

// splitGateLabel is reviewdecision.SplitGateLabel.
func splitGateLabel(label string) (gate, component string, ok bool) {
	return reviewdecision.SplitGateLabel(label)
}
