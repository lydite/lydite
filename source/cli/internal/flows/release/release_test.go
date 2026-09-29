package releaseflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/flow"
	releasestages "lydite/lydite/internal/stages/release"
)

// releaseFixtureRepo runs git commands in dir with a user configured locally,
// never depending on any global git config.
func releaseFixtureRepo(t *testing.T, dir string) func(args ...string) string {
	t.Helper()
	ctx := context.Background()
	return func(args ...string) string {
		t.Helper()
		r := executil.RunQuiet(ctx, dir, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
		return strings.TrimSpace(r.Output)
	}
}

// releaseFirstTag is a repository holding one commit tagged v0.1.0, and
// nothing before it.
func releaseFirstTag(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := releaseFixtureRepo(t, dir)
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "chore: first commit")
	run("tag", "v0.1.0")
	return dir
}

// releaseSecondTag is releaseFirstTag with one more commit, declaring a
// break, tagged v0.2.0.
func releaseSecondTag(t *testing.T) string {
	t.Helper()
	dir := releaseFirstTag(t)
	run := releaseFixtureRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "src.go"), []byte("package src"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "feat!: x")
	run("tag", "v0.2.0")
	return dir
}

// Every binding in the declaration is checked by Build, so a flow that builds
// is one whose stages can only fail at run time for their own reasons.
func TestTheFlowBuilds(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if f.Name() != Name {
		t.Errorf("Name() = %q, want %q", f.Name(), Name)
	}
}

// A first release has no predecessor, so read-commits and judge have nothing
// to check and never run.
func TestAFirstReleaseSkipsReadingCommitsAndJudging(t *testing.T) {
	dir := releaseFirstTag(t)

	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	params := Params{Dir: dir, Tag: "v0.1.0"}
	result, err := f.Run(context.Background(), params.Inputs())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	tag, err := flow.Output[releasestages.ResolveTagOut](result, StageResolveTag)
	if err != nil {
		t.Fatalf("reading the resolve-tag output: %v", err)
	}
	if tag.Tag != "v0.1.0" {
		t.Errorf("Tag = %q, want %q", tag.Tag, "v0.1.0")
	}

	previous, err := flow.Output[releasestages.PreviousTagOut](result, StagePreviousTag)
	if err != nil {
		t.Fatalf("reading the previous-tag output: %v", err)
	}
	if !previous.First {
		t.Error("First = false, want true")
	}

	if _, err := flow.Output[releasestages.ReadCommitsOut](result, StageReadCommits); err == nil {
		t.Error("read-commits ran, want it skipped")
	}
	if _, err := flow.Output[releasestages.JudgeOut](result, StageJudge); err == nil {
		t.Error("judge ran, want it skipped")
	}
}

// A second release reads the commits since the first and judges what they
// declare against the bump between the two tags.
func TestASecondReleaseJudgesTheDeclaredBreak(t *testing.T) {
	dir := releaseSecondTag(t)

	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	params := Params{Dir: dir, Tag: "v0.2.0"}
	result, err := f.Run(context.Background(), params.Inputs())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	judge, err := flow.Output[releasestages.JudgeOut](result, StageJudge)
	if err != nil {
		t.Fatalf("reading the judge output: %v", err)
	}
	wantDeclaring := []string{"feat!: x"}
	if len(judge.Declaring) != 1 || judge.Declaring[0] != wantDeclaring[0] {
		t.Errorf("Declaring = %v, want %v", judge.Declaring, wantDeclaring)
	}
	if !judge.Admits {
		t.Error("Admits = false, want true")
	}
	if judge.Range != "v0.1.0..v0.2.0" {
		t.Errorf("Range = %q, want %q", judge.Range, "v0.1.0..v0.2.0")
	}
}
