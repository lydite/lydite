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

// An install that failed hands back no path. A caller that reads the path
// without the error would otherwise put a binary that was never installed on
// PATH, and the component would fail on the invocation rather than on the
// install that is the actual cause.
func TestAFailedInstallReturnsNoPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := Ensure(context.Background(), []string{"GOPROXY=off", "GOFLAGS=-mod=mod"},
		"gotestsum", "v0.0.0-does-not-exist", "gotest.tools/gotestsum@v0.0.0-does-not-exist", "")
	if err == nil {
		t.Fatal("Ensure reported success for a package that cannot be installed")
	}
	if got != "" {
		t.Errorf("Ensure = %q on a failed install, want no path at all", got)
	}
}

// A requirement is part of what an install is: a binary built against one
// version of a dependency must not answer for the same tool built against
// another, or raising the requirement would keep serving the old build.
func TestARequirementSeparatesOtherwiseIdenticalInstalls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cached := func(version string) string {
		dir, err := BinDir("gosec", "v2.29.0", "go1.27.2-tools-"+version)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, "gosec")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return bin
	}
	old, bumped := cached("v0.50.0"), cached("v0.51.0")
	env := []string{"GOPROXY=off"}
	for version, want := range map[string]string{"v0.50.0": old, "v0.51.0": bumped} {
		got, err := EnsureWith(context.Background(), env, "gosec", "v2.29.0", "github.com/securego/gosec/v2/cmd/gosec@v2.29.0",
			"go1.27.2", []Require{{Module: "golang.org/x/tools", Version: version}})
		if err != nil {
			t.Fatalf("EnsureWith re-installed a tool already cached for tools %s: %v", version, err)
		}
		if got != want {
			t.Errorf("tools %s resolved to %s, want %s", version, got, want)
		}
	}
}

// A failed install under a requirement hands back no path, for the reason a
// plain one does, and names the step that failed.
func TestAFailedInstallWithARequirementReturnsNoPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := EnsureWith(context.Background(), []string{"GOPROXY=off"},
		"gotestsum", "v0.0.0-does-not-exist", "gotest.tools/gotestsum@v0.0.0-does-not-exist", "",
		[]Require{{Module: "golang.org/x/tools", Version: "v0.51.0"}})
	if err == nil {
		t.Fatal("EnsureWith reported success for a package that cannot be installed")
	}
	if got != "" {
		t.Errorf("EnsureWith returned %q beside an error, want no path", got)
	}
	if !strings.Contains(err.Error(), "gotest.tools/gotestsum@v0.0.0-does-not-exist") {
		t.Errorf("error = %q, want it to name the package that could not be installed", err)
	}
}

// fakeGo puts a `go` on PATH that records how it was invoked and, for an
// install, leaves the binary where GOBIN says. It stands in for the real
// command so the test reads the install's shape rather than the network's.
func fakeGo(t *testing.T) (env []string, calls func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$*|GOFLAGS=$GOFLAGS|GOBIN=$GOBIN|DIR=$(basename \"$PWD\")\" >> \"$FAKEGO_LOG\"\n" +
		"if [ \"$1\" = install ]; then mkdir -p \"$GOBIN\" && : > \"$GOBIN/tool\"; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o700); err != nil { // #nosec G306 -- an executable stub in a test directory
		t.Fatal(err)
	}
	env = []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "FAKEGO_LOG=" + log}
	return env, func() []string {
		data, err := os.ReadFile(log) // #nosec G304 -- a path this test wrote
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

// Without a requirement the install is the plain one: a single
// `go install pkg@version` outside any module, with nothing added to the
// cache key.
func TestAnInstallWithoutARequirementIsAPlainInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	env, calls := fakeGo(t)
	bin, err := EnsureWith(context.Background(), env, "tool", "v1.0.0", "example.com/tool/cmd/tool@v1.0.0", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := calls()
	if len(got) != 1 || !strings.HasPrefix(got[0], "install example.com/tool/cmd/tool@v1.0.0|") {
		t.Fatalf("go was run as %q, want one `go install pkg@version`", got)
	}
	if strings.Contains(got[0], "GOFLAGS=-mod=mod") {
		t.Errorf("a plain install ran with %q, want the caller's flags untouched", got[0])
	}
	want, err := BinDir("tool", "v1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if bin != filepath.Join(want, "tool") {
		t.Errorf("bin = %s, want it under %s", bin, want)
	}
}

// A requirement is built in a module of its own: the tool is added at its
// pin, each requirement beside it, and the package is installed by path with
// no version left to override them.
func TestAnInstallWithARequirementIsBuiltInAThrowawayModule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	env, calls := fakeGo(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	bin, err := EnsureWith(context.Background(), env, "tool", "v1.0.0", "example.com/tool/cmd/tool@v1.0.0", "",
		[]Require{{Module: "golang.org/x/tools", Version: "v0.51.0"}})
	if err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Dir(bin)
	got := calls()
	want := []string{
		"mod init lydite.invalid/install",
		"get example.com/tool/cmd/tool@v1.0.0",
		"get golang.org/x/tools@v0.51.0",
		"install example.com/tool/cmd/tool",
	}
	if len(got) != len(want) {
		t.Fatalf("go was run %d times (%q), want %d", len(got), got, len(want))
	}
	for i, call := range got {
		if !strings.HasPrefix(call, want[i]+"|") {
			t.Errorf("call %d = %q, want it to be `go %s`", i, call, want[i])
		}
		if !strings.Contains(call, "GOFLAGS=-mod=mod") {
			t.Errorf("call %d = %q, want -mod=mod so the empty module can take requirements", i, call)
		}
		if !strings.Contains(call, "GOBIN="+binDir+"|") {
			t.Errorf("call %d = %q, want GOBIN=%s", i, call, binDir)
		}
		if !strings.Contains(call, "DIR=lydite-install-") {
			t.Errorf("call %d = %q, want it run in the throwaway module's directory", i, call)
		}
	}
	// With no key of its own the requirement is the whole of the directory's
	// suffix, with no dangling separator in front of it.
	if base := filepath.Base(binDir); !strings.HasSuffix(base, "-v1.0.0-tools-v0.51.0") || strings.Contains(base, "--") {
		t.Errorf("install directory %q, want ...-v1.0.0-tools-v0.51.0 with a single separator", base)
	}
	// The module is removed once the build is done.
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) != 0 {
		t.Errorf("temp directory holds %v (err %v), want the throwaway module removed", entries, err)
	}
}
