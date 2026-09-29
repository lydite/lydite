package mutation

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The files one component's mutation state is kept in, inside its own
// directory.
const (
	stateFingerprintFile = "fingerprint"
	stateBaselineFile    = "baseline.json"
	stateVerdictsFile    = "verdicts.jsonl"
)

// MutantID is a mutant's identity across runs: its path, operator, byte range,
// and the text it replaces and replaces it with.
//
// Line and Column are left out because Offset already locates the site, and
// Reason because it is the answer an acknowledgement gives rather than the
// mutant it is given for. Every field is framed by its length before it is
// hashed, so no two different mutants concatenate to the same bytes — without
// the framing, an Original ending where a Mutated begins could be shifted
// between the two and name a different mutant under the same id.
func MutantID(m Mutant) string {
	h := sha256.New()
	for _, field := range []string{
		"lydite-mutant-v1",
		m.Path,
		string(m.Operator),
		strconv.Itoa(m.Offset),
		strconv.Itoa(m.Length),
		m.Original,
		m.Mutated,
	} {
		_, _ = fmt.Fprintf(h, "%d:%s", len(field), field) // a hash.Hash's Write never fails
	}
	return hex.EncodeToString(h.Sum(nil))
}

// StateDir is the directory under root a component's mutation state is kept
// in.
//
// A component name may contain `/`, and a name that becomes a path is a path:
// unescaped, `web/app` would nest inside `web`'s state and `..` would climb
// out of root. Every byte outside [A-Za-z0-9_-], and a leading `.`, is written
// as `%XX` — `%` included — so the mapping is reversible and two names never
// share a directory. The empty name is `%`, which no escaped name can be.
func StateDir(root, component string) string {
	if component == "" {
		return filepath.Join(root, "%")
	}
	var b strings.Builder
	for i := 0; i < len(component); i++ {
		c := component[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteByte(c)
		case c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return filepath.Join(root, b.String())
}

// Baseline is what a component's baseline run established, kept so a resumed
// run derives the identical per-mutant budget and mutates the identical lines
// without running the suite again.
type Baseline struct {
	// Passed is whether the suite passed unmutated.
	Passed bool `json:"passed"`
	// Elapsed is how long the baseline took, which the per-mutant timeout is
	// derived from.
	Elapsed time.Duration `json:"elapsed_ns"`
	// MaxRSS is the baseline's own peak resident set in bytes, which the
	// per-mutant memory ceiling is derived from.
	MaxRSS int64 `json:"max_rss"`
	// Executed is every line the suite ran, per file, with its hit count. It
	// selects where mutants go, so a resumed run that re-measured it could
	// want a different set of mutants from the verdicts it is resuming.
	Executed map[string]map[int]int `json:"executed,omitempty"`
	// MemoryUnbounded reports that a memory ceiling was asked for and could
	// not be set.
	MemoryUnbounded bool `json:"memory_unbounded,omitempty"`
}

// State is one component's mutation state: the baseline it measured and every
// verdict decided under one fingerprint.
//
// It is a cache, not a ledger. Losing it costs the time spent measuring what
// it held and nothing else, so every failure is returned for the caller to
// report and none of them is fatal. Nothing is synced to disk: a verdict lost
// to a crash is re-measured, which is what the cache exists to save and not
// what it exists to guarantee.
type State struct {
	dir string

	mu       sync.Mutex
	verdicts *os.File
}

// OpenState opens the state kept in dir under fingerprint, creating it where
// there is none.
//
// A state recorded under any other fingerprint is discarded: a component keeps
// only its latest state, so there is never anything to prune. The old
// verdicts and baseline are removed before the new fingerprint is written, so
// an interrupted truncation leaves a state that still mismatches and is
// truncated again, never old verdicts under a new fingerprint.
func OpenState(dir, fingerprint string) (*State, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("creating the mutation state %s: %w", dir, err)
	}
	recorded, err := os.ReadFile(filepath.Join(dir, stateFingerprintFile)) // #nosec G304 -- a fixed basename inside the state directory the caller named
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading the mutation state's fingerprint: %w", err)
	}
	if err != nil || string(recorded) != fingerprint {
		for _, name := range []string{stateVerdictsFile, stateBaselineFile} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("discarding the mutation state's %s: %w", name, err)
			}
		}
		if err := writeReplacing(filepath.Join(dir, stateFingerprintFile), []byte(fingerprint)); err != nil {
			return nil, fmt.Errorf("writing the mutation state's fingerprint: %w", err)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, stateVerdictsFile), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600) // #nosec G304 -- a fixed basename inside the state directory the caller named
	if err != nil {
		return nil, fmt.Errorf("opening the mutation state's verdicts: %w", err)
	}
	if err := terminateTornLine(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("opening the mutation state's verdicts: %w", err)
	}
	return &State{dir: dir, verdicts: f}, nil
}

// terminateTornLine ends a file whose last write was interrupted with a
// newline. Without it, the next verdict appended would continue the torn line
// and be unreadable with it; with it, the fragment is a line of its own that
// reading skips.
func terminateTornLine(f *os.File) error {
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return err
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil || last[0] == '\n' {
		return err
	}
	_, err = f.Write([]byte{'\n'})
	return err
}

// Dir is the directory the state is kept in.
func (s *State) Dir() string { return s.dir }

// verdictLine is one recorded verdict as it is written: a Result, flattened,
// with every field a later run needs to reproduce it.
type verdictLine struct {
	Path            string   `json:"path"`
	Line            int      `json:"line"`
	Column          int      `json:"column"`
	Operator        Operator `json:"operator"`
	Offset          int      `json:"offset"`
	Length          int      `json:"length"`
	Original        string   `json:"original"`
	Mutated         string   `json:"mutated"`
	Reason          string   `json:"reason,omitempty"`
	Outcome         Outcome  `json:"outcome"`
	Detail          string   `json:"detail,omitempty"`
	MemoryUnbounded bool     `json:"memory_unbounded,omitempty"`
	OutputHeldOpen  bool     `json:"output_held_open,omitempty"`
}

func toLine(r Result) verdictLine {
	m := r.Mutant
	return verdictLine{
		Path: m.Path, Line: m.Line, Column: m.Column, Operator: m.Operator,
		Offset: m.Offset, Length: m.Length, Original: m.Original, Mutated: m.Mutated, Reason: m.Reason,
		Outcome: r.Outcome, Detail: r.Detail, MemoryUnbounded: r.MemoryUnbounded, OutputHeldOpen: r.OutputHeldOpen,
	}
}

func (l verdictLine) result() Result {
	return Result{
		Mutant: Mutant{
			Path: l.Path, Line: l.Line, Column: l.Column, Operator: l.Operator,
			Offset: l.Offset, Length: l.Length, Original: l.Original, Mutated: l.Mutated, Reason: l.Reason,
		},
		Outcome: l.Outcome, Detail: l.Detail, MemoryUnbounded: l.MemoryUnbounded, OutputHeldOpen: l.OutputHeldOpen,
	}
}

// knownOutcome reports whether o is a verdict this build decides. A line
// carrying anything else is not reused, since a later run would otherwise
// count a mutant under an outcome no Summary has a column for.
func knownOutcome(o Outcome) bool {
	switch o {
	case Killed, TimedOut, OutOfMemory, Survived, Unviable, Acknowledged:
		return true
	}
	return false
}

// Record appends one verdict, as one line, in one write. It is safe to call
// from every worker at once: the writes are serialised here, and the file is
// opened for appending so each lands whole after the last.
func (s *State) Record(r Result) error {
	if !knownOutcome(r.Outcome) {
		return fmt.Errorf("recording %s: %q is not a verdict", r.Mutant, r.Outcome)
	}
	data, err := json.Marshal(toLine(r))
	if err != nil {
		return fmt.Errorf("recording %s: %w", r.Mutant, err)
	}
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.verdicts == nil {
		return fmt.Errorf("recording %s: the mutation state is closed", r.Mutant)
	}
	if _, err := s.verdicts.Write(data); err != nil {
		return fmt.Errorf("recording %s: %w", r.Mutant, err)
	}
	return nil
}

// Verdicts is every verdict recorded under this state's fingerprint, keyed by
// MutantID. Where one mutant was recorded twice the later line wins.
//
// A line that does not parse is skipped rather than failing the read: the
// last line of a run killed mid-write is torn, and a cache answering nothing
// because one line of it is unreadable would discard every verdict beside it.
func (s *State) Verdicts() (map[string]Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(filepath.Join(s.dir, stateVerdictsFile)) // #nosec G304 -- a fixed basename inside the state directory the caller named
	if err != nil {
		return nil, fmt.Errorf("reading the mutation state's verdicts: %w", err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]Result{}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			// A final line with no newline is one whose write never finished.
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading the mutation state's verdicts: %w", err)
		}
		var v verdictLine
		if json.Unmarshal(bytes.TrimSpace(line), &v) != nil || !knownOutcome(v.Outcome) {
			continue
		}
		res := v.result()
		out[MutantID(res.Mutant)] = res
	}
}

// SaveBaseline replaces the recorded baseline. It is written to a temporary
// file and renamed over the old one, so a reader finds the previous baseline
// or this one and never half of either.
func (s *State) SaveBaseline(b Baseline) error {
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("recording the baseline: %w", err)
	}
	if err := writeReplacing(filepath.Join(s.dir, stateBaselineFile), append(data, '\n')); err != nil {
		return fmt.Errorf("recording the baseline: %w", err)
	}
	return nil
}

// Baseline is the recorded baseline, and false where none was recorded under
// this state's fingerprint.
func (s *State) Baseline() (Baseline, bool, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, stateBaselineFile)) // #nosec G304 -- a fixed basename inside the state directory the caller named
	if errors.Is(err, fs.ErrNotExist) {
		return Baseline{}, false, nil
	}
	if err != nil {
		return Baseline{}, false, fmt.Errorf("reading the recorded baseline: %w", err)
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return Baseline{}, false, fmt.Errorf("reading the recorded baseline: %w", err)
	}
	return b, true, nil
}

// Close releases the verdicts file. A Record after it returns an error.
func (s *State) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.verdicts == nil {
		return nil
	}
	err := s.verdicts.Close()
	s.verdicts = nil
	return err
}

// writeReplacing writes data to a temporary file beside path and renames it
// over path.
func writeReplacing(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	_, writeErr := tmp.Write(data)
	if err = errors.Join(writeErr, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(name, path)
}
