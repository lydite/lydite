// Package threadsstages holds the stages that reconcile a pull request's
// review threads with the located findings a run made: read the findings,
// list the threads already standing, plan the delta between them, write the
// plan down, and apply it. Named apart from internal/threads, the domain
// package that decides what a fingerprint matches and what may be deleted, so
// a flow definition can import both without renaming either.
//
// TakeDown, Answer and Open apply a plan with whatever identity the
// repository was built from. That is the `github-token` fallback, and a
// designed path rather than a stopgap: a consumer who has installed nothing
// still gets its threads, from `github-actions[bot]`, and the only thing they
// lose is whose name is on them. A flow runs them in that order, so a run
// writes its deletes, then its replies, then its review, then its file
// comments. Every id they write to is one the plan names, and the plan is
// computed from the listing the same run made: nothing is written to a
// comment the run did not itself find on the pull request.
package threadsstages

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/threads"
)

// FindingsReader reads the findings a report directory holds.
//
// Reading a report document belongs to the command that wrote it, so it is
// injected; which of the findings become threads is decided here.
type FindingsReader interface {
	// Findings returns every finding the report documents in dir carry, in
	// document order.
	Findings(dir string) ([]finding.Finding, error)
}

// ReadFindingsIn names the report directories ReadFindings reads.
type ReadFindingsIn struct {
	// Reports are the report directories, read in order.
	Reports []string
	// Reader reads one report directory's findings.
	Reader FindingsReader
}

// ReadFindingsOut is the located claims the report directories hold.
type ReadFindingsOut struct {
	// Located is every finding that reaches the change at a line or at a
	// file, one per fingerprint, first copy kept.
	Located []finding.Finding
	// Total is how many findings the directories held, located or not,
	// before any duplicate was dropped.
	Total int
	// Dropped is the fingerprint of every located copy dropped as a
	// duplicate, once per copy dropped.
	Dropped []string
	// Missing names each directory that could not be read, and why.
	Missing []string
}

// ReadFindings collects every located claim the named directories hold.
//
// A directory that could not be read is named in Missing and does not stop
// the run, for the standing comment's reason: the claims that did arrive are
// still worth putting on their lines, and a missing input is already rendered
// as a section saying so in the comment the same run publishes.
//
// One claim is one thread, so a fingerprint reported twice keeps its first
// copy and names the rest in Dropped.
func ReadFindings(_ context.Context, in ReadFindingsIn) (ReadFindingsOut, error) {
	var (
		found   []finding.Finding
		missing []string
	)
	for _, dir := range in.Reports {
		read, err := in.Reader.Findings(dir)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s holds no findings: %v", dir, err))
			continue
		}
		found = append(found, read...)
	}
	located, dropped := finding.Dedup(threads.Located(found))
	return ReadFindingsOut{
		Located: located,
		Total:   len(found),
		Dropped: dropped,
		Missing: missing,
	}, nil
}

// ListThreadsIn names the pull request ListThreads reads.
type ListThreadsIn struct {
	Repository forge.SCMRepository
	// Ref is the pull request ListThreads reads; only Ref.Number is used.
	Ref forge.PullRequestRef
}

// ListThreadsOut is the threads standing on the pull request.
type ListThreadsOut struct {
	// Threads is every thread on the pull request's diff, each root
	// comment with its replies.
	Threads []threads.Thread
	// Standing is how many threads stand on the pull request.
	Standing int
}

// ListThreads reads the review threads standing on Number, live.
//
// This listing is the only one a run makes: every id a later stage writes to
// comes from the operations planned against it, so nothing is written to a
// comment this run did not itself find on the pull request.
func ListThreads(ctx context.Context, in ListThreadsIn) (ListThreadsOut, error) {
	comments, err := in.Repository.ReviewComments(ctx, in.Ref.Number)
	if err != nil {
		return ListThreadsOut{}, err
	}
	grouped := threads.Threads(comments)
	return ListThreadsOut{Threads: grouped, Standing: len(grouped)}, nil
}

// PlanIn is what Plan reconciles.
type PlanIn struct {
	// Located is ReadFindingsOut.Located.
	Located []finding.Finding
	// Threads is ListThreadsOut.Threads.
	Threads []threads.Thread
	// Ref is the pull request the operations are for; Ref.SHA is the
	// revision new threads are anchored to.
	Ref forge.PullRequestRef
}

// PlanOut is the operations that reconcile the two.
type PlanOut struct {
	Ops threads.Ops
}

// Plan computes the operations that reconcile the threads standing on the
// pull request with the claims this run makes. What a fingerprint matches,
// and what may be deleted, is threads.Delta's alone.
func Plan(_ context.Context, in PlanIn) (PlanOut, error) {
	return PlanOut{Ops: threads.Delta(in.Located, in.Threads, in.Ref.Number, in.Ref.SHA)}, nil
}

// WriteOpsIn is the operations document WriteOps writes, and where.
type WriteOpsIn struct {
	Path string
	Ops  threads.Ops
}

// WriteOps puts the operations where the caller asked for them.
//
// Always, and never into .lydite-reports/. The document is lydite's own wire
// between the delta and whatever applies it, and nothing about it is promised
// to a consumer — `lydite test plan` is the precedent for a command that
// reaches no verdict writing no report document.
func WriteOps(_ context.Context, in WriteOpsIn) (struct{}, error) {
	if dir := filepath.Dir(in.Path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return struct{}{}, err
		}
	}
	raw, err := json.MarshalIndent(in.Ops, "", "  ")
	if err != nil {
		return struct{}{}, err
	}
	return struct{}{}, os.WriteFile(in.Path, append(raw, '\n'), 0o600)
}
