// Package reviewstages holds the stages that decide whether a change needs a
// human before it merges, and compose the commit status that answer is
// published as: resolving the base, reading or making each opted-in
// component's public-API comparison, deciding, noticing uncommitted changes,
// and composing, rendering and writing what the decision produced. Named apart
// from internal/reviewdecision so a flow definition can import both without
// renaming either.
//
// Every stage is a plain function of its own In. Nothing here prints: what a
// stage has to say to the job log it returns as Warnings, for whoever runs the
// flow to write.
package reviewstages

import (
	"context"
	"io"

	"lydite/lydite/internal/reviewdecision"
)

// ResolveBaseIn is what ResolveBase resolves a base commit from.
type ResolveBaseIn struct {
	// Dir is the checkout the base is resolved in.
	Dir string
	// Base is the requested base: a commit, or "auto" for the merge-base with
	// BaseBranch.
	Base       string
	BaseBranch string
}

// ResolveBaseOut is the resolved base.
type ResolveBaseOut struct {
	// Base is the full SHA of the commit the change is measured against.
	Base string
}

// ResolveBase resolves the commit the change is measured against, refusing
// anything that is not an ancestor of HEAD (see reviewdecision.ResolveBase).
//
// The base is always resolved here, never taken from a surfaces document: that
// document crosses from a job that ran the change's own code, and a base
// claimed equal to HEAD is exactly what that code could forge to make the diff
// read empty.
func ResolveBase(ctx context.Context, in ResolveBaseIn) (ResolveBaseOut, error) {
	base, err := reviewdecision.ResolveBase(ctx, in.Dir, in.Base, in.BaseBranch)
	if err != nil {
		return ResolveBaseOut{}, err
	}
	return ResolveBaseOut{Base: base}, nil
}

// SurfacesIn is what Surfaces reads or makes each comparison from.
type SurfacesIn struct {
	// Dir is the scan root, which locates the component declaration.
	Dir string
	// Base is the commit ResolveBase resolved.
	Base string
	// SurfacesDocument names a comparison `review compare` already wrote, and
	// is empty for a run that makes the comparison itself.
	SurfacesDocument string
	// GuardCredential refuses to run, in this process, any comparison that
	// executes the tree under review, reporting it uncomputable instead. A
	// caller sets it exactly when this process holds, or is about to use, a
	// publishing credential.
	GuardCredential bool
	// Toolchains provisions each opted-in component's toolchain when the
	// comparison is made here.
	Toolchains reviewdecision.Toolchains
	// Progress is where a comparison's own tool reports what it is doing.
	Progress io.Writer
}

// SurfacesOut is every opted-in component's comparison.
type SurfacesOut struct {
	Surfaces []reviewdecision.SurfaceComparison
}

// Surfaces reads every opted-in component's comparison from SurfacesDocument,
// or makes it here when none is named (see reviewdecision.Surfaces).
//
// A document is reconciled against Base and must carry a result for every
// component this tree says opted in; neither is taken on the document's own
// word. The guard is In's to set, never this stage's to infer: only the caller
// knows whether this process holds a credential a Rust comparison's own build
// code could reach.
func Surfaces(ctx context.Context, in SurfacesIn) (SurfacesOut, error) {
	surfaces, err := reviewdecision.Surfaces(ctx, in.Dir, in.Base, in.SurfacesDocument, in.GuardCredential, in.Toolchains, in.Progress)
	if err != nil {
		return SurfacesOut{}, err
	}
	return SurfacesOut{Surfaces: surfaces}, nil
}

// CompareSurfacesIn is what CompareSurfaces makes each comparison from.
type CompareSurfacesIn struct {
	// Dir is the scan root, which locates the component declaration.
	Dir string
	// Base is the commit ResolveBase resolved.
	Base string
	// GuardCredential refuses to run any comparison that executes the tree
	// under review; see SurfacesIn.GuardCredential.
	GuardCredential bool
	// Toolchains provisions each opted-in component's toolchain.
	Toolchains reviewdecision.Toolchains
	// Progress is where a comparison's own tool reports what it is doing.
	Progress io.Writer
}

// CompareSurfacesOut is every opted-in component's raw comparison.
type CompareSurfacesOut struct {
	Surfaces []reviewdecision.SurfaceComparison
}

// CompareSurfaces makes every opted-in component's comparison and nothing
// else: no decision, no exemptions, no declaration (see
// reviewdecision.CompareSurfaces). It is the one stage that may run the
// change's own code, which is why it returns data a job holding no credential
// can hand to one that does.
func CompareSurfaces(ctx context.Context, in CompareSurfacesIn) (CompareSurfacesOut, error) {
	surfaces, err := reviewdecision.CompareSurfaces(ctx, in.Dir, in.Base, in.GuardCredential, in.Toolchains, in.Progress)
	if err != nil {
		return CompareSurfacesOut{}, err
	}
	return CompareSurfacesOut{Surfaces: surfaces}, nil
}

// WriteSurfacesIn is the comparison WriteSurfaces records, and where.
type WriteSurfacesIn struct {
	// Path is where the document is written.
	Path string
	// Base is the commit the comparisons ran against, recorded with them so
	// the reader reconciles against the base they actually ran against.
	Base     string
	Surfaces []reviewdecision.SurfaceComparison
}

// WriteSurfaces records the comparison as the document `review --surfaces`
// reads (see reviewdecision.WriteSurfaces).
func WriteSurfaces(_ context.Context, in WriteSurfacesIn) (struct{}, error) {
	return struct{}{}, reviewdecision.WriteSurfaces(in.Path, in.Base, in.Surfaces)
}
