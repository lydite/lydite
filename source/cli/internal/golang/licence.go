package golang

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/licensecheck"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/licence"
)

// GateLicence is the licence gate as this package reports it. It is
// licence.Gate rather than a second literal, so the row a Go component carries
// and the row a Rust component carries can never drift apart.
const GateLicence = licence.Gate

// licencePrefixes are the names a licence file at a module root carries,
// matched case-insensitively by prefix. The probe module alone produced
// `LICENSE`, `License`, `LICENSE.txt` and `LICENSE.libyaml`, so an exact-name
// match reads one file of the four and calls the rest of a module's licences
// absent.
var licencePrefixes = []string{"license", "licence", "copying"}

// goListPackage is one object of `go list -deps -json ./...`.
//
// The scope is `-deps` and never `-m all`: the module graph names more modules
// than the build downloads, and a third of them answer no directory at all —
// so a classifier reading the graph calls every one of them unknown and an
// allow-list then rejects a repository over modules its build never compiles.
//
// `-test` is equally deliberately absent. A licence obligation attaches to
// what is distributed and lydite's consumers ship a binary, so a test-only
// dependency is out of scope. The cost is stated in ADR 0038: a copyleft test
// helper that reaches a shipped artefact by some other route is not caught
// here.
type goListPackage struct {
	ImportPath string        `json:"ImportPath"`
	Dir        string        `json:"Dir"`
	Standard   bool          `json:"Standard"`
	Module     *goListModule `json:"Module"`
}

// goListModule is the module a package belongs to. A standard-library package
// has none at all, which is what Standard also says and what makes the two
// checks agree.
type goListModule struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	Dir     string `json:"Dir"`
	Main    bool   `json:"Main"`
}

// goListArgv is the enumeration, as argv.
func goListArgv() []string { return []string{"list", "-deps", "-json", "./..."} }

// decodeGoList reads the whole stream.
//
// `go list -json` emits a concatenation of objects rather than an array, so it
// is decoded with a streaming decoder. A stream that stops parsing part-way
// keeps what it had read: each object is written complete, so a truncated
// stream is a command that was killed and the modules it did name are real.
func decodeGoList(r io.Reader) []goListPackage {
	dec := json.NewDecoder(r)
	var out []goListPackage
	for {
		var p goListPackage
		if err := dec.Decode(&p); err != nil {
			return out
		}
		out = append(out, p)
	}
}

// moduleDir is where a dependency module's licence files are read from.
//
// Module.Dir when the module was resolved out of the module cache. Under a
// `vendor/` tree there is no Module.Dir at all, while each package's own Dir
// points into `vendor/<module path>` — which is where `go mod vendor` copied
// the licence files. Reading Module.Dir alone classifies every dependency of
// every vendoring repository as unknown.
//
// The package directory is walked back to its module's root by the import
// path's own suffix, because a vendored subpackage's directory is
// `vendor/<module path>/<subpackage>` and the licence files sit at the module
// root above it.
func moduleDir(p goListPackage) string {
	if p.Module.Dir != "" {
		return p.Module.Dir
	}
	if p.Dir == "" {
		return ""
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, p.Module.Path), "/")
	if rel == "" {
		return p.Dir
	}
	return strings.TrimSuffix(p.Dir, string(filepath.Separator)+filepath.FromSlash(rel))
}

// classifyModule is every licence identifier the files at dir classify as.
//
// A directory that cannot be read, a file that cannot be read and a file
// nothing recognises each contribute no identifier rather than ending the
// scan: licence.Expression answers Unknown for an empty set, and an
// unclassifiable dependency is a pair the gate carries rather than one it
// drops.
func classifyModule(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ids []string
	for _, e := range entries {
		// The module root only. A licence file deeper in the tree covers the
		// subtree holding it, and reading it here would attribute one
		// vendored component's licence to the whole module.
		if e.IsDir() || !isLicenceFile(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- dir is a module root go list resolved
		if err != nil {
			continue
		}
		for _, m := range licensecheck.Scan(data).Match {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// isLicenceFile reports whether name is a licence file's name.
func isLicenceFile(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range licencePrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// licenceDependencies is every module the build compiles, deduplicated and
// ordered by module path, with the licence its files classify as.
//
// The main module is skipped: a repository's own licence is not a dependency's
// and is not this gate's to judge. Standard-library packages carry no module
// at all.
//
// A module is keyed by the path `go list` reports, which is the right-hand
// side of a `replace` — the tree the build actually compiles, and the same
// side readGoMod records a manifest line for. So the line a claim lands on and
// the licence that claim is about name one entry.
func licenceDependencies(pkgs []goListPackage) []licence.Dependency {
	ids := map[string][]string{}
	versions := map[string]string{}
	var order []string
	for _, p := range pkgs {
		if p.Standard || p.Module == nil || p.Module.Main || p.Module.Path == "" {
			continue
		}
		path := p.Module.Path
		if _, seen := versions[path]; !seen {
			order = append(order, path)
			versions[path] = p.Module.Version
			if dir := moduleDir(p); dir != "" {
				ids[path] = classifyModule(dir)
			}
		}
	}
	out := make([]licence.Dependency, 0, len(order))
	for _, path := range order {
		out = append(out, licence.Dependency{
			Package: path,
			Version: versions[path],
			// Several licence files at one root are read the way an SPDX
			// expression's OR is read: any one allowed identifier conforms.
			// gopkg.in/yaml.v2 carries Apache-2.0 in LICENSE and MIT in
			// LICENSE.libyaml, and an allow-list holding either passes it.
			Licence: licence.Expression(ids[path]),
		})
	}
	return out
}

// LicenceSet is the component's non-conforming dependencies under policy.
//
// It is only ever the non-conforming ones and never a full inventory, which is
// what makes recomputing it at the merge-base affordable.
func LicenceSet(ctx context.Context, dir string, env []string, policy licence.Policy) (licence.Set, error) {
	r := executil.RunQuietEnv(ctx, dir, env, "go", goListArgv()...)
	if !r.Ok() {
		// The tool's own first line, because the reason reaches an
		// `unmeasured` row and has to name what failed — a module that would
		// not download, a toolchain that would not resolve — rather than
		// restate that something did.
		return licence.Set{}, fmt.Errorf("go list -deps in %s: %w%s", dir, r.Err, firstLine(r.Stderr))
	}
	return policy.Reject(licenceDependencies(decodeGoList(strings.NewReader(r.Output)))), nil
}

// firstLine is the leading line of a tool's diagnostics, ready to append to an
// error. Empty stays empty, so an error over a silent failure does not end in
// a dangling separator.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return ": " + s
}

// LicenceFindings is each introduced pair as a claim on the manifest line
// naming its module.
//
// The require line, because the one edit that clears the claim is to the
// dependency — dropping it, or moving to a release under a licence the policy
// allows. A module reached only transitively is named by no line and reports
// zero rather than a guess, for the reason readGoMod's own Line states: a
// guessed line is a review thread on code that has nothing to do with the
// claim.
//
// The site is the pair rather than the text of that line, the departure ADR
// 0032 already makes for a dependency advisory and for the same reason: a
// module offering two non-conforming licences is two claims against one line,
// and a site read from the line would make them one and drop the second.
func LicenceFindings(dir string, pairs []licence.Dependency) []finding.Finding {
	mod := readGoMod(dir)
	out := make([]finding.Finding, 0, len(pairs))
	for _, d := range pairs {
		out = append(out, finding.Finding{
			Gate:     GateLicence,
			Path:     goModFile,
			Line:     mod.Line(d.Package),
			Rule:     d.Licence,
			Severity: "licence",
			Message:  licenceMessage(d),
			Site:     d.Key().Site(),
		})
	}
	finding.Number(out)
	return out
}

// licenceMessage is the one-line claim: which dependency, at which version,
// under which licence.
func licenceMessage(d licence.Dependency) string {
	msg := d.Package
	if d.Version != "" {
		msg += " " + d.Version
	}
	return msg + " is " + d.Licence + ", which the licence policy does not allow"
}

// LicenceCheck is the gate's answer for one Go component: the non-conforming
// set the build compiles, compared against the set recomputed at the
// merge-base, with the introduced pairs as located claims.
//
// Findings accompany a failing verdict alone. Every other verdict gates
// nothing — no policy stated, no diff base given, a base that could not be
// built — and a claim published under one would ask an author to answer for a
// dependency the gate has not decided anything about.
//
// The current set is read even where the base could not be built, because
// Compare reports it as the context a reader needs to see what the gate would
// have compared.
func LicenceCheck(ctx context.Context, dir string, env []string, policy licence.Policy, base licence.Base) (licence.Comparison, []finding.Finding, error) {
	if !policy.Configured() {
		// Nothing is enumerated for a repository that stated no policy:
		// `go list -deps` downloads every module the build compiles, and
		// charging that to a repository whose row is going to say `not
		// configured` is a cost for an answer already known.
		return licence.Comparison{Verdict: licence.VerdictNotConfigured}, nil, nil
	}
	current, err := LicenceSet(ctx, dir, env, policy)
	if err != nil {
		return licence.Comparison{}, nil, err
	}
	comparison := licence.Compare(policy, current, base)
	if comparison.Verdict != licence.VerdictFail {
		return comparison, nil, nil
	}
	return comparison, LicenceFindings(dir, comparison.Pairs), nil
}
