package orphan

import "testing"

// A repository's own root go.mod is a module boundary. path.Dir("go.mod") is
// ".", whose one segment begins with a dot, so the ignored-directory rule read
// it as a directory the go command skips. Nothing answers differently today —
// a file with no enclosing module is treated as being in its component's — but
// the two are only accidentally compensating, and the next reader of either
// has no reason to expect it.
func TestARootGoModIsAModuleBoundary(t *testing.T) {
	if goIgnored(".") {
		t.Fatal("the scan root reads as a directory the go command ignores")
	}
	if !goIgnored("testdata/broken") || !goIgnored("_build") || !goIgnored(".git") {
		t.Error("a testdata, _ or . directory must still be ignored")
	}
	got := goModuleDirs([]string{"go.mod", "sdk/go.mod", "testdata/x/go.mod"})
	if !got["."] || !got["sdk"] || got["testdata/x"] {
		t.Errorf("goModuleDirs = %v, want the root and sdk but not the fixture", got)
	}
}
