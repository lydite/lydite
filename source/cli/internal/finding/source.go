package finding

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Source reads the code a gate fired on, so a claim can be identified by what
// it is about rather than by where it sits.
//
// It exists once because two gates need the same thing and a second copy would
// read a file by a different root, or normalise it by a different rule, and
// the two would agree until one of them learned something. A claim identified
// differently by two producers is a claim reported twice.
//
// Each file is read once. A gate with fifty claims in one file would otherwise
// open it fifty times, and the whole point of reading it at all is cheap
// enough to be worth doing.
// Reads are confined to the root rather than checked against it. A path
// reaching here was reported by a tool reading a repository lydite does not
// own, and the text it names becomes a finding's Site, which travels in the
// report document — so a path climbing out of the tree would put a line of
// somebody's private file into lydite's own output. os.Root refuses that at
// the syscall, and leaves no window between resolving a path and reading it,
// which a lexical check does.
type Source struct {
	root  *os.Root
	files map[string][]string
}

// NewSource reads paths relative to root, which is the root every finding's
// Path is already relative to.
//
// A root that cannot be opened yields a Source that answers empty for
// everything, for the reason an unreadable file does: identity is what is
// lost, and the gate has already reported what it found.
func NewSource(root string) *Source {
	opened, err := os.OpenRoot(root)
	if err != nil {
		return &Source{files: map[string][]string{}}
	}
	return &Source{root: opened, files: map[string][]string{}}
}

// Line is the file's nth line, one-based, normalised.
//
// It answers empty for a line it cannot read — a file that has changed under
// the run, a path outside the tree, a line past the end. Empty is the right
// answer rather than an error: the caller is building an identity, and a
// missing ingredient makes two claims in one file share one, which the ordinal
// then separates. Failing the run instead would turn an unreadable file into a
// gate that could not report what it had already found.
func (s *Source) Line(path string, n int) string {
	if n < 1 {
		return ""
	}
	lines, ok := s.files[path]
	if !ok {
		lines = s.read(path)
		s.files[path] = lines
	}
	if n > len(lines) {
		return ""
	}
	return Normalise(lines[n-1])
}

func (s *Source) read(path string) []string {
	if s.root == nil {
		return nil
	}
	f, err := s.root.Open(filepath.FromSlash(path))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}
