package finding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceReadsTheCodeAClaimIsAbout(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("\tif a < b {\nreturn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := NewSource(root)

	if got, want := src.Line("a.go", 1), "if a < b {"; got != want {
		t.Errorf("Line = %q, want %q — the layout is not part of the identity", got, want)
	}
	if got := src.Line("a.go", 2); got != "return" {
		t.Errorf("Line(2) = %q, want return", got)
	}
}

// A claim about a file that has changed under the run loses only what tells it
// from a neighbour. Failing instead would turn an unreadable file into a gate
// that could not report what it already found.
func TestSourceAnswersEmptyForWhatItCannotRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := NewSource(root)

	for name, got := range map[string]string{
		"a line before the first":  src.Line("a.go", 0),
		"a negative line":          src.Line("a.go", -3),
		"a line past the end":      src.Line("a.go", 99),
		"a file that is not there": src.Line("gone.go", 1),
	} {
		if got != "" {
			t.Errorf("%s returned %q, want empty", name, got)
		}
	}
}

// A path reaching here was reported by a tool reading a repository lydite does
// not own, and the text it names becomes a Site that travels in the report
// document. A path climbing out of the tree would put a line of somebody
// else's file into lydite's own output.
func TestAPathThatClimbsOutOfTheTreeReadsNothing(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("a line nobody asked lydite to read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(outside, "tree")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	// The file is there, and readable, and one level up.
	if _, err := os.ReadFile(secret); err != nil {
		t.Fatalf("the fixture is wrong, the file must be readable: %v", err)
	}

	if got := NewSource(root).Line("../secret.txt", 1); got != "" {
		t.Errorf("a path out of the tree read %q", got)
	}
}

// A symlink is the case a lexical check cannot see: the name is spotless and
// only the resolved path leaves the tree.
func TestASymlinkOutOfTheTreeReadsNothing(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(outside, "tree")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "innocent.go")); err != nil {
		t.Skipf("this platform does not allow symlinks: %v", err)
	}

	if got := NewSource(root).Line("innocent.go", 1); got != "" {
		t.Errorf("a symlink out of the tree read %q", got)
	}
}

// A gate with fifty claims in one file must not open it fifty times.
func TestSourceReadsEachFileOnce(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := NewSource(root)
	if got := src.Line("a.go", 1); got != "first" {
		t.Fatalf("Line = %q", got)
	}
	if err := os.WriteFile(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := src.Line("a.go", 1); got != "first" {
		t.Errorf("Line = %q after the file changed, want the first read to stand", got)
	}
}

// A root that cannot be opened answers empty for everything, for the reason an
// unreadable file does: identity is what is lost, and the gate has already
// reported what it found.
func TestASourceOnARootThatIsNotThereReadsNothing(t *testing.T) {
	src := NewSource(filepath.Join(t.TempDir(), "no-such-directory"))
	if got := src.Line("a.go", 1); got != "" {
		t.Errorf("a source with no root read %q", got)
	}
}

// A minified bundle is one line of several megabytes, and a scanner firing on
// it would otherwise embed the whole file in every claim's identity — which
// travels in the report document and is read back whole by the fold.
func TestAFileTooLargeToIdentifyAClaimIsNotRead(t *testing.T) {
	root := t.TempDir()
	oversized := make([]byte, maxFileBytes+1)
	for i := range oversized {
		oversized[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(root, "bundle.js"), oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := NewSource(root).Line("bundle.js", 1); got != "" {
		t.Errorf("an oversized file contributed %d characters to a claim's identity", len(got))
	}
}

// A file at the cap is still read whole: refusing it would lose the identity of
// every claim in it for being one byte from a limit.
func TestAFileExactlyAtTheCapIsRead(t *testing.T) {
	root := t.TempDir()
	body := append([]byte("first\n"), make([]byte, maxFileBytes-len("first\n"))...)
	for i := len("first\n"); i < len(body); i++ {
		body[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(root, "big.go"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := NewSource(root).Line("big.go", 1); got != "first" {
		t.Errorf("a file at the cap read %q, want first", got)
	}
}

// One line is bounded too, or a single-line bundle becomes the whole of a
// claim's identity in the document.
func TestOneLineContributesABoundedAmountToAnIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "min.js"), []byte(strings.Repeat("a", maxSiteRunes*3)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := NewSource(root).Line("min.js", 1)
	if len([]rune(got)) != maxSiteRunes {
		t.Errorf("one line contributed %d runes, want it clipped to %d", len([]rune(got)), maxSiteRunes)
	}
}

// A file whose last line has no newline after it still has that line. Splitting
// on newlines gives no trailing empty element there, so the last line is the
// last element — and a bound that is one out reads it as past the end.
func TestTheLastLineOfAFileWithNoTrailingNewlineIsRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("first\nlast"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := NewSource(root)

	if got := src.Line("a.go", 2); got != "last" {
		t.Errorf("the last line read %q, want last", got)
	}
	if got := src.Line("a.go", 3); got != "" {
		t.Errorf("a line past the end read %q", got)
	}
}
