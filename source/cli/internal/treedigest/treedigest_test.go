package treedigest

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func digest(t *testing.T, root string, files ...string) string {
	t.Helper()
	d, err := Digest(root, files)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDigestIgnoresInputOrder(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "one")
	write(t, root, "sub/b.txt", "two")
	write(t, root, "c.txt", "three")
	want := digest(t, root, "a.txt", "sub/b.txt", "c.txt")
	for _, order := range [][]string{
		{"c.txt", "sub/b.txt", "a.txt"},
		{"sub/b.txt", "a.txt", "c.txt"},
	} {
		if got := digest(t, root, order...); got != want {
			t.Fatalf("order %v: %s, want %s", order, got, want)
		}
	}
}

func TestDigestDoesNotMutateInput(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a", "1")
	write(t, root, "b", "2")
	files := []string{"b", "a"}
	digest(t, root, files...)
	if files[0] != "b" || files[1] != "a" {
		t.Fatalf("input reordered: %v", files)
	}
}

func TestDigestChangesWithContent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "hello")
	before := digest(t, root, "a.txt")
	write(t, root, "a.txt", "hellp")
	if digest(t, root, "a.txt") == before {
		t.Fatal("a changed byte left the digest unchanged")
	}
}

func TestDigestChangesWithAddedFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "x")
	write(t, root, "b.txt", "")
	if digest(t, root, "a.txt") == digest(t, root, "a.txt", "b.txt") {
		t.Fatal("an added file left the digest unchanged")
	}
}

func TestDigestChangesWithRename(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "same")
	write(t, root, "b.txt", "same")
	if digest(t, root, "a.txt") == digest(t, root, "b.txt") {
		t.Fatal("a renamed file left the digest unchanged")
	}
}

func TestDigestFramesPathAndContent(t *testing.T) {
	root := t.TempDir()
	write(t, root, "ab", "c")
	write(t, root, "a", "bc")
	if digest(t, root, "ab") == digest(t, root, "a") {
		t.Fatal("path and content boundaries collide")
	}
	one := t.TempDir()
	write(t, one, "a", "xb")
	write(t, one, "c", "y")
	two := t.TempDir()
	write(t, two, "a", "x")
	write(t, two, "bc", "y")
	if digest(t, one, "a", "c") == digest(t, two, "a", "bc") {
		t.Fatal("concatenations of different file sets collide")
	}
}

func TestDigestEmptySet(t *testing.T) {
	if digest(t, t.TempDir()) == "" {
		t.Fatal("empty set produced no digest")
	}
}

func TestDigestDuplicatePathCountsOnce(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a", "1")
	if digest(t, root, "a", "a") != digest(t, root, "a") {
		t.Fatal("a duplicated path changed the digest")
	}
}

func TestDigestMissingFileIsAnError(t *testing.T) {
	root := t.TempDir()
	if _, err := Digest(root, []string{"absent"}); err == nil {
		t.Fatal("want an error for a missing file")
	}
}

func TestDigestDirectoryIsAnError(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Digest(root, []string{"d"}); err == nil {
		t.Fatal("want an error for a directory")
	}
}

func TestDigestUnreadableFileIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	root := t.TempDir()
	write(t, root, "a", "1")
	if err := os.Chmod(filepath.Join(root, "a"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Digest(root, []string{"a"}); err == nil {
		t.Fatal("want an error for an unreadable file")
	}
}
