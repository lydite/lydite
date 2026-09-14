package secrets

// Pinned so every run scans with one gitleaks, whatever the machine already
// holds.
//
// A Go constant rather than a value read from gitleaks-pin/go.mod, for the
// reason internal/golang's gosec and govulncheck constants are: the pin has to
// be a module of its own or gitleaks' dependency graph joins lydite's, and an
// embed directive cannot read a file inside a nested module. internal/pins is
// what keeps the constant and the manifest saying the same thing, and
// `go run ./tools/pinsync` is what writes the constant when a bump arrives.
const (
	gitleaksVersion = "v8.30.1"

	gitleaksPkg = "github.com/zricethezav/gitleaks/v8@" + gitleaksVersion
)
