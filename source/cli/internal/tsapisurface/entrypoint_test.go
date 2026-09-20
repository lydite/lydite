package tsapisurface

import (
	"encoding/json"
	"strings"
	"testing"

	"lydite/lydite/internal/fixture"
)

// The probe's own manifest is the one ADR 0040's amendment measured, so the
// resolution is read off it rather than off a manifest written to suit it.
func TestTheProbeResolvesItsTypesCondition(t *testing.T) {
	packages, err := packagesOf(fixture.Tree(t, "testdata/probe/base"))
	if err != nil {
		t.Fatalf("reading the probe: %v", err)
	}
	if len(packages) != 1 {
		t.Fatalf("got %d package(s), want 1: %+v", len(packages), packages)
	}
	p := packages[0]
	if p.rel != "." || p.name != "@lydite-probe/surface" {
		t.Errorf("package = %q at %q, want @lydite-probe/surface at .", p.name, p.rel)
	}
	if len(p.entries) != 1 || p.entries[0] != (entry{subpath: ".", dts: "dist/index.d.ts"}) {
		t.Errorf("entries = %+v, want the one . entry at dist/index.d.ts", p.entries)
	}
}

// A package naming no exports, no types, no typings and no main names no
// surface a consumer can reach, which is a skip and not a failure.
func TestAPackageNamingNoEntryPointResolvesNone(t *testing.T) {
	packages, err := packagesOf(fixture.Tree(t, "testdata/probe/noentry/base"))
	if err != nil {
		t.Fatalf("reading the probe: %v", err)
	}
	if len(packages) != 1 {
		t.Fatalf("got %d package(s), want 1: %+v", len(packages), packages)
	}
	if len(packages[0].entries) != 0 {
		t.Errorf("entries = %+v, want none", packages[0].entries)
	}
}

// A version bump changes nothing about which entry point is compared, which is
// the other half of why an author-controlled bump can silence no break here.
func TestAVersionBumpResolvesTheSameEntryPoint(t *testing.T) {
	base := entriesOf(manifestOf(t, `{"version":"0.1.0","exports":{".":{"types":"./dist/index.d.ts"}}}`))
	head := entriesOf(manifestOf(t, `{"version":"1.0.0","exports":{".":{"types":"./dist/index.d.ts"}}}`))
	if len(base) != 1 || len(head) != 1 || base[0] != head[0] {
		t.Errorf("base %+v and head %+v resolve different entry points", base, head)
	}
}

// A workspace root is one comparison per member, and a member naming no entry
// point is skipped where its siblings have one.
func TestAWorkspaceIsOneComparisonPerMember(t *testing.T) {
	dir := fixture.Tree(t, "testdata/workspace")
	packages, err := packagesOf(dir)
	if err != nil {
		t.Fatalf("reading the workspace: %v", err)
	}
	got := map[string][]entry{}
	for _, p := range packages {
		got[p.rel] = p.entries
	}
	if _, root := got["."]; root {
		t.Errorf("the workspace root is a package of its own: %+v", packages)
	}
	if _, stray := got["packages/stray"]; stray {
		t.Errorf("a glob match holding no package.json is a member: %+v", packages)
	}
	want := map[string][]entry{
		"packages/core": {
			{subpath: ".", dts: "dist/index.d.ts"},
			{subpath: "./sub", dts: "dist/sub.d.ts"},
		},
		"packages/legacy": {{subpath: ".", dts: "dist/index.d.ts"}},
		"packages/tool":   nil,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d member(s), want %d: %+v", len(got), len(want), packages)
	}
	for rel, entries := range want {
		if len(got[rel]) != len(entries) {
			t.Errorf("%s resolved %+v, want %+v", rel, got[rel], entries)
			continue
		}
		for at, e := range entries {
			if got[rel][at] != e {
				t.Errorf("%s entry %d = %+v, want %+v", rel, at, got[rel][at], e)
			}
		}
	}
}

// Every member is compared and the one naming no entry point is named back to
// the caller rather than dropped, because a package that quietly went
// uncompared reads exactly like one that was compared and found clean.
func TestAMemberNamingNoEntryPointIsNamedInTheSkips(t *testing.T) {
	dir := fixture.Tree(t, "testdata/workspace")
	all, err := surfaces(dir, dir)
	if err != nil {
		t.Fatalf("reading the workspace: %v", err)
	}
	compared, skipped := partition(all)
	if len(compared) != 2 {
		t.Errorf("compared %d package(s), want 2: %+v", len(compared), compared)
	}
	if len(skipped) != 1 || skipped[0] != "@lydite-probe/tool" {
		t.Errorf("skipped = %v, want [@lydite-probe/tool]", skipped)
	}
}

// A subpath only one tree names still has to be compared: one the merge-base
// named and this change does not is a whole entry point withdrawn.
func TestSurfacesUnionBothTreesSubpaths(t *testing.T) {
	base := fixture.Tree(t, "testdata/workspace")
	head := fixture.Tree(t, "testdata/workspace")
	writeManifest(t, head, "packages/core", `{"name":"@lydite-probe/core","exports":{".":{"types":"./dist/index.d.ts"},"./added":{"types":"./dist/added.d.ts"}}}`)

	all, err := surfaces(base, head)
	if err != nil {
		t.Fatalf("reading the workspace: %v", err)
	}
	var core surface
	for _, s := range all {
		if s.rel == "packages/core" {
			core = s
		}
	}
	want := []string{".", "./added", "./sub"}
	if strings.Join(core.subpaths, " ") != strings.Join(want, " ") {
		t.Fatalf("subpaths = %v, want %v", core.subpaths, want)
	}
	if _, inHead := core.entries[1]["./sub"]; inHead {
		t.Errorf("the withdrawn ./sub resolves an entry in the head tree")
	}
	if _, inBase := core.entries[0]["./added"]; inBase {
		t.Errorf("the new ./added resolves an entry in the merge-base tree")
	}
}

// A component this change introduces has no merge-base manifest to read, which
// is every declaration added rather than a comparison that could not be made.
func TestATreeWithNoManifestIsEveryPackageNew(t *testing.T) {
	head := fixture.Tree(t, "testdata/probe/base")
	all, err := surfaces(t.TempDir(), head)
	if err != nil {
		t.Fatalf("reading the probe: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d surface(s), want 1", len(all))
	}
	if len(all[0].entries[0]) != 0 {
		t.Errorf("the merge-base resolved %+v, want nothing", all[0].entries[0])
	}
	if len(all[0].entries[1]) != 1 {
		t.Errorf("this change resolved %+v, want the one entry point", all[0].entries[1])
	}
}

// A head tree with no manifest at all is the one shape this cannot read: there
// is no component there to compare.
func TestAHeadTreeWithNoManifestIsAnError(t *testing.T) {
	if _, err := surfaces(t.TempDir(), t.TempDir()); err == nil {
		t.Error("a tree holding no package.json read as a component")
	}
}

func TestEntryPointResolutionOrder(t *testing.T) {
	cases := []struct {
		what     string
		manifest string
		want     []entry
	}{
		{
			what:     "the types condition beats every runtime one",
			manifest: `{"exports":{".":{"types":"./dist/index.d.ts","import":"./dist/index.mjs","default":"./dist/index.js"}}}`,
			want:     []entry{{subpath: ".", dts: "dist/index.d.ts"}},
		},
		{
			what:     "a runtime condition resolves the declaration beside it",
			manifest: `{"exports":{".":{"import":"./dist/index.mjs","require":"./dist/index.cjs"}}}`,
			want:     []entry{{subpath: ".", dts: "dist/index.d.mts"}},
		},
		{
			what:     "a nested condition is followed",
			manifest: `{"exports":{".":{"node":{"types":"./dist/node.d.ts","default":"./dist/node.js"}}}}`,
			want:     []entry{{subpath: ".", dts: "dist/node.d.ts"}},
		},
		{
			what:     "an exports string is the one entry point",
			manifest: `{"exports":"./dist/index.js"}`,
			want:     []entry{{subpath: ".", dts: "dist/index.d.ts"}},
		},
		{
			what:     "a bare conditions object is the . subpath",
			manifest: `{"exports":{"types":"./dist/index.d.ts","default":"./dist/index.js"}}`,
			want:     []entry{{subpath: ".", dts: "dist/index.d.ts"}},
		},
		{
			what:     "a subpath pattern names no one file",
			manifest: `{"exports":{"./*":{"types":"./dist/*.d.ts"}}}`,
			want:     nil,
		},
		{
			what:     "exports beats types",
			manifest: `{"types":"./dist/other.d.ts","exports":{".":{"types":"./dist/index.d.ts"}}}`,
			want:     []entry{{subpath: ".", dts: "dist/index.d.ts"}},
		},
		{
			what:     "types beats typings and main",
			manifest: `{"types":"./dist/index.d.ts","typings":"./dist/legacy.d.ts","main":"./dist/other.js"}`,
			want:     []entry{{subpath: ".", dts: "dist/index.d.ts"}},
		},
		{
			what:     "typings beats main",
			manifest: `{"typings":"./dist/legacy.d.ts","main":"./dist/other.js"}`,
			want:     []entry{{subpath: ".", dts: "dist/legacy.d.ts"}},
		},
		{
			what:     "main alone resolves the declaration beside it",
			manifest: `{"main":"./dist/index.js"}`,
			want:     []entry{{subpath: ".", dts: "dist/index.d.ts"}},
		},
		{
			what:     "an exports map naming nothing resolvable falls through to main",
			manifest: `{"exports":{"./*":"./dist/*.js"},"main":"./dist/index.js"}`,
			want:     []entry{{subpath: ".", dts: "dist/index.d.ts"}},
		},
		{
			what: "an entry point that is not a declaration is carried through, " +
				"so api-extractor refusing it is uncomputable rather than a skip",
			manifest: `{"types":"./src/index.ts"}`,
			want:     []entry{{subpath: ".", dts: "src/index.ts"}},
		},
		{
			what:     "a manifest naming nothing resolves nothing",
			manifest: `{"name":"@probe/none","private":true}`,
			want:     nil,
		},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			got := entriesOf(manifestOf(t, c.manifest))
			if len(got) != len(c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			for at, e := range c.want {
				if got[at] != e {
					t.Errorf("entry %d = %+v, want %+v", at, got[at], e)
				}
			}
		})
	}
}

func TestWorkspaceGlobsInEitherShape(t *testing.T) {
	cases := []struct {
		what     string
		manifest string
		want     []string
	}{
		{what: "a list", manifest: `{"workspaces":["packages/*","apps/*"]}`, want: []string{"packages/*", "apps/*"}},
		{what: "an object", manifest: `{"workspaces":{"packages":["packages/*"]}}`, want: []string{"packages/*"}},
		{what: "none", manifest: `{"name":"@probe/one"}`, want: nil},
		{what: "neither shape", manifest: `{"workspaces":"packages/*"}`, want: nil},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			got := workspaceGlobs(manifestOf(t, c.manifest).Workspaces)
			if strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Errorf("globs = %v, want %v", got, c.want)
			}
		})
	}
}

// manifestOf decodes one manifest written inline, so a case states the whole of
// what it is about.
func manifestOf(t *testing.T, text string) manifest {
	t.Helper()
	var m manifest
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("decoding %s: %v", text, err)
	}
	return m
}
