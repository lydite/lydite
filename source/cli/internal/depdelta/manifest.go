package depdelta

import "path"

// Ecosystem is the package manager a manifest belongs to.
type Ecosystem string

const (
	// EcosystemNone is a path that is no dependency manifest. It is the zero
	// value, so a Manifest nobody detected names no ecosystem rather than
	// Go's.
	EcosystemNone Ecosystem = ""
	// EcosystemGo is `go.mod` and `go.sum`.
	EcosystemGo Ecosystem = "go"
	// EcosystemCargo is `Cargo.lock`.
	EcosystemCargo Ecosystem = "cargo"
	// EcosystemNPM is `package-lock.json`.
	EcosystemNPM Ecosystem = "npm"
	// EcosystemYarn is `yarn.lock`, which has two incompatible formats and no
	// reader here.
	EcosystemYarn Ecosystem = "yarn"
	// EcosystemPnpm is `pnpm-lock.yaml`, which encodes pnpm's own store
	// layout and has no reader here.
	EcosystemPnpm Ecosystem = "pnpm"
	// EcosystemPip is `requirements.txt`, `poetry.lock` and `Pipfile.lock`,
	// none of which has a reader here.
	EcosystemPip Ecosystem = "pip"
)

// Manifest is the kind of dependency manifest a path names.
//
// The named-but-unreadable kinds exist so that a yarn, pnpm or pip manifest is
// a gap this can say out loud. A reader written by guessing at one of those
// formats fails in the one direction that matters — it reports no additions
// from a file it half-understood — so the format is named and not read, and a
// caller reports which manifest it could not measure.
type Manifest string

const (
	// ManifestNone is a path naming no dependency manifest at all.
	ManifestNone         Manifest = ""
	ManifestGoMod        Manifest = "go.mod"
	ManifestGoSum        Manifest = "go.sum"
	ManifestCargoLock    Manifest = "Cargo.lock"
	ManifestNPMLock      Manifest = "package-lock.json"
	ManifestYarnLock     Manifest = "yarn.lock"
	ManifestPnpmLock     Manifest = "pnpm-lock.yaml"
	ManifestRequirements Manifest = "requirements.txt"
	ManifestPoetryLock   Manifest = "poetry.lock"
	ManifestPipfileLock  Manifest = "Pipfile.lock"
)

// manifests is every manifest this recognises, keyed by the file name it
// carries wherever in the tree it sits.
//
// A manifest is recognised by its base name and by nothing else, because that
// is what a changed path answers completely. Reading a component's declared
// lockfile instead would make a question about paths depend on component
// loading, and would go wrong by missing a manifest no component declared.
var manifests = map[string]Manifest{
	string(ManifestGoMod):        ManifestGoMod,
	string(ManifestGoSum):        ManifestGoSum,
	string(ManifestCargoLock):    ManifestCargoLock,
	string(ManifestNPMLock):      ManifestNPMLock,
	string(ManifestYarnLock):     ManifestYarnLock,
	string(ManifestPnpmLock):     ManifestPnpmLock,
	string(ManifestRequirements): ManifestRequirements,
	string(ManifestPoetryLock):   ManifestPoetryLock,
	string(ManifestPipfileLock):  ManifestPipfileLock,
}

// Detect is the manifest a repository-root-relative path names, and
// ManifestNone when it names none.
func Detect(p string) Manifest { return manifests[path.Base(p)] }

// Ecosystem is the package manager this manifest belongs to.
func (m Manifest) Ecosystem() Ecosystem {
	switch m {
	case ManifestGoMod, ManifestGoSum:
		return EcosystemGo
	case ManifestCargoLock:
		return EcosystemCargo
	case ManifestNPMLock:
		return EcosystemNPM
	case ManifestYarnLock:
		return EcosystemYarn
	case ManifestPnpmLock:
		return EcosystemPnpm
	case ManifestRequirements, ManifestPoetryLock, ManifestPipfileLock:
		return EcosystemPip
	}
	return EcosystemNone
}

// Readable reports whether Extract can build a set from this manifest's
// content. A manifest that is not readable is one a comparison cannot be made
// over, never one with nothing to say.
func (m Manifest) Readable() bool {
	switch m {
	case ManifestGoMod, ManifestGoSum, ManifestCargoLock, ManifestNPMLock:
		return true
	}
	return false
}
