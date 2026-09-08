package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/junit"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func entry(commit, parent, branch, when string) Record {
	return Record{
		Kind: KindEntry, At: at(when), Commit: commit, Parent: parent, Branch: branch,
		Components: map[string]Component{"cli": {Coverage: &Lines{Covered: 8, Total: 10}}},
	}
}

// lines reads a partition back as the records it holds, so an assertion is
// about the file on the branch rather than about what Append returned.
func lines(t *testing.T, root, path string) []Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var recs []Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec Record
		// Skipped rather than fatal, the way the package itself reads a
		// partition: a test that plants an unreadable line is asserting the
		// append beneath it, not the helper's own parser.
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		recs = append(recs, rec)
	}
	return recs
}

// The partition is named by the COMMIT's date, never by the clock at append
// time. A CI job that runs hours late — or a re-run days later — must land in
// the file the first attempt would have used, or the deduplication below looks
// in the wrong month and writes the record twice.
func TestARecordLandsInTheMonthItsCommitBelongsTo(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Append(root, []Record{entry("a", "", "main", "2026-03-15T10:00:00Z")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	recs := lines(t, root, "history/v1/2026-03.ndjson")
	if len(recs) != 1 || recs[0].Commit != "a" {
		t.Fatalf("2026-03.ndjson = %+v, want the one record for commit a", recs)
	}
}

// A repeated recording leaves one line. A retried push, a re-run workflow and
// a second `record` invocation all describe the same commit, and a ledger that
// double-counted them would show a day with twice the recordings it had.
func TestARecordAlreadyPresentIsNotAppendedAgain(t *testing.T) {
	root := t.TempDir()
	rec := entry("a", "", "main", "2026-03-15T10:00:00Z")
	for range 3 {
		if _, _, err := Append(root, []Record{rec}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if got := lines(t, root, "history/v1/2026-03.ndjson"); len(got) != 1 {
		t.Errorf("the partition holds %d records, want 1", len(got))
	}
	rollup := readRollupOrFail(t, root, "history/v1/daily/main/2026.ndjson")
	if len(rollup) != 1 || rollup[0].Entries != 1 {
		t.Errorf("the projection = %+v, want one row counting one entry", rollup)
	}
}

// A gap and an entry for the same commit are two different records, so the
// deduplication must separate them by kind: the gap the next successful append
// writes always names the very commit whose entry sits beside it.
func TestAGapAndAnEntryForOneCommitAreBothKept(t *testing.T) {
	root := t.TempDir()
	gap := Record{Kind: KindGap, At: at("2026-03-15T10:00:00Z"), Commit: "b", Branch: "main",
		Gap: &Gap{From: "a", Missing: 2, Reason: "two were never recorded"}}
	if _, _, err := Append(root, []Record{gap, entry("b", "x", "main", "2026-03-15T10:00:00Z")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	recs := lines(t, root, "history/v1/2026-03.ndjson")
	if len(recs) != 2 || recs[0].Kind != KindGap || recs[1].Kind != KindEntry {
		t.Fatalf("the partition = %+v, want the gap then the entry", recs)
	}
	if recs[0].Gap == nil || recs[0].Gap.Missing != 2 {
		t.Errorf("the gap record = %+v, want it to carry its width", recs[0].Gap)
	}
}

// The month is the partition ADR 0009 chose and a busier repository grows
// parts within it rather than a finer granularity, so that every file stays
// independently fetchable under the Contents API's 1 MB cap.
func TestAFullMonthRollsToTheNextPart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "history", "v1", "2026-03.ndjson")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	// Just under the cap, so the next record cannot fit.
	filler := strings.Repeat("{}\n", (maxPartitionBytes-10)/3)
	if err := os.WriteFile(path, []byte(filler), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Append(root, []Record{entry("a", "", "main", "2026-03-15T10:00:00Z")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if recs := lines(t, root, "history/v1/2026-03.1.ndjson"); len(recs) != 1 || recs[0].Commit != "a" {
		t.Fatalf("2026-03.1.ndjson = %+v, want the record that did not fit", recs)
	}
	// Rolled BEFORE the write, never after: a part that goes over the cap and
	// is only rolled next time is a file the Contents API refuses to serve for
	// as long as it is the newest one.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxPartitionBytes {
		t.Errorf("the first part is %d bytes, over the %d cap", info.Size(), maxPartitionBytes)
	}
}

func TestARecordIsFoundInALaterPartOfTheSameMonth(t *testing.T) {
	root := t.TempDir()
	write(t, root, "history/v1/2026-03.ndjson", "{}\n")
	rec := entry("a", "", "main", "2026-03-15T10:00:00Z")
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "history/v1/2026-03.1.ndjson", string(line)+"\n")
	if _, _, err := Append(root, []Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := lines(t, root, "history/v1/2026-03.1.ndjson"); len(got) != 1 {
		t.Errorf("the record was appended again into part 1: %+v", got)
	}
}

// The projection is what the dashboard reads instead of walking every
// partition, so a day's row must be the state at the end of that day and must
// say how many records it is standing in for.
func TestTheProjectionKeepsTheLastRecordOfEachDay(t *testing.T) {
	root := t.TempDir()
	morning := entry("a", "", "main", "2026-03-15T08:00:00Z")
	evening := entry("b", "a", "main", "2026-03-15T20:00:00Z")
	evening.Components = map[string]Component{"cli": {Coverage: &Lines{Covered: 9, Total: 10}}}
	next := entry("c", "b", "main", "2026-03-16T09:00:00Z")
	if _, _, err := Append(root, []Record{morning, evening, next}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	rows := readRollupOrFail(t, root, "history/v1/daily/main/2026.ndjson")
	if len(rows) != 2 {
		t.Fatalf("the projection has %d rows, want one per day", len(rows))
	}
	if rows[0].Day != "2026-03-15" || rows[0].Commit != "b" || rows[0].Entries != 2 {
		t.Errorf("the first day = %+v, want the evening record and a count of 2", rows[0])
	}
	if rows[0].Components["cli"].Coverage.Covered != 9 {
		t.Errorf("the first day kept %+v, want the evening record's coverage", rows[0].Components["cli"].Coverage)
	}
}

// A recording that arrives out of order must not overwrite a newer one with an
// older commit's numbers: the row says what the repository looked like when
// the day ended, and the order records are written in is the order jobs
// happened to finish.
func TestALaterRecordIsNotOverwrittenByAnEarlierOne(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Append(root, []Record{entry("b", "a", "main", "2026-03-15T20:00:00Z")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, _, err := Append(root, []Record{entry("a", "", "main", "2026-03-15T08:00:00Z")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	rows := readRollupOrFail(t, root, "history/v1/daily/main/2026.ndjson")
	if len(rows) != 1 || rows[0].Commit != "b" {
		t.Fatalf("the day's row = %+v, want the later commit b", rows)
	}
	if rows[0].Entries != 2 {
		t.Errorf("the day counted %d entries, want both", rows[0].Entries)
	}
}

// A gap is counted separately from an entry, so a downsampled row still says a
// break happened inside the day it summarises.
func TestTheProjectionCountsGapsApartFromEntries(t *testing.T) {
	root := t.TempDir()
	gap := Record{Kind: KindGap, At: at("2026-03-15T10:00:00Z"), Commit: "b", Branch: "main",
		Gap: &Gap{From: "a", Reason: "unknown"}}
	if _, _, err := Append(root, []Record{gap, entry("b", "x", "main", "2026-03-15T10:00:00Z")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	rows := readRollupOrFail(t, root, "history/v1/daily/main/2026.ndjson")
	if len(rows) != 1 || rows[0].Entries != 1 || rows[0].Gaps != 1 {
		t.Fatalf("the day's row = %+v, want one entry and one gap", rows)
	}
}

// Each branch is its own line, so two branches recording on one day are two
// rows and neither hides the other.
// Each branch gets its own projection file. History is per branch, so a chart
// reads exactly the branch it is drawing — and a shared file would walk
// towards the same 1 MB cap the partitions have a roll rule for, since a day's
// row carries a scalar per component.
func TestTheProjectionKeepsABranchPerFile(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Append(root, []Record{
		entry("a", "", "main", "2026-03-15T08:00:00Z"),
		entry("b", "", "release/1.x", "2026-03-15T09:00:00Z"),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	for _, tc := range []struct{ file, branch, commit string }{
		{"history/v1/daily/main/2026.ndjson", "main", "a"},
		{"history/v1/daily/release/1.x/2026.ndjson", "release/1.x", "b"},
	} {
		rows := readRollupOrFail(t, root, tc.file)
		if len(rows) != 1 || rows[0].Branch != tc.branch || rows[0].Commit != tc.commit {
			t.Errorf("%s = %+v, want the one row for %s", tc.file, rows, tc.branch)
		}
	}
}

func TestLatestIsTheNewestEntryForOneBranch(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Append(root, []Record{
		entry("a", "", "main", "2026-03-15T08:00:00Z"),
		entry("b", "a", "main", "2026-03-15T20:00:00Z"),
		entry("z", "", "release", "2026-03-16T09:00:00Z"),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, ok := Latest(root, "main", at("2026-03-31T00:00:00Z"))
	if !ok || got.Commit != "b" {
		t.Errorf("Latest = %+v (%v), want commit b", got, ok)
	}
	// Another branch's records are not this branch's history: reading them
	// would make a gap appear whenever two branches recorded in turn.
	if got, ok := Latest(root, "unrecorded", at("2026-03-31T00:00:00Z")); ok {
		t.Errorf("Latest for a branch with no records = %+v, want a miss", got)
	}
}

// A branch that was quiet for a few months still has a history, and the
// previous record is what a gap is measured from — so the lookback has to
// cross the month boundary rather than treating the empty partition as the
// start of the line.
func TestLatestLooksBackIntoEarlierMonths(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Append(root, []Record{entry("a", "", "main", "2026-01-04T08:00:00Z")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, ok := Latest(root, "main", at("2026-03-15T10:00:00Z"))
	if !ok || got.Commit != "a" {
		t.Errorf("Latest = %+v (%v), want the record from two months earlier", got, ok)
	}
}

// A gap record is not an entry, so it must never be mistaken for the commit
// the branch was last recorded at — a gap names the commit that DISCOVERED the
// break, and treating it as the previous record would make the next append see
// a contiguous chain that is not there.
func TestLatestIgnoresGapRecords(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Append(root, []Record{
		entry("a", "", "main", "2026-03-15T08:00:00Z"),
		{Kind: KindGap, At: at("2026-03-15T20:00:00Z"), Commit: "c", Branch: "main", Gap: &Gap{Reason: "unknown"}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, ok := Latest(root, "main", at("2026-03-31T00:00:00Z"))
	if !ok || got.Commit != "a" {
		t.Errorf("Latest = %+v (%v), want the entry a rather than the gap", got, ok)
	}
}

// A partition is append-only and nothing rewrites it, so a line this version
// cannot parse is one a future version wrote or one somebody hand-edited.
// Refusing the file over it would stop every later append to that month —
// permanently, and for a much larger span than the one line.
func TestALineThatWillNotParseDoesNotStopTheMonth(t *testing.T) {
	root := t.TempDir()
	write(t, root, "history/v1/2026-03.ndjson", "{\"kind\":\"entry\",\"at\":\"nonsense\"}\nnot json at all\n")
	if _, _, err := Append(root, []Record{entry("a", "", "main", "2026-03-15T10:00:00Z")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	recs := lines(t, root, "history/v1/2026-03.ndjson")
	if len(recs) != 1 || recs[0].Commit != "a" {
		t.Fatalf("the partition parsed back as %+v, want the appended record beneath the unreadable lines", recs)
	}
}

// A record naming no commit could never be found again — not by the
// deduplication, not by a gap check, not by a reader — so it is refused rather
// than written where nothing can act on it.
func TestARecordWithNoIdentityIsRefused(t *testing.T) {
	root := t.TempDir()
	for _, rec := range []Record{
		{Kind: KindEntry, At: at("2026-03-15T10:00:00Z"), Branch: "main"},
		{Kind: KindEntry, At: at("2026-03-15T10:00:00Z"), Commit: "a"},
	} {
		if _, _, err := Append(root, []Record{rec}); err == nil {
			t.Errorf("Append accepted %+v", rec)
		}
	}
}

// Append names the files it wrote so the caller stages exactly those, and a
// caller that staged a path Append did not touch would commit whatever else
// the worktree happened to hold.
func TestAppendNamesEveryFileItWrote(t *testing.T) {
	root := t.TempDir()
	written, _, err := Append(root, []Record{entry("a", "", "main", "2026-03-15T10:00:00Z")})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	want := []string{"history/v1/2026-03.ndjson", "history/v1/daily/main/2026.ndjson"}
	if len(written) != len(want) {
		t.Fatalf("Append wrote %v, want %v", written, want)
	}
	for i, path := range want {
		if written[i] != path {
			t.Errorf("Append wrote %v, want %v", written, want)
			break
		}
	}
}

// Every scalar is optional and absent is not zero: a component with no
// function above the CRAP threshold records zero, and one lydite scores none
// of records nothing, and a series that read the second as the first would
// draw a clean line through a metric nobody measured.
func TestAnAbsentScalarIsNotAZeroOne(t *testing.T) {
	root := t.TempDir()
	rec := entry("a", "", "main", "2026-03-15T10:00:00Z")
	rec.Components = map[string]Component{
		"scored":   {CRAP: &CRAP{Above: 0, Worst: 12.5}},
		"unscored": {Tests: &junit.Counts{Total: 4}},
	}
	if _, _, err := Append(root, []Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got := lines(t, root, "history/v1/2026-03.ndjson")[0]
	if c := got.Components["scored"]; c.CRAP == nil || c.CRAP.Above != 0 {
		t.Errorf("the scored component = %+v, want a CRAP count of exactly zero", c.CRAP)
	}
	if c := got.Components["unscored"]; c.CRAP != nil {
		t.Errorf("the unscored component gained a CRAP score of %+v", c.CRAP)
	}
	if c := got.Components["unscored"]; c.Tests == nil || c.Tests.Total != 4 {
		t.Errorf("the unscored component's tests = %+v, want 4 of them", c.Tests)
	}
}

func write(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRollupOrFail(t *testing.T, root, path string) []Rollup {
	t.Helper()
	rows, err := readRollup(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return rows
}

// One commit is a point on every branch that carries it. A release branch cut
// from the default one records the same commit again, and deduplicating it
// against the other line would leave that branch's history starting at
// whatever it did next — a hole nothing later fills, because every run would
// drop it the same way.
func TestOneCommitIsRecordedOnceForEachBranch(t *testing.T) {
	root := t.TempDir()
	if _, _, err := Append(root, []Record{
		entry("a", "", "main", "2026-03-15T08:00:00Z"),
		entry("a", "", "release/1.x", "2026-03-15T08:00:00Z"),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	recs := lines(t, root, "history/v1/2026-03.ndjson")
	if len(recs) != 2 {
		t.Fatalf("the partition holds %d records for one commit on two branches, want 2", len(recs))
	}
	if recs[0].Branch == recs[1].Branch {
		t.Errorf("both records name branch %q", recs[0].Branch)
	}
}

// A commit dated at month end must still look back the full lookback. AddDate
// normalises an out-of-range day, so stepping from the 31st lands on the same
// month again — and the branch's previous record, one month back, goes unseen.
func TestTheLookbackReachesAFullYearFromAMonthEndCommit(t *testing.T) {
	root := t.TempDir()
	// Eleven months before a 31st is exactly the entry an un-normalised walk
	// steps over.
	if _, _, err := Append(root, []Record{entry("a", "", "main", "2025-04-10T08:00:00Z")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, ok := Latest(root, "main", at("2026-03-31T10:00:00Z"))
	if !ok || got.Commit != "a" {
		t.Errorf("Latest from a month-end commit = %+v (%v), want the record eleven months back", got, ok)
	}
}

// A branch name becomes a path segment in the projection, and the claim that
// this is safe is a property of git's ref format rather than of this package's
// callers — so it is enforced here.
func TestABranchThatIsNotARefPathIsRefused(t *testing.T) {
	root := t.TempDir()
	for _, branch := range []string{"../escape", "a/../../b", "/absolute", "trailing/", "a//b", "."} {
		if _, _, err := Append(root, []Record{entry("a", "", branch, "2026-03-15T10:00:00Z")}); err == nil {
			t.Errorf("Append accepted branch %q as a path segment", branch)
		}
	}
	// A perfectly ordinary namespaced branch is still accepted; the rule
	// rejects traversal, not slashes.
	if _, _, err := Append(root, []Record{entry("a", "", "release/1.x", "2026-03-15T10:00:00Z")}); err != nil {
		t.Errorf("Append refused an ordinary namespaced branch: %v", err)
	}
}
