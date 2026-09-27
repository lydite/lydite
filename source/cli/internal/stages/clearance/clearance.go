// Package clearancestages holds the stages that answer a clearance command
// posted on a pull request: reading the command out of a comment, deciding
// what it does, fingerprinting the decision a clearance is given for,
// recording or rendering the clearance, and composing the reply. Named apart
// from internal/clearance so a flow definition can import both without
// renaming either.
//
// Every stage is a plain function of its own In. Nothing here prints: what a
// stage has to say to the job log it returns as Warnings, for whoever runs the
// flow to write.
package clearancestages

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
)

// ParseCommandIn is the comment ParseCommand reads.
type ParseCommandIn struct {
	Comment forge.IssueComment
}

// ParseCommandOut is the command, and the comment's own facts every later
// stage reads one at a time.
type ParseCommandOut struct {
	Command clearance.Command
	// OnPullRequest reports whether the comment is on a pull request rather
	// than a plain issue. A plain issue names no revision, so there is
	// nothing a clearance could apply to.
	OnPullRequest bool
	// Addressed reports whether this run has anything to act on: a command
	// addressed to lydite, on a pull request. Every stage past this one runs
	// only when it holds.
	Addressed bool
	// Number is the pull request the comment is on.
	Number int
	// Login is the commenter, whose permission decides whether the command
	// is honoured at all.
	Login string
	// CommentAt is when the platform recorded the comment.
	CommentAt time.Time
}

// ParseCommand reads the command out of a comment's first line.
//
// A comment that is not on a pull request is never parsed: whatever it says,
// there is no revision for it to be about, and reporting it as addressed would
// send a later stage looking for a head that does not exist.
func ParseCommand(_ context.Context, in ParseCommandIn) (ParseCommandOut, error) {
	out := ParseCommandOut{
		OnPullRequest: in.Comment.OnPullRequest,
		Number:        in.Comment.Number,
		Login:         in.Comment.Author,
		CommentAt:     in.Comment.CreatedAt,
	}
	if !out.OnPullRequest {
		return out, nil
	}
	out.Command = clearance.Parse(in.Comment.Body)
	out.Addressed = out.Command.Verb != clearance.VerbNone
	return out, nil
}

// DecideIn is everything a decision is made from, and what the lazy
// uncovered-paths read needs should the ladder reach it.
type DecideIn struct {
	Repository forge.SCMRepository
	// Dir is the scan root, which locates the exemptions file in force.
	Dir       string
	Number    int
	Command   clearance.Command
	Head      string
	CanWrite  bool
	Status    *clearance.Status
	CommentAt time.Time
}

// DecideOut is the decision, and the conditions a flow branches on.
type DecideOut struct {
	Action clearance.Action
	// Clears reports a clearance to record: a fingerprint to take, two
	// statuses to write, and the cleared reply.
	Clears bool
	// Answers reports a command answered by a reply and nothing else — an
	// explanation, a proposal or a refusal. It never holds beside Clears.
	Answers bool
	// Warnings are job-log lines, each without its newline.
	Warnings []string
}

// Decide answers the command through clearance.Decide.
//
// The uncovered set is derived inside the ladder rather than before it, so a
// command the ladder refuses never reads the exemptions file nor asks the
// platform what the pull request touched. A derivation that fails is named in
// Warnings, for the job log; the commenter reads a refusal, because a command
// that errors out with no reply is one whose author has only silence to go on.
func Decide(ctx context.Context, in DecideIn) (DecideOut, error) {
	var warnings []string
	uncover := func() ([]string, error) {
		uncovered, err := uncoveredPaths(ctx, in.Repository, in.Dir, in.Number)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("lydite: deriving the change's uncovered paths: %v", err))
		}
		return uncovered, err
	}
	action := clearance.Decide(clearance.Request{
		Command:   in.Command,
		HeadSHA:   in.Head,
		CanWrite:  in.CanWrite,
		Status:    in.Status,
		CommentAt: in.CommentAt,
	}, uncover)
	out := DecideOut{Action: action, Warnings: warnings}
	switch action.Kind {
	case clearance.KindClear:
		out.Clears = true
	case clearance.KindExplain, clearance.KindExempt, clearance.KindRefuse:
		out.Answers = true
	}
	return out, nil
}

// uncoveredPaths answers which of a pull request's changed paths no declared
// exemption covers.
//
// The exemptions file comes from the working tree, which is the clearance
// job's own checkout of the default branch — so the declarations consulted
// are the ones in force, never the ones the pull request proposes for itself.
// Which file that is comes from dir the way every other command resolves it:
// referral.RootRelative turns the scan root into its path from the repository
// root, so a repository whose scan root is a subdirectory reads the
// declarations governing it rather than missing them and proposing an entry
// over every path the change touches.
//
// An absent file is the day-one state rather than an error; an unparseable
// one is an error, because "nothing is exempt" and "the file nobody can read"
// are different answers and only the first is a repository's decision.
//
// The changed paths are the platform's own list of names, and are the only
// thing the comment surface asks about the pull request itself. Nothing of
// its content is fetched: see
// docs/adr/0049-exempt-proposes-an-entry-and-lands-nothing.md.
func uncoveredPaths(ctx context.Context, repository forge.SCMRepository, dir string, number int) ([]string, error) {
	prefix, err := referral.RootRelative(ctx, dir)
	if err != nil {
		return nil, err
	}
	// repoPath is repository-root-relative, for the parse label and any error
	// a person reads; opening the file has to go through dir instead, since
	// the process's own directory is not necessarily the repository root and
	// a path.Join with prefix would then name the wrong file.
	repoPath := path.Join(prefix, referral.FileName)
	var file referral.File
	data, err := os.ReadFile(filepath.Join(dir, referral.FileName)) // #nosec G304 -- dir is the operator's own --dir flag, not pull-request content
	switch {
	case err == nil:
		if file, err = referral.Parse(data, repoPath); err != nil {
			return nil, err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("reading %s: %w", repoPath, err)
	}
	changed, err := repository.ChangedPaths(ctx, number)
	if err != nil {
		return nil, err
	}
	return referral.Uncovered(changed, file.Exemptions), nil
}
