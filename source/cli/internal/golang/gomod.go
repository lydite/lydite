package golang

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// goModFile is the name every Go module's manifest carries, and the file a
// dependency advisory against that module is located in.
const goModFile = "go.mod"

// goMod is a module manifest read for the lines its dependencies are named on.
//
// An advisory against a dependency is located at the manifest line naming that
// dependency, because the one edit which clears it is the bump — see ADR 0032.
// The call site the vulnerability is reached through is what the finding's
// Detail carries; editing it clears nothing.
type goMod struct {
	// modules maps a module path to its one-based line in the manifest.
	modules map[string]int
	// goDirective is the line of the `go` directive, which is where a
	// standard-library advisory is located: the edit that clears one is a
	// toolchain bump, and that is the line it is written on.
	goDirective int
}

// readGoMod reads dir's manifest, answering an empty one when it cannot.
//
// An unreadable manifest costs every advisory its line and no advisory its
// existence, which is the same trade finding.Source makes: the gate has
// already found what it found, and a claim with no line belongs in the
// standing comment rather than nowhere.
func readGoMod(dir string) *goMod {
	m := &goMod{modules: map[string]int{}}
	data, err := os.ReadFile(filepath.Join(dir, goModFile)) // #nosec G304 -- dir is a declared component's directory
	if err != nil {
		return m
	}
	// The directive a bare entry belongs to, while inside a parenthesised
	// block. It is tracked rather than guessed, because a block entry is just
	// a path and a version whichever block it is in — and an `exclude` entry
	// is indistinguishable from a `require` one by shape alone, while meaning
	// the opposite. Anchoring an advisory to the line saying a version is *not*
	// used would point an author at the one edit that cannot clear it.
	block := ""
	for n, raw := range strings.Split(string(data), "\n") {
		fields := strings.Fields(stripModComment(raw))
		if len(fields) == 0 {
			continue
		}
		if block != "" {
			if fields[0] == ")" {
				block = ""
				continue
			}
			m.record(block, fields, n+1)
			continue
		}
		directive := fields[0]
		rest := fields[1:]
		if len(rest) == 1 && rest[0] == "(" {
			block = directive
			continue
		}
		m.record(directive, rest, n+1)
	}
	return m
}

// record files one manifest entry under the directive that governs it.
//
// Only `go`, `require` and `replace` say anything about a version the build
// resolves. `module`, `exclude` and `retract` are skipped rather than
// tolerated: each names a path or a version that is not what the build uses,
// and the whole point of this lookup is the line whose edit changes what it
// does.
func (m *goMod) record(directive string, fields []string, line int) {
	switch directive {
	case "go":
		if len(fields) == 1 && m.goDirective == 0 {
			m.goDirective = line
		}
	case "require":
		if len(fields) >= 2 && strings.HasPrefix(fields[1], "v") {
			m.setModule(fields[0], line)
		}
	case "replace":
		// The right-hand side, which is the module the build actually
		// resolves and the one govulncheck names. A replace with a local path
		// on the right names no module and records nothing; there is no
		// version to bump, and the edit that clears such an advisory is to the
		// replacement's own tree.
		// i > 0, not i >= 0: a replace names the module it replaces before the
		// arrow, so a line opening with `=>` states no such module and is
		// malformed. Reading the right-hand side out of one anyway would file
		// a module under a line whose edit cannot clear an advisory against it.
		if i := slices.Index(fields, "=>"); i > 0 && i+2 < len(fields) && strings.HasPrefix(fields[i+2], "v") {
			m.setModule(fields[i+1], line)
		}
	}
}

// setModule records a module's line, keeping the first of several.
//
// A manifest naming one module twice keeps the first, which is the require the
// version comes from; a replace naming a module no require mentions records it
// at the replace, which is the only line naming it at all.
func (m *goMod) setModule(path string, line int) {
	if _, seen := m.modules[path]; !seen {
		m.modules[path] = line
	}
}

// stripModComment drops a `//` comment, so `// indirect` cannot be read as
// part of the entry it annotates.
func stripModComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		return line[:i]
	}
	return line
}

// Line is the manifest line naming module, or zero when the manifest does not
// name it.
//
// Zero is the answer for a module reached only transitively, which a manifest
// legitimately never mentions, and it is reported as zero rather than guessed
// at: after a finding became a review thread, a guessed line is a thread on
// code that has nothing to do with the advisory, on a pull request whose
// author cannot act on it.
func (m *goMod) Line(module string) int {
	if module == "stdlib" {
		return m.goDirective
	}
	return m.modules[module]
}
