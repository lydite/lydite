// Package queueflow declares the flow that carries a clearance forward onto a
// merge-queue entry: reading the entry out of the merge_group payload,
// resolving the base its decision is recomputed against, recomputing and
// fingerprinting that decision, minting the OIDC token the relay accepts, and
// submitting the comparison for the relay to publish.
//
// It is a straight line with no branching: five stages, each feeding the
// next, and no stage conditioned on another's output. It is a declaration and
// nothing else — nothing here reads the environment, mints a token itself, or
// talks to the relay; what the run produced is read back off the Result by
// the stage names below.
package queueflow

import (
	"lydite/lydite/internal/flow"
	"lydite/lydite/internal/relay"
	queuestages "lydite/lydite/internal/stages/queue"
)

// Name is the flow's name, which every error it raises for a stage carries.
const Name = "queue"

// The keys of the flow.Inputs the flow is run with. Params.Inputs supplies
// every one of them.
const (
	// InputDir is the checkout the decision is recomputed over.
	InputDir = "Dir"
	// InputEventPath names the merge_group payload load-event reads.
	InputEventPath = "EventPath"
	// InputBase is a commit, or "auto" for the merge-base with the queued
	// branch.
	InputBase = "Base"
	// InputRelay is the relay's origin: the audience the token is minted
	// for, and where the comparison is submitted.
	InputRelay = "Relay"
	// InputMintURL and InputMintToken are the platform's mint endpoint and
	// the token that authorises the mint.
	InputMintURL   = "MintURL"
	InputMintToken = "MintToken"
	// InputClient is the relay.Doer every request is made with.
	InputClient = "Client"
)

// The names of the flow's stages, in the order they run.
const (
	StageLoadEvent         = "load-event"
	StageResolveBase       = "resolve-base"
	StageRecomputeDecision = "recompute-decision"
	StageMintToken         = "mint-token"
	StageSubmitComparison  = "submit-comparison"
)

// Params is what one run carries an entry's clearance forward with.
type Params struct {
	Dir       string
	EventPath string
	Base      string
	Relay     string
	MintURL   string
	MintToken string
	Client    relay.Doer
}

// Inputs are p as the flow is run with them.
func (p Params) Inputs() flow.Inputs {
	return flow.Inputs{
		InputDir:       p.Dir,
		InputEventPath: p.EventPath,
		InputBase:      p.Base,
		InputRelay:     p.Relay,
		InputMintURL:   p.MintURL,
		InputMintToken: p.MintToken,
		InputClient:    p.Client,
	}
}

// New builds the flow.
//
// Five stages, one responsibility each: load-event reads the entry the
// merge_group payload names; resolve-base resolves the commit its decision is
// recomputed against; recompute-decision recomputes and fingerprints that
// decision; mint-token mints the OIDC token the relay accepts, audienced to
// the relay's own origin; submit-comparison submits the fingerprint for the
// relay to compare and publish. mint-token is declared after
// recompute-decision and before submit-comparison: nothing about minting
// depends on the decision, but a run that cannot resolve the base or
// recompute the decision has nothing to submit, and should fail for that
// reason before it fails for a missing mint endpoint. No stage is
// conditioned on another's output, so none needs a guard beyond Build's own
// binding checks.
func New() (*flow.Flow, error) {
	return flow.New(Name).
		Stage(StageLoadEvent, queuestages.LoadEvent).
		With("EventPath", flow.FromInput(InputEventPath)).
		Stage(StageResolveBase, queuestages.ResolveBase).
		With("Dir", flow.FromInput(InputDir)).
		With("Base", flow.FromInput(InputBase)).
		With("BaseBranch", flow.FromStage(StageLoadEvent, "BaseBranch")).
		Stage(StageRecomputeDecision, queuestages.RecomputeDecision).
		With("Dir", flow.FromInput(InputDir)).
		With("BaseSHA", flow.FromStage(StageResolveBase, "BaseSHA")).
		Stage(StageMintToken, queuestages.MintToken).
		With("Client", flow.FromInput(InputClient)).
		With("MintURL", flow.FromInput(InputMintURL)).
		With("MintToken", flow.FromInput(InputMintToken)).
		With("Relay", flow.FromInput(InputRelay)).
		Stage(StageSubmitComparison, queuestages.SubmitComparison).
		With("Client", flow.FromInput(InputClient)).
		With("Relay", flow.FromInput(InputRelay)).
		With("Token", flow.FromStage(StageMintToken, "Token")).
		With("HeadRef", flow.FromStage(StageLoadEvent, "HeadRef")).
		With("PullRequest", flow.FromStage(StageLoadEvent, "PullRequest")).
		With("HeadSHA", flow.FromStage(StageLoadEvent, "HeadSHA")).
		With("BaseSHA", flow.FromStage(StageResolveBase, "BaseSHA")).
		With("Fingerprint", flow.FromStage(StageRecomputeDecision, "Fingerprint")).
		With("Referred", flow.FromStage(StageRecomputeDecision, "Referred")).
		Build()
}
