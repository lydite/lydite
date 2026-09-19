package depdelta

import "testing"

// The classification is the raw arithmetic on the positions, so a caller
// reading it sees what the numbers say and not what an exemption makes of
// them. A `0.4.0` to `0.5.0` move is a minor here even though nothing may
// exempt it.
func TestClassifyReadsThePositionsAndNothingElse(t *testing.T) {
	for _, c := range []struct {
		base, head string
		want       Level
	}{
		{"1.2.3", "1.2.4", LevelPatch},
		{"1.2.3", "1.3.0", LevelMinor},
		{"1.2.3", "2.0.0", LevelMajor},
		{"0.4.0", "0.5.0", LevelMinor},
		{"0.0.1", "0.0.2", LevelPatch},
		{"v1.10.1", "v1.10.2", LevelPatch},
		{"3.2.4", "4.0.1", LevelMajor},
		{"2.5.11", "2.5.12", LevelPatch},
		// A pre-release and a build tag are semver, and sit on the patch
		// position's side of every boundary above it.
		{"1.2.3-rc.1", "1.2.3", LevelPatch},
		{"v2.0.0+incompatible", "v2.0.1+incompatible", LevelPatch},
		// A Go pseudo-version is semver, and is in the `0.0.0` line.
		{"v0.0.0-20260908205506-85c1c2202aba", "v0.0.0-20260910120000-3f0b1c2d4e5f", LevelPatch},
	} {
		if got := Classify(c.base, c.head); got != c.want {
			t.Errorf("Classify(%q, %q) = %q, want %q", c.base, c.head, got, c.want)
		}
	}
}

// A version this cannot measure is its own answer. A git ref, a wildcard or a
// requirement operator says nothing about how far the package moved, and
// reading one as a small move is how the largest change takes the quietest
// path.
func TestClassifyRefusesAVersionItCannotRead(t *testing.T) {
	for _, c := range [][2]string{
		{"1.2.3", "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"},
		{"*", "1.2.3"},
		{"1.2.3", "=1.2.4"},
		{"1.2.3", "^1.3.0"},
		{"1.2.3", ""},
		{"git+https://github.com/example/pkg#main", "1.2.3"},
	} {
		if got := Classify(c[0], c[1]); got != LevelNotComparable {
			t.Errorf("Classify(%q, %q) = %q, want %q", c[0], c[1], got, LevelNotComparable)
		}
		if PatchOrMinorEligible(c[0], c[1]) {
			t.Errorf("PatchOrMinorEligible(%q, %q) = true", c[0], c[1])
		}
	}
}

// Eligibility is stated in positions rather than in the arithmetic, because a
// `0.x` line is not boring the way a `1.x` one is: SemVer holds that `0.y.z`
// is initial development and that inside `0.0.*` anything may change at any
// time.
func TestPatchOrMinorEligibility(t *testing.T) {
	for _, c := range []struct {
		base, head string
		want       bool
	}{
		{"1.2.3", "1.2.4", true},
		{"1.2.3", "1.3.0", true},
		{"1.2.3", "2.0.0", false},
		{"0.4.1", "0.4.2", true},
		{"0.4.0", "0.5.0", false},
		{"0.5.0", "0.4.0", false},
		{"0.0.1", "0.0.2", false},
		{"0.0.1", "0.1.0", false},
		{"0.1.0", "1.0.0", false},
		{"v0.40.0", "v0.41.0", false},
		{"v1.10.1", "v1.10.2", true},
	} {
		if got := PatchOrMinorEligible(c.base, c.head); got != c.want {
			t.Errorf("PatchOrMinorEligible(%q, %q) = %v, want %v", c.base, c.head, got, c.want)
		}
	}
}

// A package pinned at two versions on one side states no pair, so there is no
// distance to classify and nothing to exempt. Picking one of the two would
// measure a move nobody wrote down.
func TestAChangeWithTwoPinsOnASideIsNotEligible(t *testing.T) {
	c := Change{Name: "rand", Base: []string{"0.8.5"}, Head: []string{"0.8.5", "0.8.6"}}
	if c.PatchOrMinorEligible() {
		t.Fatal("a change pinning two versions at HEAD is eligible")
	}
}
