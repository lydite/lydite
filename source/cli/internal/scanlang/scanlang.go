// Package scanlang is the one list of languages lydite has scanners for.
//
// Two readers ask it the same question and must get the same answer: `lydite
// scan`, deciding whether a component's language gets checks or unmeasured
// rows, and internal/orphan, deciding whether a file nobody scans is a gap a
// declaration could close. A second copy of the list in either would agree with
// this one only until a language gained a scanner in one of them.
//
// It is a bare enumeration of language names and imports no scanner, so a
// package that only needs the answer does not pull in the checks themselves.
package scanlang

import "lydite/lydite/internal/runner"

// Scanned reports whether lydite has checks for a language at all, which is a
// property of lydite rather than of the repository — a language switched off in
// .lydite/config.yml has scanners and is not being asked to run them.
//
// Enumerated rather than derived from runner.Runs. Having a runner and having a
// scanner are independent: a language gains one without the other — Python has
// a runner and no scanner, and shell has a scanner and no runner — so reading
// either off the other would scan a language nothing checks, or skip one
// something does.
func Scanned(l runner.Lang) bool {
	switch l {
	case runner.Go, runner.Rust, runner.TypeScript, runner.Shell:
		return true
	default:
		return false
	}
}
