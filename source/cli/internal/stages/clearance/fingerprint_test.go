package clearancestages

import (
	"context"
	"errors"
	"strings"
	"testing"

	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/reviewdecision"
)

// clearedCheckout is the checkout a clearance recomputes its decision over:
// the pull request's own head, one commit on top of the base, with one path no
// exemption covers.
func clearedCheckout(t *testing.T) (dir, base, head string) {
	t.Helper()
	return checkout(t, map[string]string{"README.md": "hello"}, map[string]string{"src/auth.go": "package src"})
}

// titled answers every title lookup with title.
func titled(t *testing.T, title string) *fakeRepository {
	return &fakeRepository{t: t, pullRequestTitle: func(_ context.Context, number int) (string, error) {
		if number != 40 {
			t.Errorf("PullRequestTitle asked for %d, want 40", number)
		}
		return title, nil
	}}
}

func fingerprintIn(t *testing.T, repository *fakeRepository, dir, base, head string) FingerprintIn {
	return FingerprintIn{
		Repository: repository,
		Dir:        dir,
		Base:       base,
		Number:     40,
		Head:       head,
		Toolchains: refusingToolchains{t},
	}
}

// The fingerprint is review's own decision over the same tree, hashed: a
// clearance recording anything else is one the merge queue never matches.
func TestFingerprintIsTheRecomputedDecisions(t *testing.T) {
	dir, base, head := clearedCheckout(t)

	out, err := Fingerprint(context.Background(), fingerprintIn(t, titled(t, ""), dir, base, head))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	result, err := reviewdecision.Decide(context.Background(), reviewdecision.Input{
		Dir: dir, Base: base, Title: func() string { return "" },
	})
	if err != nil {
		t.Fatalf("reviewdecision.Decide: %v", err)
	}
	want := referral.Fingerprint(result.Decision.Uncovered, result.Decision.Disqualifications)
	if out.Fingerprint == "" || out.Fingerprint != want {
		t.Errorf("Fingerprint = %q, want %q", out.Fingerprint, want)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("a fingerprint that was taken warned: %q", out.Warnings)
	}
}

// A break declared only in the pull request's title reaches the cleared
// decision, because the title is resolved live through the repository.
func TestFingerprintReadsWhatTheTitleDeclares(t *testing.T) {
	dir, base, head := clearedCheckout(t)

	plain, err := Fingerprint(context.Background(), fingerprintIn(t, titled(t, "fix: a thing"), dir, base, head))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	declared, err := Fingerprint(context.Background(), fingerprintIn(t, titled(t, "feat!: break the thing"), dir, base, head))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if plain.Fingerprint == "" || plain.Fingerprint == declared.Fingerprint {
		t.Errorf("a break declared only in the title did not change the fingerprint: %q and %q",
			plain.Fingerprint, declared.Fingerprint)
	}
}

// A title the platform cannot answer for can only under-refer, so it is a
// warning and the fingerprint is still taken.
func TestFingerprintWarnsWhenTheTitleCannotBeResolved(t *testing.T) {
	dir, base, head := clearedCheckout(t)
	repository := &fakeRepository{t: t, pullRequestTitle: func(context.Context, int) (string, error) {
		return "", errors.New("the pull request would not load")
	}}

	out, err := Fingerprint(context.Background(), fingerprintIn(t, repository, dir, base, head))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if out.Fingerprint == "" {
		t.Error("an unresolvable title cost the clearance its fingerprint")
	}
	want := "lydite: could not resolve the pull request's title (the pull request would not load) — a break declared only there is not seen"
	if len(out.Warnings) != 1 || out.Warnings[0] != want {
		t.Errorf("Warnings = %q, want %q", out.Warnings, want)
	}
}

// A checkout at any other revision is refused before anything is resolved or
// fetched: the fake fails the test on a title lookup, and the fingerprint is
// empty rather than one taken over reasons nobody was shown.
func TestFingerprintOffTheRevisionItClearsIsEmpty(t *testing.T) {
	dir, base, _ := clearedCheckout(t)

	out, err := Fingerprint(context.Background(), fingerprintIn(t, &fakeRepository{t: t}, dir, base, head))
	if err != nil {
		t.Fatalf("Fingerprint returned an error, which would fail a clearance it has no reason to: %v", err)
	}
	if out.Fingerprint != "" {
		t.Errorf("a decision computed off another revision was fingerprinted: %q", out.Fingerprint)
	}
	if len(out.Warnings) != 1 ||
		!strings.HasPrefix(out.Warnings[0], "lydite: this clearance records no fingerprint, so it will not carry onto a merge-queue entry: ") ||
		!strings.Contains(out.Warnings[0], "not the revision being cleared ("+shortSHA(head)+")") {
		t.Errorf("Warnings = %q, want the missing fingerprint and its cause named", out.Warnings)
	}
}

// A base that cannot be resolved is the same answer: no fingerprint, said so.
func TestFingerprintWithAnUnresolvableBaseIsEmpty(t *testing.T) {
	dir, _, head := clearedCheckout(t)

	out, err := Fingerprint(context.Background(), fingerprintIn(t, &fakeRepository{t: t}, dir, "", head))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if out.Fingerprint != "" || len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "--base is empty") {
		t.Errorf("Fingerprint = %q, Warnings = %q, want nothing recorded and the base named", out.Fingerprint, out.Warnings)
	}
}
