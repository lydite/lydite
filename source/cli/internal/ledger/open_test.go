package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func sampleOpen(n int) OpenFindings {
	o := OpenFindings{Branch: "main", Commit: "abc", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	for i := 0; i < n; i++ {
		o.Findings = append(o.Findings, OpenFinding{Gate: "sast", Component: "c", Path: "a.go", Line: i + 1, Fingerprint: "fp" + strconv.Itoa(i)})
	}
	return o
}

func writeParts(t *testing.T, dir string, parts []OpenPart) {
	t.Helper()
	for _, p := range parts {
		full := filepath.Join(dir, filepath.FromSlash(p.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, p.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenFindingsPath(t *testing.T) {
	for _, c := range []struct {
		branch string
		part   int
		want   string
	}{
		{"main", 0, "snapshot/v1/main/findings.json"},
		{"release/1.x", 0, "snapshot/v1/release/1.x/findings.json"},
		{"release/1.x", 2, "snapshot/v1/release/1.x/findings.2.json"},
	} {
		got, err := OpenFindingsPath(c.branch, c.part)
		if err != nil || got != c.want {
			t.Errorf("OpenFindingsPath(%q, %d) = %q, %v; want %q", c.branch, c.part, got, err, c.want)
		}
	}
	for _, b := range []string{"", "../x", "/x", "a//b", "a/"} {
		if got, err := OpenFindingsPath(b, 0); err == nil || got != "" {
			t.Errorf("branch %q: path %q, err %v; want no path and an error", b, got, err)
		}
	}
	if got, err := OpenFindingsPath("main", -1); err == nil || got != "" {
		t.Errorf("negative part: path %q, err %v; want no path and an error", got, err)
	}
}

func TestEncodeOpenFindingsIsDeterministic(t *testing.T) {
	a := sampleOpen(5)
	b := sampleOpen(5)
	b.Findings[0], b.Findings[4] = b.Findings[4], b.Findings[0]
	b.Findings[1], b.Findings[3] = b.Findings[3], b.Findings[1]
	pa, err := EncodeOpenFindings(a)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := EncodeOpenFindings(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pa, pb) {
		t.Errorf("shuffled input encoded differently")
	}
}

func TestEncodeOpenFindingsEmptyIsOnePart(t *testing.T) {
	parts, err := EncodeOpenFindings(sampleOpen(0))
	if err != nil || len(parts) != 1 {
		t.Fatalf("parts = %d, err %v", len(parts), err)
	}
	dir := t.TempDir()
	writeParts(t, dir, parts)
	got, ok, err := ReadOpenFindings(dir, "main")
	if err != nil || !ok || len(got.Findings) != 0 {
		t.Errorf("read = %+v, %v, %v", got, ok, err)
	}
}

func TestEncodeOpenFindingsSplitsExactlyAtTheCap(t *testing.T) {
	o := sampleOpen(3)
	whole, err := encodeOpenFindings(o, 1<<20)
	if err != nil || len(whole) != 1 {
		t.Fatalf("whole = %d parts, %v", len(whole), err)
	}
	size := len(whole[0].Data)
	at, err := encodeOpenFindings(o, size)
	if err != nil || len(at) != 1 {
		t.Fatalf("at cap: %d parts, %v", len(at), err)
	}
	over, err := encodeOpenFindings(o, size-1)
	if err != nil || len(over) != 2 {
		t.Fatalf("one under cap: %d parts, %v", len(over), err)
	}
	for _, p := range over {
		if len(p.Data) > size-1 {
			t.Errorf("part %s is %d bytes", p.Path, len(p.Data))
		}
	}
	if over[1].Path != "snapshot/v1/main/findings.1.json" {
		t.Errorf("second part at %s", over[1].Path)
	}
}

func TestEncodeOpenFindingsRejectsAFindingTooLargeForAnyPart(t *testing.T) {
	o := sampleOpen(1)
	if _, err := encodeOpenFindings(o, 50); err == nil {
		t.Error("oversized finding accepted")
	}
}

func TestReadOpenFindingsRoundTrip(t *testing.T) {
	o := sampleOpen(12)
	parts, err := encodeOpenFindings(o, 600)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 2 {
		t.Fatalf("expected a split, got %d parts", len(parts))
	}
	dir := t.TempDir()
	writeParts(t, dir, parts)
	got, ok, err := ReadOpenFindings(dir, "main")
	if err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	if got.Commit != o.Commit || !got.At.Equal(o.At) || !reflect.DeepEqual(got.Findings, o.Findings) {
		t.Errorf("round trip differs: %+v", got)
	}
}

func TestReadOpenFindingsAbsent(t *testing.T) {
	if _, ok, err := ReadOpenFindings(t.TempDir(), "main"); ok || err != nil {
		t.Errorf("no snapshot: ok=%v err=%v", ok, err)
	}
	if _, _, err := ReadOpenFindings(t.TempDir(), "../x"); err == nil {
		t.Error("invalid branch accepted")
	}
}

func TestReadOpenFindingsTornSetIsAbsent(t *testing.T) {
	parts, err := encodeOpenFindings(sampleOpen(12), 400)
	if err != nil || len(parts) < 3 {
		t.Fatalf("parts %d, %v", len(parts), err)
	}
	other := sampleOpen(12)
	other.Commit = "def"
	otherParts, err := encodeOpenFindings(other, 400)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(dir string){
		"disagreeing commit": func(dir string) { writeParts(t, dir, []OpenPart{otherParts[1]}) },
		"missing part": func(dir string) {
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(parts[1].Path))); err != nil {
				t.Fatal(err)
			}
		},
		"part index mismatch": func(dir string) {
			writeParts(t, dir, []OpenPart{{Path: parts[1].Path, Data: parts[2].Data}})
		},
		"unparseable part": func(dir string) { writeParts(t, dir, []OpenPart{{Path: parts[1].Path, Data: []byte("{")}}) },
	}
	for name, tear := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeParts(t, dir, parts)
			tear(dir)
			if _, ok, err := ReadOpenFindings(dir, "main"); ok || err != nil {
				t.Errorf("ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestEncodeOpenFindingsOrdersOnEveryKey(t *testing.T) {
	base := OpenFinding{Gate: "g", Component: "c", Path: "p", Line: 5, Ordinal: 5, Fingerprint: "f"}
	vary := map[string]func(f *OpenFinding, high bool){
		"gate":        func(f *OpenFinding, h bool) { f.Gate = pick(h, "g1", "g2") },
		"component":   func(f *OpenFinding, h bool) { f.Component = pick(h, "c1", "c2") },
		"path":        func(f *OpenFinding, h bool) { f.Path = pick(h, "p1", "p2") },
		"line":        func(f *OpenFinding, h bool) { f.Line = map[bool]int{false: 1, true: 2}[h] },
		"ordinal":     func(f *OpenFinding, h bool) { f.Ordinal = map[bool]int{false: 1, true: 2}[h] },
		"fingerprint": func(f *OpenFinding, h bool) { f.Fingerprint = pick(h, "f1", "f2") },
	}
	for name, set := range vary {
		t.Run(name, func(t *testing.T) {
			lo, hi := base, base
			set(&lo, false)
			set(&hi, true)
			o := OpenFindings{Branch: "main", Commit: "abc", Findings: []OpenFinding{hi, lo}}
			parts, err := EncodeOpenFindings(o)
			if err != nil || len(parts) != 1 {
				t.Fatalf("parts %d, %v", len(parts), err)
			}
			dir := t.TempDir()
			writeParts(t, dir, parts)
			got, ok, err := ReadOpenFindings(dir, "main")
			if err != nil || !ok {
				t.Fatalf("read: %v %v", ok, err)
			}
			if !reflect.DeepEqual(got.Findings, []OpenFinding{lo, hi}) {
				t.Errorf("order = %+v, want %+v then %+v", got.Findings, lo, hi)
			}
		})
	}
}

func pick(high bool, lo, hi string) string {
	if high {
		return hi
	}
	return lo
}

func TestEncodeOpenFindingsRejectsWhatCannotBeEncoded(t *testing.T) {
	badBranch := sampleOpen(1)
	badBranch.Branch = "../x"
	if _, err := EncodeOpenFindings(badBranch); err == nil {
		t.Error("a branch that is no snapshot path was encoded")
	}
	badTime := sampleOpen(1)
	badTime.At = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := EncodeOpenFindings(badTime); err == nil {
		t.Error("a time JSON cannot hold was encoded")
	}
}

func TestReadOpenFindingsFromAWorktreeThatDoesNotExistIsAbsent(t *testing.T) {
	if _, ok, err := ReadOpenFindings(filepath.Join(t.TempDir(), "missing"), "main"); ok || err != nil {
		t.Errorf("missing worktree: ok=%v err=%v", ok, err)
	}
}

func TestEncodeOpenFindingsHeaderDigitsCountTowardsTheCap(t *testing.T) {
	o := sampleOpen(40)
	parts, err := encodeOpenFindings(o, 330)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 10 {
		t.Fatalf("expected a split into two-digit part indexes, got %d parts", len(parts))
	}
	for _, p := range parts {
		if len(p.Data) > 330 {
			t.Errorf("part %s is %d bytes, over the cap", p.Path, len(p.Data))
		}
	}
}

func TestEncodeOpenFindingsFillsEverySingleDigitPartExactlyToTheCap(t *testing.T) {
	o := OpenFindings{Branch: "main", Commit: "abc", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	for i := 0; i < 60; i++ {
		o.Findings = append(o.Findings, OpenFinding{Gate: "sast", Component: "c", Path: "a.go", Line: 1000 + i, Fingerprint: "fp" + strconv.Itoa(100+i)})
	}
	encoded, err := json.Marshal(o.Findings[0])
	if err != nil {
		t.Fatal(err)
	}
	const k = 3
	// A part whose index has one digit, in a set of two-digit part count.
	limit := openBase(o, 0, 11) + k*len(encoded) + (k - 1)
	parts, err := encodeOpenFindings(o, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 11 {
		t.Fatalf("got %d parts, want at least 11", len(parts))
	}
	for i, p := range parts {
		if len(p.Data) > limit {
			t.Errorf("part %d is %d bytes, over the %d byte cap", i, len(p.Data), limit)
		}
		if i >= 10 {
			continue
		}
		if len(p.Data) != limit {
			t.Errorf("part %d is %d bytes, want exactly %d", i, len(p.Data), limit)
		}
		var doc openDocument
		if err := json.Unmarshal(p.Data, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Findings) != k {
			t.Errorf("part %d holds %d findings, want %d", i, len(doc.Findings), k)
		}
	}
}

func TestEncodeOpenFindingsAcceptsAFindingThatFillsAPartExactly(t *testing.T) {
	o := sampleOpen(1)
	encoded, err := json.Marshal(o.Findings[0])
	if err != nil {
		t.Fatal(err)
	}
	limit := openBase(o, 0, 1) + len(encoded)
	parts, err := encodeOpenFindings(o, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || len(parts[0].Data) != limit {
		t.Errorf("got %d parts, first %d bytes; want one part of exactly %d", len(parts), len(parts[0].Data), limit)
	}
	if _, err := encodeOpenFindings(o, limit-1); err == nil {
		t.Error("a finding one byte over the cap was accepted")
	}
}

func TestOpenFindingsPartPathIsTheEncodedPath(t *testing.T) {
	o := sampleOpen(12)
	parts, err := encodeOpenFindings(o, 600)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range parts {
		if got := o.PartPath(i); got != p.Path {
			t.Errorf("PartPath(%d) = %q, want %q", i, got, p.Path)
		}
	}
}
