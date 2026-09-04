package golang

import (
	"strings"
	"testing"

	"lydite/lydite/internal/toolchain"
)

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
