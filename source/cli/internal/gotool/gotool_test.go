package gotool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A cache directory is keyed by the tool's version, which is the whole of what
// makes a pin mean anything: a bump must not be able to reuse what is already
// there.
func TestABinDirIsKeyedByVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first, err := BinDir("gosec", "v2.29.0", "")
	if err != nil {
		t.Fatal(err)
	}
	bumped, err := BinDir("gosec", "v2.30.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if first == bumped {
		t.Errorf("a version bump reuses %s, so a stale binary would answer for the new pin", first)
	}
	if !strings.Contains(first, "v2.29.0") {
		t.Errorf("BinDir = %s, want the version in the path so a stale install is visible", first)
	}
}

// The extra key is what keeps two callers' installs apart when the version
// alone does not distinguish them — a scanner built by one Go toolchain must
// not answer for a component that declared another.
func TestAnExtraKeySeparatesOtherwiseIdenticalInstalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plain, err := BinDir("gosec", "v2.29.0", "")
	if err != nil {
		t.Fatal(err)
	}
	keyed, err := BinDir("gosec", "v2.29.0", "go1.26.6")
	if err != nil {
		t.Fatal(err)
	}
	if plain == keyed {
		t.Errorf("a keyed install shares %s with an unkeyed one", plain)
	}
	if !strings.HasSuffix(keyed, "-go1.26.6") {
		t.Errorf("BinDir = %s, want the key on the end", keyed)
	}
	// A caller that passes no key gets no trailing separator, so the directory
	// a wrapper installs into is stable rather than merely usually right.
	if strings.HasSuffix(plain, "-") {
		t.Errorf("BinDir = %s, want no dangling separator when no key is given", plain)
	}
}

// A binary already in the cache is used as it is. Downloading something
// present and correct is pure cost, and on the runners lydite runs on it is
// the common case — so the common path must not shell out at all.
func TestAnInstalledToolIsNotInstalledAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := BinDir("gotestsum", "v1.13.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "gotestsum")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// GOPROXY=off makes any install attempt fail, so a nil error here can only
	// mean the cached binary was taken without one.
	got, err := Ensure(context.Background(), []string{"GOPROXY=off", "GOFLAGS=-mod=mod"},
		"gotestsum", "v1.13.0", "gotest.tools/gotestsum@v1.13.0", "")
	if err != nil {
		t.Fatalf("Ensure re-installed a tool already in the cache: %v", err)
	}
	if got != bin {
		t.Errorf("Ensure = %s, want the cached binary at %s", got, bin)
	}
}

// An install that cannot run is an error naming the package, not a silent
// fallback to whatever the machine happens to carry — an absent pinned tool is
// a component that cannot run at all.
func TestAnInstallThatFailsIsReported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := Ensure(context.Background(), []string{"GOPROXY=off", "GOFLAGS=-mod=mod"},
		"gotestsum", "v0.0.0-does-not-exist", "gotest.tools/gotestsum@v0.0.0-does-not-exist", "")
	if err == nil {
		t.Fatal("Ensure reported success for a package that cannot be installed")
	}
	if !strings.Contains(err.Error(), "gotest.tools/gotestsum@v0.0.0-does-not-exist") {
		t.Errorf("error = %q, want it to name the package that could not be installed", err)
	}
}
