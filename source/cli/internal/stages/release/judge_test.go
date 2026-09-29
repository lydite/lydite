package releasestages

import (
	"context"
	"reflect"
	"testing"
)

// Every commit declaring a break is named by its subject, whether it declared
// in the header or in a footer, and the range is named by the two versions.
func TestJudgeNamesEveryDeclaringCommitBySubject(t *testing.T) {
	out, err := Judge(context.Background(), JudgeIn{
		Previous: "v0.2.0",
		Tag:      "v0.3.0",
		Messages: []string{
			"feat(api)!: the verdict is a status, not a boolean",
			"fix: a leader dot",
			"  refactor: the report\n\nBREAKING CHANGE: the document drops ok  \n",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := JudgeOut{
		Declaring: []string{"feat(api)!: the verdict is a status, not a boolean", "refactor: the report"},
		Count:     3,
		Range:     "v0.2.0..v0.3.0",
		Admits:    true,
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("Judge = %+v, want %+v", out, want)
	}
}

// A range declaring nothing names nobody, and still reports the bump and the
// count, because the caller's row reads both.
func TestJudgeDeclaringNothingNamesNobody(t *testing.T) {
	out, err := Judge(context.Background(), JudgeIn{
		Previous: "v1.2.0",
		Tag:      "v1.2.1",
		Messages: []string{"fix: a leader dot", "docs: the handbook"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := JudgeOut{Count: 2, Range: "v1.2.0..v1.2.1", Admits: false}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("Judge = %+v, want %+v", out, want)
	}
}

// Admits is the bump's own answer, independent of whether anything declared.
func TestJudgeAdmitsFollowsTheBump(t *testing.T) {
	for _, c := range []struct {
		previous, tag string
		want          bool
	}{
		{"v0.2.0", "v0.2.1", false},
		{"v1.2.0", "v2.0.0", true},
	} {
		out, err := Judge(context.Background(), JudgeIn{Previous: c.previous, Tag: c.tag, Messages: []string{"feat!: a break"}})
		if err != nil {
			t.Fatal(err)
		}
		if out.Admits != c.want {
			t.Errorf("Judge(%s..%s).Admits = %v, want %v", c.previous, c.tag, out.Admits, c.want)
		}
	}
}

// The bump rule is ADR 0010's, in both eras: a break lands where the leftmost
// non-zero component of the previous version increases. Each case names the
// era it is testing, because the two halves of the rule disagree about exactly
// the same minor bump.
func TestBumpAdmitsBreakOnlyWhereTheLeftmostNonZeroComponentIncreases(t *testing.T) {
	for _, c := range []struct {
		previous, tag string
		want          bool
		why           string
	}{
		{"v0.2.0", "v0.3.0", true, "in 0.x the leftmost non-zero component is the minor"},
		{"v0.2.0", "v0.2.1", false, "a patch bump carries no break in any era"},
		{"v0.2.0", "v0.10.0", true, "the minor is compared numerically, not lexically"},
		{"v0.9.0", "v1.0.0", true, "the release that leaves 0.x may carry one"},
		{"v0.2.0", "v1.0.0", true, "crossing eras, though the minor it is measured on went 2 to 0"},
		{"v1.2.0", "v1.3.0", false, "from 1.0.0 onward a minor bump is backward-compatible"},
		{"v1.2.0", "v1.2.1", false, "nor does a patch bump in 1.x"},
		{"v1.2.0", "v2.0.0", true, "from 1.0.0 onward the leftmost non-zero component is the major"},
		{"v1.2.0", "v10.0.0", true, "the major is compared numerically too"},
		// A prerelease is the line it leads to arriving early, so it carries
		// that line's bump: the marker is not a version component.
		{"v0.2.0", "v0.3.0-rc.1", true, "a prerelease of an admitting bump admits one"},
		{"v0.2.0", "v0.2.1-rc.1", false, "a prerelease of a patch bump admits none"},
		{"v1.2.0", "v2.0.0-rc.1", true, "a prerelease of a major bump admits one"},
		// Below 0.1.0 the leftmost non-zero component is the patch, which is
		// the same rule read one position further right.
		{"v0.0.3", "v0.0.4", true, "in 0.0.x the leftmost non-zero component is the patch"},
		{"v0.0.3", "v0.1.0", true, "a minor bump out of 0.0.x increases it too"},
		{"v0.0.3", "v0.0.3", false, "no bump at all admits nothing, even in 0.0.x"},
	} {
		if got := bumpAdmitsBreak(c.previous, c.tag); got != c.want {
			t.Errorf("bumpAdmitsBreak(%q, %q) = %v, want %v — %s", c.previous, c.tag, got, c.want, c.why)
		}
	}
}
