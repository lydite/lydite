// Package scmstages holds generic stages that read and write the hosting
// platform's repository: the comment a command arrived on, a pull request's
// current head, a commenter's permission, a revision's standing referral
// status, and a reply. Named apart from internal/forge so a flow definition
// can import both without renaming either.
package scmstages

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/trust"
)

// ErrNoCredential is returned by InitSCM when the TrustedContext it was given
// holds no credential to write with.
var ErrNoCredential = errors.New("no credential: GITHUB_TOKEN or GH_TOKEN is not set")

// InitSCMIn is what InitSCM builds a repository from.
type InitSCMIn struct {
	Trust trust.TrustedContext
}

// InitSCMOut is what InitSCM establishes.
type InitSCMOut struct {
	Repository forge.SCMRepository
}

// InitSCM builds the repository every later stage in this package reads and
// writes through.
//
// A TrustedContext holding no credential is refused with ErrNoCredential:
// reading the head, the commenter's permission and the standing referral, and
// posting the reply, all need a token, so a run given none has nothing for a
// later stage to do with the repository it would otherwise build.
func InitSCM(_ context.Context, in InitSCMIn) (InitSCMOut, error) {
	if !in.Trust.CanWrite() {
		return InitSCMOut{}, ErrNoCredential
	}
	repository, err := forge.NewGitHubRepository(in.Trust)
	if err != nil {
		return InitSCMOut{}, err
	}
	return InitSCMOut{Repository: repository}, nil
}

// LoadCommentIn is what LoadComment resolves a comment from.
type LoadCommentIn struct {
	Trust      trust.TrustedContext
	Repository forge.SCMRepository
	Ref        forge.CommentRef
}

// LoadCommentOut is the comment LoadComment resolved.
type LoadCommentOut struct {
	Comment forge.IssueComment
}

// LoadComment resolves the comment a command arrived on, live, from its id
// alone.
//
// Ref.Repository is the event payload's own claim of which repository the
// comment is on, and is checked against Trust.Repository() — the repository
// this run was started for — before anything is fetched. GitHub owner/name
// segments are case-insensitive, so the comparison is too: a payload naming
// "Owner/Name" against a run trusted for "owner/name" names the same
// repository. A payload naming any other repository is refused outright,
// never fetched against the trusted one instead.
func LoadComment(ctx context.Context, in LoadCommentIn) (LoadCommentOut, error) {
	if !strings.EqualFold(in.Ref.Repository, in.Trust.Repository()) {
		return LoadCommentOut{}, fmt.Errorf(
			"the event payload names %q, not the repository this run is trusted for (%q)",
			in.Ref.Repository, in.Trust.Repository())
	}
	comment, err := in.Repository.IssueComment(ctx, in.Ref.ID)
	if err != nil {
		return LoadCommentOut{}, err
	}
	return LoadCommentOut{Comment: comment}, nil
}

// ResolveHeadIn names the pull request ResolveHead reads the head of.
type ResolveHeadIn struct {
	Repository forge.SCMRepository
	Number     int
}

// ResolveHeadOut is the revision ResolveHead resolved.
type ResolveHeadOut struct {
	SHA string
}

// ResolveHead resolves a pull request's current head, live — never a
// revision named in a comment or a payload, both of which can be stale by
// the time this runs.
func ResolveHead(ctx context.Context, in ResolveHeadIn) (ResolveHeadOut, error) {
	sha, err := in.Repository.HeadSHA(ctx, in.Number)
	if err != nil {
		return ResolveHeadOut{}, err
	}
	return ResolveHeadOut{SHA: sha}, nil
}

// CheckPermissionIn names the login CheckPermission reads a permission for.
type CheckPermissionIn struct {
	Repository forge.SCMRepository
	Login      string
}

// CheckPermissionOut is what CheckPermission found.
type CheckPermissionOut struct {
	CanWrite bool
}

// CheckPermission reports whether Login may push to the repository, read
// about the commenter rather than taken from anything they wrote, since
// nothing an author writes can produce it.
func CheckPermission(ctx context.Context, in CheckPermissionIn) (CheckPermissionOut, error) {
	canWrite, err := in.Repository.CanWrite(ctx, in.Login)
	if err != nil {
		return CheckPermissionOut{}, err
	}
	return CheckPermissionOut{CanWrite: canWrite}, nil
}

// ReadStatusIn names the revision ReadStatus reads the standing referral
// status of.
type ReadStatusIn struct {
	Repository forge.SCMRepository
	SHA        string
}

// ReadStatusOut is the standing referral status ReadStatus found, or a nil
// Status when none is.
type ReadStatusOut struct {
	Status *clearance.Status
}

// ReadStatus reads the referral status standing on SHA, or a nil Status when
// none has been posted yet.
func ReadStatus(ctx context.Context, in ReadStatusIn) (ReadStatusOut, error) {
	status, err := in.Repository.ReferralStatus(ctx, in.SHA)
	if err != nil {
		return ReadStatusOut{}, err
	}
	return ReadStatusOut{Status: status}, nil
}

// PostCommentIn is the reply PostComment posts.
type PostCommentIn struct {
	Repository forge.SCMRepository
	Number     int
	Body       string
}

// PostComment posts Body as a new comment on Number.
//
// A reply answers a question somebody asked, so it is a new comment rather
// than an edit of anything already standing there: editing a standing
// comment would answer in a place the asker is not looking, and would
// overwrite it with a reply meant for one person.
func PostComment(ctx context.Context, in PostCommentIn) (struct{}, error) {
	return struct{}{}, in.Repository.CreateComment(ctx, in.Number, in.Body)
}
