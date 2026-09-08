package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
)

// The tool config is what turns cargo-nextest's JUnit report on, so an
// invocation that claims a report must leave one on disk for nextest to read —
// and it must say what it says, since the path lydite then looks in comes from
// this file.
func TestTheNextestToolConfigIsStagedForAnInvocationThatClaimsAReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inv := llvmCovNextest(nil)
	if inv.JUnitReport == "" {
		t.Fatal("the instrumented invocation claims no JUnit report, so there is nothing to stage for")
	}
	if err := stageNextestToolConfig(inv); err != nil {
		t.Fatalf("stageNextestToolConfig: %v", err)
	}
	cfg, ok := nextestToolConfig()
	if !ok {
		t.Fatal("no tool config path on a machine with a cache directory")
	}
	body, err := os.ReadFile(cfg) // #nosec G304 -- a path this package composed under a temp HOME
	if err != nil {
		t.Fatalf("the config the invocation points at was not staged: %v", err)
	}
	if string(body) != nextestToolConfigBody {
		t.Errorf("the staged config is %q, want the JUnit profile", body)
	}
	// The invocation must point at the file that was actually written, or
	// nextest is told to read a config nobody staged.
	if !strings.Contains(strings.Join(inv.Args, " "), "lydite:"+cfg) {
		t.Errorf("the invocation names no tool config; args = %v", inv.Args)
	}
}

// Staging is a rename, not a truncating write: components run concurrently, so
// another component's nextest may be reading this file as it starts, and a
// half-written TOML fails that component's suite for a reason unrelated to its
// code. Re-staging over an existing config must leave the whole of one version
// or the whole of the other, and no temporary file behind.
func TestReStagingTheNextestConfigLeavesNoDebris(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inv := llvmCovNextest(nil)
	for range 3 {
		if err := stageNextestToolConfig(inv); err != nil {
			t.Fatalf("stageNextestToolConfig: %v", err)
		}
	}
	cfg, _ := nextestToolConfig()
	entries, err := os.ReadDir(filepath.Dir(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(cfg) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the config directory holds %v, want only %s", names, filepath.Base(cfg))
	}
}

// An invocation that asks for no report stages nothing. The plain variant is
// what mutation runs once per mutant, and a config written thousands of times
// is a cost paid for a report nobody reads.
func TestAnInvocationWithNoReportStagesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plain, ok := buildCargoNextest(Plain, nil)
	if !ok {
		t.Fatal("cargo-nextest builds no plain variant")
	}
	if plain.JUnitReport != "" {
		t.Fatal("the plain variant claims a JUnit report, so this asserts nothing")
	}
	if err := stageNextestToolConfig(plain); err != nil {
		t.Fatalf("stageNextestToolConfig: %v", err)
	}
	cfg, _ := nextestToolConfig()
	if _, err := os.Stat(cfg); err == nil {
		t.Error("a config was staged for an invocation that asks for no report")
	}
}

// What a runner installs is read off the built command, never off the variant
// that named it. `go test` needs nothing, so the plain variant must not reach
// for the wrapper — and the instrumented one must, or it runs a program
// nothing put on PATH.
func TestOnlyTheInvocationThatRunsTheWrapperInstallsIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// An install that cannot possibly succeed, so an attempted one is visible
	// as an error and a skipped one as a nil.
	env := executil.Env{Install: []string{"GOPROXY=off", "GOFLAGS=-mod=mod"}}

	plain, ok := Lookup(GoTest)
	if !ok {
		t.Fatal("no go-test runner")
	}
	for _, variant := range []Variant{Plain, BuildOnly} {
		inv, ok := plain.Build(variant, nil)
		if !ok {
			t.Fatalf("go-test builds no %s variant", variant)
		}
		if err := plain.Prepare(context.Background(), inv, t.TempDir(), "", env, io.Discard); err != nil {
			t.Errorf("the %s variant tried to install the wrapper it does not run: %v", variant, err)
		}
	}

	instrumented, ok := plain.Build(Instrumented, nil)
	if !ok {
		t.Fatal("go-test builds no instrumented variant")
	}
	if instrumented.Name != gotestsumName {
		t.Fatalf("the instrumented variant runs %q, not the wrapper", instrumented.Name)
	}
	if err := plain.Prepare(context.Background(), instrumented, t.TempDir(), "", env, io.Discard); err == nil {
		t.Error("the instrumented variant reported success without installing the wrapper it runs")
	}
}

// A config that cannot be staged is an error, not a silent skip. nextest would
// otherwise run without the JUnit profile and lydite would look for a report
// nobody was told to write — reported as a component whose suite ran no tests.
func TestAToolConfigThatCannotBeStagedIsReported(t *testing.T) {
	home := t.TempDir()
	// A file where the cache directory belongs, so nothing under it can be
	// created.
	if err := os.WriteFile(filepath.Join(home, "blocked"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(home, "blocked"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "blocked", "cache"))

	if err := stageNextestToolConfig(llvmCovNextest(nil)); err == nil {
		t.Error("stageNextestToolConfig reported success though it could not create its directory")
	}
}
