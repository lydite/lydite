package teststages

import (
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/runner"
	testmeasure "lydite/lydite/internal/test/measure"
)

// A base tree's measurement names every component it failed to measure, and
// says nothing about one nothing could ever measure. That absence is permanent
// and expected — testmeasure.RecordingBlockedBy already exempts it, and
// testmeasure.FloorRows already excludes it — so warning about it prints what reads as a measurement failure
// on every baseline computation, forever, about a state the repository stated
// on purpose.
func TestABaseTreesUnmeasurableComponentIsNotAFailedMeasurement(t *testing.T) {
	t.Parallel()
	decl := []component.Component{
		{Name: "api", Dir: "api", Runner: runner.GoTest},
		{Name: "docs", Dir: "docs", Command: []string{"make"}},
		{Name: "web", Dir: "web", Runner: runner.Vitest},
	}
	ms := []testmeasure.Measurement{
		measured("api", 9, 10),
		testmeasure.UnmeasurableComponent(decl[1], "the component declares a raw command, which has no instrumented variant"),
		testmeasure.UnmeasuredComponent(decl[2], "no container runtime"),
	}
	var warnings strings.Builder
	out := baseTreeBaseline(&warnings, "abcdef1234567890", ms, map[string][]string{"web": {"docker: not found"}})

	if _, ok := out["api"]; !ok || len(out) != 1 {
		t.Errorf("baseline = %v, want the one measured component", out)
	}
	if got := warnings.String(); strings.Contains(got, "docs") {
		t.Errorf("warnings = %q, want nothing about a component nothing could ever measure", got)
	}
	// The component that genuinely failed there is still named, with the tail
	// that is the only account of it outliving the worktree.
	if got := warnings.String(); !strings.Contains(got, "web") || !strings.Contains(got, "docker: not found") {
		t.Errorf("warnings = %q, want the failed component and its tail", got)
	}
}
