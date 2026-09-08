// Package ledger is the quality history lydite keeps on the state branch: one
// record per recorded commit, appended and never rewritten.
//
// It is a different policy from the baselines stored beside it, and the terms
// exist to keep the difference drawn. A baseline is a CACHE: regenerable by
// re-running the tool over the same tree, so a write that never lands costs a
// slower run and no information, and an entry may be overwritten by a better
// measurement of the same tree. A ledger is not recomputable at all — the pins
// that produced a number may have moved since, and after a squash merge the
// commit it describes no longer exists — so a record is appended, never
// deleted, and never edited.
//
// Writes are still non-fatal. Failing a consumer's build over a push race
// would erode trust in a gate that is otherwise about their code. The
// invariant is therefore not that there are no gaps but that THE LEDGER NEVER
// LIES ABOUT ITS OWN COMPLETENESS: every record names the commit's first
// parent, so a reader that finds a record whose parent is not the previous
// record's commit has found a break without needing anything but the file it
// is already reading. A writer that can see the break — it has the repository,
// so it can count what lies between — additionally appends an explicit Gap
// record saying how wide it is.
//
// That two-part answer is what makes a gap recordable at all. The obvious
// mechanism, a sequence number consumed per append, cannot work: the append
// that would have consumed one is the append that failed, so nothing wrote and
// nothing was consumed. The parent chain is not written by the failing run at
// all — it is written by the NEXT successful one, out of git history, which is
// the one input a failed write cannot have damaged.
//
// # Layout
//
// Every file is independently fetchable under the GitHub Contents API's 1 MB
// cap, because the dashboard reads this branch directly with the viewer's own
// credentials and has no other way in.
//
//	history/v1/2026-09.ndjson          the month's records, one JSON object per line
//	history/v1/2026-09.1.ndjson        the same month, once the first part is full
//	history/v1/daily/main/2026.ndjson  the downsampled projection, per branch
//
// A record's month comes from the COMMIT's own date and never from the clock
// at append time, so a re-run of a recording lands in the file the first run
// would have used and the deduplication below can find it. A CI job that ran
// hours late does not scatter a commit across two partitions.
//
// The projection is a daily rollup, one row per day, holding the last record of
// that day. It exists so the dashboard reads one small file instead of walking
// every partition. It is derived, so it may be rebuilt from the records at any
// time; nothing reads it back to decide what to write.
package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lydite/lydite/internal/junit"
)

// Dir is the directory, inside the state branch, that the history lives in.
//
// Versioned like the baseline directories, and for a related but weaker
// reason. A baseline directory version says "entries here are not comparable
// to entries there", and a gained field bumps it because an entry lacking that
// field would still read as a cache hit. Nothing here is ever compared against
// a stored entry, so a gained field costs a series that starts on the day it
// was added — which is what an append-only ledger is for, and is rendered
// honestly by the same machinery that renders a gap. The version is for a
// change that would make an OLD record unreadable, or mean something else.
const Dir = "history/v1"

// projectionDir holds the daily rollup, one file per branch per calendar year.
//
// Per branch and not one file for all of them, because a day's row carries a
// scalar per component and a repository recording on several branches would
// walk a shared file towards the same 1 MB cap the partitions have a roll rule
// for — while a per-branch file is bounded by a year of days against one
// declaration, which is a few hundred kilobytes at any component count anyone
// has. It is also what the dashboard wants: history is per branch, so a chart
// reads exactly the branch it is drawing rather than filtering the rest out.
//
// Per year rather than per month because that is what makes the bound hold
// without a rule to enforce it, and a chart of five years then reads five
// files instead of sixty.
//
// A branch name is a path here, which is safe by construction: git stores refs
// as paths, so no branch name can be both a file and a directory in this tree
// any more than it can be in .git/refs.
const projectionDir = Dir + "/daily"

// projectionFile is the rollup a record belongs in.
func projectionFile(branch string, at time.Time) string {
	return projectionDir + "/" + branch + "/" + at.UTC().Format("2006") + ".ndjson"
}

// maxPartitionBytes is when a month rolls to a numbered sibling.
//
// The cap that matters is the GitHub Contents API's 1 MB, which is what the
// dashboard fetches a file through; this leaves headroom so the record that
// crosses the line is written to the next part rather than being the one that
// makes a file unfetchable. A month is the granularity ADR 0009 chose and it
// suffices for every repository anyone has: at roughly 300 bytes a record, a
// month holds around 3,000 recordings. The roll is what a busier repository
// does — the partition stays a month, and the month grows parts.
const maxPartitionBytes = 900 * 1024

// lookbackMonths is how far back a writer looks for the branch's previous
// record before giving up on saying how wide a gap is.
//
// Bounded because the walk is the only unbounded thing here, and its purpose
// is bounded too: a branch with no record in a year is a branch adopting the
// ledger, not one with a gap somebody wants measured. Giving up produces no
// gap record rather than a wrong one — the parent chain in the records still
// says a break is there.
const lookbackMonths = 12

// Kind is what a line in a partition is.
type Kind string

const (
	// KindEntry is a recording of one commit's scalars.
	KindEntry Kind = "entry"
	// KindGap is the writer saying that records between two commits are
	// missing and will never arrive.
	KindGap Kind = "gap"
)

// Record is one line of a partition.
type Record struct {
	Kind Kind `json:"kind"`
	// At is the commit's own date, in UTC. The clock at append time is
	// deliberately not used: it would put a re-recording in a different
	// partition from the recording it repeats, where the deduplication cannot
	// see it, and would make a delayed CI job look like a delayed commit.
	At time.Time `json:"at"`
	// Commit is the commit this record describes, and is its identity: an
	// append that finds this commit already recorded writes nothing. A commit
	// and not a tree, which is what a baseline is keyed by and for a reason
	// that does not carry over — a baseline answers "what was measured for
	// this content", so a pull request and the commit it becomes share one
	// deliberately, while history is a sequence of events and two commits with
	// the same tree are two points on the line.
	Commit string `json:"commit"`
	// Parent is the commit's first parent, and is what makes the file
	// self-checking: a record whose parent is not the previous record's commit
	// is a break, whether or not the Gap record that should sit between them
	// was ever written.
	Parent string `json:"parent,omitempty"`
	// Tree is what the commit points at, so a record can be joined to the
	// baseline recorded for the same content.
	Tree string `json:"tree,omitempty"`
	// Branch is what the recording ran on. History is per branch: a line
	// drawn across every branch at once is not a line anybody can read.
	Branch string `json:"branch"`
	// Components is each component's scalars, absent on a gap.
	//
	// Per component and not per repository, because the component is the unit
	// every gate lydite has reports at, and a repository-wide figure cannot
	// answer which component moved. The repository-wide figures are sums over
	// these and are deliberately not stored beside them: two quantities that
	// must agree are two quantities free to disagree.
	Components map[string]Component `json:"components,omitempty"`
	// Gap says how wide the break before this record is, and is set only on a
	// gap record.
	Gap *Gap `json:"gap,omitempty"`
}

// Component is one component's scalars for one commit.
//
// Every metric is a pointer, so absent and zero are different answers. They
// have to be: a component with no function above the CRAP threshold records
// zero, and one lydite computes no score for records nothing — and a series
// that read the second as the first would draw a clean line through a metric
// nobody measured.
type Component struct {
	// Coverage is the component's line counts.
	Coverage *Lines `json:"coverage,omitempty"`
	// CRAP is how many of its functions sit above the threshold, and the
	// worst of them.
	CRAP *CRAP `json:"crap,omitempty"`
	// Tests is what became of its suite's tests.
	Tests *junit.Counts `json:"tests,omitempty"`
	// Producer names the instrument that measured the coverage, for the
	// reason a baseline entry carries one: a runner or provider bump changes
	// what a line is, so a step in the line is a change of definition rather
	// than a change in the code. A ledger cannot refuse the comparison the
	// way a gate does — it records both sides — so it records what made them.
	Producer string `json:"producer,omitempty"`
}

// Lines is a covered-of-total count.
type Lines struct {
	Covered int `json:"covered"`
	Total   int `json:"total"`
}

// CRAP is the two complexity scalars, exactly as the baseline stores them.
type CRAP struct {
	Above int     `json:"above"`
	Worst float64 `json:"worst"`
}

// Gap is a break in the history, and what the writer could establish about it.
type Gap struct {
	// From is the newest recorded ancestor, and is empty when the writer
	// could not find one.
	From string `json:"from,omitempty"`
	// Missing is how many commits lie strictly between From and the record's
	// own commit, along the first-parent chain. Zero when unknown, which
	// Reason then says.
	Missing int `json:"missing,omitempty"`
	// Reason says what the writer could and could not establish, in the words
	// a reader of the branch gets rather than a code they would have to look
	// up. A gap is the one record whose whole content is an explanation.
	Reason string `json:"reason"`
}

// Append writes recs into the history under root, skipping any whose commit
// and kind are already recorded, and updates the projection.
//
// It returns the paths it wrote — relative to root and slash-separated, so the
// caller can stage exactly those — and the records that actually landed.
// Nothing is written for a record already present: a recording repeated (a
// retried push, a re-run workflow, a second invocation) must leave one line,
// not two, and the commit is what says which. The second return is what lets a
// caller say "already recorded" rather than claiming an append that was a
// no-op.
func Append(root string, recs []Record) ([]string, []Record, error) {
	written := map[string]bool{}
	var landed []Record
	for _, rec := range recs {
		if rec.Commit == "" || rec.Branch == "" {
			return nil, nil, fmt.Errorf("a %s record names no commit or no branch, so nothing could read it back", rec.Kind)
		}
		if !validBranch(rec.Branch) {
			return nil, nil, fmt.Errorf("a %s record names branch %q, which is not a ref path", rec.Kind, rec.Branch)
		}
		have, err := Recorded(root, rec)
		if err != nil {
			return nil, nil, err
		}
		if have {
			continue
		}
		part, err := appendRecord(root, rec)
		if err != nil {
			return nil, nil, err
		}
		written[part] = true
		proj, err := project(root, rec)
		if err != nil {
			return nil, nil, err
		}
		written[proj] = true
		landed = append(landed, rec)
	}
	out := make([]string, 0, len(written))
	for p := range written {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, landed, nil
}

// validBranch reports whether a branch name may be used as the path segment
// the projection files it under.
//
// Enforced here rather than left to the caller, because the claim that a
// branch name is safe as a path is a property of git's ref format and this
// package's callers are not all git. A `..` segment, an absolute name or an
// empty segment would each place the projection outside the directory it
// belongs in — which for a writer holding a token that can push is not a
// property to take on trust.
func validBranch(branch string) bool {
	if strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return false
	}
	for _, segment := range strings.Split(branch, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// Recorded reports whether an identical record is already on the branch.
//
// Identity is the commit, the branch and the kind. All three: the same commit
// is a point on every branch that carries it, so a release branch cut from the
// default one records it again and must not be silently deduplicated against
// the other line — and an entry and the gap discovered at the same commit are
// two records, since a gap always names the very commit whose entry sits
// beside it.
//
// The month and not the whole history, and that is exact rather than an
// approximation: a record's partition is a function of the commit's own date,
// so a repeat of it resolves to the same month whenever it is attempted.
func Recorded(root string, want Record) (bool, error) {
	parts, err := partsFor(root, want.At)
	if err != nil {
		return false, err
	}
	for _, p := range parts {
		found, err := scan(filepath.Join(root, filepath.FromSlash(p)), func(rec Record) bool {
			return rec.Commit == want.Commit && rec.Branch == want.Branch && rec.Kind == want.Kind
		})
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// Latest is the newest entry recorded for branch at or before at, looking back
// at most lookbackMonths.
//
// Newest by the record's own timestamp and not by its position in the file:
// two recordings can land out of order, and the question this answers is which
// commit the branch was last known at.
func Latest(root, branch string, at time.Time) (Record, bool) {
	// Anchored to the first of the month before stepping. AddDate normalises
	// an out-of-range day, so walking back from the 31st gives 2026-02-31 →
	// 2026-03-03: the same month twice, and one month fewer than the lookback
	// says. A commit's date is whatever day it happens to fall on.
	month := time.Date(at.UTC().Year(), at.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for range lookbackMonths {
		parts, err := partsFor(root, month)
		if err != nil {
			return Record{}, false
		}
		var best Record
		found := false
		for _, p := range parts {
			_, _ = scan(filepath.Join(root, filepath.FromSlash(p)), func(rec Record) bool {
				if rec.Kind != KindEntry || rec.Branch != branch || rec.At.After(at) {
					return false
				}
				if !found || rec.At.After(best.At) {
					best, found = rec, true
				}
				return false
			})
		}
		if found {
			return best, true
		}
		month = month.AddDate(0, -1, 0)
	}
	return Record{}, false
}

// appendRecord adds one line to the month's current part, rolling to the next
// when the line would take that part over the cap.
func appendRecord(root string, rec Record) (string, error) {
	line, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	line = append(line, '\n')

	parts, err := partsFor(root, rec.At)
	if err != nil {
		return "", err
	}
	target := partitionName(rec.At, 0)
	if len(parts) > 0 {
		target = parts[len(parts)-1]
		size, err := sizeOf(filepath.Join(root, filepath.FromSlash(target)))
		if err != nil {
			return "", err
		}
		// Rolled before the write and never after it, so no part is ever over
		// the cap even momentarily — a reader fetching between the two would
		// otherwise get a file the Contents API refuses to serve.
		if size+len(line) > maxPartitionBytes {
			target = partitionName(rec.At, len(parts))
		}
	}
	return target, appendLine(filepath.Join(root, filepath.FromSlash(target)), line)
}

// partitionName is the file a record of this month and part number lands in.
// Part 0 carries no number, so the common case reads as the month it is.
func partitionName(at time.Time, part int) string {
	name := at.UTC().Format("2006-01")
	if part > 0 {
		name = fmt.Sprintf("%s.%d", name, part)
	}
	return Dir + "/" + name + ".ndjson"
}

// partsFor lists the existing parts of a month, in order.
//
// Contiguous from part 0: a hole would mean a part was deleted, which nothing
// here does, and stopping at it is what keeps a later part from being read as
// the current one.
func partsFor(root string, at time.Time) ([]string, error) {
	var parts []string
	for i := 0; ; i++ {
		name := partitionName(at, i)
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
		if errors.Is(err, os.ErrNotExist) {
			return parts, nil
		}
		if err != nil {
			return nil, err
		}
		parts = append(parts, name)
	}
}

// project updates the daily rollup for rec's day and branch.
//
// The rollup holds the last record of the day, which is what the repository
// looked like when that day ended — a mean over the day would be a number no
// commit ever had. The counts beside it are what makes the downsample honest:
// a row saying it summarises nine records and one gap is a row a reader can
// tell from a quiet day.
func project(root string, rec Record) (string, error) {
	file := projectionFile(rec.Branch, rec.At)
	path := filepath.Join(root, filepath.FromSlash(file))
	rows, err := readRollup(path)
	if err != nil {
		return "", err
	}
	day := rec.At.UTC().Format("2006-01-02")
	i := -1
	for n, row := range rows {
		if row.Day == day {
			i = n
			break
		}
	}
	if i < 0 {
		rows = append(rows, Rollup{Day: day, Branch: rec.Branch})
		i = len(rows) - 1
	}
	row := rows[i]
	switch rec.Kind {
	case KindGap:
		row.Gaps++
	default:
		row.Entries++
	}
	// Later by the record's own timestamp, so a recording that arrives out of
	// order does not overwrite a newer one with an older commit's numbers.
	if rec.Kind == KindEntry && !rec.At.Before(row.At) {
		row.At, row.Commit, row.Components = rec.At, rec.Commit, rec.Components
	}
	rows[i] = row
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].Day < rows[b].Day })
	return file, writeRollup(path, rows)
}

// Rollup is one day of one branch, as the projection stores it.
//
// It names its branch even though the file it lives in is that branch's, so a
// row read on its own says what it is about. The path is where a reader looks;
// the field is what a row means.
type Rollup struct {
	Day    string `json:"day"`
	Branch string `json:"branch"`
	// At and Commit are the last entry of the day, and are what the row's
	// components describe.
	At         time.Time            `json:"at"`
	Commit     string               `json:"commit,omitempty"`
	Components map[string]Component `json:"components,omitempty"`
	// Entries and Gaps are how many records the day held, so a downsampled
	// row still says how much it is standing in for.
	Entries int `json:"entries"`
	Gaps    int `json:"gaps,omitempty"`
}

func readRollup(path string) ([]Rollup, error) {
	f, err := os.Open(path) // #nosec G304 -- a path this package composed under a caller-named root
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var rows []Rollup
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), maxPartitionBytes)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var row Rollup
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			// A projection is derived, so an unreadable line is not
			// information anybody loses: it is dropped and rebuilt from the
			// records the next time that day is touched. The records
			// themselves are never treated this way.
			continue
		}
		rows = append(rows, row)
	}
	return rows, s.Err()
}

func writeRollup(path string, rows []Rollup) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	var b strings.Builder
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// scan reads a partition line by line, stopping at the first line the visitor
// accepts.
//
// A line that will not parse is skipped rather than failing the read. A
// partition is append-only and nothing rewrites it, so a line that cannot be
// parsed is one a future version wrote or one somebody hand-edited — and
// refusing the whole file over it would stop every later append to that month,
// permanently, which is a much larger hole than the one line.
func scan(path string, visit func(Record) bool) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- a path this package composed under a caller-named root
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), maxPartitionBytes)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if visit(rec) {
			return true, nil
		}
	}
	return false, s.Err()
}

func sizeOf(path string) (int, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return int(fi.Size()), nil
}

// appendLine adds one line to a partition, creating it if this is the month's
// first record.
//
// Append mode, so a partition is only ever extended: the whole policy this
// package exists for is that a record, once written, is not rewritten — and a
// read-modify-write would put every line at the mercy of the one that failed
// to serialise.
func appendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- a path this package composed under a caller-named root
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
