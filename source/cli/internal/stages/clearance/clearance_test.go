package clearancestages

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
)

const head = "4c2eaea1f2b3c4d5e6f708192a3b4c5d6e7f8091"

var (
	commented = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	earlier   = commented.Add(-time.Hour)
)

func TestParseCommandReadsTheCommentsFacts(t *testing.T) {
	out, err := ParseCommand(context.Background(), ParseCommandIn{Comment: forge.IssueComment{
		Body: "/lydite clear", Author: "octocat", CreatedAt: commented, Number: 40, OnPullRequest: true,
	}})
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	if !out.Addressed || !out.OnPullRequest {
		t.Errorf("a clearance command on a pull request reads as Addressed=%t OnPullRequest=%t", out.Addressed, out.OnPullRequest)
	}
	if out.Command.Verb != clearance.VerbClear {
		t.Errorf("Command.Verb = %q, want %q", out.Command.Verb, clearance.VerbClear)
	}
	if out.Number != 40 || out.Login != "octocat" || !out.CommentAt.Equal(commented) {
		t.Errorf("the comment's facts are not carried: %+v", out)
	}
}

// Ordinary conversation is not addressed to lydite, and nothing past parsing
// may act on it.
func TestParseCommandLeavesConversationUnaddressed(t *testing.T) {
	out, err := ParseCommand(context.Background(), ParseCommandIn{Comment: forge.IssueComment{
		Body: "looks good, merging tomorrow", OnPullRequest: true,
	}})
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	if out.Addressed {
		t.Error("ordinary conversation reads as addressed to lydite")
	}
	if !out.OnPullRequest {
		t.Error("a comment on a pull request reads as one on a plain issue")
	}
}

// A comment on a plain issue names no revision, so even a well-formed command
// there is nothing to act on.
func TestParseCommandLeavesAPlainIssueUnaddressed(t *testing.T) {
	out, err := ParseCommand(context.Background(), ParseCommandIn{Comment: forge.IssueComment{
		Body: "/lydite clear", Number: 7,
	}})
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	if out.Addressed || out.OnPullRequest {
		t.Errorf("a command on a plain issue reads as Addressed=%t OnPullRequest=%t", out.Addressed, out.OnPullRequest)
	}
	if out.Command.Verb != clearance.VerbNone {
		t.Errorf("a command on a plain issue was parsed as %q", out.Command.Verb)
	}
}

// A mistyped verb is addressed: it is answered rather than ignored.
func TestParseCommandAddressesAMistypedVerb(t *testing.T) {
	out, err := ParseCommand(context.Background(), ParseCommandIn{Comment: forge.IssueComment{
		Body: "/lydite cler", OnPullRequest: true,
	}})
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	if !out.Addressed || out.Command.Verb != clearance.VerbUnknown {
		t.Errorf("a mistyped verb reads as Addressed=%t Verb=%q", out.Addressed, out.Command.Verb)
	}
}

func pending(at time.Time) *clearance.Status {
	return &clearance.Status{State: clearance.StatePending, Description: "referred", CreatedAt: at}
}

func TestDecideClearsAReferralStandingBeforeTheComment(t *testing.T) {
	out, err := Decide(context.Background(), DecideIn{
		Repository: &fakeRepository{t: t},
		Dir:        t.TempDir(),
		Number:     40,
		Command:    clearance.Command{Verb: clearance.VerbClear},
		Head:       head,
		CanWrite:   true,
		Status:     pending(earlier),
		CommentAt:  commented,
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Action.Kind != clearance.KindClear || out.Action.SHA != head {
		t.Errorf("Action = %+v, want a clearance of the head", out.Action)
	}
	if !out.Clears || out.Answers {
		t.Errorf("Clears=%t Answers=%t, want a clearance and nothing answered alone", out.Clears, out.Answers)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("a clearance warned: %q", out.Warnings)
	}
}

// Deriving the uncovered set reads the exemptions file and asks the platform
// what the pull request touched. A command the ladder refuses does neither:
// the fake fails the test on any ChangedPaths call.
func TestDecideRefusesAStrangerAndAsksNothing(t *testing.T) {
	out, err := Decide(context.Background(), DecideIn{
		Repository: &fakeRepository{t: t},
		Dir:        t.TempDir(),
		Number:     40,
		Command:    clearance.Command{Verb: clearance.VerbExempt, Shape: "docs-only"},
		Head:       head,
		Status:     pending(earlier),
		CommentAt:  commented,
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Action.Kind != clearance.KindRefuse || out.Action.Reason != clearance.ReasonNotPermitted {
		t.Errorf("Action = %+v, want a refusal for permission", out.Action)
	}
	if out.Clears || !out.Answers {
		t.Errorf("Clears=%t Answers=%t, want an answer and nothing cleared", out.Clears, out.Answers)
	}
}

func TestDecideAnswersAnExplanation(t *testing.T) {
	out, err := Decide(context.Background(), DecideIn{
		Repository: &fakeRepository{t: t},
		Command:    clearance.Command{Verb: clearance.VerbExplain},
		Head:       head,
		CanWrite:   true,
		Status:     pending(earlier),
		CommentAt:  commented,
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Action.Kind != clearance.KindExplain || out.Clears || !out.Answers {
		t.Errorf("Action = %+v, Clears=%t Answers=%t, want an explanation answered alone", out.Action, out.Clears, out.Answers)
	}
}

// A decision that ignores the comment neither clears nor answers.
func TestDecideIgnoringACommentAnswersNothing(t *testing.T) {
	out, err := Decide(context.Background(), DecideIn{Repository: &fakeRepository{t: t}})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Action.Kind != clearance.KindIgnore || out.Clears || out.Answers {
		t.Errorf("Action = %+v, Clears=%t Answers=%t, want nothing done", out.Action, out.Clears, out.Answers)
	}
}

// exemptIn is a permitted `/lydite exempt` against a standing referral, with
// the scan root at dir.
func exemptIn(repository forge.SCMRepository, dir string) DecideIn {
	return DecideIn{
		Repository: repository,
		Dir:        dir,
		Number:     40,
		Command:    clearance.Command{Verb: clearance.VerbExempt, Shape: "moved-sources"},
		Head:       head,
		CanWrite:   true,
		Status:     pending(earlier),
		CommentAt:  commented,
	}
}

// repositoryAt is a git repository with files and nothing committed, which is
// all referral.RootRelative asks of the scan root.
func repositoryAt(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--quiet", "-b", "main")
	writeFiles(t, dir, files)
	return dir
}

// The proposal covers the paths nothing declared covers, and nothing else.
func TestDecideProposesTheUncoveredPathsAlone(t *testing.T) {
	dir := repositoryAt(t, map[string]string{
		referral.FileName: "exemptions:\n  - name: docs\n    reason: prose only\n    paths: [\"docs/**\"]\n",
	})
	repository := &fakeRepository{t: t, changedPaths: func(_ context.Context, number int) ([]string, error) {
		if number != 40 {
			t.Errorf("ChangedPaths asked for %d, want 40", number)
		}
		return []string{"docs/one.md", "src/new.go"}, nil
	}}

	out, err := Decide(context.Background(), exemptIn(repository, dir))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Action.Kind != clearance.KindExempt || out.Action.Name != "moved-sources" {
		t.Fatalf("Action = %+v, want a proposal named moved-sources", out.Action)
	}
	if !slices.Equal(out.Action.Paths, []string{"src/new.go"}) {
		t.Errorf("Paths = %q, want only the uncovered path", out.Action.Paths)
	}
	if out.Clears || !out.Answers {
		t.Errorf("Clears=%t Answers=%t, want a proposal answered alone", out.Clears, out.Answers)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("a derivation that worked warned: %q", out.Warnings)
	}
}

// A scan root below the repository root declares its exemptions there, and the
// file is located from dir rather than from the process's own directory.
func TestDecideReadsTheExemptionsFileUnderTheScanRoot(t *testing.T) {
	root := repositoryAt(t, map[string]string{
		"source/" + referral.FileName: "exemptions:\n  - name: docs\n    reason: prose only\n    paths: [\"source/docs/**\"]\n",
	})
	repository := &fakeRepository{t: t, changedPaths: func(context.Context, int) ([]string, error) {
		return []string{"source/docs/one.md", "source/src/a.go"}, nil
	}}

	out, err := Decide(context.Background(), exemptIn(repository, filepath.Join(root, "source")))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !slices.Equal(out.Action.Paths, []string{"source/src/a.go"}) {
		t.Errorf("Paths = %q, want the path the scan root's declarations leave uncovered", out.Action.Paths)
	}
}

// What went wrong deriving the uncovered set is named for the job log,
// because the refusal the commenter reads deliberately names no cause.
func TestDecideNamesAFailedDerivationInItsWarnings(t *testing.T) {
	listErr := errors.New("the listing was refused")
	repository := &fakeRepository{t: t, changedPaths: func(context.Context, int) ([]string, error) {
		return nil, listErr
	}}

	out, err := Decide(context.Background(), exemptIn(repository, repositoryAt(t, nil)))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Action.Kind != clearance.KindRefuse || out.Action.Reason != clearance.ReasonCouldNotDerive {
		t.Errorf("Action = %+v, want a refusal naming no derivation", out.Action)
	}
	if !out.Answers {
		t.Error("a failed derivation is not answered")
	}
	want := "lydite: deriving the change's uncovered paths: " + listErr.Error()
	if !slices.Equal(out.Warnings, []string{want}) {
		t.Errorf("Warnings = %q, want %q", out.Warnings, want)
	}
}

// A file that exists but does not parse is a different answer from a
// repository that declared nothing.
func TestDecideRefusesAnUnparseableExemptionsFile(t *testing.T) {
	dir := repositoryAt(t, map[string]string{referral.FileName: "exemptions: [this is not a mapping]\n"})
	repository := &fakeRepository{t: t}

	out, err := Decide(context.Background(), exemptIn(repository, dir))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Action.Reason != clearance.ReasonCouldNotDerive {
		t.Errorf("Action = %+v, want a refusal naming no derivation", out.Action)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "deriving the change's uncovered paths") {
		t.Errorf("Warnings = %q, want the failed derivation named", out.Warnings)
	}
}

// referral.RootRelative shells out to git, and a directory that is not one
// answers with an error rather than a prefix.
func TestDecideOutsideAGitRepositoryRefuses(t *testing.T) {
	out, err := Decide(context.Background(), exemptIn(&fakeRepository{t: t}, t.TempDir()))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Action.Reason != clearance.ReasonCouldNotDerive || len(out.Warnings) != 1 {
		t.Errorf("Action = %+v, Warnings = %q, want a refusal and the failure named", out.Action, out.Warnings)
	}
}

// Every changed path is already covered, so there is no entry to propose.
func TestDecideProposesNothingWhenEveryPathIsCovered(t *testing.T) {
	dir := repositoryAt(t, map[string]string{
		referral.FileName: "exemptions:\n" +
			"  - name: docs\n    reason: prose only\n    paths: [\"docs/**\"]\n" +
			"  - name: sources\n    reason: code only\n    paths: [\"src/**\"]\n",
	})
	repository := &fakeRepository{t: t, changedPaths: func(context.Context, int) ([]string, error) {
		return []string{"docs/one.md", "src/a.go"}, nil
	}}

	out, err := Decide(context.Background(), exemptIn(repository, dir))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Action.Reason != clearance.ReasonNothingToPropose {
		t.Errorf("Action = %+v, want a refusal with nothing to propose", out.Action)
	}
}
