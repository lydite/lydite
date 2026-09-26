package clearancestages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
)

const runURL = "https://example.invalid/lydite/lydite/actions/runs/12"

// cleared and resolved are the two statuses a clearance of head records.
var (
	cleared = forge.Status{
		State: clearance.StateSuccess, Context: clearance.ClearanceContext,
		Description: "cleared by @octocat at 4c2eaea1f2b3", TargetURL: runURL, SHA: head, PullRequest: 40,
	}
	resolved = forge.Status{
		State: clearance.StateSuccess, Context: clearance.Context,
		Description: "cleared by @octocat at 4c2eaea1f2b3", TargetURL: runURL, SHA: head, PullRequest: 40,
	}
)

// posting records every status posted, refusing the failOn'th (1-based).
func posting(t *testing.T, posted *[]forge.Status, failOn int) *fakeRepository {
	return &fakeRepository{t: t, postStatus: func(_ context.Context, s forge.Status) error {
		if len(*posted)+1 == failOn {
			return errors.New("the platform refused the post")
		}
		*posted = append(*posted, s)
		return nil
	}}
}

func recordIn(repository forge.SCMRepository) RecordStatusesIn {
	return RecordStatusesIn{
		Repository: repository, Head: head, Number: 40,
		Description: cleared.Description, TargetURL: runURL,
	}
}

// The clearance is posted first and the referral resolved on the same head
// after it, so a partial failure never leaves a green referral with no
// clearance record.
func TestRecordStatusesPostsTheClearanceThenTheResolvedReferral(t *testing.T) {
	var posted []forge.Status
	if _, err := RecordStatuses(context.Background(), recordIn(posting(t, &posted, 0))); err != nil {
		t.Fatalf("RecordStatuses: %v", err)
	}
	want := []forge.Status{cleared, resolved}
	if len(posted) != len(want) {
		t.Fatalf("posted %+v, want %+v", posted, want)
	}
	for i := range want {
		if posted[i] != want[i] {
			t.Errorf("post %d = %+v, want %+v", i, posted[i], want[i])
		}
	}
}

// A clearance the platform refused posts nothing after it.
func TestRecordStatusesPostsNothingAfterAFailedClearance(t *testing.T) {
	var posted []forge.Status
	if _, err := RecordStatuses(context.Background(), recordIn(posting(t, &posted, 1))); err == nil {
		t.Fatal("a refused clearance post was reported as recorded")
	}
	if len(posted) != 0 {
		t.Errorf("posted %+v after the clearance was refused", posted)
	}
}

// A referral the platform refused fails the stage, leaving the clearance
// record a repeated comment repairs from.
func TestRecordStatusesFailsOnAFailedReferralPost(t *testing.T) {
	var posted []forge.Status
	if _, err := RecordStatuses(context.Background(), recordIn(posting(t, &posted, 2))); err == nil {
		t.Fatal("a refused referral post was reported as recorded")
	}
	if len(posted) != 1 || posted[0] != cleared {
		t.Errorf("posted %+v, want the clearance alone", posted)
	}
}

func readStatus(t *testing.T, path string) forge.Status {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the rendered status: %v", err)
	}
	var got forge.Status
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the rendered status is not a status: %v: %s", err, raw)
	}
	return got
}

// The rendered route writes the same two statuses: the clearance at the path
// named, and the resolved referral at its .referral sibling.
func TestRenderStatusesWritesTheClearanceAndItsReferralSibling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "lydite-status.json")
	_, err := RenderStatuses(context.Background(), RenderStatusesIn{
		Path: path, Head: head, Number: 40, Description: cleared.Description, TargetURL: runURL,
	})
	if err != nil {
		t.Fatalf("RenderStatuses: %v", err)
	}
	if got := readStatus(t, path); got != cleared {
		t.Errorf("the clearance document = %+v, want %+v", got, cleared)
	}
	if got := readStatus(t, filepath.Join(filepath.Dir(path), "lydite-status.referral.json")); got != resolved {
		t.Errorf("the referral document = %+v, want %+v", got, resolved)
	}
}

// A document that cannot be written is the stage's error: the caller asked for
// nothing else, and a run that wrote nothing leaves the revision with no
// status.
func TestRenderStatusesFailsOnAnUnwritablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := RenderStatuses(context.Background(), RenderStatusesIn{
		Path: filepath.Join(blocker, "status.json"), Head: head, Number: 40, Description: cleared.Description,
	})
	if err == nil {
		t.Fatal("a status that could not be written was reported as rendered")
	}
}

// A clearance's fingerprint lives in the status description and nowhere
// else, and forge clips that description to the platform's cap on the way
// out — so the fingerprint has to survive the write and not merely the
// composition. Both documents a clearance records carry it, because both are
// written from one document.
func TestARecordedClearanceCarriesTheFingerprintThroughTheWrite(t *testing.T) {
	want := referral.Fingerprint([]string{"src/auth.go"}, nil)
	// Longer than any login the platform issues: the attribution is what
	// gives way to the budget, and asserting that needs a handle that spends
	// it.
	handle := strings.Repeat("handle", 40)
	described, err := DescribeClearance(context.Background(), DescribeClearanceIn{
		Login: handle, Head: head, Fingerprint: want,
	})
	if err != nil {
		t.Fatalf("DescribeClearance: %v", err)
	}

	path := filepath.Join(t.TempDir(), "lydite-status.json")
	if _, err := RenderStatuses(context.Background(), RenderStatusesIn{
		Path: path, Head: head, Number: 40, Description: described.Description, TargetURL: runURL,
	}); err != nil {
		t.Fatalf("RenderStatuses: %v", err)
	}

	for _, p := range []string{path, referralDocument(path)} {
		got := readStatus(t, p)
		if n := len([]rune(got.Description)); n > clearance.DescriptionLimit {
			t.Errorf("%s: the description is %d characters, past the cap of %d: %q",
				filepath.Base(p), n, clearance.DescriptionLimit, got.Description)
		}
		fingerprint, ok := clearance.FingerprintIn(got.Description)
		if !ok || fingerprint != want {
			t.Errorf("%s: the recorded description reads back as %q, %v; want %q, true",
				filepath.Base(p), fingerprint, ok, want)
		}
	}
}

// The sibling is derived from whatever path the clearance document is written
// at, so the step that posts it can compute the path without lydite telling it.
func TestTheReferralDocumentIsTheSiblingOfTheClearanceDocument(t *testing.T) {
	for _, c := range []struct{ out, want string }{
		{"lydite-status.json", "lydite-status.referral.json"},
		{"/tmp/run/status.json", "/tmp/run/status.referral.json"},
		{"status", "status.referral"},
		{"/tmp/run.d/status", "/tmp/run.d/status.referral"},
	} {
		if got := referralDocument(c.out); got != c.want {
			t.Errorf("referralDocument(%q) = %q, want %q", c.out, got, c.want)
		}
	}
}
