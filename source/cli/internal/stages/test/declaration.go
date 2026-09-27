package teststages

import (
	"context"
	"errors"
	"fmt"
	"io"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/gitdiff"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/ui"
)

// DeclarationIn is the scan root and what this run was told it is responsible
// for.
type DeclarationIn struct {
	// Dir is the scan root, which holds .lydite/.
	Dir string
	// Components is the `--component` list, empty for every declared
	// component.
	Components []string
	// Stderr is where an exclude covering no file is warned about.
	Stderr io.Writer
}

// DeclarationOut is the configuration and declaration in force, the two gates
// that ask about the declaration rather than about any run, and the set this
// run reports on.
type DeclarationOut struct {
	Config config.Config
	Decl   component.File
	// Own is what this run is responsible for: its `--component` list, or the
	// whole declaration. It reports one row per component in it and nothing
	// at all about any other, so every declared component appears exactly
	// once across a matrix of shards — the property `lydite test merge`
	// decides completeness from.
	Own []component.Component
	// Narrowed says `--component` was passed, which is what makes a
	// repository-wide figure unanswerable here: coverage(repo) and patch(repo)
	// sum every component, and a shard holding two of four would publish its
	// own two under a label about the repository. `lydite test merge` emits
	// them once, from every shard's measurements.
	Narrowed bool
	// Rows are the orphans and watch rows, in that order.
	Rows []ui.Row
}

// Declaration reads the configuration and declaration, gates the declaration,
// and resolves what this run is responsible for.
//
// Both gates run before the responsibility set is resolved and before anything
// runs, because each asks whether the declaration is complete, and that
// question depends neither on which components this invocation chose nor on
// there being any. A repository that declares none is exactly the one whose
// every source file is orphaned, and a gate it never saw would be the failure
// it exists to catch. A watch pattern that fires for nothing is a component
// that stops running when its input changes, which is likewise about the
// declaration rather than the invocation.
func Declaration(ctx context.Context, in DeclarationIn) (DeclarationOut, error) {
	cfg, err := config.Load(in.Dir)
	if err != nil {
		return DeclarationOut{}, err
	}
	file, err := component.Load(in.Dir)
	if err != nil {
		return DeclarationOut{}, err
	}
	rows := []ui.Row{
		testrun.OrphanRow(ctx, in.Dir, file, orDiscard(in.Stderr)),
		watchRow(ctx, in.Dir, file),
	}
	// `--component` says what this job is responsible for and `--affected`
	// says which of those need running, so the two compose rather than
	// competing — a shard runs `--affected --component <slice>` and reports
	// one row per component in the slice, whether or not selection ran it.
	own, err := file.Select(in.Components)
	if err != nil {
		return DeclarationOut{}, err
	}
	return DeclarationOut{
		Config:   cfg,
		Decl:     file,
		Own:      own,
		Narrowed: len(in.Components) > 0,
		Rows:     rows,
	}, nil
}

// watchRow gates every declared watch pattern against the tree.
//
// A pattern covering no file is a component that will not run when its input
// changes — silently, permanently, and green every time. It fails where an
// unused exclude only warns, because an exclude covering nothing leaves the
// orphan gate stricter than declared while this leaves a suite unrun.
//
// Outside a git repository it reports unmeasured and passes, the same shape
// the orphans row takes: a gate that could not run must be visibly distinct
// from one that passed, and turning `lydite test` in an exported tarball into
// a hard failure would be the gate firing on ordinary work.
func watchRow(ctx context.Context, dir string, file component.File) ui.Row {
	const label = "watch"
	declared := 0
	for _, c := range file.Components {
		declared += len(c.Watch)
	}
	if declared == 0 {
		return ui.Row{Status: ui.StatusPass, Label: label, Value: "none declared"}
	}
	files, err := gitdiff.Tracked(ctx, dir)
	if errors.Is(err, gitdiff.ErrNoRepository) {
		return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "no git repository"}
	}
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not checked", Detail: []string{err.Error()}}
	}
	// A scan root git lists nothing for — one that is itself ignored, a
	// vendored checkout, a --dir pointed at build output — sits inside a work
	// tree and exits zero. Every pattern would read as covering no file, and
	// the gate would fail a declaration that is correct while claiming the
	// author had written a typo. The same case the orphans row reports as no
	// source files found.
	if len(files) == 0 {
		return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "no files found"}
	}
	unmatched := affected.UnmatchedWatch(file, files)
	if len(unmatched) == 0 {
		return ui.Row{Status: ui.StatusPass, Label: label, Value: fmt.Sprintf("%d pattern(s) match", declared)}
	}
	detail := make([]string, 0, len(unmatched)+1)
	for _, u := range unmatched {
		detail = append(detail, fmt.Sprintf("%s: %q covers no file", u.Component, u.Pattern))
	}
	// The rule rather than a guess at what was meant, the same stance the
	// unused-exclude warning takes: a pattern whose file was deleted has no
	// better spelling at all.
	detail = append(detail, "patterns are anchored, so a subtree is spelled \"dir/**\"; correct or remove each pattern in "+component.FileName)
	return ui.Row{
		Status: ui.StatusFail,
		Label:  label,
		Value:  fmt.Sprintf("%d of %d pattern(s) cover no file", len(unmatched), declared),
		Detail: detail,
	}
}
