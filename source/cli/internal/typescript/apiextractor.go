package typescript

import (
	"context"
	_ "embed"
)

// The pin manifest — see the package doc in typescript.go for why the version
// lives in package.json rather than a Go constant.
var (
	//go:embed api-extractor-pin/package.json
	apiExtractorPackageJSON []byte
	//go:embed api-extractor-pin/package-lock.json
	apiExtractorPackageLock []byte
)

// APIExtractorBin is the binary the pin installs, under the toolchain
// directory's node_modules/.bin.
const APIExtractorBin = "api-extractor"

// EnsureAPIExtractor installs the pinned @microsoft/api-extractor and answers
// the toolchain directory holding it.
//
// env is what lydite provisions its own tools with — executil.Env's Install,
// never a scanned repository's own — for the reason that field states.
//
// It is exported where ensureBiome is not because its caller is
// internal/tsapisurface: go:embed reaches no directory but its own, so the
// manifest Dependabot watches sits beside this package's other npm pin and the
// install is reached from here rather than copied there.
func EnsureAPIExtractor(ctx context.Context, env []string) (string, error) {
	return ensureNPMToolchain(ctx, env, "api-extractor", APIExtractorBin, apiExtractorPackageJSON, apiExtractorPackageLock)
}
