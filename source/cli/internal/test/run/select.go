package run

import (
	"context"
	"errors"
	"fmt"
	"io"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/orphan"
	"lydite/lydite/internal/ui"
)

// OrphanRow runs the orphan gate and renders its verdict.
//
// It fails, rather than referring: a source file under no component is
// something the author clears by doing work they can do — declaring the
// component, or writing the exclude that says this code is tested by nobody
// and someone decided that. Both leave a line in a file whose history is the
// record of what gets tested.
//
// A tree that is not a git repository reports unmeasured and passes. The gate
// is preparation for nothing and blocks nobody in that state, and turning a
// working `lydite test` in an exported tarball into a hard failure would be
// the gate firing on ordinary work. Distinct from a pass, because a gate that
// did not run must never read as one.
//
// An exclude covering no file is warned about on w rather than reported as a
// row: it leaves the gate stricter than declared, never weaker.
func OrphanRow(ctx context.Context, dir string, file component.File, w io.Writer) ui.Row {
	const label = "orphans"
	res, err := orphan.Find(ctx, dir, file)
	if errors.Is(err, orphan.ErrNoRepository) {
		return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "no git repository"}
	}
	if errors.Is(err, orphan.ErrNoFiles) {
		return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "no source files found"}
	}
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not checked", Detail: []string{err.Error()}}
	}
	for _, e := range res.UnusedExcludes {
		// The rule, not a guess at what was meant. Deriving a suggestion from
		// the pattern produces advice that cannot be followed as soon as the
		// pattern is not a bare directory name: "tools/gen.go" becomes
		// "tools/gen.go/**", and a stale exclude whose file was deleted —
		// the other reason one covers nothing — has no better spelling at all.
		_, _ = fmt.Fprintf(w, "lydite: %s: exclude %q covers no file. Patterns are anchored, so a subtree is spelled \"dir/**\"\n", component.FileName, e)
	}
	if len(res.Orphans) == 0 {
		return ui.Row{Status: ui.StatusPass, Label: label, Value: fmt.Sprintf("none in %d source file(s)", res.Scanned)}
	}
	// Every orphan, not a sample. The author's next action is to decide
	// which component each one belongs to, and a truncated list turns that
	// into a second run to discover the rest.
	detail := make([]string, 0, len(res.Orphans)+1)
	detail = append(detail, res.Orphans...)
	detail = append(detail, "declare a component covering these, or add them to "+component.FileName+"'s excludes")
	return ui.Row{
		Status: ui.StatusFail,
		Label:  label,
		Value:  fmt.Sprintf("%d under no component", len(res.Orphans)),
		Detail: detail,
	}
}

// AffectedFrom narrows the run to the components the change against a base
// already resolved could have broken.
//
// It takes the base rather than resolving one so a caller that resolved it
// some other way — `lydite mutation`, pointed at an explicit revision — selects
// from the same commit its own work is scoped to. Resolving it a second time
// here would be a second fetch, and under a different flag a different range.
func AffectedFrom(ctx context.Context, dir string, file component.File, base string) (affected.Result, error) {
	// On the default branch the merge-base is HEAD itself, so there is no
	// change to select by and a computed selection narrows to nothing. ADR
	// 0016 requires that run to be complete — a forgotten depends_on edge
	// surfaces at merge or never — so this is where that rule is enforced
	// rather than left to every caller to remember. Without it a consumer
	// wiring --affected into one workflow gets a permanently green `lydite
	// test` that executed no suite at all.
	head, err := gitstate.HeadSHA(ctx, dir)
	if err != nil {
		return affected.Result{}, err
	}
	if head == base {
		return affected.All(file, affected.Reason{Kind: affected.KindDefaultBranch}), nil
	}
	touched, err := gitdiff.Changed(ctx, dir, base)
	if err != nil {
		return affected.Result{}, err
	}
	// Component directories are scan-root relative and the diff is
	// repository-root relative, so the two are mapped before anything is
	// matched. A path outside the scan root matches no component and
	// therefore widens.
	prefix, err := gitdiff.Prefix(ctx, dir)
	if err != nil {
		return affected.Result{}, err
	}
	return affected.Select(file, affected.Paths(prefix, touched.All)), nil
}

// SelectRow says what selection actually did.
//
// It carries the count because "0 of 4 affected" and "4 of 4 passed" must not
// read alike, and the reason each selected component was chosen because a
// selection that quietly returned everything is otherwise indistinguishable
// from one that narrowed correctly.
//
// Zero selected is unmeasured rather than a pass. It can only mean the diff
// was empty — every changed path selects at least one component — so nothing
// was gated, and a gate that did not run must never render as one that did.
func SelectRow(res affected.Result, declared int) ui.Row {
	row := ui.Row{
		Status: ui.StatusPass,
		Label:  "select",
		Value:  fmt.Sprintf("%d of %d affected", len(res.Selected), declared),
	}
	if len(res.Selected) == 0 {
		row.Status = ui.StatusUnmeasured
		row.Detail = []string{"no changes against the merge-base, so no component could have been broken"}
		return row
	}
	// A reason about the run rather than about any component is stated once.
	// Repeating "every component runs on the default branch" per component
	// scales with the declaration and says nothing more at the twentieth
	// line than at the first.
	if r := res.Reasons[res.Selected[0].Name]; r.Kind == affected.KindDefaultBranch {
		row.Detail = []string{r.String()}
		return row
	}
	for _, c := range res.Selected {
		row.Detail = append(row.Detail, c.Name+": "+res.Reasons[c.Name].String())
	}
	return row
}

// Intersect narrows a selection to the components this run is responsible
// for, keeping the order it was given.
//
// Selection runs over the whole declaration, because a dependency edge
// reaches components in other shards and a closure computed over a slice
// would stop at its boundary. What each shard then reports is its own share
// of that one answer.
func Intersect(cs []component.Component, own []component.Component) []component.Component {
	mine := make(map[string]bool, len(own))
	for _, c := range own {
		mine[c.Name] = true
	}
	out := make([]component.Component, 0, len(cs))
	for _, c := range cs {
		if mine[c.Name] {
			out = append(out, c)
		}
	}
	return out
}
