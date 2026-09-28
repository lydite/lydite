package mutationstages

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
)

// fakeShape is a Shape whose every method fails the test if called without a
// function supplied for it — a stage asking a question nothing in the test
// case expects is itself the bug under test.
//
// Errorf rather than Fatal, here and in every fake below: RunMutants calls
// them from the scheduler's goroutines, where Fatal cannot stop the test.
type fakeShape struct {
	t *testing.T

	invocation func(c component.Component, v runner.Variant) (runner.Invocation, error)
	lang       func(c component.Component) runner.Lang
	env        func(tc *toolchain.Env, c component.Component, inv runner.Invocation) []string
	noSuite    func(c component.Component) bool
	scope      func(changed map[string][]int, c component.Component) map[string][]int
}

func (f *fakeShape) Invocation(c component.Component, v runner.Variant) (runner.Invocation, error) {
	if f.invocation == nil {
		f.t.Errorf("Shape.Invocation(%s, %s): unexpected call", c.Name, v)
		return runner.Invocation{}, nil
	}
	return f.invocation(c, v)
}

func (f *fakeShape) Lang(c component.Component) runner.Lang {
	if f.lang == nil {
		f.t.Errorf("Shape.Lang(%s): unexpected call", c.Name)
		return ""
	}
	return f.lang(c)
}

func (f *fakeShape) Env(tc *toolchain.Env, c component.Component, inv runner.Invocation) []string {
	if f.env == nil {
		f.t.Errorf("Shape.Env(%s): unexpected call", c.Name)
		return nil
	}
	return f.env(tc, c, inv)
}

func (f *fakeShape) NoSuite(c component.Component) bool {
	if f.noSuite == nil {
		f.t.Errorf("Shape.NoSuite(%s): unexpected call", c.Name)
		return false
	}
	return f.noSuite(c)
}

func (f *fakeShape) Scope(changed map[string][]int, c component.Component) map[string][]int {
	if f.scope == nil {
		f.t.Errorf("Shape.Scope(%s): unexpected call", c.Name)
		return nil
	}
	return f.scope(changed, c)
}

// runnerLang is the language a component's runner implies, as a Shape
// answers it.
func runnerLang(c component.Component) runner.Lang {
	if r, ok := runner.Lookup(c.Runner); ok {
		return r.Lang
	}
	return ""
}

// fakeLifecycle is a Lifecycle whose every method fails the test if called
// without a function supplied for it.
type fakeLifecycle struct {
	t *testing.T

	plan          func(ctx context.Context, root string, selected []component.Component, stream bool) []Planned
	close         func()
	clearReport   func(dir, report string) error
	prepare       func(ctx context.Context, name string, inv runner.Invocation, dir, root string, cfg config.Config, tc *toolchain.Env) error
	startServices func(ctx context.Context, name string) (func(), error)
	runCommands   func(ctx context.Context, name, dir, kind string, cmds []string, tc *toolchain.Env) error
}

func (f *fakeLifecycle) Plan(ctx context.Context, root string, selected []component.Component, stream bool) []Planned {
	if f.plan == nil {
		f.t.Error("Lifecycle.Plan: unexpected call")
		return nil
	}
	return f.plan(ctx, root, selected, stream)
}

func (f *fakeLifecycle) Close() {
	if f.close == nil {
		f.t.Error("Lifecycle.Close: unexpected call")
		return
	}
	f.close()
}

func (f *fakeLifecycle) ClearReport(dir, report string) error {
	if f.clearReport == nil {
		f.t.Errorf("Lifecycle.ClearReport(%s, %s): unexpected call", dir, report)
		return nil
	}
	return f.clearReport(dir, report)
}

func (f *fakeLifecycle) Prepare(ctx context.Context, name string, inv runner.Invocation, dir, root string, cfg config.Config, tc *toolchain.Env) error {
	if f.prepare == nil {
		f.t.Errorf("Lifecycle.Prepare(%s, %s): unexpected call", name, dir)
		return nil
	}
	return f.prepare(ctx, name, inv, dir, root, cfg, tc)
}

func (f *fakeLifecycle) StartServices(ctx context.Context, name string) (func(), error) {
	if f.startServices == nil {
		f.t.Errorf("Lifecycle.StartServices(%s): unexpected call", name)
		return func() {}, nil
	}
	return f.startServices(ctx, name)
}

func (f *fakeLifecycle) RunCommands(ctx context.Context, name, dir, kind string, cmds []string, tc *toolchain.Env) error {
	if f.runCommands == nil {
		f.t.Errorf("Lifecycle.RunCommands(%s, %s): unexpected call", name, kind)
		return nil
	}
	return f.runCommands(ctx, name, dir, kind, cmds, tc)
}

// calls records the order a fake was called in, safely across the
// scheduler's goroutines.
type calls struct {
	mu   sync.Mutex
	seen []string
}

func (c *calls) add(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, call)
}

func (c *calls) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

// fakeToolchains is a Toolchains answering with ensure, and failing the test
// when there is none.
type fakeToolchains struct {
	t      *testing.T
	ensure func(ctx context.Context, dir string, cfg config.Config, components []component.Component) (toolchain.Envs, error)
}

func (f fakeToolchains) Ensure(ctx context.Context, dir string, cfg config.Config, components []component.Component) (toolchain.Envs, error) {
	if f.ensure == nil {
		f.t.Error("Toolchains.Ensure: unexpected call")
		return nil, nil
	}
	return f.ensure(ctx, dir, cfg, components)
}

// writeFile writes body to rel under root, creating its directory.
func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// git runs git in dir as a fixed author, and fails the test when it does not
// succeed.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=lydite", "GIT_AUTHOR_EMAIL=lydite@example.com",
		"GIT_COMMITTER_NAME=lydite", "GIT_COMMITTER_EMAIL=lydite@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// mutationRepoWithOrigin is a repository whose `origin` is a bare clone of it
// at main, then a `change` branch holding one commit per entry of changes on
// top — the smallest thing a merge-base can be resolved against without a
// network.
func mutationRepoWithOrigin(t *testing.T, seed map[string]string, changes ...map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range seed {
		writeFile(t, root, rel, body)
	}
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(t, root, "init", "--quiet", "--initial-branch=main")
	git(t, root, "add", "-A")
	git(t, root, "commit", "--quiet", "-m", "seed")
	git(t, root, "init", "--quiet", "--bare", origin)
	git(t, root, "remote", "add", "origin", "file://"+origin)
	git(t, root, "push", "--quiet", "origin", "main")
	git(t, root, "checkout", "--quiet", "-b", "change")
	for i, change := range changes {
		for rel, body := range change {
			writeFile(t, root, rel, body)
		}
		git(t, root, "add", "-A")
		git(t, root, "commit", "--quiet", "-m", "change "+string(rune('a'+i)))
	}
	return root
}
