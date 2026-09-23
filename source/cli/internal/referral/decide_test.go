package referral

import "testing"

func TestFingerprintIsOrderIndependent(t *testing.T) {
	uncoveredA := []string{"a.go", "b.go", "c.go"}
	uncoveredB := []string{"c.go", "a.go", "b.go"}
	dqA := []Disqualification{
		{Kind: "suppression", Path: "x.go", Evidence: "one"},
		{Kind: "coverage-decline", Path: "y.go", Evidence: "two"},
	}
	dqB := []Disqualification{
		{Kind: "coverage-decline", Path: "y.go", Evidence: "two"},
		{Kind: "suppression", Path: "x.go", Evidence: "one"},
	}

	got := Fingerprint(uncoveredA, dqA)
	want := Fingerprint(uncoveredB, dqB)
	if got != want {
		t.Fatalf("Fingerprint depended on input order: %q != %q", got, want)
	}
}

func TestFingerprintChangesWithAPath(t *testing.T) {
	f1 := Fingerprint([]string{"a.go", "b.go"}, nil)
	f2 := Fingerprint([]string{"a.go", "c.go"}, nil)
	if f1 == f2 {
		t.Fatalf("a changed uncovered path must change the fingerprint, both were %q", f1)
	}
}

func TestFingerprintChangesWithADisqualification(t *testing.T) {
	f1 := Fingerprint(nil, []Disqualification{{Kind: "suppression", Path: "x.go"}})
	f2 := Fingerprint(nil, []Disqualification{{Kind: "suppression", Path: "y.go"}})
	if f1 == f2 {
		t.Fatalf("a changed disqualification path must change the fingerprint, both were %q", f1)
	}

	f3 := Fingerprint(nil, []Disqualification{{Kind: "coverage-decline", Path: "x.go"}})
	if f1 == f3 {
		t.Fatalf("a changed disqualification kind must change the fingerprint, both were %q", f1)
	}
}

// The disqualification's Evidence text is not part of its identity — Kind
// and Path are, matching disqualify.go's own doc comment — so a change to
// Evidence alone must not move the fingerprint.
func TestFingerprintIgnoresDisqualificationEvidence(t *testing.T) {
	f1 := Fingerprint(nil, []Disqualification{{Kind: "suppression", Path: "x.go", Evidence: "first count"}})
	f2 := Fingerprint(nil, []Disqualification{{Kind: "suppression", Path: "x.go", Evidence: "second count"}})
	if f1 != f2 {
		t.Fatalf("Evidence alone must not change the fingerprint: %q != %q", f1, f2)
	}
}

// docs/adr/0053: a disqualifier-only referral must not read the same as a
// fully-exempt change, and must not read the same as a purely
// path-uncovered referral over the same paths — Uncovered alone is blind to
// exactly this case.
func TestFingerprintDistinguishesEmptyFromDisqualifierOnly(t *testing.T) {
	fullyExempt := Fingerprint(nil, nil)
	disqualifierOnly := Fingerprint(nil, []Disqualification{{Kind: "suppression", Path: "x.go"}})
	if fullyExempt == disqualifierOnly {
		t.Fatalf("a disqualifier-only referral must not fingerprint the same as a fully-exempt change")
	}

	pathUncoveredOnly := Fingerprint([]string{"x.go"}, nil)
	if disqualifierOnly == pathUncoveredOnly {
		t.Fatalf("a disqualifier-only referral must not fingerprint the same as a path-uncovered referral over the same path")
	}
	if fullyExempt == pathUncoveredOnly {
		t.Fatalf("a path-uncovered referral must not fingerprint the same as a fully-exempt change")
	}
}
