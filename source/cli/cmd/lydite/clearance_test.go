package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/pathmatch"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// fakeForge stands in for the hosting platform, recording what was published
// so a test can assert on the decision's effect rather than on its wording.
type fakeForge struct {
	permission string
	statuses   []map[string]any
	// changed is the platform's own list-files answer, which is the only
	// thing the comment surface asks about the pull request itself.
	changed []map[string]any
	// changedCalls counts how often that answer was asked for, so a test can
	// assert a refused command asked for it not at all.
	changedCalls int
	// changedFails stands in for a platform that will not hand the listing
	// over.
	changedFails bool
	published    []map[string]string
	comments     []string
}

func (f *fakeForge) start(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			f.changedCalls++
			if f.changedFails {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(f.changed)
		case strings.Contains(r.URL.Path, "/pulls/"):
			_, _ = w.Write([]byte(`{"head":{"sha":"` + head + `"}}`))
		case strings.Contains(r.URL.Path, "/permission"):
			_, _ = w.Write([]byte(`{"permission":"` + f.permission + `"}`))
		case strings.Contains(r.URL.Path, "/statuses") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(f.statuses)
		case strings.Contains(r.URL.Path, "/statuses") && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.published = append(f.published, body)
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(r.URL.Path, "/comments"):
			if r.Method == http.MethodPost {
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.comments = append(f.comments, body["body"])
				w.WriteHeader(http.StatusCreated)
				return
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITHUB_API_URL", server.URL)
	t.Setenv("GITHUB_TOKEN", "token")
}

const head = "4c2eaea1f2b3c4d5e6f708192a3b4c5d6e7f8091"

func eventFile(t *testing.T, body, login string, at time.Time) string {
	t.Helper()
	payload := map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       40,
			"pull_request": map[string]any{"url": "https://example.invalid/pulls/40"},
		},
		"comment": map[string]any{
			"body":       body,
			"created_at": at.Format(time.RFC3339),
			"user":       map[string]any{"login": login},
		},
		"repository": map[string]any{"full_name": "lydite/lydite"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func statusEntry(state string, at time.Time) map[string]any {
	return map[string]any{
		"state": state, "context": clearance.Context,
		"description": "referred", "created_at": at.Format(time.RFC3339),
	}
}

func runClearanceCmd(t *testing.T, eventPath string) string {
	t.Helper()
	return runClearanceCmdIn(t, ".", eventPath)
}

// runClearanceCmdIn answers the comment with the scan root at dir, which is
// where the declarations in force are read from.
func runClearanceCmdIn(t *testing.T, dir, eventPath string) string {
	t.Helper()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runClearance(context.Background(), cmd, dir, eventPath, true); err != nil {
		t.Fatalf("runClearance: %v", err)
	}
	return out.String()
}

var (
	commented = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	earlier   = commented.Add(-time.Hour)
	later     = commented.Add(time.Hour)
)

func TestClearFlipsTheStandingReferralOnTheHead(t *testing.T) {
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	out := runClearanceCmd(t, eventFile(t, "/lydite clear", "pedromvgomes", commented))

	if len(forge.published) != 1 {
		t.Fatalf("published %d statuses, want 1", len(forge.published))
	}
	got := forge.published[0]
	if got["state"] != "success" || got["context"] != clearance.Context {
		t.Fatalf("published %+v", got)
	}
	if !strings.Contains(got["description"], "pedromvgomes") {
		t.Errorf("the clearance does not name who gave it: %q", got["description"])
	}
	if !strings.Contains(out, "clearance") {
		t.Errorf("report did not mention the clearance:\n%s", out)
	}
}

// The repository is public, so anyone may comment. Nothing a stranger writes
// may change a verdict.
func TestAStrangerChangesNoStatus(t *testing.T) {
	forge := &fakeForge{permission: "read", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite clear", "passer-by", commented))

	if len(forge.published) != 0 {
		t.Fatalf("a stranger published %+v", forge.published)
	}
	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "write access") {
		t.Errorf("the refusal does not say why: %+v", forge.comments)
	}
}

// A verdict recorded after the comment was written cannot be the one the
// person read, so their decision must not attach to it.
func TestAHeadThatMovedAfterTheCommentIsNotCleared(t *testing.T) {
	forge := &fakeForge{permission: "write", statuses: []map[string]any{statusEntry("pending", later)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite clear", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("cleared a revision the commenter never read: %+v", forge.published)
	}
	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "head moved") {
		t.Errorf("the refusal does not name the race: %+v", forge.comments)
	}
}

// The isolation gate is the author's to clear by splitting the change.
func TestAFailingGateIsNotClearedByComment(t *testing.T) {
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("failure", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite clear", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("a comment resolved a gate: %+v", forge.published)
	}
}

// Ordinary conversation must reach neither the platform nor the report as an
// action.
func TestAnOrdinaryCommentPublishesNothing(t *testing.T) {
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "looks good, merging tomorrow", "pedromvgomes", commented))

	if len(forge.published) != 0 || len(forge.comments) != 0 {
		t.Fatalf("conversation caused %+v / %+v", forge.published, forge.comments)
	}
}

func TestExplainAnswersWithoutChangingTheVerdict(t *testing.T) {
	forge := &fakeForge{permission: "write", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite explain", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("explain changed a status: %+v", forge.published)
	}
	if len(forge.comments) != 1 {
		t.Fatalf("explain posted %d comments, want 1", len(forge.comments))
	}
}

// A mistyped verb must never read as a clearance, and must not pass in
// silence either.
func TestAMistypedVerbIsAnsweredAndClearsNothing(t *testing.T) {
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite cler", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("a typo cleared the change: %+v", forge.published)
	}
	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "unknown command") {
		t.Errorf("a typo went unanswered: %+v", forge.comments)
	}
}

// inCheckoutWith puts the test in a working directory shaped like the
// clearance job's own checkout of the default branch, which is where the
// declarations in force are read from. An empty document stands in for a
// repository that has declared nothing.
func inCheckoutWith(t *testing.T, exemptions string) {
	t.Helper()
	inCheckoutUnder(t, ".", exemptions)
}

// inCheckoutUnder is that checkout with the scan root at sub rather than at
// the repository root. The fixture is a repository because the exemptions
// file is located from the scan root's own path inside one.
func inCheckoutUnder(t *testing.T, sub, exemptions string) {
	t.Helper()
	dir := t.TempDir()
	if r := executil.RunQuiet(context.Background(), dir, "git", "init", "--quiet", "-b", "main"); !r.Ok() {
		t.Fatalf("git init: %v\n%s", r.Err, r.Output)
	}
	if exemptions != "" {
		file := filepath.Join(dir, sub, referral.FileName)
		if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(exemptions), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

// A repository whose scan root is a subdirectory declares its exemptions
// there, and --dir is what says where. Reading the file from the process's
// own directory finds nothing in that shape, reports every changed path as
// uncovered, and proposes an entry over the whole change under a headline
// calling those the paths no declared exemption covers.
func TestExemptReadsTheExemptionsFileUnderTheScanRoot(t *testing.T) {
	inCheckoutUnder(t, "source", "exemptions:\n  - name: docs\n    reason: prose only\n    paths: [\"source/docs/**\"]\n")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed: []map[string]any{
			{"filename": "source/docs/one.md"},
			{"filename": "source/src/a.go"},
		},
	}
	forge.start(t)

	runClearanceCmdIn(t, "source", eventFile(t, "/lydite exempt sources", "pedromvgomes", commented))

	if len(forge.comments) != 1 {
		t.Fatalf("posted %d comments, want 1", len(forge.comments))
	}
	body := forge.comments[0]
	if strings.Contains(body, "source/docs/one.md") {
		t.Errorf("the declarations under the scan root were not read, so a covered path was proposed:\n%s", body)
	}
	if !strings.Contains(body, "source/src/a.go") {
		t.Errorf("the uncovered path is missing from the proposal:\n%s", body)
	}
}

// referral.RootRelative names a path from the repository root, and opening
// the file by joining that onto the root is only right when the process's
// own directory already is the root. Run from inside the scan root itself —
// --dir . from source/ — and the file has to be found relative to dir, not
// relative to a repository root nothing here is standing in.
func TestExemptReadsTheExemptionsFileFromInsideTheScanRoot(t *testing.T) {
	dir := t.TempDir()
	if r := executil.RunQuiet(context.Background(), dir, "git", "init", "--quiet", "-b", "main"); !r.Ok() {
		t.Fatalf("git init: %v\n%s", r.Err, r.Output)
	}
	file := filepath.Join(dir, "source", referral.FileName)
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		t.Fatal(err)
	}
	exemptions := "exemptions:\n  - name: docs\n    reason: prose only\n    paths: [\"source/docs/**\"]\n"
	if err := os.WriteFile(file, []byte(exemptions), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(dir, "source"))

	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed: []map[string]any{
			{"filename": "source/docs/one.md"},
			{"filename": "source/src/a.go"},
		},
	}
	forge.start(t)

	runClearanceCmdIn(t, ".", eventFile(t, "/lydite exempt sources", "pedromvgomes", commented))

	if len(forge.comments) != 1 {
		t.Fatalf("posted %d comments, want 1", len(forge.comments))
	}
	body := forge.comments[0]
	if strings.Contains(body, "source/docs/one.md") {
		t.Errorf("the declarations were not found from inside the scan root, so a covered path was proposed:\n%s", body)
	}
	if !strings.Contains(body, "source/src/a.go") {
		t.Errorf("the uncovered path is missing from the proposal:\n%s", body)
	}
}

// The block a reader pastes is indented the two spaces `.lydite/exemptions.yml`
// is written in. yaml.v3 indents four unless told otherwise, and a block that
// has to be re-indented before it lands is one a reader edits by hand — which
// is exactly what the encoder is here to spare them.
func TestTheProposedBlockIsIndentedTwoSpacesPerLevel(t *testing.T) {
	lines, err := proposalYAML("docs-only", []string{"docs/one.md"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exemptions:", "  - name: docs-only", "    paths:", "      - docs/one.md"} {
		if !slices.Contains(lines, want) {
			t.Errorf("the block carries no %q line:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

// What went wrong deriving the uncovered set goes to the job log, because the
// refusal the commenter reads deliberately names no cause. A run that logs
// nothing leaves whoever investigates it with the refusal alone.
func TestAFailedDerivationIsNamedInTheJobLog(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{
		permission:   "write",
		statuses:     []map[string]any{statusEntry("pending", earlier)},
		changedFails: true,
	}
	forge.start(t)

	log := capturedStderr(t, func() {
		runClearanceCmd(t, eventFile(t, "/lydite exempt docs-only", "pedromvgomes", commented))
	})

	if !strings.Contains(log, "deriving the change's uncovered paths") {
		t.Fatalf("a failed derivation left nothing in the job log:\n%s", log)
	}
}

// And a derivation that worked says nothing. A log line on every run is one
// nobody reads on the run that failed.
func TestAProposalThatWorkedLogsNothing(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed:    []map[string]any{{"filename": "src/a.go"}},
	}
	forge.start(t)

	log := capturedStderr(t, func() {
		runClearanceCmd(t, eventFile(t, "/lydite exempt sources", "pedromvgomes", commented))
	})

	if log != "" {
		t.Fatalf("a derivation that worked wrote to the job log:\n%s", log)
	}
	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "exemptions:") {
		t.Fatalf("the proposal this logs nothing about was not posted: %+v", forge.comments)
	}
}

// The proposal covers the paths nothing declared covers, and nothing else:
// the paths already under an exemption are not widened into a second entry,
// and the commenter contributes the name alone.
func TestExemptProposesAnEntryOverTheUncoveredPathsAlone(t *testing.T) {
	inCheckoutWith(t, "exemptions:\n  - name: docs\n    reason: prose only\n    paths: [\"docs/**\"]\n")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed: []map[string]any{
			{"filename": "docs/one.md"},
			{"filename": "src/new.go", "status": "renamed", "previous_filename": "src/old.go"},
		},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt moved-sources", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("a proposal changed a status: %+v", forge.published)
	}
	if len(forge.comments) != 1 {
		t.Fatalf("posted %d comments, want 1", len(forge.comments))
	}
	body := forge.comments[0]
	for _, want := range []string{"exemptions:", "- name: moved-sources", referral.ReasonPlaceholderMarker,
		"      - src/new.go", "      - src/old.go"} {
		if !strings.Contains(body, want) {
			t.Errorf("the proposal is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "docs/one.md") {
		t.Errorf("a path an exemption already covers was proposed again:\n%s", body)
	}
}

// The generated entry is a draft and not an exemption: landed unedited it
// fails the parse every route to the file passes through.
func TestTheProposedEntryDoesNotParseAsAnExemption(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed:    []map[string]any{{"filename": "docs/one.md"}},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt docs-only", "pedromvgomes", commented))

	block := fencedBlock(t, forge.comments[0])
	if _, err := referral.Parse([]byte(block), referral.FileName); err == nil {
		t.Fatal("the proposal parsed as a live exemption, so pasting it unedited would declare one")
	} else if !strings.Contains(err.Error(), "placeholder marker") {
		t.Errorf("the entry was rejected for something other than its unanswered reason: %v", err)
	}
}

// fencedBlock returns the one fenced block of a rendered comment, which is
// the YAML a reader copies out of it.
func fencedBlock(t *testing.T, body string) string {
	t.Helper()
	parts := strings.Split(body, "```")
	if len(parts) < 3 {
		t.Fatalf("the comment carries no fenced block:\n%s", body)
	}
	return strings.TrimPrefix(parts[1], "\n")
}

// A change every one of whose paths some exemption already covers has no
// entry to propose, and the refusal does not guess which of the things that
// can refer it anyway is doing so.
func TestExemptProposesNothingWhenEveryPathIsAlreadyCovered(t *testing.T) {
	inCheckoutWith(t, "exemptions:\n"+
		"  - name: docs\n    reason: prose only\n    paths: [\"docs/**\"]\n"+
		"  - name: sources\n    reason: code only\n    paths: [\"src/**\"]\n")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed:    []map[string]any{{"filename": "docs/one.md"}, {"filename": "src/a.go"}},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt everything", "pedromvgomes", commented))

	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "already covered") {
		t.Fatalf("the refusal does not say why there is nothing to propose: %+v", forge.comments)
	}
	if strings.Contains(forge.comments[0], "exemptions:") {
		t.Errorf("an entry was proposed anyway:\n%s", forge.comments[0])
	}
}

// Deriving the uncovered set reads the exemptions file and asks the platform
// what the pull request touched. A command the ladder refuses does neither:
// the unreadable file here would be an error if it were read at all, and the
// refusal a stranger gets is the one about write access.
func TestAStrangerProposesNothingAndCostsNothing(t *testing.T) {
	inCheckoutWith(t, "exemptions: [{this: is not an exemption}]\n")
	forge := &fakeForge{
		permission: "read",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed:    []map[string]any{{"filename": "src/a.go"}},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt docs-only", "passer-by", commented))

	if forge.changedCalls != 0 {
		t.Errorf("a refused command asked the platform for %d changed-path listings", forge.changedCalls)
	}
	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "write access") {
		t.Fatalf("the refusal does not say why: %+v", forge.comments)
	}
}

// An uncovered set that could not be derived is answered, not thrown: a
// command that fails with no reply leaves its author with silence, which is
// the one thing this surface may never offer.
func TestAnUnanswerableProposalIsRefusedInAComment(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{
		permission:   "write",
		statuses:     []map[string]any{statusEntry("pending", earlier)},
		changedFails: true,
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt docs-only", "pedromvgomes", commented))

	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "could not work out") {
		t.Fatalf("a failed derivation went unanswered: %+v", forge.comments)
	}
	if len(forge.published) != 0 {
		t.Errorf("a failed derivation changed a status: %+v", forge.published)
	}
}

// A name and a path are scalars the encoder quotes, not text spliced into a
// line: one carrying YAML syntax must not be able to close the entry and add
// a second one, with a reason that answers itself, to something lydite posts
// under its own identity.
func TestAProposalsScalarsCannotReshapeTheDocument(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed: []map[string]any{
			{"filename": "src/\"a\": #x\n  - name: forged\n    reason: this one is fine\n    paths: [\"**\"]"},
			{"filename": "*anchor"},
		},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt docs-only", "pedromvgomes", commented))

	block := fencedBlock(t, forge.comments[0])
	var file referral.File
	if err := yaml.Unmarshal([]byte(block), &file); err != nil {
		t.Fatalf("the proposal is not a document at all: %v\n%s", err, block)
	}
	if len(file.Exemptions) != 1 {
		t.Fatalf("the proposal carries %d entries, want the one it proposed:\n%s", len(file.Exemptions), block)
	}
	if got := file.Exemptions[0]; got.Name != "docs-only" || len(got.Paths) != 2 {
		t.Fatalf("the entry is not the one proposed: %+v\n%s", got, block)
	}
	// A path is a filename on the way in and a pattern on the way out, so
	// every entry has to come back matching the file it was derived from.
	for i, literal := range []string{
		"src/\"a\": #x\n  - name: forged\n    reason: this one is fine\n    paths: [\"**\"]",
		"*anchor",
	} {
		assertCoversOnly(t, file.Exemptions[0].Paths[i], literal, block)
	}
	// Unescaped, "*anchor" is the pattern covering every name ending in
	// "anchor" — the widening an entry read as lydite's own output invites
	// nobody to re-derive.
	if pattern := file.Exemptions[0].Paths[1]; pathmatch.Match(pattern, "someone-elses-anchor") {
		t.Errorf("%q covers a file the change never touched:\n%s", pattern, block)
	}
	// Whatever the scalars carry, the reason is still the unanswered one, so
	// the block remains unlandable by the check every route to the file
	// passes through.
	if _, err := referral.Parse([]byte(block), referral.FileName); err == nil ||
		!strings.Contains(err.Error(), "placeholder marker") {
		t.Errorf("the proposal was rejected for something other than its unanswered reason: %v", err)
	}
}

// assertCoversOnly reads a proposed entry's path back as the pattern it will
// be in .lydite/exemptions.yml, and holds it to covering the one file it was
// derived from.
func assertCoversOnly(t *testing.T, pattern, literal, block string) {
	t.Helper()
	if err := pathmatch.ValidatePattern(pattern); err != nil {
		t.Errorf("the proposed path is not a pattern the file accepts: %v\n%s", err, block)
	}
	if !pathmatch.Match(pattern, literal) {
		t.Errorf("%q does not cover %q, the path it was derived from:\n%s", pattern, literal, block)
	}
}

// A changed file's name is not a pattern. Proposed verbatim, a Next.js route
// segment is a character class covering four one-letter names and missing the
// file that produced it.
func TestAProposedPathWithACharacterClassCoversOnlyThatFile(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed:    []map[string]any{{"filename": "app/[slug]/page.tsx"}},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt routes", "pedromvgomes", commented))

	block := fencedBlock(t, forge.comments[0])
	pattern := onlyProposedPath(t, block)
	assertCoversOnly(t, pattern, "app/[slug]/page.tsx", block)
	for _, other := range []string{"app/s/page.tsx", "app/l/page.tsx", "app/u/page.tsx", "app/g/page.tsx"} {
		if pathmatch.Match(pattern, other) {
			t.Errorf("%q covers %q, which the change never touched:\n%s", pattern, other, block)
		}
	}
}

// A file named "**" is an edge case git permits, and the one path whose
// verbatim proposal would exempt the whole repository while reading as the
// narrow entry the change asked for.
func TestAProposedPathOfLiteralStarsCoversOnlyThatFile(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed:    []map[string]any{{"filename": "**"}},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt stars", "pedromvgomes", commented))

	block := fencedBlock(t, forge.comments[0])
	pattern := onlyProposedPath(t, block)
	assertCoversOnly(t, pattern, "**", block)
	for _, other := range []string{"src/a.go", ".github/workflows/lydite-pr.yml", "x"} {
		if pathmatch.Match(pattern, other) {
			t.Errorf("%q covers %q, so the entry exempts the repository:\n%s", pattern, other, block)
		}
	}
}

// onlyProposedPath returns the single path of a proposal carrying one entry.
func onlyProposedPath(t *testing.T, block string) string {
	t.Helper()
	var file referral.File
	if err := yaml.Unmarshal([]byte(block), &file); err != nil {
		t.Fatalf("the proposal is not a document at all: %v\n%s", err, block)
	}
	if len(file.Exemptions) != 1 || len(file.Exemptions[0].Paths) != 1 {
		t.Fatalf("the proposal is not the one entry over one path it proposed:\n%s", block)
	}
	return file.Exemptions[0].Paths[0]
}

// The shape names the entry, and a command without one is answered rather
// than guessed at.
func TestExemptWithoutAShapeIsAnswered(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt", "pedromvgomes", commented))

	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "unknown command") {
		t.Fatalf("a command with no shape went unanswered: %+v", forge.comments)
	}
}

func TestClearanceNeedsAnEventPayload(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	err := runClearance(context.Background(), cmd, ".", "", true)
	if err == nil || !strings.Contains(err.Error(), "event payload") {
		t.Fatalf("err = %v, want a message naming the missing payload", err)
	}
}

func TestPublishNeedsThePlatformsEnvironmentRatherThanSkipping(t *testing.T) {
	for _, missing := range []string{"GITHUB_TOKEN", "GITHUB_REPOSITORY", "GITHUB_EVENT_PATH"} {
		t.Run("without "+missing, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", "token")
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
			t.Setenv("GITHUB_EVENT_PATH", filepath.Join(t.TempDir(), "absent.json"))
			t.Setenv(missing, "")
			if missing == "GITHUB_TOKEN" {
				t.Setenv("GH_TOKEN", "")
			}
			if _, err := resolveTarget("--publish", ""); err == nil {
				t.Fatalf("publishing without %s was accepted, so a run could report success having posted nothing", missing)
			}
		})
	}
}

func TestStateForKeepsAReferralDistinctFromAFailure(t *testing.T) {
	if stateFor(ui.VerdictRefer) == stateFor(ui.VerdictFail) {
		t.Fatal("a referral and a gate failure publish the same state, so a person cannot tell them apart")
	}
	if got := stateFor(ui.VerdictRefer); got != clearance.StatePending {
		t.Fatalf("a referral publishes %q, want pending", got)
	}
}

// A pending status renders as a yellow dot, which is what a job still
// running looks like. The description is the only thing that separates them.
func TestStatusDescriptionNamesTheWayForward(t *testing.T) {
	got := describe(referral.Decision{Referred: true}, ui.VerdictRefer)
	if !strings.Contains(got, "/lydite clear") {
		t.Fatalf("description %q does not say what resolves it", got)
	}
}

func TestPublishedDescriptionsFitThePlatformsLimit(t *testing.T) {
	d := referral.Decision{Referred: true, Exemption: "readme-only"}
	for _, verdict := range []ui.Verdict{ui.VerdictRefer, ui.VerdictFail, ui.VerdictPass} {
		got := describe(d, verdict)
		if n := len([]rune(got)); n == 0 || n > 140 {
			t.Errorf("%s description is %d characters: %q", verdict, n, got)
		}
	}
}
