package queuestages

import (
	"context"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/reviewdecision"
)

// A named base resolves to the commit it names.
func TestResolveBaseResolvesANamedCommit(t *testing.T) {
	dir, base := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)

	out, err := ResolveBase(context.Background(), ResolveBaseIn{Dir: dir, Base: base, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if out.BaseSHA != base {
		t.Errorf("BaseSHA = %q, want %q", out.BaseSHA, base)
	}
}

// "auto" resolves the merge-base with the branch the entry is queued for, not
// with main: the fixture's own remote holds release/1.x at the base commit and
// main at the head, so only the queued branch resolves to the base.
func TestResolveBaseResolvesAutoAgainstTheQueuedBranch(t *testing.T) {
	dir, base := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	for _, args := range [][]string{
		{"branch", "release/1.x", base},
		{"remote", "add", "origin", dir},
	} {
		if r := executil.RunQuiet(context.Background(), dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}

	out, err := ResolveBase(context.Background(), ResolveBaseIn{Dir: dir, Base: "auto", BaseBranch: "release/1.x"})
	if err != nil {
		t.Fatal(err)
	}
	if out.BaseSHA != base {
		t.Errorf("BaseSHA = %q, want the merge-base with release/1.x, %q", out.BaseSHA, base)
	}
}

// "auto" with no remote to fetch the queued branch from is refused naming the
// branch, exactly as reviewdecision.ResolveBase words it.
func TestResolveBaseRefusesAutoWithNoRemote(t *testing.T) {
	dir, _ := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)

	_, err := ResolveBase(context.Background(), ResolveBaseIn{Dir: dir, Base: "auto", BaseBranch: "release/1.x"})
	want := "--base auto: fetch origin release/1.x: exit status 128 (a full-history checkout is required — set fetch-depth: 0)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A base the checkout cannot resolve leaves nothing to fingerprint, and is
// refused naming the base.
func TestResolveBaseRefusesABaseItCannotResolve(t *testing.T) {
	dir, _ := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)

	_, err := ResolveBase(context.Background(), ResolveBaseIn{
		Dir:        dir,
		Base:       "1111111111111111111111111111111111111111",
		BaseBranch: "main",
	})
	want := `--base "1111111111111111111111111111111111111111" does not name a commit`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// The decision is DecideFromDiff's, and the fingerprint is taken over the
// reasons it refers on.
func TestRecomputeDecisionFingerprintsAReferredDecision(t *testing.T) {
	dir, base := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)

	out, err := RecomputeDecision(context.Background(), RecomputeDecisionIn{Dir: dir, BaseSHA: base})
	if err != nil {
		t.Fatal(err)
	}
	want, err := reviewdecision.DecideFromDiff(context.Background(), dir, base)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Referred || !out.Decision.Referred {
		t.Errorf("Referred = %v, Decision.Referred = %v, want both true for a change no exemption covers",
			out.Referred, out.Decision.Referred)
	}
	if fp := referral.Fingerprint(want.Uncovered, want.Disqualifications); out.Fingerprint != fp {
		t.Errorf("Fingerprint = %q, want %q", out.Fingerprint, fp)
	}
	if len(out.Decision.Uncovered) == 0 {
		t.Error("the decision names no uncovered path")
	}
}

// A change every exemption covers refers for nothing, and says so.
func TestRecomputeDecisionSaysWhenTheDecisionDoesNotRefer(t *testing.T) {
	covering := "exemptions:\n  - name: documentation\n    reason: prose changes no behaviour\n    paths: [\"docs/**\"]\n"
	dir, base := queueFixtureRepo(t,
		map[string]string{"README.md": "hello", referral.FileName: covering},
		map[string]string{"docs/guide.md": "a paragraph"},
	)

	out, err := RecomputeDecision(context.Background(), RecomputeDecisionIn{Dir: dir, BaseSHA: base})
	if err != nil {
		t.Fatal(err)
	}
	if out.Referred || out.Decision.Referred {
		t.Errorf("Referred = %v, want false for a change every exemption covers: %+v", out.Referred, out.Decision)
	}
}

// The exemptions come from the base commit, never from the queued tree.
func TestRecomputeDecisionReadsExemptionsFromTheBase(t *testing.T) {
	selfServing := "exemptions:\n  - name: anything\n    reason: it is fine, trust me\n    paths: [\"**\"]\n"
	dir, base := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{referral.FileName: selfServing, "src/auth.go": "package src"},
	)

	out, err := RecomputeDecision(context.Background(), RecomputeDecisionIn{Dir: dir, BaseSHA: base})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Referred {
		t.Error("a queued change that exempts itself is not exempt")
	}
}

// An exemptions file the base cannot parse, and a base the repository does not
// have, are both refused rather than recomputed into a decision that refers
// for nothing — and the error is DecideFromDiff's own.
func TestRecomputeDecisionRefusesWhatItCannotRead(t *testing.T) {
	unparseable, unparseableBase := queueFixtureRepo(t,
		map[string]string{"README.md": "hello", referral.FileName: "exemptions: ["},
		map[string]string{"src/auth.go": "package src"},
	)
	missing, _ := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	for name, in := range map[string]RecomputeDecisionIn{
		"unparseable exemptions": {Dir: unparseable, BaseSHA: unparseableBase},
		"missing base":           {Dir: missing, BaseSHA: "1111111111111111111111111111111111111111"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RecomputeDecision(context.Background(), in)
			if err == nil {
				t.Fatal("RecomputeDecision recomputed a decision it could not read")
			}
			_, want := reviewdecision.DecideFromDiff(context.Background(), in.Dir, in.BaseSHA)
			if want == nil || err.Error() != want.Error() {
				t.Errorf("err = %v, want DecideFromDiff's own %v", err, want)
			}
		})
	}
}
