package queuestages

import (
	"context"

	"lydite/lydite/internal/relay"
)

// MintTokenIn is what the run's OIDC token is minted with.
type MintTokenIn struct {
	Client relay.Doer
	// MintURL and MintToken are the platform's mint endpoint and the token
	// that authorises the mint. Either one empty is a job that was not
	// granted id-token: write.
	MintURL   string
	MintToken string
	// Relay is the relay's origin, and the audience the token is minted
	// for.
	Relay string
}

// MintTokenOut is the minted OIDC token.
type MintTokenOut struct {
	Token string
}

// MintToken mints the run's own OIDC token with the relay's origin as its
// audience, so the token cannot be replayed against another service.
//
// A missing endpoint or mint token is relay.ErrNoMintEndpoint, returned as it
// is, and no request is made.
func MintToken(ctx context.Context, in MintTokenIn) (MintTokenOut, error) {
	token, err := relay.MintIDToken(ctx, in.Client, in.MintURL, in.MintToken, in.Relay)
	if err != nil {
		return MintTokenOut{}, err
	}
	return MintTokenOut{Token: token}, nil
}

// SubmitComparisonIn is the comparison submitted, where, and under which
// token.
type SubmitComparisonIn struct {
	Client relay.Doer
	// Relay is the relay's origin.
	Relay string
	// Token is the OIDC token MintToken minted for Relay.
	Token string
	// HeadRef and PullRequest name the entry.
	HeadRef     string
	PullRequest int
	// HeadSHA is the queue revision the verdict is published against.
	HeadSHA string
	// BaseSHA is the revision the decision was recomputed against.
	BaseSHA     string
	Fingerprint string
	Referred    bool
}

// SubmitComparisonOut is what the relay answered.
type SubmitComparisonOut struct {
	Answer relay.Answer
}

// SubmitComparison submits the recomputed decision's fingerprint to the relay,
// which compares it against the clearance the originating pull request holds
// and publishes the status itself.
//
// Every answer but a 200 naming a state is an error, and nothing falls back:
// this job holds no token that could publish anything, so a refused or
// unreachable relay is "no verdict was published".
func SubmitComparison(ctx context.Context, in SubmitComparisonIn) (SubmitComparisonOut, error) {
	answer, err := relay.SubmitMergeGroup(ctx, in.Client, in.Relay, in.Token, relay.MergeGroupRequest{
		QueueRef:    in.HeadRef,
		PullRequest: in.PullRequest,
		SHA:         in.HeadSHA,
		BaseSHA:     in.BaseSHA,
		Fingerprint: in.Fingerprint,
		Referred:    in.Referred,
	})
	if err != nil {
		return SubmitComparisonOut{}, err
	}
	return SubmitComparisonOut{Answer: answer}, nil
}
