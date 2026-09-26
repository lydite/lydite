// Package trust decides, once per run, which repository the run acts on and
// whether it holds a credential it could write to the platform with, and hands
// that decision on as a value nothing outside this package can forge.
package trust

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// TrustedContext is the repository the run acts on and the credential it
// holds, if any.
//
// It is decided from the environment alone — GITHUB_REPOSITORY, and whichever
// of GITHUB_TOKEN or GH_TOKEN is set — never from a value a caller hands in,
// and never from a token's own claims: a GitHub token does not describe its
// own scope, so the only honest question is which operating mode the job was
// started in.
//
// It is sealed. Every field is unexported and FromEnvironment is the one
// constructor, so outside this package the only TrustedContext that can be
// made is the zero value, which names no repository, holds no token and
// answers CanWrite false: a forged one can only ever claim less than the run
// holds, never more.
//
// That nothing else in the codebase bypasses FromEnvironment is a property of
// Go's export rules, confirmed by the compiler, go vet, golangci-lint and
// review. go test cannot demonstrate it: code that reached into a
// TrustedContext's fields from another package would not compile, so there is
// nothing for a test to run.
type TrustedContext struct {
	token      string
	repository string
}

// FromEnvironment reads the run's TrustedContext from the process environment.
//
// The token is GITHUB_TOKEN, or GH_TOKEN when GITHUB_TOKEN is unset or empty.
// A missing token is not an error: a run holding no credential is a legitimate
// read-only run, and CanWrite answers false for it.
//
// A missing or malformed GITHUB_REPOSITORY is an error. The platform sets it
// for every job, so its absence means the run is not where it believes it is,
// and a credential with no repository to scope it to is not one lydite can
// act on. The value must be exactly owner/name: two non-empty segments and one
// slash, since a platform repository name never contains another.
func FromEnvironment() (TrustedContext, error) {
	slug := os.Getenv("GITHUB_REPOSITORY")
	if slug == "" {
		return TrustedContext{}, errors.New("GITHUB_REPOSITORY is not set, and the platform sets it for every job")
	}
	owner, name, ok := strings.Cut(slug, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return TrustedContext{}, fmt.Errorf("GITHUB_REPOSITORY: %q is not in owner/name form", slug)
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	return TrustedContext{token: token, repository: slug}, nil
}

// Token is the credential the run holds, or empty when it holds none.
func (t TrustedContext) Token() string {
	return t.token
}

// CanWrite reports whether the run holds a credential it could write with.
func (t TrustedContext) CanWrite() bool {
	return t.token != ""
}

// Repository is the owner/name of the repository the run acts on.
func (t TrustedContext) Repository() string {
	return t.repository
}
