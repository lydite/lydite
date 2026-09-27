// Package truststages holds the stage that establishes a run's
// trust.TrustedContext. It is named apart from internal/trust so a flow
// definition can import both without renaming either.
package truststages

import (
	"context"

	"lydite/lydite/internal/trust"
)

// Out is what InitTrust establishes.
type Out struct {
	Trusted trust.TrustedContext
}

// InitTrust reads the run's TrustedContext from the process environment. It
// is the one stage that reads the environment; every later stage receives the
// TrustedContext it returns rather than looking again.
//
// An environment trust.FromEnvironment refuses is this stage's error: a run
// that cannot say which repository it acts on has nothing for a later stage
// to do.
func InitTrust(_ context.Context, _ struct{}) (Out, error) {
	trusted, err := trust.FromEnvironment()
	if err != nil {
		return Out{}, err
	}
	return Out{Trusted: trusted}, nil
}
