package depdelta

import (
	"strings"

	"golang.org/x/mod/semver"
)

// Level is how far a version pair moved.
type Level string

const (
	// LevelNotComparable is a pair this cannot measure: a git-ref pin, a
	// wildcard, a requirement operator, anything no semver parser accepts. It
	// is a level of its own rather than the smallest one, because a distance
	// nobody could measure and a short distance are not the same answer.
	LevelNotComparable Level = "not-comparable"
	// LevelPatch is a pair whose major and minor positions are equal.
	LevelPatch Level = "patch"
	// LevelMinor is a pair whose major position is equal and whose minor
	// position is not.
	LevelMinor Level = "minor"
	// LevelMajor is a pair whose major position changed.
	LevelMajor Level = "major"
)

// Classify is the semver level a version pair moved by.
//
// This is the raw arithmetic on the positions and deliberately nothing else:
// `0.4.0` to `0.5.0` is a minor here, because that is what the numbers say.
// Whether such a pair is routine maintenance is a separate question, asked by
// PatchOrMinorEligible, which is where the pre-1.0 carve-out lives.
func Classify(base, head string) Level {
	b, ok := canonical(base)
	if !ok {
		return LevelNotComparable
	}
	h, ok := canonical(head)
	if !ok {
		return LevelNotComparable
	}
	switch {
	case semver.Major(b) != semver.Major(h):
		return LevelMajor
	case semver.MajorMinor(b) != semver.MajorMinor(h):
		return LevelMinor
	}
	return LevelPatch
}

// PatchOrMinorEligible reports whether a version pair is the routine
// maintenance a `versions: patch-and-minor` exemption may cover.
//
// A pair this cannot compare is not eligible, for the same reason an
// unreadable manifest measures nothing: lydite cannot say how far the version
// moved, so it does not say it moved a little.
//
// A `0.x` line is not boring the way a `1.x` one is, and the rule is stated in
// positions rather than in the arithmetic Classify does. SemVer holds that
// `0.y.z` is initial development whose API is not stable, and that inside
// `0.0.*` anything may change at any time; cargo's caret default encodes the
// first half by treating `0.y.z` as compatible only within `y`. So a minor
// move inside `0.x` is not eligible however plainly it reads as a minor, and
// no move at all inside `0.0.*` is eligible.
func PatchOrMinorEligible(base, head string) bool {
	b, ok := canonical(base)
	if !ok {
		return false
	}
	h, ok := canonical(head)
	if !ok {
		return false
	}
	switch Classify(base, head) {
	case LevelMajor, LevelNotComparable:
		return false
	case LevelPatch, LevelMinor:
	}
	if semver.Major(b) != "v0" {
		return true
	}
	// Inside `0.x`, the minor position is the compatibility boundary, and
	// inside `0.0.*` there is no boundary at all.
	return semver.MajorMinor(b) == semver.MajorMinor(h) && semver.MajorMinor(b) != "v0.0"
}

// canonical is a version string as x/mod/semver reads it, and false for one it
// does not read at all.
//
// The leading `v` is Go's spelling and not cargo's or npm's, so it is supplied
// when it is missing. Nothing else is repaired: a requirement operator, a
// wildcard or a git ref stays unparseable, which is the answer a caller needs
// about it.
func canonical(version string) (string, bool) {
	v := strings.TrimSpace(version)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return "", false // [lydite:exclude_from_mutation][the string beside a
		// false second value is read by nobody: both callers discard it and
		// return early on the bool alone, so no caller can be shown a
		// different one]
	}
	return v, true
}
