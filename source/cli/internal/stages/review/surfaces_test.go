package reviewstages

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lydite/lydite/internal/reviewdecision"
)

// guardRefusal is the text reviewdecision reports a guarded comparison
// uncomputable with.
const guardRefusal = "must not happen in the same process"

func TestResolveBaseResolvesACommitToItsFullSHA(t *testing.T) {
	dir, shas := checkout(t, map[string]string{"README.md": "hello"}, map[string]string{"README.md": "again"})
	out, err := ResolveBase(context.Background(), ResolveBaseIn{Dir: dir, Base: shas[0][:12]})
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if out.Base != shas[0] {
		t.Errorf("ResolveBase Base = %q; want %q", out.Base, shas[0])
	}
}

func TestResolveBasePropagatesARefusal(t *testing.T) {
	dir, _ := checkout(t, map[string]string{"README.md": "hello"})
	out, err := ResolveBase(context.Background(), ResolveBaseIn{Dir: dir, Base: ""})
	if err == nil || !strings.Contains(err.Error(), "--base is empty") {
		t.Errorf("ResolveBase error = %v; want the empty base refused", err)
	}
	if out.Base != "" {
		t.Errorf("ResolveBase Base = %q on error; want empty", out.Base)
	}
}

// The guard is In's own: set, a comparison that would execute the tree under
// review is refused and reported uncomputable; unset, it is attempted. Both
// stages that can make a comparison take it from In, never decide it.
func TestSurfaceStagesTakeTheGuardFromIn(t *testing.T) {
	stages := map[string]func(ctx context.Context, dir, base string, guard bool, tc reviewdecision.Toolchains) ([]reviewdecision.SurfaceComparison, error){
		"Surfaces": func(ctx context.Context, dir, base string, guard bool, tc reviewdecision.Toolchains) ([]reviewdecision.SurfaceComparison, error) {
			out, err := Surfaces(ctx, SurfacesIn{Dir: dir, Base: base, GuardCredential: guard, Toolchains: tc, Progress: io.Discard})
			return out.Surfaces, err
		},
		"CompareSurfaces": func(ctx context.Context, dir, base string, guard bool, tc reviewdecision.Toolchains) ([]reviewdecision.SurfaceComparison, error) {
			out, err := CompareSurfaces(ctx, CompareSurfacesIn{Dir: dir, Base: base, GuardCredential: guard, Toolchains: tc, Progress: io.Discard})
			return out.Surfaces, err
		},
	}
	for name, stage := range stages {
		for _, guard := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s guarded %t", name, guard), func(t *testing.T) {
				dir, shas := checkout(t, crateBase(), map[string]string{"probe/src/lib.rs": "pub fn other() {}\n"})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				tc := &cancellingToolchains{cancel: cancel}

				got, err := stage(ctx, dir, shas[0], guard, tc)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if len(got) != 1 || got[0].Component != "probe" {
					t.Fatalf("%s = %+v; want one result for probe", name, got)
				}
				refused := strings.Contains(got[0].Uncomputable, guardRefusal)
				attempted := len(tc.checkEnvFor) > 0
				if refused != guard || attempted == guard {
					t.Errorf("%s with GuardCredential %t: refused %t, attempted %t (%q)",
						name, guard, refused, attempted, got[0].Uncomputable)
				}
			})
		}
	}
}

// A document names the comparison, so it is read rather than made: no
// toolchain is provisioned, and what reaches the decision is the document's
// result reconciled against this run's own base.
func TestSurfacesReadsADocumentInsteadOfComparing(t *testing.T) {
	dir, shas := checkout(t, crateBase(), map[string]string{"probe/src/lib.rs": "pub fn other() {}\n"})
	document := filepath.Join(t.TempDir(), "surfaces.json")
	written := []reviewdecision.SurfaceComparison{{Component: "probe", Dir: "probe", Uncomputable: "from the document"}}
	if err := reviewdecision.WriteSurfaces(document, shas[0], written); err != nil {
		t.Fatal(err)
	}

	out, err := Surfaces(context.Background(), SurfacesIn{
		Dir: dir, Base: shas[0], SurfacesDocument: document, GuardCredential: true,
		Toolchains: refusingToolchains{t}, Progress: io.Discard,
	})
	if err != nil {
		t.Fatalf("Surfaces: %v", err)
	}
	if !reflect.DeepEqual(out.Surfaces, written) {
		t.Errorf("Surfaces = %+v; want the document's %+v", out.Surfaces, written)
	}
}

func TestSurfacesPropagatesAnError(t *testing.T) {
	dir, shas := checkout(t, map[string]string{"README.md": "hello"})
	writeFiles(t, dir, map[string]string{".lydite/components.yml": "components: [\n"})
	if _, err := Surfaces(context.Background(), SurfacesIn{Dir: dir, Base: shas[0], Toolchains: refusingToolchains{t}}); err == nil {
		t.Error("Surfaces over a malformed components file succeeded")
	}
}

func TestCompareSurfacesPropagatesAnError(t *testing.T) {
	dir, shas := checkout(t, map[string]string{"README.md": "hello"})
	writeFiles(t, dir, map[string]string{".lydite/components.yml": "components: [\n"})
	if _, err := CompareSurfaces(context.Background(), CompareSurfacesIn{Dir: dir, Base: shas[0], Toolchains: refusingToolchains{t}}); err == nil {
		t.Error("CompareSurfaces over a malformed components file succeeded")
	}
}

// What WriteSurfaces records is what a later `review --surfaces` reads back:
// the base and every result.
func TestWriteSurfacesRecordsTheDocumentReviewReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "surfaces.json")
	results := []reviewdecision.SurfaceComparison{{Component: "probe", Dir: "probe", Skipped: []string{"a"}}}
	if _, err := WriteSurfaces(context.Background(), WriteSurfacesIn{Path: path, Base: "abc123", Surfaces: results}); err != nil {
		t.Fatalf("WriteSurfaces: %v", err)
	}
	doc, err := reviewdecision.ReadSurfaces(path)
	if err != nil {
		t.Fatalf("ReadSurfaces: %v", err)
	}
	if doc.Base != "abc123" || !reflect.DeepEqual(doc.Results, results) {
		t.Errorf("read back %+v; want base abc123 and %+v", doc, results)
	}
}

func TestWriteSurfacesPropagatesAnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "surfaces.json")
	if _, err := WriteSurfaces(context.Background(), WriteSurfacesIn{Path: path, Base: "abc123"}); err == nil {
		t.Error("WriteSurfaces to a directory that does not exist succeeded")
	}
}
