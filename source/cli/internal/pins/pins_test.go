package pins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moduleRoot is where the manifests and their mirrors live, from this
// package's own directory.
const moduleRoot = "../.."

// TestNoDrift is the guard a Dependabot bump trips.
//
// Dependabot edits a manifest and nothing else, so a bump lands with the
// mirror still stating the old version — installing a tool lydite no longer
// pins, or validating a config against a schema that is not the pinned
// engine's. This is what fails such a change; `go run ./tools/pinsync` is what
// fixes it, and the message says so.
func TestNoDrift(t *testing.T) {
	drifted, err := Check(moduleRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range drifted {
		t.Errorf("%s\nRun `go run ./tools/pinsync` to write the pin into its mirror.", d)
	}
}

// A mirror naming a file that is not there is a pin nothing propagates, and
// the failure is silent: Check reads both files, so a renamed manifest would
// surface as a read error somewhere far from the entry that is wrong.
func TestEveryMirrorNamesFilesThatExist(t *testing.T) {
	for _, m := range Mirrors() {
		for _, path := range []string{m.Pin, m.File} {
			if _, err := os.Stat(filepath.Join(moduleRoot, path)); err != nil {
				t.Errorf("%s: %v", m.Name, err)
			}
		}
	}
}

func TestModuleVersionMatchesThePathExactly(t *testing.T) {
	gomod := []byte("require (\n\tgolang.org/x/vulndb v0.0.1\n\tgolang.org/x/vuln v1.7.0\n)\n")
	got, err := moduleVersion(gomod, "golang.org/x/vuln")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.7.0" {
		t.Errorf("moduleVersion = %q, want v1.7.0 — a prefix match would take vulndb's", got)
	}
	if _, err := moduleVersion(gomod, "golang.org/x/absent"); err == nil {
		t.Error("moduleVersion of an absent module returned no error, so a renamed require would read as no drift")
	}
}

// gosecPkg mentions gosecVersion, so a pattern that is not anchored to the
// name reads the version off the wrong line and writes it there too.
func TestGoConstReadsTheDeclarationAndNotItsUse(t *testing.T) {
	src := []byte("const (\n\tgosecVersion = \"v2.29.0\"\n\n\tgosecPkg = \"github.com/securego/gosec/v2/cmd/gosec@\" + gosecVersion\n)\n")
	got, err := goConst(src, "gosecVersion")
	if err != nil {
		t.Fatal(err)
	}
	if got != "v2.29.0" {
		t.Fatalf("goConst = %q, want v2.29.0", got)
	}
	out := string(writeGoConst(src, "gosecVersion", "v2.30.0"))
	if !strings.Contains(out, `gosecVersion = "v2.30.0"`) {
		t.Errorf("writeGoConst did not rewrite the declaration:\n%s", out)
	}
	if !strings.Contains(out, `"github.com/securego/gosec/v2/cmd/gosec@" + gosecVersion`) {
		t.Errorf("writeGoConst rewrote the package expression:\n%s", out)
	}
}

func TestSchemaVersionRoundTrips(t *testing.T) {
	cfg := []byte(`{"$schema": "https://biomejs.dev/schemas/2.5.10/schema.json"}`)
	got, err := schemaVersion(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2.5.10" {
		t.Fatalf("schemaVersion = %q, want 2.5.10", got)
	}
	out := string(writeSchemaVersion(cfg, "2.5.11"))
	if !strings.Contains(out, "schemas/2.5.11/schema.json") {
		t.Errorf("writeSchemaVersion = %s", out)
	}
}

// Write must leave a mirror that already agrees untouched. Rewriting every
// file on every run would put an unrelated diff in a bump's pull request.
func TestWriteTouchesOnlyWhatDrifted(t *testing.T) {
	root := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/golang/go-pin/go.mod", "require (\n\tgithub.com/securego/gosec/v2 v2.29.0\n\tgolang.org/x/vuln v1.8.0\n)\n")
	write("internal/golang/golang.go", "const (\n\tgosecVersion = \"v2.29.0\"\n\tgovulncheckVersion = \"v1.7.0\"\n)\n")
	write("internal/typescript/biome-pin/package.json", `{"dependencies":{"@biomejs/biome":"2.5.10"}}`)
	write("internal/typescript/biome.json", `{"$schema": "https://biomejs.dev/schemas/2.5.10/schema.json"}`)

	before, err := os.Stat(filepath.Join(root, "internal/typescript/biome.json"))
	if err != nil {
		t.Fatal(err)
	}
	drifted, err := Write(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(drifted) != 1 || drifted[0].Name != "govulncheck" {
		t.Fatalf("Write resolved %v, want govulncheck alone", drifted)
	}
	after, err := os.Stat(filepath.Join(root, "internal/typescript/biome.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("Write rewrote biome.json, which already stated its pin")
	}
	got, err := os.ReadFile(filepath.Join(root, "internal/golang/golang.go")) // #nosec G304 -- a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `govulncheckVersion = "v1.8.0"`) {
		t.Errorf("golang.go = %s", got)
	}
	if !strings.Contains(string(got), `gosecVersion = "v2.29.0"`) {
		t.Errorf("Write changed a constant that already agreed:\n%s", got)
	}
}
