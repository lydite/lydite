package ledger

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"slices"
	"strconv"
	"time"
)

// OpenFinding is one finding still open on a branch, as the open-findings
// snapshot records it. It carries a location and the tool's one-line message,
// never an excerpt of source or any tool text that can quote it (no site, no
// detail): a secret a scanner flagged must not gain a second copy in the
// branch's history. An entry with no known fields is identity only —
// fingerprint, gate and component.
type OpenFinding struct {
	Gate        string `json:"gate"`
	Component   string `json:"component"`
	Path        string `json:"path,omitempty"`
	Line        int    `json:"line,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
	Rule        string `json:"rule,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Message     string `json:"message,omitempty"`
	Ordinal     int    `json:"ordinal,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

// OpenFindings is the open set of one branch at one commit.
type OpenFindings struct {
	Branch   string
	Commit   string
	At       time.Time
	Findings []OpenFinding
}

// OpenPart is one file of an encoded open-findings snapshot: a path relative
// to the worktree root and the document's bytes.
type OpenPart struct {
	Path string
	Data []byte
}

// openHeader is the header every part carries, so a reader can check that the
// parts it fetched belong together.
type openHeader struct {
	Branch string    `json:"branch"`
	Commit string    `json:"commit"`
	At     time.Time `json:"at"`
	Part   int       `json:"part"`
	Parts  int       `json:"parts"`
}

type openDocument struct {
	openHeader
	Findings []OpenFinding `json:"findings"`
}

// OpenFindingsPath is where part n of a branch's open-findings snapshot lives,
// relative to the worktree root: snapshot/v1/<branch>/findings.json for part
// 0 and findings.<n>.json after it. The branch's slashes are directories.
func OpenFindingsPath(branch string, part int) (string, error) {
	if !validBranch(branch) {
		return "", fmt.Errorf("branch %q cannot be used as a snapshot path", branch)
	}
	if part < 0 {
		return "", fmt.Errorf("snapshot part %d is negative", part)
	}
	return openFindingsPath(branch, part), nil
}

// PartPath is where part n of o's snapshot lives, for a caller that has
// already had EncodeOpenFindings accept o and so knows its branch is valid.
func (o OpenFindings) PartPath(part int) string {
	return openFindingsPath(o.Branch, part)
}

// openFindingsPath is OpenFindingsPath for a branch already known valid and a
// part already known non-negative.
func openFindingsPath(branch string, part int) string {
	name := "findings.json"
	if part > 0 {
		name = "findings." + strconv.Itoa(part) + ".json"
	}
	return path.Join("snapshot", "v1", branch, name)
}

// EncodeOpenFindings encodes o as the documents that make up its snapshot,
// in part order, each at the path OpenFindingsPath names. Findings are sorted
// by (gate, component, path, line, ordinal, fingerprint), so the same input
// encodes to the same bytes, and are split so no part exceeds
// maxPartitionBytes. An empty open set is exactly one part with an empty
// findings array, so a branch whose findings all resolved has its file
// rewritten. A single finding too large for any part is an error.
func EncodeOpenFindings(o OpenFindings) ([]OpenPart, error) {
	return encodeOpenFindings(o, maxPartitionBytes)
}

func encodeOpenFindings(o OpenFindings, limit int) ([]OpenPart, error) {
	if !validBranch(o.Branch) {
		return nil, fmt.Errorf("branch %q cannot be used as a snapshot path", o.Branch)
	}
	sorted := append([]OpenFinding(nil), o.Findings...)
	slices.SortStableFunc(sorted, func(a, b OpenFinding) int {
		return cmp.Or(
			cmp.Compare(a.Gate, b.Gate),
			cmp.Compare(a.Component, b.Component),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Ordinal, b.Ordinal),
			cmp.Compare(a.Fingerprint, b.Fingerprint),
		)
	})
	// The one field of a header that can refuse to encode is the time, which
	// JSON limits to the years 0 through 9999.
	if _, err := json.Marshal(o.At); err != nil {
		return nil, fmt.Errorf("open-findings time: %w", err)
	}
	encoded := make([][]byte, len(sorted))
	for i, f := range sorted {
		// A struct of strings and ints always encodes.
		encoded[i], _ = json.Marshal(f)
	}

	// A header states the part count, whose digits count towards the size, so
	// the split is retried until the count it assumed is the count it made.
	parts := 1
	for {
		groups, err := splitOpen(o, encoded, parts, limit)
		if err != nil {
			return nil, err
		}
		if len(groups) != parts {
			parts = len(groups)
			continue
		}
		out := make([]OpenPart, parts)
		for i, g := range groups {
			head := openHeaderJSON(o, i, parts)
			var buf bytes.Buffer
			buf.Write(head[:len(head)-1])
			buf.WriteString(findingsOpen)
			buf.Write(bytes.Join(g, []byte(",")))
			buf.WriteString(findingsClose)
			out[i] = OpenPart{Path: openFindingsPath(o.Branch, i), Data: buf.Bytes()}
		}
		return out, nil
	}
}

const (
	findingsOpen  = `,"findings":[`
	findingsClose = "]}\n"
)

// openHeaderJSON is the header object of part n of parts. The time it carries
// has already been checked to encode.
func openHeaderJSON(o OpenFindings, part, parts int) []byte {
	b, _ := json.Marshal(openHeader{Branch: o.Branch, Commit: o.Commit, At: o.At, Part: part, Parts: parts})
	return b
}

// splitOpen packs encoded findings greedily into parts whose documents, with a
// header naming parts as the total, stay within limit.
func splitOpen(o OpenFindings, encoded [][]byte, parts, limit int) ([][][]byte, error) {
	groups := [][][]byte{nil}
	size := 0
	// The empty document is the header plus `[]` and a newline; each finding
	// adds its bytes, and each after the first a comma. A header differs
	// between parts only in the digits of its part index.
	base := openBase(o, 0, parts)
	for i, e := range encoded {
		add := len(e)
		if len(groups[len(groups)-1]) > 0 {
			add++
		}
		if len(groups[len(groups)-1]) > 0 && base+size+add > limit {
			groups = append(groups, nil)
			size = 0
			base = openBase(o, len(groups)-1, parts)
			add = len(e)
		}
		if base+size+add > limit {
			return nil, fmt.Errorf("open finding %d is %d bytes and fits no part under the %d byte limit", i, len(e), limit)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], e)
		size += add
	}
	return groups, nil
}

// openBase is the size of the document for part n of parts when it holds no
// findings.
func openBase(o OpenFindings, part, parts int) int {
	return len(openHeaderJSON(o, part, parts)) - 1 + len(findingsOpen) + len(findingsClose)
}

// ReadOpenFindings reads the open-findings snapshot of branch from a worktree
// rooted at worktree. It reports false, with no error, when there is no
// snapshot and when the set is torn: a missing or unparseable part, parts that
// disagree on branch, commit, time or count, or a part whose header names a
// different index than its file. A snapshot is a cache, so a set that cannot
// be trusted reads the same as none. An unreadable file is absent too; only an invalid branch is an error.
func ReadOpenFindings(worktree, branch string) (OpenFindings, bool, error) {
	if !validBranch(branch) {
		return OpenFindings{}, false, fmt.Errorf("branch %q cannot be used as a snapshot path", branch)
	}
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return OpenFindings{}, false, nil
	}
	defer func() { _ = root.Close() }()
	var result OpenFindings
	parts := 1
	for i := 0; i < parts; i++ {
		data, err := root.ReadFile(openFindingsPath(branch, i))
		if err != nil {
			return OpenFindings{}, false, nil
		}
		var doc openDocument
		if json.Unmarshal(data, &doc) != nil || doc.Part != i || doc.Branch != branch || doc.Parts < 1 {
			return OpenFindings{}, false, nil
		}
		if i == 0 {
			parts = doc.Parts
			result = OpenFindings{Branch: doc.Branch, Commit: doc.Commit, At: doc.At}
		} else if doc.Commit != result.Commit || doc.Parts != parts || !doc.At.Equal(result.At) {
			return OpenFindings{}, false, nil
		}
		result.Findings = append(result.Findings, doc.Findings...)
	}
	return result, true, nil
}
