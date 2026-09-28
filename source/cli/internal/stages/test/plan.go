package teststages

// The stages `lydite test plan` runs: loading the declaration the matrix
// covers, and grouping its components into shards. Unlike the stages `lydite
// test` runs, neither returns a ui.Row. Each returns what it found as data,
// and a failure as the error that caused it, unchanged — which rows a plan
// becomes, and the words its refusals are shown in, are the command's to
// decide.

import (
	"context"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/compose"
	"lydite/lydite/internal/scheduler"
	testrun "lydite/lydite/internal/test/run"
)

// LoadPlanComponentsIn is the root whose declaration applies.
type LoadPlanComponentsIn struct {
	Dir string
}

// LoadPlanComponentsOut is the declaration in force.
type LoadPlanComponentsOut struct {
	File component.File
	// Declared reports whether the declaration names any component at all.
	// Grouping runs only when it holds.
	Declared bool
}

// LoadPlanComponents reads the declaration, and nothing else: a plan runs no
// suite, provisions no toolchain and narrows nothing, so it has no
// configuration to consult.
//
// A declaration naming no component is not an error: Declared is false, and
// what that means for the matrix is the caller's to say.
func LoadPlanComponents(_ context.Context, in LoadPlanComponentsIn) (LoadPlanComponentsOut, error) {
	file, err := component.Load(in.Dir)
	if err != nil {
		return LoadPlanComponentsOut{}, err
	}
	return LoadPlanComponentsOut{File: file, Declared: len(file.Components) > 0}, nil
}

// GroupShardsIn is the scan root and the declaration read under it.
type GroupShardsIn struct {
	// Dir is the scan root each component's directory is relative to.
	Dir  string
	File component.File
}

// GroupShardsOut is the matrix, one PlanShard per job.
type GroupShardsOut struct {
	// Shards are ordered by the declaration position of their first member.
	Shards []PlanShard
}

// PlanShard is one CI job's worth of components: the set that must run in one
// process, and what makes it a set.
type PlanShard struct {
	// Name is the members joined by "-", and no two shards of one plan share
	// it.
	Name string
	// Components are its members, in declaration order.
	Components []string
	// Conflicts are the pairs inside it that the scheduler will serialise,
	// which is why they are together at all.
	Conflicts []scheduler.Conflict
}

// GroupShards groups every declared component that runs a suite into the
// transitive closure of scheduler.Conflicts.
//
// A compose file that will not load, and two shards that would take one name,
// each fail the stage with the error that says so, and nothing is grouped: a
// matrix built on unknown ports, or one whose jobs collide on upload, is worse
// than none.
func GroupShards(_ context.Context, in GroupShardsIn) (GroupShardsOut, error) {
	items, err := planItems(in.Dir, in.File)
	if err != nil {
		return GroupShardsOut{}, err
	}
	shards := shardsOf(in.File.Components, items)
	if err := uniqueShardNames(shards); err != nil {
		return GroupShardsOut{}, err
	}
	return GroupShardsOut{Shards: shards}, nil
}

// planItems is every declared component as the scheduler sees it: its root,
// the further paths it declares it writes into, and the host ports its compose
// services publish.
//
// It holds the same fields a run's scheduler item does, because the planner
// groups by the predicate the scheduler serialises by: a field one of the two
// left out is a pair the matrix splits across jobs and nothing serialises.
//
// The stack is read with no container runtime, because plan starts nothing.
// Probing would make the matrix depend on the state of the machine that
// planned it, and the ports are in the file whether or not anything can run
// it.
//
// A compose file that will not load is an error rather than a component with
// no ports. Its ports are unknown, so a matrix built without them could put
// two components that contend for one port into different jobs — which is the
// single thing a plan exists to prevent.
//
// A component declaring no suite is not among them. It runs nothing, so it
// contends with nothing, and no shard runs it: `test merge` reports it from
// the declaration.
func planItems(root string, file component.File) ([]scheduler.Item, error) {
	items := make([]scheduler.Item, 0, len(file.Components))
	for _, c := range file.Components {
		if testrun.DeclaresNoSuite(c) {
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
// a pair shares, so a pair sharing two ports is one edge here and two
// Conflicts on the shard.
//
// Shards are ordered by the declaration position of their first member, and
// members are in declaration order, so two runs of one declaration emit an
// identical matrix. A component with no item — one declaring no suite — is in
// no shard.
func shardsOf(components []component.Component, items []scheduler.Item) []PlanShard {
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

	byRoot := map[int]*PlanShard{}
	var order []int
	for _, c := range components {
		i, ok := at[c.Name]
		if !ok {
			continue
		}
		root := find(i)
		s, ok := byRoot[root]
		if !ok {
			s = &PlanShard{}
			byRoot[root] = s
			order = append(order, root)
		}
		s.Components = append(s.Components, c.Name)
	}
	for _, c := range conflicts {
		byRoot[find(at[c.A])].Conflicts = append(byRoot[find(at[c.A])].Conflicts, c)
	}
	out := make([]PlanShard, 0, len(order))
	for _, root := range order {
		s := byRoot[root]
		s.Name = strings.Join(s.Components, "-")
		out = append(out, *s)
	}
	return out
}

// uniqueShardNames refuses a plan whose shards would take the same name.
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
func uniqueShardNames(shards []PlanShard) error {
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
