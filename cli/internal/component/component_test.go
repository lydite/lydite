package component

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
)

func TestParseFullDeclaration(t *testing.T) {
	f, err := Parse([]byte(`
components:
  - name: cli
    dir: cli
    runner: go-test
    args: ["-race", "./..."]
    watch: ["Makefile", "VERSION"]
    depends_on: [sdk]
    env:
      FOO: bar
    compose:
      file: ./docker/compose.yaml
      up: [db]
      wait: healthy
    setup: ["make migrate"]
    teardown: ["rm -rf ./data"]
    mutation: false
  - name: sdk
    dir: sdk
    runner: go-test
`), "components.yml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cli := f.Components[0]
	if cli.Dir != "cli" || cli.Runner != runner.GoTest {
		t.Errorf("dir/runner = %q/%q", cli.Dir, cli.Runner)
	}
	if got := cli.Args; len(got) != 2 || got[0] != "-race" {
		t.Errorf("args = %v", got)
	}
	if cli.Env["FOO"] != "bar" {
		t.Errorf("env = %v", cli.Env)
	}
	if cli.Compose.Wait != WaitHealthy || cli.Compose.File != "./docker/compose.yaml" {
		t.Errorf("compose = %+v", cli.Compose)
	}
	if cli.MutationEnabled() {
		t.Error("mutation: false must disable mutation")
	}
	if !f.Components[1].MutationEnabled() {
		t.Error("mutation defaults to enabled — it is an opt-out")
	}
}

// A key lydite silently drops is a component configured differently from
// what its author wrote, with every run still reporting a result.
func TestParseRejectsUnknownKeys(t *testing.T) {
	_, err := Parse([]byte("components:\n  - name: a\n    dir: .\n    runner: go-test\n    lang: go\n"), "components.yml")
	if err == nil || !strings.Contains(err.Error(), "lang") {
		t.Fatalf("want an error naming the unknown key, got %v", err)
	}
}

// lang is derived from the runner, never declared, so there is nothing for a
// second statement of it to disagree with.
func TestLangIsDerivedFromRunner(t *testing.T) {
	for name, want := range map[runner.Name]runner.Lang{
		runner.GoTest:              runner.Go,
		runner.CargoNextest:        runner.Rust,
		runner.CargoLLVMCovNextest: runner.Rust,
		runner.Vitest:              runner.TypeScript,
		runner.Jest:                runner.TypeScript,
	} {
		if got := (Component{Runner: name}).Lang(); got != want {
			t.Errorf("%s: lang = %q, want %q", name, got, want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{
			name: "missing name",
			yaml: "components:\n  - dir: cli\n    runner: go-test\n",
			want: "name is required",
		},
		{
			name: "duplicate name",
			yaml: "components:\n  - {name: a, dir: cli, runner: go-test}\n  - {name: a, dir: cli, runner: go-test}\n",
			want: "duplicate name",
		},
		{
			name: "missing dir",
			yaml: "components:\n  - name: a\n    runner: go-test\n",
			want: "dir is required",
		},
		{
			name: "absolute dir",
			yaml: "components:\n  - {name: a, dir: /etc, runner: go-test}\n",
			want: "must be relative",
		},
		{
			name: "escaping dir",
			yaml: "components:\n  - {name: a, dir: ../elsewhere, runner: go-test}\n",
			want: "escapes the scan root",
		},
		{
			name: "no runner and no command",
			yaml: "components:\n  - {name: a, dir: cli}\n",
			want: "one of runner or command is required",
		},
		{
			name: "both runner and command",
			yaml: "components:\n  - {name: a, dir: cli, runner: go-test, command: [make, test]}\n",
			want: "mutually exclusive",
		},
		{
			name: "unknown runner",
			yaml: "components:\n  - {name: a, dir: cli, runner: go-check}\n",
			want: `unknown runner "go-check"`,
		},
		{
			name: "args alongside a command",
			yaml: "components:\n  - {name: a, dir: cli, command: [make, test], args: [-race]}\n",
			want: "args applies to a runner",
		},
		{
			name: "unknown wait",
			yaml: "components:\n  - {name: a, dir: cli, runner: go-test, compose: {wait: ready}}\n",
			want: "compose.wait must be",
		},
		{
			name: "dangling dependency",
			yaml: "components:\n  - {name: a, dir: cli, runner: go-test, depends_on: [ghost]}\n",
			want: "not a declared component",
		},
		{
			name: "self dependency",
			yaml: "components:\n  - {name: a, dir: cli, runner: go-test, depends_on: [a]}\n",
			want: "names itself",
		},
		{
			name: "dependency cycle",
			yaml: "components:\n  - {name: a, dir: cli, runner: go-test, depends_on: [b]}\n  - {name: b, dir: cli, runner: go-test, depends_on: [a]}\n",
			want: "cycle",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml), "components.yml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// An error naming only "a cycle exists" leaves the reader to find it.
func TestCycleErrorNamesTheCycle(t *testing.T) {
	_, err := Parse([]byte("components:\n  - {name: a, dir: cli, runner: go-test, depends_on: [b]}\n  - {name: b, dir: cli, runner: go-test, depends_on: [a]}\n"), "components.yml")
	if err == nil || !strings.Contains(err.Error(), "a -> b -> a") {
		t.Fatalf("want the cycle named, got %v", err)
	}
}

// A repository that has declared nothing is a state lydite reports, not one
// it refuses to run in.
func TestLoadMissingFileIsNotAnError(t *testing.T) {
	f, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Components) != 0 {
		t.Errorf("want no components, got %v", f.Components)
	}
}

func TestLoadReadsFromConfigDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, FileName, "components:\n  - {name: cli, dir: cli, runner: go-test}\n")
	if err := os.MkdirAll(filepath.Join(root, "cli"), 0o750); err != nil {
		t.Fatal(err)
	}
	f, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Components) != 1 || f.Components[0].Name != "cli" {
		t.Fatalf("components = %v", f.Components)
	}
}

// The declaration lives beside every other file that configures lydite.
func TestFileNameIsInsideTheConfigDir(t *testing.T) {
	if want := config.Dir + "/components.yml"; FileName != want {
		t.Errorf("FileName = %q, want %q", FileName, want)
	}
}

// A dir nobody checks is a component nothing runs: the code it covers is
// tested by nobody and the build stays green.
func TestLoadRejectsAMissingDir(t *testing.T) {
	root := t.TempDir()
	write(t, root, FileName, "components:\n  - {name: cli, dir: nowhere, runner: go-test}\n")
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("want a missing-dir error, got %v", err)
	}
}

func TestLoadRejectsADirThatIsAFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, FileName, "components:\n  - {name: cli, dir: file.txt, runner: go-test}\n")
	write(t, root, "file.txt", "")
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("want a not-a-directory error, got %v", err)
	}
}

func TestSelect(t *testing.T) {
	f, err := Parse([]byte("components:\n  - {name: a, dir: cli, runner: go-test}\n  - {name: b, dir: cli, runner: go-test}\n"), "components.yml")
	if err != nil {
		t.Fatal(err)
	}
	all, err := f.Select(nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("Select(nil) = %v, %v", all, err)
	}
	// Declaration order, not the order the names were given, so a report
	// reads the same however the flags were typed.
	some, err := f.Select([]string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(some) != 2 || some[0].Name != "a" {
		t.Errorf("Select = %v, want declaration order", some)
	}
	if _, err := f.Select([]string{"ghost"}); err == nil {
		t.Error("selecting an undeclared component must be an error, not an empty run")
	}
}

func TestComposeDeclared(t *testing.T) {
	if (Compose{}).Declared() {
		t.Error("an empty compose block declares no services")
	}
	for _, c := range []Compose{{File: "compose.yaml"}, {Up: []string{"db"}}, {Wait: WaitNone}} {
		if !c.Declared() {
			t.Errorf("%+v declares services", c)
		}
	}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// An exclude that lydite cannot parse is rejected where it is written, not
// treated as one that matches nothing. A pattern matching nothing leaves the
// file it was written for orphaned, and the author has already said what they
// meant.
func TestMalformedExcludeIsRejected(t *testing.T) {
	if _, err := Parse([]byte("excludes: [\"src/[unclosed\"]\n"), FileName); err == nil {
		t.Error("a malformed exclude glob must be rejected at load time")
	}
}

// An exclude naming something outside the scan root describes a file the
// repository does not contain, so nothing about it is reviewable from the
// declaration and no orphan it could clear exists.
func TestExcludeMustStayInsideTheScanRoot(t *testing.T) {
	for _, bad := range []string{"/etc/passwd", "../outside/**", "~/secrets", ""} {
		if _, err := Parse([]byte("excludes: [\""+bad+"\"]\n"), FileName); err == nil {
			t.Errorf("exclude %q must be rejected", bad)
		}
	}
}

func TestExcludesParse(t *testing.T) {
	f, err := Parse([]byte("components: []\nexcludes: [\"scripts/**\", \"tools/gen.go\"]\n"), FileName)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Excludes) != 2 {
		t.Errorf("excludes = %v, want two", f.Excludes)
	}
}

// TestWatchPatternsAreValidated: a watch entry is the pattern deciding whether
// a component runs when a file outside its directory changes, so a malformed
// one must be refused rather than folded into "matches nothing". An exclude
// that covers nothing is fail-safe; a watch that covers nothing is a component
// that silently stops being invalidated by its own declared input.
func TestWatchPatternsAreValidated(t *testing.T) {
	for _, tc := range []struct{ name, watch, want string }{
		{"malformed", "docs/[openapi.json", "watch[0]"},
		{"empty", "", "is empty"},
		{"absolute", "/etc/passwd", "must be relative to the scan root"},
		{"escaping", "../outside/thing", "escapes the scan root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "components:\n  - name: a\n    dir: .\n    runner: go-test\n    watch: [\"" + tc.watch + "\"]\n"
			_, err := Parse([]byte(doc), FileName)
			if err == nil {
				t.Fatalf("watch %q was accepted; a pattern that cannot fire is a component that never runs", tc.watch)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestValidWatchPatternIsAccepted(t *testing.T) {
	doc := "components:\n  - name: a\n    dir: .\n    runner: go-test\n    watch: [\"Makefile\", \"docs/**\", \"**/VERSION\"]\n"
	if _, err := Parse([]byte(doc), FileName); err != nil {
		t.Errorf("valid watch patterns were rejected: %v", err)
	}
}
