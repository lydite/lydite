package teststages

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/compose"
	"lydite/lydite/internal/scheduler"
)

// groupRepo is a declaration whose shards are not the declaration order: `api`
// and `tally` publish one host port under differently named services, `web`
// publishes another, and `docs` publishes nothing.
func groupRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: api\n    dir: go/api\n    runner: go-test\n    compose:\n      file: compose.yml\n"+
			"  - name: web\n    dir: web\n    runner: vitest\n    compose:\n      file: compose.yml\n"+
			"  - name: tally\n    dir: rust\n    runner: cargo-nextest\n    compose:\n      file: compose.yml\n"+
			"  - name: docs\n    dir: docs\n    runner: go-test\n")
	service := func(dir, name string, port int) {
		write(t, root, dir+"/compose.yml",
			fmt.Sprintf("services:\n  %s:\n    image: postgres\n    ports: [\"%d:5432\"]\n"+
				"    healthcheck:\n      test: [\"CMD\", \"true\"]\n", name, port))
	}
	service("go/api", "db", 5432)
	service("web", "cache", 6379)
	service("rust", "postgres", 5432)
	write(t, root, "docs/.keep", "")
	return root
}

// loadPlan runs LoadPlanComponents over root and fails the test on an error.
func loadPlan(t *testing.T, root string) LoadPlanComponentsOut {
	t.Helper()
	out, err := LoadPlanComponents(context.Background(), LoadPlanComponentsIn{Dir: root})
	if err != nil {
		t.Fatalf("LoadPlanComponents: %v", err)
	}
	return out
}

// A declaration naming no component is a matrix with no entries, which every
// gate downstream of it would read as green. The stage says so as data rather
// than as an error, so the words of the refusal stay the command's.
func TestADeclarationNamingNoComponentIsNotDeclared(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName, "components: []\n")
	out := loadPlan(t, root)
	if out.Declared {
		t.Errorf("Declared = true over an empty declaration")
	}
	if len(out.File.Components) != 0 {
		t.Errorf("File.Components = %+v, want none", out.File.Components)
	}
}

func TestADeclarationNamingAComponentIsDeclared(t *testing.T) {
	t.Parallel()
	out := loadPlan(t, groupRepo(t))
	if !out.Declared {
		t.Fatal("Declared = false over a declaration naming four components")
	}
	var names []string
	for _, c := range out.File.Components {
		names = append(names, c.Name)
	}
	if want := []string{"api", "web", "tally", "docs"}; !reflect.DeepEqual(names, want) {
		t.Errorf("components = %v, want %v in declaration order", names, want)
	}
}

// Two components publishing one host port are one shard, where the scheduler
// serialises them; the shard names the port, which is what says the grouping
// came from the compose files rather than from the declaration order.
func TestComponentsPublishingOnePortAreOneShard(t *testing.T) {
	t.Parallel()
	root := groupRepo(t)
	got, err := GroupShards(context.Background(), GroupShardsIn{Dir: root, File: loadPlan(t, root).File})
	if err != nil {
		t.Fatalf("GroupShards: %v", err)
	}
	want := []PlanShard{
		{Name: "api-tally", Components: []string{"api", "tally"},
			Conflicts: []scheduler.Conflict{{A: "api", B: "tally", On: "port 5432"}}},
		{Name: "web", Components: []string{"web"}},
		{Name: "docs", Components: []string{"docs"}},
	}
	if !reflect.DeepEqual(got.Shards, want) {
		t.Errorf("shards = %+v\nwant     %+v", got.Shards, want)
	}
}

// A shard's name is its matrix job's and its artifact's, so a second plan of
// one declaration naming a shard differently would orphan the directory the
// fold reads.
func TestGroupingOneDeclarationTwiceIsIdentical(t *testing.T) {
	t.Parallel()
	root := groupRepo(t)
	in := GroupShardsIn{Dir: root, File: loadPlan(t, root).File}
	first, err := GroupShards(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GroupShards(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two groupings of one declaration differ:\n%+v\n%+v", first, second)
	}
}

// Components writing into one tree are one shard for the reason two sharing a
// port are, and a component declaring no suite contends with nothing and is
// in no shard — declared at the root, it would otherwise overlap every
// sibling's tree and pull them into one job.
func TestOccupyingOneTreeGroupsAndDeclaringNoSuiteDoesNot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: ui\n    dir: packages/ui\n    runner: vitest\n    occupies: [\"packages/tokens\"]\n"+
			"  - name: scripts\n    dir: .\n    lang: shell\n"+
			"  - name: api\n    dir: go/api\n    runner: go-test\n"+
			"  - name: storybook\n    dir: apps/storybook\n    runner: vitest\n    occupies: [\"packages/tokens/dist\"]\n")
	for _, dir := range []string{"packages/ui", "go/api", "apps/storybook"} {
		write(t, root, dir+"/.keep", "")
	}
	got, err := GroupShards(context.Background(), GroupShardsIn{Dir: root, File: loadPlan(t, root).File})
	if err != nil {
		t.Fatalf("GroupShards: %v", err)
	}
	want := []PlanShard{
		{Name: "ui-storybook", Components: []string{"ui", "storybook"},
			Conflicts: []scheduler.Conflict{{A: "ui", B: "storybook", On: "directory packages/tokens"}}},
		{Name: "api", Components: []string{"api"}},
	}
	if !reflect.DeepEqual(got.Shards, want) {
		t.Errorf("shards = %+v\nwant     %+v", got.Shards, want)
	}
}

// `a-b` beside `c` and `a` beside `b-c` both spell `a-b-c`: two matrix jobs
// uploading one artifact name, which the fold reads as one shard read twice
// and another that died. The refusal is the stage's own error, word for word,
// since the command shows it as it stands.
func TestTwoShardsTakingOneNameFailTheStage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: a-b\n    dir: one\n    runner: go-test\n"+
			"  - name: c\n    dir: one\n    runner: go-test\n"+
			"  - name: a\n    dir: two\n    runner: go-test\n"+
			"  - name: b-c\n    dir: two\n    runner: go-test\n")
	write(t, root, "one/.keep", "")
	write(t, root, "two/.keep", "")
	got, err := GroupShards(context.Background(), GroupShardsIn{Dir: root, File: loadPlan(t, root).File})
	if err == nil {
		t.Fatalf("two shards took one name: %+v", got.Shards)
	}
	want := `two shards would both be named "a-b-c" — [a-b, c] and [a, b-c]` +
		"\n       a shard is named for its members, so rename a component so the two differ"
	if err.Error() != want {
		t.Errorf("error = %q\nwant    %q", err.Error(), want)
	}
	if got.Shards != nil {
		t.Errorf("shards = %+v, want none beside a refusal", got.Shards)
	}
}

// A compose file that will not load leaves the component's ports unknown, and
// a matrix built on unknown ports can split two contending components across
// jobs. The stage fails with the error that names the component and carries
// the loader's own, rather than grouping without it.
func TestAComposeFileThatWillNotLoadFailsTheStage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n  - name: api\n    dir: api\n    runner: go-test\n    compose:\n      file: compose.yml\n")
	write(t, root, "api/.keep", "")
	file := loadPlan(t, root).File

	got, err := GroupShards(context.Background(), GroupShardsIn{Dir: root, File: file})
	if err == nil {
		t.Fatalf("a component whose compose file is absent was grouped: %+v", got.Shards)
	}
	_, cause := compose.LoadWith(compose.NoRuntime, filepath.Join(root, "api"), file.Components[0], io.Discard)
	if cause == nil {
		t.Fatal("the compose file loaded, so the comparison proves nothing")
	}
	want := "planning api: " + cause.Error() +
		"\n       a shard is grouped by the host ports its components publish, and this file's are unknown"
	if err.Error() != want {
		t.Errorf("error = %q\nwant    %q", err.Error(), want)
	}
	if got.Shards != nil {
		t.Errorf("shards = %+v, want none beside a failure", got.Shards)
	}
}

// A declaration that will not parse fails the stage with the loader's own
// error, rather than reading as a declaration naming nothing.
func TestALoadFailureFailsTheStage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName, "components: [\n")
	want := func() error { _, err := component.Load(root); return err }()
	if want == nil {
		t.Fatal("component.Load accepted a malformed declaration")
	}
	_, err := LoadPlanComponents(context.Background(), LoadPlanComponentsIn{Dir: root})
	if err == nil || err.Error() != want.Error() {
		t.Errorf("err = %v, want the loader's own %q", err, want)
	}
}

// A pair sharing both a port and a directory is still one shard, not two: the
// second conflict for an already-grouped pair finds the two already sharing a
// root and is folded onto that same shard.
func TestTwoConflictsOnOnePairStillMakeOneShard(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: ui\n    dir: packages/ui\n    runner: vitest\n    occupies: [\"packages/tokens\"]\n    compose:\n      file: compose.yml\n"+
			"  - name: storybook\n    dir: apps/storybook\n    runner: vitest\n    occupies: [\"packages/tokens/dist\"]\n    compose:\n      file: compose.yml\n")
	service := func(dir string) {
		write(t, root, dir+"/compose.yml",
			"services:\n  db:\n    image: postgres\n    ports: [\"5432:5432\"]\n"+
				"    healthcheck:\n      test: [\"CMD\", \"true\"]\n")
	}
	service("packages/ui")
	service("apps/storybook")
	got, err := GroupShards(context.Background(), GroupShardsIn{Dir: root, File: loadPlan(t, root).File})
	if err != nil {
		t.Fatalf("GroupShards: %v", err)
	}
	want := []PlanShard{
		{Name: "ui-storybook", Components: []string{"ui", "storybook"},
			Conflicts: []scheduler.Conflict{
				{A: "ui", B: "storybook", On: "directory packages/tokens"},
				{A: "ui", B: "storybook", On: "port 5432"},
			}},
	}
	if !reflect.DeepEqual(got.Shards, want) {
		t.Errorf("shards = %+v\nwant     %+v", got.Shards, want)
	}
}

// A component conflicting separately with two others that never conflict with
// each other still groups all three into one shard: the second of the two
// conflicts finds its components already rooted through the first.
func TestATransitiveConflictGroupsThreeIntoOneShard(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: one\n    dir: one\n    runner: go-test\n    compose:\n      file: compose.yml\n"+
			"  - name: two\n    dir: two\n    runner: go-test\n    occupies: [\"shared\"]\n"+
			"  - name: three\n    dir: three\n    runner: go-test\n    occupies: [\"shared/inner\"]\n    compose:\n      file: compose.yml\n")
	service := func(dir string) {
		write(t, root, dir+"/compose.yml",
			"services:\n  db:\n    image: postgres\n    ports: [\"5432:5432\"]\n"+
				"    healthcheck:\n      test: [\"CMD\", \"true\"]\n")
	}
	service("one")
	service("three")
	write(t, root, "two/.keep", "")
	got, err := GroupShards(context.Background(), GroupShardsIn{Dir: root, File: loadPlan(t, root).File})
	if err != nil {
		t.Fatalf("GroupShards: %v", err)
	}
	want := []PlanShard{
		{Name: "one-two-three", Components: []string{"one", "two", "three"},
			Conflicts: []scheduler.Conflict{
				{A: "one", B: "three", On: "port 5432"},
				{A: "two", B: "three", On: "directory shared"},
			}},
	}
	if !reflect.DeepEqual(got.Shards, want) {
		t.Errorf("shards = %+v\nwant     %+v", got.Shards, want)
	}
}
