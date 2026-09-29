package queuestages

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/relay"
)

// queueDoer answers both endpoints the queue path talks to and keeps every
// request, so what is sent is asserted rather than inferred from an error.
type queueDoer struct {
	requests []*http.Request
	bodies   []string
	// status and answer are what the relay's own endpoint replies.
	status int
	answer string
	// tokenStatus and tokenAnswer are what the mint endpoint replies.
	tokenStatus int
	tokenAnswer string
}

func (d *queueDoer) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	d.requests = append(d.requests, req)
	d.bodies = append(d.bodies, body)

	code, reply := d.tokenStatus, d.tokenAnswer
	if strings.HasSuffix(req.URL.Path, relay.MergeGroupRoute) {
		code, reply = d.status, d.answer
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(reply)),
		Header:     http.Header{},
	}, nil
}

func newQueueDoer() *queueDoer {
	return &queueDoer{
		status:      http.StatusOK,
		answer:      `{"state":"success","description":"cleared by @octocat at 1a2b3c4d5e6f"}`,
		tokenStatus: http.StatusOK,
		tokenAnswer: `{"value":"an-oidc-token"}`,
	}
}

// queueEventFile writes payload to a file and returns its path.
func queueEventFile(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "merge-group.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// queueEvent writes a merge_group payload naming one entry on the given
// revision.
func queueEvent(t *testing.T, number int, headSHA, baseBranch string) string {
	t.Helper()
	return queueEventFile(t, fmt.Sprintf(`{
	  "action": "checks_requested",
	  "merge_group": {
	    "head_sha": %q,
	    "head_ref": "refs/heads/gh-readonly-queue/%s/pr-%d-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
	    "base_sha": "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
	    "base_ref": "refs/heads/%s"
	  },
	  "repository": {"full_name": "vipengele/typescript"}
	}`, headSHA, baseBranch, number, baseBranch))
}

// queueFixtureRepo is a git repository on main holding baseFiles in one
// commit and headFiles in the next, checked out at the second. It returns the
// directory and the base commit.
func queueFixtureRepo(t *testing.T, baseFiles, headFiles map[string]string) (string, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		r := executil.RunQuiet(ctx, dir, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
		return strings.TrimSpace(r.Output)
	}
	write := func(files map[string]string) {
		t.Helper()
		for name, body := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	write(baseFiles)
	run("add", "-A")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	write(headFiles)
	run("add", "-A")
	run("commit", "-m", "head")
	return dir, base
}
