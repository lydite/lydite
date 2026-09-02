package golang

import (
	"os"
	"strings"
	"testing"

	"lydite/lydite/internal/toolchain"
)

// TestPinnedVersionsMatchGoPinModule is the drift guard for the one pair of
// pins lydite cannot read from its manifest at runtime.
//
// Every other pinned tool (Biome, cargo-audit, cargo-deny, Semgrep) has
// its version read directly out of the package-manager manifest Dependabot
// edits, so drift is impossible by construction. Go's two can't work that way:
// gosecPkg/govulncheckPkg are const expressions that concatenate the version at
// compile time, and go-pin is a separate module whose go.mod //go:embed cannot
// reach. So the constants stay, and this test is what makes a Dependabot bump
// to go-pin/go.mod fail loudly instead of silently leaving lydite installing
// the old version.
//
// If this test fails, a bump landed in internal/golang/go-pin/go.mod that was
// not mirrored into the constants in golang.go. Update them to match.
func TestPinnedVersionsMatchGoPinModule(t *testing.T) {
	data, err := os.ReadFile("go-pin/go.mod")
	if err != nil {
		t.Fatalf("reading go-pin/go.mod: %v", err)
	}

	for _, tc := range []struct {
		module   string
		constant string
		name     string
	}{
		{"github.com/securego/gosec/v2", gosecVersion, "gosecVersion"},
		{"golang.org/x/vuln", govulncheckVersion, "govulncheckVersion"},
	} {
		got, ok := moduleVersion(string(data), tc.module)
		if !ok {
			t.Errorf("go-pin/go.mod has no require entry for %s", tc.module)
			continue
		}
		if got != tc.constant {
			t.Errorf("%s = %q but go-pin/go.mod pins %s at %q\n"+
				"A Dependabot bump landed in go-pin/go.mod without updating golang.go — "+
				"set %s to %q.", tc.name, tc.constant, tc.module, got, tc.name, got)
		}
	}
}

// moduleVersion finds the version a go.mod requires a module at. It matches the
// module path exactly so that golang.org/x/vuln is never satisfied by a line
// for golang.org/x/vulndb.
func moduleVersion(gomod, module string) (string, bool) {
	for line := range strings.Lines(gomod) {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == module {
			return fields[1], true
		}
	}
	return "", false
}

// The toolchain is per component, so the cache key has to carry it. A
// repository declaring `go 1.24` in one module and `go 1.28` in another builds
// this tool under two toolchains, and a key naming only the tool would let
// whichever component ran first decide which binary every other one gets — a
// tool built by an older Go rejects newer source outright.
func TestToolCacheKeyDistinguishesToolchains(t *testing.T) {
	// The case a key read back out of the environment cannot see: two
	// downloaded Go toolchains both set GOTOOLCHAIN=local, and what separates
	// them is the version-keyed directory each puts on PATH.
	first := (&toolchain.Env{PathDirs: []string{"/cache/lydite/go-1.24.0/bin"}, Vars: []string{"GOTOOLCHAIN=local"}}).Key()
	second := (&toolchain.Env{PathDirs: []string{"/cache/lydite/go-1.28.0/bin"}, Vars: []string{"GOTOOLCHAIN=local"}}).Key()
	if first == second {
		t.Fatalf("two downloaded toolchains share the key %q, so one component's build would serve the other", first)
	}
	pinned := (&toolchain.Env{Vars: []string{"GOTOOLCHAIN=go1.28.0"}}).Key()
	if pinned == first {
		t.Errorf("a pinned toolchain shares a key with a downloaded one")
	}
	var none *toolchain.Env
	if got := none.Key(); got != "ambient" {
		t.Errorf("Key on a nil Env = %q, want the shared ambient key", got)
	}
	// A directory name, so nothing can nest the install somewhere else.
	if strings.ContainsAny(first, `/\`) {
		t.Errorf("Key = %q, want a plain directory name", first)
	}
}
