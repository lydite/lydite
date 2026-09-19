package golang

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// goModFile is the name every Go module's manifest carries, and the file a
// dependency advisory against that module is located in.
const goModFile = "go.mod"

// goSumFile is the file enumerating every module version the build pins,
// transitives included, which `go.mod` alone does not.
const goSumFile = "go.sum"

// goMod is a module manifest read for the lines its dependencies are named on,
// and for the versions those lines state.
//
// An advisory against a dependency is located at the manifest line naming that
// dependency, because the one edit which clears it is the bump — see ADR 0032.
// The call site the vulnerability is reached through is what the finding's
// Detail carries; editing it clears nothing.
type goMod struct {
	// modules maps a module path to its one-based line in the manifest.
	modules map[string]int
	// versions maps a module path to every version the manifest states for
	// it. It is filled from the same entries as modules and under the same
	// rule — what the build resolves, never what an `exclude` or a `retract`
	// says it does not — so the two can never disagree about which modules
	// the manifest names.
	versions map[string][]string
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
	data, err := os.ReadFile(filepath.Join(dir, goModFile)) // #nosec G304 -- dir is a declared component's directory
	if err != nil {
		return &goMod{modules: map[string]int{}, versions: map[string][]string{}}
	}
	return parseGoMod(data)
}

// parseGoMod reads a manifest's content, whichever tree it came from. The base
// side of a comparison is `git show`n rather than checked out, so the parser
// takes bytes and never a path.
func parseGoMod(data []byte) *goMod {
	m := &goMod{modules: map[string]int{}, versions: map[string][]string{}}
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
			m.addVersion(fields[0], fields[1])
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
			m.addVersion(fields[i+1], fields[i+2])
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

// addVersion records one version a manifest entry states for a module, keeping
// each distinct version once.
//
// A module legitimately carries more than one: a `require` and a `replace` of
// the same path state two, and which of them the build resolves is not a
// question the manifest's text answers. Both are kept, because a caller
// comparing two trees must be able to see that the answer is not a single
// version rather than be handed one of them.
func (m *goMod) addVersion(path, version string) {
	if !slices.Contains(m.versions[path], version) {
		m.versions[path] = append(m.versions[path], version)
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

// GoModDependencies is every module a `go.mod`'s content names a version for,
// mapped to those versions.
//
// It is the manifest's own requires and the right-hand side of its replaces —
// what the module states, which is a subset of what the build pins. A module
// reached only transitively is in go.sum and not here, so a comparison that
// must see every pinned version reads GoSumDependencies and uses this for the
// versions a `replace` states, which go.sum never records.
func GoModDependencies(content []byte) map[string][]string {
	return parseGoMod(content).versions
}

// GoSumDependencies is every module a `go.sum`'s content pins, mapped to the
// versions pinned for it, and the content hash recorded for each (module,
// version) pair.
//
// go.sum is the fuller of the two sources: it enumerates every module version
// the build resolves, transitives included, where a manifest names only what
// its own module requires. A module appears on two lines per version — the
// module zip's hash and its `go.mod`'s — and the `/go.mod` suffix is trimmed
// so both read as the one version they are; the zip's hash is what a pin's
// origin is reported as, since that is the line whose hash changes when a
// module keeps its path and version but the content behind them does not.
//
// A line that is neither blank nor a `module version hash` triple is an error
// rather than a skip: a file this cannot read whole yields a dependency set
// that is short by however much it did not understand, and a short set reports
// no addition from exactly the manifest that was unusual.
func GoSumDependencies(content []byte) (map[string][]string, map[[2]string]string, error) {
	out := map[string][]string{}
	origins := map[[2]string]string{}
	for n, raw := range strings.Split(string(content), "\n") {
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return nil, nil, fmt.Errorf("%s line %d: expected a module, a version and a hash", goSumFile, n+1)
		}
		isGoModLine := strings.HasSuffix(fields[1], "/"+goModFile)
		path, version, hash := fields[0], strings.TrimSuffix(fields[1], "/"+goModFile), fields[2]
		if !slices.Contains(out[path], version) {
			out[path] = append(out[path], version)
		}
		// The module-zip line names the content this pins the package to; the
		// go.mod line's own hash is kept only as a fallback for a module that
		// somehow carries one line and not the other, since the zip's hash is
		// what changes when a package keeps its name and version but the
		// content behind them does not.
		key := [2]string{path, version}
		if !isGoModLine || origins[key] == "" {
			origins[key] = hash
		}
	}
	return out, origins, nil
}
