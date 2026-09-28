package reviewdecision

import (
	"context"
	"reflect"
	"testing"

	"lydite/lydite/internal/depdelta"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/referral"
)

// statuses is each outcome's kind and status, in order, which is what a
// report's rows and the verdict are both read from.
func statuses(outs []Outcome) [][2]string {
	var got [][2]string
	for _, o := range outs {
		got = append(got, [2]string{string(o.Kind), string(o.Status)})
	}
	return got
}

// Every concern the review decides gets the status its own rule gives it, and
// the verdict is the worst of them: a failure outranks a referral, a referral
// outranks a pass, and a concern that never votes moves nothing.
func TestOutcomesAndTheirVerdict(t *testing.T) {
	broke := []finding.Finding{{Message: "Removed: Do"}}
	exempt := referral.Decision{Exemption: "readme-only"}
	for _, tc := range []struct {
		name     string
		declared string
		surfaces []SurfaceComparison
		deltas   []ManifestDelta
		decision referral.Decision
		want     [][2]string
		verdict  Verdict
	}{
		{
			name:     "a surface that held passes",
			surfaces: []SurfaceComparison{{Component: "sdk"}},
			decision: exempt,
			want:     [][2]string{{"api-surface", "pass"}, {"referral", "pass"}},
			verdict:  VerdictPass,
		},
		{
			name:     "a declared break refers",
			declared: "the pull request title",
			surfaces: []SurfaceComparison{{Component: "sdk", Findings: broke}},
			decision: referral.Decision{Referred: true, Disqualifications: []referral.Disqualification{{Kind: referral.DisqualificationAPIBreakDeclared}}},
			want:     [][2]string{{"api-surface", "refer"}, {"disqualification", "refer"}, {"referral", "refer"}},
			verdict:  VerdictRefer,
		},
		{
			name:     "an undeclared break fails",
			surfaces: []SurfaceComparison{{Component: "sdk", Findings: broke}},
			decision: exempt,
			want:     [][2]string{{"api-surface", "fail"}, {"referral", "pass"}},
			verdict:  VerdictFail,
		},
		{
			name:     "a surface nothing could compare is its disqualification alone",
			surfaces: []SurfaceComparison{{Component: "sdk", Uncomputable: "no go.mod"}},
			decision: referral.Decision{Referred: true, Disqualifications: []referral.Disqualification{{Kind: referral.DisqualificationAPISurfaceUncomputable}}},
			want:     [][2]string{{"disqualification", "refer"}, {"referral", "refer"}},
			verdict:  VerdictRefer,
		},
		{
			name:     "a manifest that added nothing passes",
			deltas:   []ManifestDelta{{Path: "go.sum"}},
			decision: exempt,
			want:     [][2]string{{"dependencies", "pass"}, {"referral", "pass"}},
			verdict:  VerdictPass,
		},
		{
			name: "a manifest that added a package or went unmeasured is its disqualification alone",
			deltas: []ManifestDelta{
				{Path: "go.sum", Delta: depdelta.Delta{Added: []string{"example.com/new"}}},
				{Path: "Cargo.lock", Unmeasured: "does not parse"},
			},
			decision: referral.Decision{Referred: true, Disqualifications: []referral.Disqualification{
				{Kind: referral.DisqualificationDependencyAdded},
				{Kind: referral.DisqualificationDependencyDeltaUnmeasured},
			}},
			want:    [][2]string{{"disqualification", "refer"}, {"disqualification", "refer"}, {"referral", "refer"}},
			verdict: VerdictRefer,
		},
		{
			name:     "a bundled exemption change fails",
			decision: referral.Decision{Referred: true, Bundled: []string{"src/app.go"}},
			want:     [][2]string{{"bundling", "fail"}, {"referral", "refer"}},
			verdict:  VerdictFail,
		},
		{
			name:     "an exempt change passes",
			decision: exempt,
			want:     [][2]string{{"referral", "pass"}},
			verdict:  VerdictPass,
		},
		{
			name:     "an empty change passes",
			decision: referral.Decision{Empty: true},
			want:     [][2]string{{"referral", "pass"}},
			verdict:  VerdictPass,
		},
		{
			name:     "an uncovered change refers",
			decision: referral.Decision{Referred: true, Uncovered: []string{"src/auth.go"}},
			want:     [][2]string{{"referral", "refer"}},
			verdict:  VerdictRefer,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := outcomes("base", tc.declared, tc.surfaces, tc.deltas, tc.decision, 1)
			if !reflect.DeepEqual(statuses(got), tc.want) {
				t.Errorf("outcomes = %v, want %v", statuses(got), tc.want)
			}
			if v := fold(got); v != tc.verdict {
				t.Errorf("verdict = %q, want %q", v, tc.verdict)
			}
		})
	}
}

// The fold over statuses alone, including the one no concern Decide reaches
// produces: a non-voting outcome beside a pass leaves it a pass.
func TestFold(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []Status
		want     Verdict
	}{
		{"nothing decided", nil, VerdictPass},
		{"pass", []Status{StatusPass}, VerdictPass},
		{"non-voting", []Status{StatusNonVoting}, VerdictPass},
		{"non-voting beside a pass", []Status{StatusNonVoting, StatusPass}, VerdictPass},
		{"refer", []Status{StatusPass, StatusRefer, StatusPass}, VerdictRefer},
		{"fail", []Status{StatusPass, StatusFail}, VerdictFail},
		{"fail before a refer", []Status{StatusFail, StatusRefer}, VerdictFail},
		{"fail after a refer", []Status{StatusRefer, StatusFail, StatusNonVoting}, VerdictFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var outs []Outcome
			for _, s := range tc.statuses {
				outs = append(outs, Outcome{Kind: KindReferral, Status: s})
			}
			if got := fold(outs); got != tc.want {
				t.Errorf("fold(%v) = %q, want %q", tc.statuses, got, tc.want)
			}
		})
	}
}

// Any disqualification makes the referral itself refer, even on a decision
// that does not say it was referred — so a report that shows only some of the
// disqualifications still carries their severity on the referral row.
func TestADisqualificationMakesTheReferralRefer(t *testing.T) {
	for _, d := range []referral.Decision{
		{Exemption: "readme-only", Disqualifications: []referral.Disqualification{{Kind: "suppression added"}}},
		{Empty: true, Disqualifications: []referral.Disqualification{{Kind: referral.DisqualificationAPIBreakDeclared}}},
	} {
		got := outcomes("base", "", nil, nil, d, 1)
		last := got[len(got)-1]
		if last.Kind != KindReferral || last.Status != StatusRefer {
			t.Errorf("outcomes(%+v) ends in %s/%s, want a referral that refers", d, last.Kind, last.Status)
		}
		if v := fold(got); v != VerdictRefer {
			t.Errorf("verdict = %q, want %q", v, VerdictRefer)
		}
	}
}

// A reader meets the concerns in one order: each surface, each manifest, the
// bundling check, each disqualification, then the referral — and every
// outcome carries the values its row is rendered from.
func TestOutcomesAreInTheOrderAReaderMeetsThem(t *testing.T) {
	surface := SurfaceComparison{Component: "sdk", Dir: "sdk"}
	manifest := ManifestDelta{Path: "go.sum"}
	dqs := []referral.Disqualification{{Kind: "tests removed", Evidence: "a"}, {Kind: "suppression added", Evidence: "b"}}
	d := referral.Decision{Referred: true, Bundled: []string{"src/app.go"}, Disqualifications: dqs}

	got := outcomes("abc123", "", []SurfaceComparison{surface}, []ManifestDelta{manifest}, d, 3)

	want := []Outcome{
		{Kind: KindAPISurface, Status: StatusPass, Base: "abc123", Surface: surface},
		{Kind: KindDependencies, Status: StatusPass, Base: "abc123", Manifest: manifest},
		{Kind: KindBundling, Status: StatusFail, Bundled: d.Bundled},
		{Kind: KindDisqualification, Status: StatusRefer, Disqualification: dqs[0]},
		{Kind: KindDisqualification, Status: StatusRefer, Disqualification: dqs[1]},
		{Kind: KindReferral, Status: StatusRefer, Decision: d, Exemptions: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("outcomes =\n%+v\nwant\n%+v", got, want)
	}
}

// Decide returns its outcomes and the verdict folded from them, so the result
// every caller reads carries one verdict rather than leaving it to be derived.
func TestDecideReturnsItsOutcomesAndTheirVerdict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		base    map[string]string
		head    map[string]string
		want    [][2]string
		verdict Verdict
	}{
		{
			name:    "covered",
			base:    map[string]string{"README.md": "hello", referral.FileName: readmeExemption},
			head:    map[string]string{"README.md": "hello again"},
			want:    [][2]string{{"referral", "pass"}},
			verdict: VerdictPass,
		},
		{
			name:    "nothing declared",
			base:    map[string]string{"README.md": "hello"},
			head:    map[string]string{"README.md": "hello again"},
			want:    [][2]string{{"referral", "refer"}},
			verdict: VerdictRefer,
		},
		{
			name:    "a manifest that added nothing",
			base:    map[string]string{"go.sum": goSum(map[string]string{"example.com/a": "v1.0.0"})},
			head:    map[string]string{"go.sum": goSum(map[string]string{"example.com/a": "v1.0.1"})},
			want:    [][2]string{{"dependencies", "pass"}, {"referral", "refer"}},
			verdict: VerdictRefer,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, base := repo(t, tc.base, tc.head)
			got, err := Decide(context.Background(), Input{Dir: dir, Base: base})
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if !reflect.DeepEqual(statuses(got.Outcomes), tc.want) {
				t.Errorf("outcomes = %v, want %v", statuses(got.Outcomes), tc.want)
			}
			if got.Verdict != tc.verdict {
				t.Errorf("verdict = %q, want %q", got.Verdict, tc.verdict)
			}
			for _, o := range got.Outcomes {
				if o.Kind == KindDependencies && (o.Base != base || o.Manifest.Path != "go.sum") {
					t.Errorf("dependencies outcome = %+v, want go.sum measured against %s", o, base)
				}
			}
			if last := got.Outcomes[len(got.Outcomes)-1]; !reflect.DeepEqual(last.Decision, got.Decision) || last.Exemptions != len(got.File.Exemptions) {
				t.Errorf("the referral outcome carries %+v under %d exemptions, want the result's own decision under %d",
					last.Decision, last.Exemptions, len(got.File.Exemptions))
			}
		})
	}
}
