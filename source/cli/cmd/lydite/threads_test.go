package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/threads"
	"lydite/lydite/internal/ui"
)

// fakeReviews stands in for a pull request's review surface, recording what
// was written so a test asserts on the effect rather than on the wording.
type fakeReviews struct {
	existing []map[string]any
	// deleteStatus is what a DELETE answers, so the refusal path a handover
	// between two identities produces is reachable in a test.
	deleteStatus int
	replyStatus  int
	reviewStatus int
	deleted      []string
	replied      []string
	reviews      []map[string]any
	fileComments []map[string]any
}

func (f *fakeReviews) start(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			if f.reviewStatus != 0 {
				w.WriteHeader(f.reviewStatus)
				_, _ = w.Write([]byte(`{"message":"line must be part of the diff"}`))
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.reviews = append(f.reviews, body)
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/replies"):
			if f.replyStatus != 0 {
				w.WriteHeader(f.replyStatus)
				return
			}
			f.replied = append(f.replied, r.URL.Path)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete:
			if f.deleteStatus != 0 {
				w.WriteHeader(f.deleteStatus)
				return
			}
			f.deleted = append(f.deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.fileComments = append(f.fileComments, body)
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/comments"):
			if r.URL.Query().Get("page") == "1" {
				_ = json.NewEncoder(w).Encode(f.existing)
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
	t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
}

// reportsWith writes one report directory holding a document with these
// findings, which is what a run leaves behind for this command to read.
func reportsWith(t *testing.T, found ...finding.Finding) string {
	t.Helper()
	dir := t.TempDir()
	rep := ui.NewReport("scan")
	rep.Add(ui.Row{Status: ui.StatusFail, Label: "biome(cli)", Value: "failed"})
	rep.AddFindings(found...)
	f, err := os.Create(filepath.Join(dir, "scan.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := rep.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pullRequestEvent(t *testing.T) string {
	t.Helper()
	payload := map[string]any{
		"number":       7,
		"pull_request": map[string]any{"number": 7, "head": map[string]any{"sha": head}},
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

func runThreadsCmd(t *testing.T, reports []string, opsPath string, apply bool) (string, string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(context.Background())
	err := runThreads(cmd, reports, opsPath, pullRequestEvent(t), apply)
	return out.String(), errOut.String(), err
}

func readOps(t *testing.T, path string) threads.Ops {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- the path is this test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	var ops threads.Ops
	if err := json.Unmarshal(raw, &ops); err != nil {
		t.Fatal(err)
	}
	return ops
}

func located(path string, line int) finding.Finding {
	return finding.Finding{
		Gate: "biome", Component: "cli", Path: path, Line: line,
		Message: "a claim", Site: path, Row: "biome(cli)", Anchor: finding.AnchorLine,
	}
}

// The document is written whether or not it is applied, because the relay is
// the other transport and it applies exactly this.
func TestTheOperationsDocumentIsWrittenWithoutApplying(t *testing.T) {
	forge := &fakeReviews{}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "ops", "threads.json")

	if _, _, err := runThreadsCmd(t, []string{reportsWith(t, located("a.ts", 3))}, opsPath, false); err != nil {
		t.Fatalf("runThreads: %v", err)
	}
	ops := readOps(t, opsPath)
	if ops.Version != threads.Version || ops.PullRequest != 7 || ops.Head != head {
		t.Fatalf("the document does not name the run: %+v", ops)
	}
	if len(ops.Create) != 1 || ops.Create[0].Line != 3 {
		t.Fatalf("the claim did not become a thread: %+v", ops.Create)
	}
	if len(forge.reviews) != 0 {
		t.Fatalf("nothing should have been posted without --apply: %+v", forge.reviews)
	}
}

// A finding that reaches the change nowhere is the standing comment's, and
// never a thread — the partition is read off the anchor alone.
func TestAnUnanchoredFindingBecomesNoThread(t *testing.T) {
	forge := &fakeReviews{}
	forge.start(t)
	nowhere := located("a.ts", 3)
	nowhere.Anchor = finding.AnchorNowhere
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	if _, _, err := runThreadsCmd(t, []string{reportsWith(t, nowhere)}, opsPath, false); err != nil {
		t.Fatalf("runThreads: %v", err)
	}
	if ops := readOps(t, opsPath); len(ops.Create) != 0 {
		t.Fatalf("got %+v", ops.Create)
	}
}

// A local run writes every command's document into one directory with no fold
// at all, so the same claim can arrive twice and only one may become a thread.
func TestADuplicateFindingIsDroppedAndNamed(t *testing.T) {
	forge := &fakeReviews{}
	forge.start(t)
	f := located("a.ts", 3)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	_, errOut, err := runThreadsCmd(t,
		[]string{reportsWith(t, f), reportsWith(t, f)}, opsPath, false)
	if err != nil {
		t.Fatalf("runThreads: %v", err)
	}
	if ops := readOps(t, opsPath); len(ops.Create) != 1 {
		t.Fatalf("one claim is one thread: %+v", ops.Create)
	}
	if !strings.Contains(errOut, f.Fingerprint()) {
		t.Fatalf("the drop was not named on stderr: %q", errOut)
	}
}

func TestApplyOpensDeletesAndAnswers(t *testing.T) {
	f := located("a.ts", 3)
	gone := located("b.ts", 9)
	spokenIn := located("c.ts", 11)
	forge := &fakeReviews{existing: []map[string]any{
		{"id": 101, "body": threads.Body(gone), "path": "b.ts", "line": 9, "position": 1},
		{"id": 102, "body": threads.Body(spokenIn), "path": "c.ts", "line": 11, "position": 1},
		{"id": 103, "body": "I disagree", "path": "c.ts", "in_reply_to_id": 102, "position": 1},
	}}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	out, _, err := runThreadsCmd(t, []string{reportsWith(t, f)}, opsPath, true)
	if err != nil {
		t.Fatalf("runThreads: %v", err)
	}
	if len(forge.reviews) != 1 {
		t.Fatalf("expected one review, got %+v", forge.reviews)
	}
	if len(forge.deleted) != 1 || !strings.HasSuffix(forge.deleted[0], "/101") {
		t.Fatalf("lydite's own cleared thread was not taken down: %+v", forge.deleted)
	}
	if len(forge.replied) != 1 || !strings.Contains(forge.replied[0], "/102/replies") {
		t.Fatalf("the thread somebody spoke in was not answered: %+v", forge.replied)
	}
	if !strings.Contains(out, "applied") {
		t.Fatalf("the report does not say what was applied: %q", out)
	}
}

// The platform will not let one identity delete another's comment. That is
// the handover between lydite's app and a consumer's own bot, and the thread
// is answered and left standing rather than the run failing over it.
func TestARefusedDeleteIsAnsweredInstead(t *testing.T) {
	gone := located("b.ts", 9)
	forge := &fakeReviews{
		existing:     []map[string]any{{"id": 101, "body": threads.Body(gone), "path": "b.ts", "line": 9, "position": 1}},
		deleteStatus: http.StatusForbidden,
	}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	out, errOut, err := runThreadsCmd(t, []string{reportsWith(t)}, opsPath, true)
	if err != nil {
		t.Fatalf("a refused delete is an answer, not a failure: %v", err)
	}
	if len(forge.replied) != 1 {
		t.Fatalf("the thread was left saying nothing: %+v", forge.replied)
	}
	if !strings.Contains(errOut, "another identity") {
		t.Fatalf("the refusal was not reported: %q", errOut)
	}
	if !strings.Contains(out, "refused") {
		t.Fatalf("the report does not name the refusal: %q", out)
	}
}

// Never a silent green: a run whose located findings reached no surface must
// fail, and must say how many they were.
func TestAReviewThePlatformRefusesFailsTheRun(t *testing.T) {
	forge := &fakeReviews{reviewStatus: http.StatusUnprocessableEntity}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	out, _, err := runThreadsCmd(t, []string{reportsWith(t, located("a.ts", 3))}, opsPath, true)
	if err == nil {
		t.Fatal("a review that could not be posted must fail the run")
	}
	if !strings.Contains(err.Error(), "1 located finding(s) reached no surface") {
		t.Fatalf("the error does not count what was lost: %v", err)
	}
	if !strings.Contains(out, "reached no surface") {
		t.Fatalf("the report does not say so either: %q", out)
	}
}

// A directory that could not be read costs its claims and not the run: the
// ones that did arrive are still worth putting on their lines, and the
// standing comment already renders the gap as a section saying so.
func TestAnUnreadableReportDirectoryIsNamedAndNotFatal(t *testing.T) {
	forge := &fakeReviews{}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	_, errOut, err := runThreadsCmd(t,
		[]string{reportsWith(t, located("a.ts", 3)), filepath.Join(t.TempDir(), "gone")}, opsPath, false)
	if err != nil {
		t.Fatalf("runThreads: %v", err)
	}
	if !strings.Contains(errOut, "gone") {
		t.Fatalf("the missing directory was not named: %q", errOut)
	}
	if ops := readOps(t, opsPath); len(ops.Create) != 1 {
		t.Fatalf("the claims that arrived were dropped too: %+v", ops)
	}
}

// A comment on a whole file has no position by construction rather than by
// the change having moved, so reading the null alone would take every
// file-level thread down and repost it on every push.
func TestAFileLevelThreadIsNotMistakenForAnOutdatedOne(t *testing.T) {
	f := located("a.ts", 400)
	f.Anchor = finding.AnchorFile
	forge := &fakeReviews{existing: []map[string]any{
		{"id": 101, "body": threads.Body(f), "path": "a.ts", "subject_type": "file"},
	}}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	if _, _, err := runThreadsCmd(t, []string{reportsWith(t, f)}, opsPath, false); err != nil {
		t.Fatalf("runThreads: %v", err)
	}
	ops := readOps(t, opsPath)
	if len(ops.Delete) != 0 || len(ops.Create) != 0 {
		t.Fatalf("the standing file thread was churned: %+v", ops)
	}
}

// A review's comments are drafts with no subject-type field and a required
// position, so the platform refuses a file-anchored claim inside one and
// accepts it as a comment of its own.
func TestAFileAnchoredClaimIsPostedOutsideTheReview(t *testing.T) {
	onLine := located("a.ts", 3)
	onFile := located("b.ts", 400)
	onFile.Anchor = finding.AnchorFile
	forge := &fakeReviews{}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	if _, _, err := runThreadsCmd(t, []string{reportsWith(t, onLine, onFile)}, opsPath, true); err != nil {
		t.Fatalf("runThreads: %v", err)
	}
	if len(forge.reviews) != 1 {
		t.Fatalf("expected one review, got %+v", forge.reviews)
	}
	comments, ok := forge.reviews[0]["comments"].([]any)
	if !ok || len(comments) != 1 {
		t.Fatalf("only the line claim belongs in the review: %+v", forge.reviews[0])
	}
	if len(forge.fileComments) != 1 || forge.fileComments[0]["subject_type"] != "file" {
		t.Fatalf("the file claim did not become a thread of its own: %+v", forge.fileComments)
	}
}

// The platform answers a delete of another identity's comment with 404 where
// this one cannot see it, so that path must answer the thread rather than
// fail the run.
func TestADeleteRefusedAsNotFoundIsAnsweredInstead(t *testing.T) {
	gone := located("b.ts", 9)
	forge := &fakeReviews{
		existing:     []map[string]any{{"id": 101, "body": threads.Body(gone), "path": "b.ts", "line": 9, "position": 1}},
		deleteStatus: http.StatusNotFound,
	}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	if _, _, err := runThreadsCmd(t, []string{reportsWith(t)}, opsPath, true); err != nil {
		t.Fatalf("a delete the platform will not do is an answer, not a failure: %v", err)
	}
	if len(forge.replied) != 1 {
		t.Fatalf("the thread was left saying nothing: %+v", forge.replied)
	}
}

// A comment that is really gone answers the reply with 404 too, which is the
// state the delete was asking for.
func TestACommentThatIsAlreadyGoneIsNotAFailure(t *testing.T) {
	gone := located("b.ts", 9)
	forge := &fakeReviews{
		existing:     []map[string]any{{"id": 101, "body": threads.Body(gone), "path": "b.ts", "line": 9, "position": 1}},
		deleteStatus: http.StatusNotFound,
		replyStatus:  http.StatusNotFound,
	}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	out, _, err := runThreadsCmd(t, []string{reportsWith(t)}, opsPath, true)
	if err != nil {
		t.Fatalf("a comment already gone is the state asked for: %v", err)
	}
	if strings.Contains(out, "refused") {
		t.Fatalf("nothing was refused: %q", out)
	}
}

// The flags are the command's whole interface, and a workflow that names one
// the binary does not carry fails before it has done anything.
func TestTheThreadsFlagsAreWiredToTheRun(t *testing.T) {
	forge := &fakeReviews{}
	forge.start(t)
	dir := t.TempDir()
	opsPath := filepath.Join(dir, "ops.json")

	cmd := newThreadsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{
		"--reports", reportsWith(t, located("a.ts", 3)),
		"--ops", opsPath,
		"--event", pullRequestEvent(t),
		"--apply",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("threads: %v\n%s", err, out.String())
	}
	if ops := readOps(t, opsPath); len(ops.Create) != 1 {
		t.Fatalf("--reports and --ops did not reach the run: %+v", ops)
	}
	if len(forge.reviews) != 1 {
		t.Fatalf("--apply did not reach the run: %+v", forge.reviews)
	}
}

// Neither flag has a default that could stand in for it: a run that wrote its
// operations somewhere nobody named is one whose threads never get posted.
func TestThreadsRefusesWithNoReportsAndNoOpsFile(t *testing.T) {
	for _, args := range [][]string{
		{"--ops", "ops.json"},
		{"--reports", "somewhere"},
	} {
		cmd := newThreadsCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

// The report is how a reader of the job log knows what the run decided, and
// each row answers a different question: what was found, what was already
// standing, and what is about to change.
func TestTheRunReportsWhatItFoundAndPlanned(t *testing.T) {
	forge := &fakeReviews{}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	nowhere := located("b.ts", 9)
	nowhere.Anchor = finding.AnchorNowhere
	out, _, err := runThreadsCmd(t, []string{reportsWith(t, located("a.ts", 3), nowhere)}, opsPath, false)
	if err != nil {
		t.Fatalf("runThreads: %v", err)
	}
	for _, want := range []string{"findings", "1 located of 2", "threads", "0 standing", "plan", "1 to open"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not say %q:\n%s", want, out)
		}
	}
}

// The counts are what a reader checks the run against, so a delete answered
// instead of done has to move from one column to the other rather than being
// counted twice or not at all.
func TestTheAppliedRowCountsARefusalOnce(t *testing.T) {
	gone := located("b.ts", 9)
	forge := &fakeReviews{
		existing:     []map[string]any{{"id": 101, "body": threads.Body(gone), "path": "b.ts", "line": 9, "position": 1}},
		deleteStatus: http.StatusForbidden,
	}
	forge.start(t)
	opsPath := filepath.Join(t.TempDir(), "threads.json")

	out, _, err := runThreadsCmd(t, []string{reportsWith(t)}, opsPath, true)
	if err != nil {
		t.Fatalf("runThreads: %v", err)
	}
	if !strings.Contains(out, "0 opened, 0 closed, 1 answered") {
		t.Fatalf("the counts do not add up to what happened:\n%s", out)
	}
}
