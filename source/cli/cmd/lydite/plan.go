package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/compose"
	"lydite/lydite/internal/scheduler"
	"lydite/lydite/internal/ui"
)

// newPlanCmd groups the declared components into shards and emits the matrix a
// CI job runs one of.
//
// It reads .lydite/components.yml, each component's compose file, and the
// tracked-file list the orphan gate holds the declaration against. No network,
// no suite, no toolchain, no container runtime — and the file list is a
// `git ls-files` read of the index rather than of history, so it runs on a
// shallow checkout and on a fork, which is what lets every other job depend on
// it.
//
// It can fail. An orphan — a source file under no component and under no
// exclude — exits non-zero, and since every shard depends on the plan, that
// blocks the whole matrix before any suite runs. Outside a git repository the
// orphan row is unmeasured and the plan still passes.
//
// It takes no knob. A shard is a conflict group — the transitive closure of
// scheduler.Conflicts, so components sharing a published host port, or writing
// into one tree, stay in one process where the scheduler serialises them. Two
// matrix jobs on hosted runners are separate machines and would not collide,
// but self-hosted runners routinely place several jobs on one host and then
// they do; keeping the pair together is safe on any topology. The grouping is
// the finest one that is safe, its size is a property of the declaration
// rather than a number anyone tunes, and there is nothing to set wrong.
//
// It cannot narrow by --affected, which needs a merge-base, git history and a
// checkout that is not shallow. The shards narrow instead, running
// `--affected --component <slice>`.
func newPlanCmd() *cobra.Command {
	var dir, out string
	var asJSON, noColor bool
	cmd := &cobra.Command{
		Use:           "plan",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Group the declared components into shards, and emit the matrix",
		Long: `Group every component declared in ` + component.FileName + ` into shards, and write the
matrix a CI job runs one of.

A shard is a set of components that must run in one process: two publishing the
same host port, or writing into one tree — their roots, or a path either
declares it occupies — would collide if they ran at once, and the scheduler
inside a run is what serialises them.

Nothing is fetched, no suite runs and no service starts; the one thing read
outside the declaration is git's tracked-file list, which the orphan gate holds
that declaration against. A source file under no component fails the plan, and
every shard depends on it, so the matrix runs nothing until the declaration
covers the tree. Outside a git repository the orphan row is unmeasured and the
plan passes.

The matrix goes to --out; stdout carries the report.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streamDiagnostics(asJSON)
			file, err := component.Load(dir)
			if err != nil {
				return err
			}
			// A matrix with no entries is a job that runs nothing, and every
			// gate downstream of it goes green over an untested repository.
			// The same refusal `lydite scan` makes, for the same reason.
			if len(file.Components) == 0 {
				return errors.New("no components declared in " + component.FileName +
					"\n       there is nothing to shard, and a matrix with no entries runs no suite at all")
			}
			items, err := planItems(dir, file)
			if err != nil {
				return err
			}
			shards := shardsOf(file.Components, items)
			if err := uniqueNames(shards); err != nil {
				return err
			}

			rep := ui.NewReport("plan")
			// The same orphanRow `lydite test` adds, called rather than
			// reimplemented: one predicate with two callers is what keeps the
			// two verdicts from drifting, and a second copy of the gate would
			// agree with this one only until somebody edited one of them.
			//
			// Whether the declaration is complete does not depend on which
			// components a run selects, or on the sharding — a file under no
			// component is one the matrix never gives to any job, and the plan
			// is where that is cheapest to say.
			rep.Add(orphanRow(cmd.Context(), dir, file))
			for _, s := range shards {
				rep.Add(s.row())
			}
			rep.Add(planRow(file, shards))
			if err := writeMatrix(out, shards); err != nil {
				return err
			}
			// No .lydite-reports/plan.json. Every other command writes one so
			// that a *verdict* reaches the pull-request comment without
			// depending on a redirection somebody remembered; the one verdict
			// this reaches is the orphan gate's, which `lydite test` publishes
			// from its own report, and a second document would put it in that
			// comment twice. Here it is read from stdout, the job log and the
			// exit code.
			w := cmd.OutOrStdout()
			if err := rep.Write(w, asJSON, ui.ColorEnabled(w, noColor)); err != nil {
				return err
			}
			return rep.Err()
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	cmd.Flags().StringVar(&out, "out", "", "write the matrix here as JSON; omit to emit only the report")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	return cmd
}

// shard is one CI job's worth of components: the set that must run in one
// process, and what makes it a set.
type shard struct {
	// Name is the members joined by "-", checked for collisions by
	// uniqueNames.
	Name string
	// Components are its members, in declaration order.
	Components []string
	// Conflicts are the pairs inside it that the scheduler will serialise,
	// which is why they are together at all.
	Conflicts []scheduler.Conflict
}

// row says what a shard is and why it is a group. A shard of one names no
// conflict, because it has none — it is alone precisely because nothing
// contends with it.
func (s shard) row() ui.Row {
	row := ui.Row{Status: ui.StatusContext, Label: "shard(" + s.Name + ")",
		Value: fmt.Sprintf("%d component(s)", len(s.Components))}
	for _, c := range s.Conflicts {
		row.Detail = append(row.Detail, fmt.Sprintf("%s and %s share %s", c.A, c.B, c.On))
	}
	return row
}

// planRow says how many shards the matrix holds and over how many components.
//
// The count is of the components some shard runs, and each one declaring no
// suite is named beside it rather than folded into the count: a matrix over
// fewer components than the file declares, saying nothing about the rest, is
// indistinguishable from a planner that dropped them.
func planRow(file component.File, shards []shard) ui.Row {
	sharded := 0
	for _, s := range shards {
		sharded += len(s.Components)
	}
	row := ui.Row{Status: ui.StatusPass, Label: "plan",
		Value: fmt.Sprintf("%d shard(s) over %d component(s)", len(shards), sharded)}
	for _, c := range file.Components {
		if declaresNoSuite(c) {
			row.Detail = append(row.Detail,
				c.Name+" declares no suite, so no shard runs it — `lydite test merge` reports it from the declaration")
		}
	}
	return row
}

// matrixEntry is one job of the matrix, and the whole of what a workflow needs
// from the plan: a name for the job and its artifact, and the --component list
// the shard runs with.
//
// Components is a comma-separated string rather than a list because that is
// what the flag takes: `--component a,b` is one argument a workflow can
// interpolate through an env var, where a list would have to be reassembled in
// a shell.
type matrixEntry struct {
	Name       string `json:"name"`
	Components string `json:"components"`
}

// writeMatrix saves the matrix, or does nothing when no --out was named.
func writeMatrix(out string, shards []shard) error {
	if out == "" {
		return nil
	}
	entries := make([]matrixEntry, 0, len(shards))
	for _, s := range shards {
		entries = append(entries, matrixEntry{Name: s.Name, Components: strings.Join(s.Components, ",")})
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(out); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	return os.WriteFile(out, append(data, '\n'), 0o600)
}

// planItems is every declared component as the scheduler sees it: its root,
// the further paths it declares it writes into, and the host ports its compose
// services publish.
//
// It holds the same fields itemFor does, because the planner groups by the
// predicate the scheduler serialises by: a field one of the two left out is a
// pair the matrix splits across jobs and nothing serialises.
//
// The stack is read with no container runtime, because plan starts nothing.
// Probing would make the matrix depend on the state of the machine that
// planned it, and the ports are in the file whether or not anything can run
// it.
//
// A compose file that will not load is an error rather than a component with
// no ports. Its ports are unknown, so a matrix built without them could put
// two components that contend for one port into different jobs — which is the
// single thing this command exists to prevent.
//
// A component declaring no suite is not among them. It runs nothing, so it
// contends with nothing, and no shard runs it: `test merge` reports it from
// the declaration.
func planItems(root string, file component.File) ([]scheduler.Item, error) {
	items := make([]scheduler.Item, 0, len(file.Components))
	for _, c := range file.Components {
		if declaresNoSuite(c) {
			continue
		}
		item := scheduler.Item{Name: c.Name, Dir: path.Clean(c.Dir), Occupies: c.Occupies}
		if c.Compose.Declared() {
			dir := filepath.Join(root, filepath.FromSlash(c.Dir))
			stack, err := compose.LoadWith(compose.NoRuntime, dir, c, io.Discard)
			if err != nil {
				return nil, fmt.Errorf("planning %s: %w"+
					"\n       a shard is grouped by the host ports its components publish, and this file's are unknown", c.Name, err)
			}
			item.Ports = stack.HostPorts()
		}
		items = append(items, item)
	}
	return items, nil
}

// shardsOf groups components into the transitive closure of the conflict
// relation.
//
// scheduler.Conflicts is the predicate, shared rather than reimplemented: two
// that agreed today would come apart the day one learned about a port syntax
// the other had not, and nothing would show it. It returns one entry per thing
// a pair shares, so a pair sharing two ports is one edge here and two lines in
// the shard's row.
//
// Shards are ordered by the declaration position of their first member, and
// members are in declaration order, so two runs of one declaration emit an
// identical matrix. A component with no item — one declaring no suite — is in
// no shard.
func shardsOf(components []component.Component, items []scheduler.Item) []shard {
	// Union-find over item positions, which follow declaration order, so the
	// representative of a group is the earliest component in it and the
	// ordering falls out.
	parent := make([]int, len(items))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	at := make(map[string]int, len(items))
	for i, it := range items {
		at[it.Name] = i
	}
	conflicts := scheduler.Conflicts(items)
	for _, c := range conflicts {
		a, b := find(at[c.A]), find(at[c.B])
		if a == b {
			continue
		}
		if a > b {
			a, b = b, a
		}
		parent[b] = a
	}

	byRoot := map[int]*shard{}
	var order []int
	for _, c := range components {
		i, ok := at[c.Name]
		if !ok {
			continue
		}
		root := find(i)
		s, ok := byRoot[root]
		if !ok {
			s = &shard{}
			byRoot[root] = s
			order = append(order, root)
		}
		s.Components = append(s.Components, c.Name)
	}
	for _, c := range conflicts {
		byRoot[find(at[c.A])].Conflicts = append(byRoot[find(at[c.A])].Conflicts, c)
	}
	out := make([]shard, 0, len(order))
	for _, root := range order {
		s := byRoot[root]
		s.Name = strings.Join(s.Components, "-")
		out = append(out, *s)
	}
	return out
}

// uniqueNames refuses a plan whose shards would take the same name.
//
// The name is the matrix job's and the artifact's suffix, so two shards sharing
// one collide on upload and the fold reads one of them twice while the other's
// components go missing — which it reports as a shard that died, naming
// components nothing was wrong with.
//
// Joining members with "-" is ambiguous, because nothing forbids a component
// name containing one: `a-b` beside `c` and `a` beside `b-c` both spell
// `a-b-c`. It is a declaration nobody writes and a failure nobody could
// diagnose from the symptom, so it is refused here rather than disambiguated
// with an index a reader cannot map back to the components.
func uniqueNames(shards []shard) error {
	seen := make(map[string][]string, len(shards))
	for _, s := range shards {
		if first, ok := seen[s.Name]; ok {
			return fmt.Errorf("two shards would both be named %q — [%s] and [%s]"+
				"\n       a shard is named for its members, so rename a component so the two differ",
				s.Name, strings.Join(first, ", "), strings.Join(s.Components, ", "))
		}
		seen[s.Name] = s.Components
	}
	return nil
}
