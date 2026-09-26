// Package trust decides, once per run, whether the run holds a credential it
// could write to the platform with, and hands that decision to the rest of a
// Flow as a value nothing outside this package can forge.
package trust

import (
	"context"
	"os"

	"lydite/lydite/internal/flow"
)

// TrustedContext is whether this run holds a write-capable credential at all.
//
// It is decided from the environment alone — whether GITHUB_TOKEN or GH_TOKEN
// is set to anything — never from a value a caller hands in, and never from a
// token's own claims: a GitHub token does not describe its own scope, so the
// only honest question is which operating mode the job was started in.
//
// It is sealed. Every field is unexported and newTrustedContext, called only
// from InitializeTrustContext's Run, is the one constructor, so outside this
// package the only TrustedContext that can be made is the zero value, which
// answers CanWrite false: a forged one can only ever claim less than the run
// holds, never more.
//
// That nothing else in the codebase bypasses InitializeTrustContext is a
// property of Go's export rules, confirmed by the compiler, go vet,
// golangci-lint and review. go test cannot demonstrate it: a test proves the
// public API behaves, and code that reached into a TrustedContext's fields
// from another package would not compile, so there is nothing for a test to
// run.
type TrustedContext struct {
	canWrite bool
}

// CanWrite reports whether the run holds a credential it could write with.
func (t TrustedContext) CanWrite() bool {
	return t.canWrite
}

func newTrustedContext() TrustedContext {
	return TrustedContext{canWrite: os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("GH_TOKEN") != ""}
}

// key is unexported so the only Write that can store a TrustedContext on a
// Context is the one InitializeTrustContext's Run returns.
var key = flow.NewKey[TrustedContext]("trust")

// Get reads the TrustedContext InitializeTrustContext joined into r, reporting
// false when no stage has run it. A caller treats false as no credential.
func Get(r flow.Reader) (TrustedContext, bool) {
	return flow.Get(r, key)
}

// InitializeTrustContext is the StageComponent that decides the run's
// TrustedContext and joins it into the Flow's Context.
type InitializeTrustContext struct{}

// Name is the component's name in a Flow's reports.
func (InitializeTrustContext) Name() string {
	return "initialize-trust-context"
}

// Run reads the environment and writes the TrustedContext it decides. A Flow
// that cannot establish what it may write with has no business going on, so
// the policy is FailFlow.
func (InitializeTrustContext) Run(context.Context, flow.View) (flow.Result, error) {
	return flow.Result{
		Policy: flow.FailFlow,
		Writes: []flow.Write{flow.Put(key, newTrustedContext())},
	}, nil
}
