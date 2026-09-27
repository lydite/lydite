package clearancestages

import (
	"context"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
)

// RecordStatusesIn is the clearance RecordStatuses posts.
type RecordStatusesIn struct {
	Repository forge.SCMRepository
	// Head is the revision cleared: the one the platform answered for this
	// pull request, never anything the comment named.
	Head   string
	Number int
	// Description is the clearance's description, as DescribeClearance
	// composed it.
	Description string
	// TargetURL points both statuses at the job that produced them, and is
	// empty where there is no job to point at.
	TargetURL string
}

// RecordStatuses posts the clearance: `lydite/clearance`, which says who
// cleared the revision, and then `lydite/referral` resolved to success, which
// is what a required check is gated on. A clearance that records only the
// first leaves every consumer's pull request blocked on a referral nobody can
// resolve.
//
// The clearance goes first: if the second post fails, the pull request holds
// a clearance record beside a referral still standing, which a repeated
// comment repairs. The other order could leave a green referral with nothing
// recording who cleared it. A post that fails is this stage's error, and the
// second is never attempted after the first failed — a clearance nothing
// recorded leaves the referral standing while the job that answered the
// comment reports success.
func RecordStatuses(ctx context.Context, in RecordStatusesIn) (struct{}, error) {
	cleared, resolved := statuses(in.Head, in.Number, in.Description, in.TargetURL)
	if err := in.Repository.PostStatus(ctx, cleared); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, in.Repository.PostStatus(ctx, resolved)
}

// RenderStatusesIn is the clearance RenderStatuses writes, and where.
type RenderStatusesIn struct {
	// Path is where the `lydite/clearance` document goes. The
	// `lydite/referral` document goes to its sibling with .referral before
	// the extension.
	Path        string
	Head        string
	Number      int
	Description string
	TargetURL   string
}

// RenderStatuses writes the clearance as two documents for a step that posts
// them: the `lydite/clearance` status at Path, and the `lydite/referral`
// status resolved to success at the .referral sibling.
//
// Two documents rather than one carrying both, because each is a single
// status object — which is what the relay's /status route and the posting
// step's `jq -r .context` each read — and the relay admits a clearance ref to
// `lydite/clearance` alone: the referral document is the posting step's to
// write with the job's own token, and is never relayed. The clearance is
// written first, for the reason RecordStatuses posts it first, and a document
// that cannot be written is this stage's error.
func RenderStatuses(_ context.Context, in RenderStatusesIn) (struct{}, error) {
	cleared, resolved := statuses(in.Head, in.Number, in.Description, in.TargetURL)
	if err := forge.WriteStatus(in.Path, cleared); err != nil {
		return struct{}{}, err
	}
	return struct{}{}, forge.WriteStatus(referralDocument(in.Path), resolved)
}

// statuses are the two statuses a clearance records on the head, as the
// documents both routes carry, so what a step posts and what this process
// would have posted itself are one derivation rather than two.
//
// Each names the pull request as well as the revision: a clearance run is an
// issue_comment run, whose own claims name a branch rather than a pull ref,
// so the conversation is in the document or nowhere.
//
// description is composed through clearance.WithFingerprint, which is what
// keeps the fingerprint of the cleared decision inside the platform's cap:
// the description is the whole of where that fingerprint is stored, and forge
// clips a status's description on the way out by either route.
func statuses(head string, number int, description, targetURL string) (cleared, resolved forge.Status) {
	cleared = forge.Status{
		State:       clearance.StateSuccess,
		Context:     clearance.ClearanceContext,
		Description: description,
		TargetURL:   targetURL,
		SHA:         head,
		PullRequest: number,
	}
	resolved = cleared
	resolved.Context = clearance.Context
	return cleared, resolved
}

// referralDocument names the second document a rendered clearance writes,
// beside the one the caller named.
//
// The path is derived rather than configured so that the step posting the
// documents computes it from the path it already passed, and a lydite that
// can render a clearance can always render the referral resolving it. A
// second configured path would make resolving the referral something a caller
// could omit, which is the same pull request blocked on a pending gate.
func referralDocument(out string) string {
	ext := filepath.Ext(out)
	return strings.TrimSuffix(out, ext) + ".referral" + ext
}
