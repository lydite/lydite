package clearancestages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/toolchain"
)

// fakeRepository is a forge.SCMRepository whose every method fails the test
// if called without a function supplied for it — a stage calling a method
// nothing in the test case expects is itself the bug under test.
type fakeRepository struct {
	t *testing.T

	pullRequestTitle func(ctx context.Context, number int) (string, error)
	changedPaths     func(ctx context.Context, number int) ([]string, error)
	postStatus       func(ctx context.Context, s forge.Status) error
}

func (f *fakeRepository) IssueComment(_ context.Context, _ int64) (forge.IssueComment, error) {
	f.t.Fatal("IssueComment: unexpected call")
	return forge.IssueComment{}, nil
}

func (f *fakeRepository) HeadSHA(_ context.Context, _ int) (string, error) {
	f.t.Fatal("HeadSHA: unexpected call")
	return "", nil
}

func (f *fakeRepository) PullRequestTitle(ctx context.Context, number int) (string, error) {
	if f.pullRequestTitle == nil {
		f.t.Fatal("PullRequestTitle: unexpected call")
	}
	return f.pullRequestTitle(ctx, number)
}

func (f *fakeRepository) ChangedPaths(ctx context.Context, number int) ([]string, error) {
	if f.changedPaths == nil {
		f.t.Fatal("ChangedPaths: unexpected call")
	}
	return f.changedPaths(ctx, number)
}

func (f *fakeRepository) CanWrite(_ context.Context, _ string) (bool, error) {
	f.t.Fatal("CanWrite: unexpected call")
	return false, nil
}

func (f *fakeRepository) ReferralStatus(_ context.Context, _ string) (*clearance.Status, error) {
	f.t.Fatal("ReferralStatus: unexpected call")
	return nil, nil
}

func (f *fakeRepository) PostStatus(ctx context.Context, s forge.Status) error {
	if f.postStatus == nil {
		f.t.Fatal("PostStatus: unexpected call")
	}
	return f.postStatus(ctx, s)
}

func (f *fakeRepository) CreateComment(_ context.Context, _ int, _ string) error {
	f.t.Fatal("CreateComment: unexpected call")
	return nil
}

// refusingToolchains is a reviewdecision.Toolchains for a tree where no
// component opted into a comparison, so provisioning one is never asked for.
type refusingToolchains struct{ t *testing.T }

func (r refusingToolchains) Ensure(context.Context, string, config.Config, []component.Component) (toolchain.Envs, error) {
	r.t.Fatal("Toolchains.Ensure: unexpected call")
	return nil, nil
}

func (r refusingToolchains) CheckEnv(*toolchain.Env, component.Component) []string {
	r.t.Fatal("Toolchains.CheckEnv: unexpected call")
	return nil
}

// git runs git in dir and fails the test when it does not succeed.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r := executil.RunQuiet(context.Background(), dir, "git", args...)
	if !r.Ok() {
		t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
	}
	return strings.TrimSpace(r.Output)
}

// writeFiles writes each file under dir, creating its directory.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// checkout is a repository whose head is one commit on top of base, the
// shape a clearance's decision is recomputed over.
func checkout(t *testing.T, baseFiles, headFiles map[string]string) (dir, base, head string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "--quiet", "-b", "main")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "t")
	writeFiles(t, dir, baseFiles)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "base")
	base = git(t, dir, "rev-parse", "HEAD")
	writeFiles(t, dir, headFiles)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "head")
	return dir, base, git(t, dir, "rev-parse", "HEAD")
}
