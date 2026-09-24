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
// suffices for every repository anyone has. A record's size is not a fixed
// number: a quiet commit, one on which no finding appeared or resolved, costs
// only its scalars, while each finding that transitioned adds roughly 100-150
// bytes — proportional to churn, never to how many findings are open. The roll
// is what a busier or churnier month does — the partition stays a month, and
// the month grows parts.
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
	// RootFindings is how many claims each ROOT-SCOPED gate made, by gate
	// name — a check that runs over the repository rather than over a
	// component, which is Semgrep.
	//
	// Beside Components rather than inside one, because a root-scoped claim
	// names no component and nothing may invent one for it: attributing
	// Semgrep's finding in a path to whichever component happens to contain
	// that path would be this package deciding an ownership question the
	// declaration does not answer, and a finding outside every component
	// would have nowhere to go at all.
	//
	// It is not a sum over Components and must never be read as one. The
	// repository-wide figures ARE sums nobody stores; this is a different
	// measurement, taken once over the whole tree.
	RootFindings map[string]int `json:"root_findings,omitempty"`
	// FindingEvents is which located claims appeared or resolved on this
	// commit, against the branch's open set replayed up to it.
	//
	// Transitions and not the whole open set, because a finding that stays
	// open would otherwise be written into every recording for as long as it
	// lives — for pre-existing debt, indefinitely — so a quiet commit carries
	// none and the field is omitted. OpenFindings is how the set is read back.
	//
	// Not named Findings: Component.Findings is already the per-gate counts
	// this is additive to, and one word must not answer two different
	// questions on two types in one package. Adding it needs no new Dir
	// version, because a record written without it reads back as one with no
	// finding events, which is honestly what it has.
	FindingEvents []FindingEvent `json:"finding_events,omitempty"`
	// Gap says how wide the break before this record is, and is set only on a
	// gap record.
	Gap *Gap `json:"gap,omitempty"`
}

// FindingTransition is what happened to a fingerprint between the branch's
// last recording and this one.
type FindingTransition string

const (
	// FindingAppeared is a fingerprint this recording's scan holds that the
	// branch's open set, replayed up to this commit, did not.
	FindingAppeared FindingTransition = "appeared"
	// FindingResolved is a fingerprint the branch's open set held that this
	// recording's scan, for the same gate and component, does not.
	FindingResolved FindingTransition = "resolved"
)

// FindingEvent is one fingerprint's transition, carried on the record it
// transitioned on.
//
// Gate and Component are structural — a transition without them cannot be
// bucketed — while Path and Rule are what a history view renders without a
// second lookup. The claim's message is deliberately absent: a tool that
// rewords its own diagnostic has not found something different, which is why
// the fingerprint excludes it too, and a message frozen at first appearance
// would render wording a current run no longer produces beside a fingerprint
// that correctly says nothing changed.
type FindingEvent struct {
	Transition FindingTransition `json:"transition"`
	// Fingerprint is exactly what finding.Finding.Fingerprint returns.
	Fingerprint string `json:"fingerprint"`
	Gate        string `json:"gate"`
	// Component is empty for a root-scoped gate, as RootFindings treats one.
	Component string `json:"component,omitempty"`
	Path      string `json:"path"`
	Rule      string `json:"rule,omitempty"`
}

// FindingBucket is the scope a fingerprint is open or resolved in: one gate
// over one component, or over the repository when Component is empty.
//
// A fingerprint is only ever resolved within its bucket, so a recording that
// did not measure a bucket — a gate switched off, a partial run — leaves that
// bucket's findings open rather than manufacturing a resolution for them.
type FindingBucket struct {
	Gate      string
	Component string
}

// Component is one component's scalars for one commit.
//
// Every metric is a pointer, and every per-gate count a key that may be
// missing, so absent and zero are different answers. They have to be: a
// component with no function above the CRAP threshold records zero, and one
// lydite computes no score for records nothing — and a series that read the
// second as the first would draw a clean line through a metric nobody
// measured.
type Component struct {
	// Coverage is the component's line counts.
	Coverage *Lines `json:"coverage,omitempty"`
	// CRAP is how many of its functions sit above the threshold, and the
	// worst of them.
	CRAP *CRAP `json:"crap,omitempty"`
	// Tests is what became of its suite's tests.
	Tests *junit.Counts `json:"tests,omitempty"`
	// Findings is how many located claims each scanner gate made about this
	// component, by gate name.
	//
	// Per gate rather than one total, because a total cannot answer which
	// scanner moved — the same reason the scalars are per component rather
	// than per repository — and because absent and zero have to be different
	// answers here in a way one integer cannot express: a gate that ran and
	// found nothing records 0, while a gate that does not apply to this
	// component's language is not a key at all. A series that read the second
	// as the first would draw a clean line through a scanner that never ran.
	//
	// Scanner gates only. CRAP is already counted by CRAP.Above, and
	// recording it again here would make two quantities that must agree into
	// two quantities free to disagree; patch coverage is likewise the
	// PatchPercent the coverage counts already carry. Mutation is counted by
	// Mutation, whose six outcomes one total could not express.
	Findings map[string]int `json:"findings,omitempty"`
	// Mutation is what a post-merge run made of this component's mutants.
	//
	// One pointer to a struct and not six optional counts: the six always
	// arrive together out of one run, and per-field optionality would encode a
	// granularity the data never has. It is absent for a component the merge
	// commit's diff did not touch, for one declared `mutation: false`, and for
	// one whose run did not complete — a component that ran and killed every
	// mutant records a survived count of nought, which is a measured zero and
	// a different fact from absence.
	Mutation *Mutation `json:"mutation,omitempty"`
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

// Mutation is the six outcomes a mutation run sorts every mutant into and the
// time it took to do it, exactly as mutants.json stores them.
//
// Acknowledged is here for the reason the other five are: a `//lydite:equivalent`
// declaration is a fact about a mutant that existed only on a line the change
// touched, and after the merge there is no diff left to re-derive it from. It
// is also what keeps a total reconstructible rather than an undercount missing
// whatever a declaration took out of the denominator.
type Mutation struct {
	Killed       int `json:"killed"`
	TimedOut     int `json:"timed_out"`
	OutOfMemory  int `json:"out_of_memory"`
	Survived     int `json:"survived"`
	Unviable     int `json:"unviable"`
	Acknowledged int `json:"acknowledged"`
	// ElapsedSeconds is how long the run took to sort them: the component's
	// baseline suite and every mutant after it, machine time rather than
	// wall-clock. It is what a runtime budget would later be argued from, and
	// nothing else here can be: a mutation run is not recomputable after the
	// merge, so a cost nobody wrote down is a cost nobody can ever measure.
	//
	// Nought means the run recorded no elapsed time, which is every record
	// written before this field and every one whose mutants.json carried none.
	// A run that happened cannot produce it, so absent and nought are one
	// answer, and neither is a run that took no time. It is a plain float and
	// not a pointer for that reason — the six counts beside it are the shape
	// this struct exists to keep whole, and a nought here is already
	// unambiguous.
	ElapsedSeconds float64 `json:"elapsed_seconds,omitempty"`
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
	// Collected in the order they are written — a month's partition, then the
	// projection it feeds — rather than gathered from a map and sorted. A map
	// has no order, so a sort is the only thing that would make the caller's
	// `git add` arguments stable, and a rule that holds only because a call
	//at the end of the function happens to still be there is one nothing
	// notices the loss of.
	written := map[string]bool{}
	var files []string
	add := func(path string) {
		if written[path] {
			return
		}
		written[path] = true
		files = append(files, path)
	}
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
		add(part)
		proj, err := project(root, rec)
		if err != nil {
			return nil, nil, err
		}
		add(proj)
		landed = append(landed, rec)
	}
	return files, landed, nil
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

// OpenFindings is the fingerprints open on branch immediately before the
// instant before, per bucket, replayed from the finding events of every entry
// recorded for it in the last lookbackMonths.
//
// It is the one implementation of that replay. Records are applied in the
// order of their own timestamps and not their positions in the files, because
// two recordings can land out of order and a resolution applied ahead of the
// appearance it resolves would leave a finding open forever. A record at
// before itself is excluded, so a writer asking what was open ahead of the
// commit it is recording never reads that commit's own events back.
//
// Bounded by the same walk Latest makes and for the same reason: a branch
// with nothing in that window is adopting finding history, not resuming one
// worth reconstructing further back. A bucket no event ever mentioned, and one
// whose every fingerprint has resolved, is simply absent.
func OpenFindings(root, branch string, before time.Time) map[FindingBucket]map[string]bool {
	var recs []Record
	// Anchored to the first of the month, for the reason Latest gives.
	month := time.Date(before.UTC().Year(), before.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for range lookbackMonths {
		parts, err := partsFor(root, month)
		if err != nil {
			break
		}
		for _, p := range parts {
			_, _ = scan(filepath.Join(root, filepath.FromSlash(p)), func(rec Record) bool {
				if rec.Kind == KindEntry && rec.Branch == branch && rec.At.Before(before) && len(rec.FindingEvents) > 0 {
					recs = append(recs, rec)
				}
				return false
			})
		}
		month = month.AddDate(0, -1, 0)
	}
	// Stable, so two records sharing a timestamp keep the order they were
	// written in rather than whatever the sort happens to leave.
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].At.Before(recs[j].At) })

	open := map[FindingBucket]map[string]bool{}
	for _, rec := range recs {
		for _, ev := range rec.FindingEvents {
			bucket := FindingBucket{Gate: ev.Gate, Component: ev.Component}
			switch ev.Transition {
			case FindingAppeared:
				if open[bucket] == nil {
					open[bucket] = map[string]bool{}
				}
				open[bucket][ev.Fingerprint] = true
			case FindingResolved:
				delete(open[bucket], ev.Fingerprint)
				if len(open[bucket]) == 0 {
					delete(open, bucket)
				}
			}
		}
	}
	return open
}

// BranchState is what a writer needs from a branch's own history before it
// appends a new record: the newest entry recorded so far, for gap detection,
// and the open finding set replayed up to before, for finding transitions.
// One walk answers both.
//
// Latest and OpenFindings each read the same lookbackMonths of partitions
// independently when a caller needs both, and gitstate.Write retries its
// closure up to three times on a push race — three attempts at one commit
// would otherwise parse a year of history as many as six times, on a path
// that runs on every commit. This walks it once and reduces it two ways.
//
// The two reductions keep their own existing rules rather than share one:
// Latest's "at or before" tie tolerates two commits recorded in the same
// second, which is why gap detection still finds a previous commit sharing
// head's own timestamp; OpenFindings' replay excludes a record at exactly
// before, because before is the commit about to be recorded and its own
// events, once appended, must never be read back as history for themselves.
func BranchState(root, branch string, before time.Time) (open map[FindingBucket]map[string]bool, previous Record, hasPrevious bool) {
	var recs []Record
	month := time.Date(before.UTC().Year(), before.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for range lookbackMonths {
		parts, err := partsFor(root, month)
		if err != nil {
			break
		}
		for _, p := range parts {
			_, _ = scan(filepath.Join(root, filepath.FromSlash(p)), func(rec Record) bool {
				if rec.Kind == KindEntry && rec.Branch == branch {
					recs = append(recs, rec)
				}
				return false
			})
		}
		month = month.AddDate(0, -1, 0)
	}

	for _, rec := range recs {
		if rec.At.After(before) {
			continue
		}
		if !hasPrevious || rec.At.After(previous.At) {
			previous, hasPrevious = rec, true
		}
	}

	replay := make([]Record, 0, len(recs))
	for _, rec := range recs {
		if rec.At.Before(before) && len(rec.FindingEvents) > 0 {
			replay = append(replay, rec)
		}
	}
	sort.SliceStable(replay, func(i, j int) bool { return replay[i].At.Before(replay[j].At) })
	open = map[FindingBucket]map[string]bool{}
	for _, rec := range replay {
		for _, ev := range rec.FindingEvents {
			bucket := FindingBucket{Gate: ev.Gate, Component: ev.Component}
			switch ev.Transition {
			case FindingAppeared:
				if open[bucket] == nil {
					open[bucket] = map[string]bool{}
				}
				open[bucket][ev.Fingerprint] = true
			case FindingResolved:
				delete(open[bucket], ev.Fingerprint)
				if len(open[bucket]) == 0 {
					delete(open, bucket)
				}
			}
		}
	}
	return open, previous, hasPrevious
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
		row.RootFindings = rec.RootFindings
	}
	rows[i] = row
	// By day, ascending, through the standard library's own ordering: a file
	// holds one branch, so a day identifies a row, and a comparator written
	// here would be one more thing that can be subtly wrong about the order
	// the whole file is read in.
	byDay := make(map[string]Rollup, len(rows))
	days := make([]string, 0, len(rows))
	for _, r := range rows {
		if _, seen := byDay[r.Day]; !seen {
			days = append(days, r.Day)
		}
		byDay[r.Day] = r
	}
	sort.Strings(days)
	ordered := make([]Rollup, 0, len(days))
	for _, day := range days {
		ordered = append(ordered, byDay[day])
	}
	return file, writeRollup(path, ordered)
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
	// RootFindings is that entry's root-scoped counts, carried here for the
	// same reason Components is: the projection is what the dashboard reads,
	// and a scalar the rollup drops is one no chart can draw without walking
	// every partition.
	RootFindings map[string]int `json:"root_findings,omitempty"`
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
	// No initial capacity: bufio grows the buffer as far as the cap on its
	// own, and the only thing a starting size could do here is be wrong.
	// The cap is what matters — a partition line is one record, and a
	// scanner left at its 64KB default refuses to read one a component
	// with many entries produces.
	s.Buffer(nil, maxPartitionBytes)
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
	// No initial capacity: bufio grows the buffer as far as the cap on its
	// own, and the only thing a starting size could do here is be wrong.
	// The cap is what matters — a partition line is one record, and a
	// scanner left at its 64KB default refuses to read one a component
	// with many entries produces.
	s.Buffer(nil, maxPartitionBytes)
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
	// Both errors, joined: a write that failed and a close that failed are
	// each a record that did not land, and reporting them together leaves no
	// branch here to get the wrong way round.
	_, werr := f.Write(line)
	return errors.Join(werr, f.Close())
}
