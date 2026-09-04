package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/pins"
)

// tree writes a module root whose mirrors disagree with their manifests by the
// versions given, so a test can name the drift it wants rather than restate
// four files.
func tree(t *testing.T, statedGo, pinnedGo, statedBiome, pinnedBiome string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"internal/golang/go-pin/go.mod":              "require (\n\tgithub.com/securego/gosec/v2 v2.29.0\n\tgolang.org/x/vuln " + pinnedGo + "\n)\n",
		"internal/golang/golang.go":                  "const (\n\tgosecVersion = \"v2.29.0\"\n\tgovulncheckVersion = \"" + statedGo + "\"\n)\n",
		"internal/typescript/biome-pin/package.json": `{"dependencies":{"@biomejs/biome":"` + pinnedBiome + `"}}`,
		"internal/typescript/biome.json":             `{"$schema": "https://biomejs.dev/schemas/` + statedBiome + `/schema.json"}`,
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The exit code is the whole of what CI reads. A -check that found drift and
// exited 0 is a bump that merges with a mirror nothing propagated.
func TestCheckExitsNonZeroOnDriftAndZeroWithout(t *testing.T) {
	var out, errs strings.Builder
	drifted := tree(t, "v1.6.0", "v1.7.0", "2.5.10", "2.5.10")
	if code := run([]string{"-check", "-root", drifted}, &out, &errs); code != 1 {
		t.Errorf("exit %d on drift, want 1", code)
	}
	if !strings.Contains(errs.String(), "go run ./tools/pinsync") {
		t.Errorf("the failure does not name the fix:\n%s", errs.String())
	}

	out.Reset()
	errs.Reset()
	agreed := tree(t, "v1.7.0", "v1.7.0", "2.5.10", "2.5.10")
	if code := run([]string{"-check", "-root", agreed}, &out, &errs); code != 0 {
		t.Errorf("exit %d with no drift, want 0", code)
	}
}

// -check must never write. A CI job that repaired the tree it was auditing
// would pass on a change that is still wrong once it lands.
func TestCheckWritesNothing(t *testing.T) {
	root := tree(t, "v1.6.0", "v1.7.0", "2.5.10", "2.5.10")
	path := filepath.Join(root, "internal/golang/golang.go")
	before, err := os.ReadFile(path) // #nosec G304 -- a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	var out, errs strings.Builder
	run([]string{"-check", "-root", root}, &out, &errs)
	after, err := os.ReadFile(path) // #nosec G304 -- a path this test wrote
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("-check rewrote the file it was auditing:\n%s", after)
	}
}

func TestWriteResolvesTheDriftAndSaysSo(t *testing.T) {
	root := tree(t, "v1.6.0", "v1.7.0", "2.5.10", "2.5.11")
	var out, errs strings.Builder
	if code := run([]string{"-root", root}, &out, &errs); code != 0 {
		t.Fatalf("exit %d, want 0\n%s", code, errs.String())
	}
	if !strings.Contains(errs.String(), "wrote 2 mirror(s)") {
		t.Errorf("the run does not say what it wrote:\n%s", errs.String())
	}
	if code := run([]string{"-check", "-root", root}, &out, &errs); code != 0 {
		t.Errorf("drift survived the write: exit %d\n%s", code, errs.String())
	}
}

// The workflow fetches exactly these paths from a pull request, so a mirror
// added to internal/pins has to appear here without an edit to the workflow.
func TestFilesListsEveryPathAMirrorTouches(t *testing.T) {
	var out, errs strings.Builder
	if code := run([]string{"-files"}, &out, &errs); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	listed := strings.Fields(out.String())
	for _, m := range pins.Mirrors() {
		for _, want := range []string{m.Pin, m.File} {
			if !slices.Contains(listed, want) {
				t.Errorf("%s is not listed, so the workflow would not fetch it", want)
			}
		}
	}
}

// A root that is not a module is a mistyped -root, and the reason has to reach
// the reader rather than an exit code on its own.
func TestAnUnreadableRootIsReported(t *testing.T) {
	var out, errs strings.Builder
	if code := run([]string{"-check", "-root", filepath.Join(t.TempDir(), "absent")}, &out, &errs); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(errs.String(), "pinsync:") {
		t.Errorf("no reason was printed:\n%s", errs.String())
	}
}

func TestAnUnknownFlagDoesNotWrite(t *testing.T) {
	var out, errs strings.Builder
	if code := run([]string{"-nonsense"}, &out, &errs); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}
