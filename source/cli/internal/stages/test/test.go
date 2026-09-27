// Package teststages holds the stages `lydite test` runs: reading the
// declaration and gating it, provisioning each component's toolchain,
// narrowing the run to what a change could have broken, running every selected
// suite under the scheduler, reporting the flaky gate, and measuring and gating
// coverage and CRAP. Named apart from internal/test/run and
// internal/test/measure, the engine and the row logic these stages compose, so
// a flow definition can import all three without renaming any of them.
//
// Every stage is a plain function of its own In. None renders a report or
// writes a document: each returns the rows its section of the report holds, in
// the order the report shows them, and the command appends them. What a stage
// has to say to the job log it writes to the Stderr its In names, as it goes,
// because a warning printed after the stage returned would land after the
// output of every tool the stage ran and read as belonging to none of them.
package teststages

import (
	"io"

	"lydite/lydite/internal/component"
	testrun "lydite/lydite/internal/test/run"
)

// kind is the label every suite row takes and the name each component's log is
// kept under: one component runs under `lydite test` and `lydite mutation`
// alike, and a log shared by the two would have whichever ran second overwrite
// the other's output.
const kind = "test"

// Logs opens the output each component of one run writes to.
//
// root is the scan root the run is under — the one this invocation tests, or
// the throwaway worktree a base tree is measured in — and stream says whether
// each log is mirrored to the terminal as well. The returned Opener is handed
// to testrun.PlanComponents; closeAll ends every log it opened, and is called
// once, after every component has finished.
//
// It is injected rather than opened here, because a log is a live resource —
// a file, and a mirror to stderr serialised against every other component's —
// that the command running the flow owns and closes, and the same stage runs
// under a command whose logs are files and a test whose logs go nowhere. A nil
// Logs gives every component an output that writes nowhere and names no path.
type Logs func(root, kind string, stream bool) (open testrun.Opener, closeAll func())

// open is l, or an Opener writing nowhere when l is nil.
func (l Logs) open(root string, stream bool) (testrun.Opener, func()) {
	if l == nil {
		return func(component.Component, int, bool) testrun.Output {
			return testrun.Output{W: io.Discard}
		}, func() {}
	}
	return l(root, kind, stream)
}

// orDiscard is w, or a writer that drops everything when w is nil.
func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// shortSHA is how a revision is named in a sentence a reader sees.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
