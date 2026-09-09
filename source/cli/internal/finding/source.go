package finding

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxFileBytes is how much of a file Source will read to identify a claim in
// it, and maxSiteRunes is how much of one line it will hand back.
//
// Both bound what a repository lydite does not own can put into lydite's own
// output. A minified bundle is one line of several megabytes, and a scanner
// firing on it would otherwise embed the whole file in every finding's Site —
// which travels in the report document, is uploaded as an artifact, and is
// read back whole by the fold. It is the rule the standing comment already
// follows, where quoted output is capped because a platform refuses an
// oversized one and a refused surface is no surface at all.
//
// Truncating is safe for identity. A fingerprint needs its ingredient to be
// stable, not complete, and two claims sharing a truncated prefix are told
// apart by their ordinal.
const (
	maxFileBytes = 4 << 20
	maxSiteRunes = 256
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
	return clip(Normalise(lines[n-1]))
}

// clip bounds one line's contribution to a claim's identity.
//
// Stated as a clamp rather than as a comparison, so there is no boundary to be
// wrong about: a line at exactly the cap and one under it take the same path.
func clip(s string) string {
	runes := []rune(s)
	return string(runes[:min(len(runes), maxSiteRunes)])
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
	// One byte past the cap, so a file exactly at it is still read whole and
	// one over it is refused rather than silently truncated mid-line — a
	// truncated last line is a Site that identifies a claim by code the file
	// does not contain.
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return nil
	}
	return strings.Split(string(data), "\n")
}
