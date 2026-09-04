// Package pins keeps lydite's tool pins agreeing with the places that state
// them a second time.
//
// Every pinned tool's version lives in a package-manager manifest, so that
// Dependabot can see it and open a bump rather than leaving the pin frozen in
// a constant nothing ever ages out (ADR 0006). Two of those versions are also
// stated where Dependabot cannot reach:
//
//   - internal/golang's constants. gosecPkg and govulncheckPkg concatenate the
//     version at compile time, and go:embed cannot read a file inside a nested
//     module, so go-pin/go.mod is unreadable at run time.
//   - internal/typescript's biome.json $schema URL. Biome never fetches it —
//     it is what an editor validates that file against, so a stale one makes
//     every local edit check against the wrong schema.
//
// A bump therefore arrives with the manifest changed and the mirror stale.
// Drift returns exactly that, so one test can fail a bump that did not carry
// its mirror and `go run ./tools/pinsync` can write it.
//
// The manifest is the source of truth in both directions. Nothing here ever
// edits a manifest: Dependabot owns those, and a tool that wrote one would be
// deciding a version rather than propagating it.
package pins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A Mirror is one version stated twice: in Pin, which Dependabot edits, and in
// File, which it cannot reach.
type Mirror struct {
	// Name is what a message calls this pin — the tool, not the file.
	Name string
	// Pin is the manifest holding the version, relative to the module root.
	Pin string
	// File is where the same version is stated again.
	File string

	pinned func(manifest []byte) (string, error)
	stated func(file []byte) (string, error)
	write  func(file []byte, version string) []byte
}

// Drift is a mirror that does not state its pin.
type Drift struct {
	Mirror
	Pinned string
	Stated string
}

func (d Drift) String() string {
	return fmt.Sprintf("%s: %s states %s but %s pins %s",
		d.Name, d.File, quote(d.Stated), d.Pin, quote(d.Pinned))
}

func quote(s string) string {
	if s == "" {
		return "nothing"
	}
	return `"` + s + `"`
}

// Mirrors is every version lydite states twice.
//
// The list is the whole of what pinsync knows. A pin added without an entry
// here is one nothing propagates — which is a mirror that goes stale silently,
// so a new pin that is read at run time from its own manifest belongs to no
// entry and needs none.
func Mirrors() []Mirror {
	return []Mirror{
		goPin("gosec", "github.com/securego/gosec/v2", "gosecVersion"),
		goPin("govulncheck", "golang.org/x/vuln", "govulncheckVersion"),
		{
			Name:   "biome",
			Pin:    filepath.Join("internal", "typescript", "biome-pin", "package.json"),
			File:   filepath.Join("internal", "typescript", "biome.json"),
			pinned: npmDependency("@biomejs/biome"),
			stated: schemaVersion,
			write:  writeSchemaVersion,
		},
	}
}

// Files is every path the mirrors read or write, module-root-relative and
// each named once. A caller that has to assemble the tree Check or Write runs
// against — one fetching a pull request's files rather than checking it out —
// gets the list from here instead of restating it.
func Files() []string {
	var files []string
	for _, m := range Mirrors() {
		for _, path := range []string{m.Pin, m.File} {
			if !slices.Contains(files, path) {
				files = append(files, path)
			}
		}
	}
	return files
}

func goPin(name, module, constant string) Mirror {
	return Mirror{
		Name:   name,
		Pin:    filepath.Join("internal", "golang", "go-pin", "go.mod"),
		File:   filepath.Join("internal", "golang", "golang.go"),
		pinned: func(manifest []byte) (string, error) { return moduleVersion(manifest, module) },
		stated: func(file []byte) (string, error) { return goConst(file, constant) },
		write:  func(file []byte, version string) []byte { return writeGoConst(file, constant, version) },
	}
}

// Check reports every mirror in root that does not state its pin. An empty
// result means every mirror agrees.
func Check(root string) ([]Drift, error) {
	var drifted []Drift
	for _, m := range Mirrors() {
		pinned, stated, err := m.read(root)
		if err != nil {
			return nil, err
		}
		if pinned != stated {
			drifted = append(drifted, Drift{Mirror: m, Pinned: pinned, Stated: stated})
		}
	}
	return drifted, nil
}

// Write makes every mirror in root state its pin, and returns the drift it
// resolved. A mirror already in agreement is not rewritten, so a run that
// changes nothing leaves every file's mtime alone.
func Write(root string) ([]Drift, error) {
	drifted, err := Check(root)
	if err != nil {
		return nil, err
	}
	for _, d := range drifted {
		path := filepath.Join(root, d.File)
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		content, err := os.ReadFile(path) // #nosec G304 -- a path from Mirrors(), which is a fixed list
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, d.write(content, d.Pinned), info.Mode().Perm()); err != nil {
			return nil, err
		}
	}
	return drifted, nil
}

func (m Mirror) read(root string) (pinned, stated string, err error) {
	manifest, err := os.ReadFile(filepath.Join(root, m.Pin)) // #nosec G304 -- a path from Mirrors(), which is a fixed list
	if err != nil {
		return "", "", err
	}
	file, err := os.ReadFile(filepath.Join(root, m.File)) // #nosec G304 -- a path from Mirrors(), which is a fixed list
	if err != nil {
		return "", "", err
	}
	if pinned, err = m.pinned(manifest); err != nil {
		return "", "", fmt.Errorf("%s: %w", m.Pin, err)
	}
	if stated, err = m.stated(file); err != nil {
		return "", "", fmt.Errorf("%s: %w", m.File, err)
	}
	return pinned, stated, nil
}

// moduleVersion finds the version a go.mod requires a module at. It matches the
// module path exactly, so that golang.org/x/vuln is never satisfied by a line
// for golang.org/x/vulndb.
func moduleVersion(gomod []byte, module string) (string, error) {
	for line := range strings.Lines(string(gomod)) {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == module {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("no require entry for %s", module)
}

// npmDependency reads a version out of a package.json's dependencies. The pin
// manifests declare an exact version rather than a range, which is what makes
// the string usable as a schema URL segment unchanged.
func npmDependency(name string) func([]byte) (string, error) {
	return func(manifest []byte) (string, error) {
		var pkg struct {
			Dependencies map[string]string `json:"dependencies"`
		}
		if err := json.Unmarshal(manifest, &pkg); err != nil {
			return "", err
		}
		version := pkg.Dependencies[name]
		if version == "" {
			return "", fmt.Errorf("no dependency %s", name)
		}
		return version, nil
	}
}

// goConstPattern matches a Go constant declared with a quoted string, capturing
// the value. Anchored to the name so that gosecVersion is never read off
// gosecPkg, which mentions it.
func goConstPattern(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(name) + `\s*=\s*")([^"]*)(")`)
}

func goConst(file []byte, name string) (string, error) {
	m := goConstPattern(name).FindSubmatch(file)
	if m == nil {
		return "", fmt.Errorf("no constant %s", name)
	}
	return string(m[2]), nil
}

func writeGoConst(file []byte, name, version string) []byte {
	return goConstPattern(name).ReplaceAll(file, []byte("${1}"+version+"${3}"))
}

// schemaURL matches the version segment of a Biome schema URL.
var schemaURL = regexp.MustCompile(`(https://biomejs\.dev/schemas/)([^/]+)(/schema\.json)`)

func schemaVersion(file []byte) (string, error) {
	m := schemaURL.FindSubmatch(file)
	if m == nil {
		return "", fmt.Errorf("no biomejs.dev schema URL")
	}
	return string(m[2]), nil
}

func writeSchemaVersion(file []byte, version string) []byte {
	return schemaURL.ReplaceAll(file, []byte("${1}"+version+"${3}"))
}
