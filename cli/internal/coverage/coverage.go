// Package coverage reads the coverage report a component's instrumented run
// wrote, and turns it into the two quantities every coverage gate is built
// from: the component's covered and total line counts, and its per-line hits.
//
// It measures nothing itself and executes nothing. The component declares a
// runner, internal/runner derives that runner's instrumented invocation, and
// `lydite test` runs it — so what is left here is reading the artefact that
// run named. Nothing in this package discovers a unit either: the component is
// the unit, and where its report lands is the invocation's to say.
//
// One artefact per language, and both quantities come out of it. Go's coverage
// profile serves the aggregate and the patch gate; Rust's and TypeScript's
// lcov does the same. Two artefacts per language is what produced a
// cargo-llvm-cov invocation naming two exports with one flag, which the tool
// refuses to parse.
package coverage

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/runner"
)

// LineCount is one component's tally of executable lines: how many the
// coverage report knows about, and how many of those were hit. For Go it
// counts statements rather than lines, because that is what a Go coverage
// profile records; the ratio is the same quantity either way.
//
// Counts rather than a percentage, everywhere they travel. A percentage cannot
// be re-weighted: composing a language or a global figure out of components
// needs each component's size, and so does a run that measured only some of
// them. Once a component is reduced to one number its size is gone, and the
// only aggregation left is a mean in which a 230-line component moves the
// headline as much as a 5,000-line one.
type LineCount struct {
	Covered int `json:"covered"`
	Total   int `json:"total"`
}

// Percent is the covered-over-total ratio as a percentage. A count with
// nothing to measure is never a 0% — Measured is the predicate that keeps it
// out of an aggregate and out of a floor comparison.
func (c LineCount) Percent() float64 {
	if c.Total == 0 {
		return 0
	}
	return float64(c.Covered) / float64(c.Total) * 100
}

// Measured reports whether this count came from a report with something in it.
// A component whose report lists no coverable line at all is unmeasured, never
// 0% covered: the two read identically in a percentage and mean opposite
// things.
func (c LineCount) Measured() bool { return c.Total > 0 }

// Add sums two counts. The per-language and global figures are sums over the
// same per-component entries, which is what stops the three altitudes lydite
// reports from being able to disagree.
func (c LineCount) Add(o LineCount) LineCount {
	return LineCount{Covered: c.Covered + o.Covered, Total: c.Total + o.Total}
}

// Report is one component's measurement: the counts the aggregate and floor
// gates read, and the per-line hits the patch gate reads, both out of one
// artefact.
type Report struct {
	// Lines is the component's tally.
	Lines LineCount
	// Hits maps a scan-root-relative, forward-slash path to line number to
	// hit count. Scan-root-relative because that is what git's diff paths are
	// mapped to, and the patch gate intersects the two.
	Hits LineHits
}

// Measure reads the report an instrumented run wrote for one component.
//
// root is the scan root, dir is the component's directory relative to it, and
// report is where the invocation said it would write, relative to the
// component's directory. lang selects the format, which is the only thing that
// varies: a Go profile and an lcov trace are read differently and produce the
// same two quantities.
//
// A missing or unreadable report is an error naming it, never a zero
// measurement. A component that measured nothing and one whose report lydite
// failed to find are different states, and only one of them is the
// repository's to fix — collapsing them is how a gate that could not run comes
// to read as one that passed.
func Measure(ctx context.Context, root, dir, report string, lang runner.Lang) (Report, error) {
	unitDir := filepath.Join(root, filepath.FromSlash(dir))
	reportPath := filepath.Join(unitDir, filepath.FromSlash(report))
	data, err := os.ReadFile(reportPath) // #nosec G304 -- the path is where lydite's own runner invocation was told to write, under the scan root
	if err != nil {
		return Report{}, fmt.Errorf("reading the coverage report at %s: %w", filepath.Join(dir, report), err)
	}
	switch lang {
	case runner.Go:
		return measureGo(ctx, root, unitDir, dir, reportPath)
	case runner.Rust, runner.TypeScript:
		return measureLCOV(data, unitDir, dir)
	default:
		return Report{}, fmt.Errorf("no coverage report format for %q", lang)
	}
}

// measureGo reads a Go coverage profile.
//
// The module path is needed and is not derivable from the directory: a
// profile's entries are package-qualified, and a module path need not resemble
// where the module sits — `wardnet.network/go` lives under `sdk/wardnet-go`.
// It is asked of the component's own directory, which is where a module-scoped
// go command is the only place it works.
func measureGo(ctx context.Context, root, unitDir, dir, reportPath string) (Report, error) {
	name := moduleName(ctx, unitDir)
	if name == "" {
		return Report{}, fmt.Errorf("%s is not a Go module root, so the coverage profile's package-qualified paths cannot be resolved — a go-test component's dir is the directory holding its go.mod", dir)
	}
	src := GoModuleProfile{Profile: reportPath, ModuleName: name, RelDir: relDir(dir)}
	hits, err := ParseGoProfile(src, root)
	if err != nil {
		return Report{}, fmt.Errorf("parsing the coverage profile at %s: %w", reportPath, err)
	}
	lines, err := goProfileLines(src, root)
	if err != nil {
		return Report{}, err
	}
	return Report{Lines: lines, Hits: hits}, nil
}

// measureLCOV reads an lcov trace, which is what both cargo-llvm-cov and
// Istanbul emit natively.
//
// The counts come from the LF and LH records rather than from counting the DA
// lines, and the difference is real: measured against the proving ground's
// three-crate workspace the LF/LH sums are 30 of 57, matching cargo-llvm-cov's
// own JSON export exactly, while the DA lines number 55. A line carrying more
// than one record is one line to LF and two to a DA tally, so counting DA
// silently reports a smaller denominator than the tool does.
func measureLCOV(data []byte, unitDir, dir string) (Report, error) {
	lines, hits := ParseLCOV(data, unitDir)
	return Report{Lines: lines, Hits: prefixHits(hits, relDir(dir))}, nil
}

// prefixHits puts the component's own directory back on each path, so every
// component's hits are keyed the way git names the file.
//
// Without it two components each holding a `src/index.ts` produce the same key
// and one silently overwrites the other, which is a merge that reads as a
// measurement.
func prefixHits(hits LineHits, relDir string) LineHits {
	if relDir == "" {
		return hits
	}
	out := make(LineHits, len(hits))
	for file, lines := range hits {
		out[path.Join(relDir, file)] = lines
	}
	return out
}

// relDir normalises a component directory for use as a path prefix: the scan
// root itself is the empty prefix rather than ".".
func relDir(dir string) string {
	dir = path.Clean(filepath.ToSlash(dir))
	if dir == "." {
		return ""
	}
	return dir
}

// moduleName returns the Go module path rooted at moduleDir, which is what a
// profile entry's package-qualified name is prefixed with. An answer lydite
// cannot use is reported as none rather than as a prefix that strips nothing.
//
// moduleDir must be a module root and not a directory that merely contains
// one: `go list -m` run above a module answers "command-line-arguments", which
// is not an error and is not a prefix any profile entry carries, so a caller
// that passed the wrong directory would get silent nonsense instead of a
// failure.
func moduleName(ctx context.Context, moduleDir string) string {
	// The module's own directory is asked for alongside its path, because the
	// path alone cannot say whether this directory is the module root. `go
	// list -m` run *inside* a module answers with the enclosing module — a
	// component declared at `services/api` in a repository with one go.mod at
	// its root gets the root module's path, and goRelPath then strips that
	// path and puts `services/api` back on, so every profile entry is keyed
	// `services/api/services/api/x.go`. Nothing downstream notices: the patch
	// gate finds no overlap and reports no row at all, and the generated-file
	// check stats a path that does not exist, so generated code re-enters the
	// denominator.
	r := executil.RunQuiet(ctx, moduleDir, "go", "list", "-m", "-f", "{{.Path}}\t{{.Dir}}")
	if !r.Ok() {
		return ""
	}
	out := strings.TrimSpace(r.Output)
	// A workspace prints one module per line; a non-module root prints the
	// placeholder. Neither identifies a single module to strip.
	if out == "" || strings.Contains(out, "\n") {
		return ""
	}
	name, root, ok := strings.Cut(out, "\t")
	if !ok || name == "" || name == "command-line-arguments" {
		return ""
	}
	if !sameDir(root, moduleDir) {
		return ""
	}
	return name
}

// sameDir reports whether two paths name the same directory, resolving symlinks
// so a temporary directory reached through one is not mistaken for a different
// place. A path that cannot be resolved is compared as given, which is the
// stricter answer.
func sameDir(a, b string) bool {
	resolve := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			return real
		}
		return abs
	}
	return resolve(a) == resolve(b)
}
