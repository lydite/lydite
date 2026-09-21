// Package scheduler runs a shard's items concurrently under the two
// constraints that are physical rather than logical: how many may run at once,
// and which of them hold the same host port or the same tree.
//
// It knows nothing about components, compose or reports. An item is a name, a
// set of host ports and the paths it writes into, and the caller supplies the
// function that runs one — so the constraint this package exists to enforce
// can be tested without a container runtime, and so the conflict predicate has
// one implementation rather than one here and another in the planner that
// groups shards.
//
// Nothing here orders items by anything logical. `depends_on` is an
// invalidation edge and never a build-order one: lydite passes no artifact
// between components, so ordering them would cost parallelism to express a
// claim their author never made. See
// docs/adr/0017-shards-the-scheduler-and-the-planner.md.
package scheduler

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Item is one unit of work and the things it holds for as long as it runs.
//
// All of them are physical, which is the whole of the constraint. Two items
// publishing the same host port cannot run together because the second would
// fail to bind; two writing into one tree cannot because they install into,
// build in and write their output to it — an `npm ci` removing and recreating
// a node_modules another item is importing from is not a race either of their
// suites can report honestly.
//
// Nothing logical belongs here. Two items that merely mean something to each
// other are not in conflict, and serialising them would cost parallelism to
// express a claim their author never made.
type Item struct {
	Name string
	Dir  string
	// Occupies are further paths the item writes into while it runs, beyond
	// its own root — a sibling package its setup builds, a generated tree two
	// items share. They are held exactly as Dir is, because the tree is
	// written into either way and nothing here can see the difference.
	Occupies []string
	Ports    []int
}

// occupied is every path the item holds while it runs: its own root, and each
// further path it declares.
//
// One list, because a lock on a tree is a lock on a tree: which of the two a
// path was declared as decides nothing about what a second item writing into
// it would do.
func (it Item) occupied() []string {
	out := make([]string, 0, 1+len(it.Occupies))
	if it.Dir != "" {
		out = append(out, it.Dir)
	}
	out = append(out, it.Occupies...)
	return out
}

// portLocks are the host ports an item holds, named the way a report says them.
func (it Item) portLocks() []string {
	out := make([]string, 0, len(it.Ports))
	for _, p := range it.Ports {
		out = append(out, "port "+strconv.Itoa(p))
	}
	return out
}

// dirsOverlap reports whether two paths are the same tree or one contains the
// other.
//
// Containment and not equality, because the reason a directory is a lock is
// that its whole tree is written into: a component at the repository root
// running `go test ./...` and one rooted at `web/` are building in the same
// files, and a lock that compared the two strings would let them do it at
// once. Paths arrive cleaned and slash-separated, and "." is the root, which
// contains everything.
//
// It takes two plain strings, so the same answer covers a root against a root,
// a root against an occupied path, and two occupied paths.
func dirsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return contains(a, b) || contains(b, a)
}

// outer returns whichever of two overlapping paths contains the other, since
// that is the tree they share.
func outer(a, b string) string {
	if contains(b, a) {
		return b
	}
	return a
}

// contains reports whether outer is inner or an ancestor of it.
func contains(outer, inner string) bool {
	if outer == inner || outer == "." {
		return true
	}
	return strings.HasPrefix(inner, outer+"/")
}

// Conflict is a pair of items that hold something in common, and what they
// hold.
//
// It is derived from the declaration rather than observed at run time, so it
// says the same thing whether or not the two ever came close to overlapping.
// What is observed is Outcome.MaxConcurrent, and the two answer different
// questions: this one names the constraint, that one says the scheduler
// reached it.
type Conflict struct {
	A, B string
	// On is what the two hold in common, phrased for a report: "port 5432",
	// "directory web".
	On string
}

// Outcome is what a run of the scheduler can say about itself afterwards.
//
// MaxConcurrent is observed, and it is the number that distinguishes a
// scheduler that ran from one that only claims to: every assertion about port
// locks is satisfied by a scheduler that never runs two items at once, because
// the lock is never taken. A run reporting 1 here has tested nothing.
type Outcome struct {
	MaxConcurrent int
	Conflicts     []Conflict
	// Started is how many items the run actually reached. It is less than the
	// number given only when the context was cancelled, and a caller needs it
	// to tell a run that finished from one that was cut short — the two
	// otherwise differ by nothing a report can see.
	Started int
}

// Conflicts returns every pair of items holding something in common, once per
// thing they share.
//
// The planner uses this to keep such a pair *in* one shard, where the scheduler
// serialises them; the scheduler uses it for the report. Both read the same
// predicate, because two that agreed today would come apart the day one learned
// about a port syntax the other had not.
func Conflicts(items []Item) []Conflict {
	var out []Conflict
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			for _, on := range sharedTrees(a.occupied(), b.occupied()) {
				out = append(out, Conflict{A: a.Name, B: b.Name, On: "directory " + on})
			}
			for _, on := range shared(a.portLocks(), b.portLocks()) {
				out = append(out, Conflict{A: a.Name, B: b.Name, On: on})
			}
		}
	}
	return out
}

// Pairs counts the distinct pairs among conflicts, which is not the number of
// conflicts: two components sharing both a Postgres port and a Redis port are
// one pair the scheduler serialises, and reporting them as two would say the
// run did more sequencing than it did.
func Pairs(conflicts []Conflict) int {
	seen := make(map[[2]string]struct{}, len(conflicts))
	for _, c := range conflicts {
		seen[[2]string{c.A, c.B}] = struct{}{}
	}
	return len(seen)
}

// sharedTrees returns each tree two items both write into, named by the
// outermost path that covers it, sorted and without repeats so a report reads
// the same on every run.
//
// Every path of one against every path of the other, so a root inside the
// other's occupied path is the same conflict as two occupied paths that
// overlap — the pair writes into one tree either way.
//
// A tree already covered by an ancestor in the result is dropped: a pair
// overlapping at `packages/tokens` and again at `packages/tokens/dist` shares
// one tree, and naming the inner one too would report the same contention
// twice. Sorting puts an ancestor ahead of everything it contains, so one pass
// is enough.
func sharedTrees(a, b []string) []string {
	set := make(map[string]struct{})
	for _, x := range a {
		for _, y := range b {
			if dirsOverlap(x, y) {
				set[outer(x, y)] = struct{}{}
			}
		}
	}
	trees := make([]string, 0, len(set))
	for p := range set {
		trees = append(trees, p)
	}
	sort.Strings(trees)
	out := make([]string, 0, len(trees))
	for _, p := range trees {
		if len(out) == 0 || !contains(out[len(out)-1], p) {
			out = append(out, p)
		}
	}
	return out
}

// shared returns what two items both hold, sorted so a report reads the same on
// every run.
func shared(a, b []string) []string {
	set := make(map[string]struct{}, len(a))
	for _, l := range a {
		set[l] = struct{}{}
	}
	var out []string
	for _, l := range b {
		if _, ok := set[l]; ok {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// Run runs every item, at most limit at a time, never running two at once that
// publish a host port in common or write into one tree. run is called with the
// item's index, concurrently, and its result is the caller's to store.
//
// The index rather than the item is deliberate: a caller writing into its own
// slot needs no lock and gets rows in declaration order for free. Completion
// order is the tempting default and is the one that makes two runs of the same
// declaration produce different documents.
//
// A cancelled context stops new items from starting but never abandons one
// already running: a component that is killed part-way still has services to
// tear down, and returning before its teardown is what leaks the containers
// holding the ports the next run needs. Items that never started are simply
// never passed to run, and the caller reports them from whatever it pre-filled.
func Run(ctx context.Context, items []Item, limit int, run func(context.Context, int)) Outcome {
	if limit < 1 {
		limit = 1
	}

	var (
		mu        sync.Mutex
		cond      = sync.NewCond(&mu)
		heldPorts = make(map[string]struct{})
		heldDirs  []string
		running   int
		out       = Outcome{Conflicts: Conflicts(items)}
	)

	// Indices rather than items, so the slot a result belongs in survives the
	// item being taken out of the pending list.
	pending := make([]int, len(items))
	for i := range items {
		pending[i] = i
	}

	free := func(it Item) bool {
		for _, d := range heldDirs {
			for _, p := range it.occupied() {
				if dirsOverlap(d, p) {
					return false
				}
			}
		}
		for _, l := range it.portLocks() {
			if _, taken := heldPorts[l]; taken {
				return false
			}
		}
		return true
	}

	mu.Lock()
	defer mu.Unlock()
	for len(pending) > 0 || running > 0 {
		next := -1
		// A cancelled run starts nothing further. The loop stays, because
		// what is already running still has to be waited for.
		if running < limit && ctx.Err() == nil {
			for k, idx := range pending {
				if free(items[idx]) {
					next = k
					break
				}
			}
		}
		if next == -1 {
			// Nothing startable. This cannot wait forever: with nothing
			// running no lock is held, so the first pending item is always
			// startable — unless the context is done, and then the only wait
			// is for the items still finishing, each of which broadcasts.
			if running == 0 {
				break
			}
			cond.Wait()
			continue
		}

		idx := pending[next]
		pending = append(pending[:next], pending[next+1:]...)
		for _, l := range items[idx].portLocks() {
			heldPorts[l] = struct{}{}
		}
		heldDirs = append(heldDirs, items[idx].occupied()...)
		running++
		out.Started++
		if running > out.MaxConcurrent {
			out.MaxConcurrent = running
		}

		go func(idx int) {
			defer func() {
				mu.Lock()
				for _, l := range items[idx].portLocks() {
					delete(heldPorts, l)
				}
				// Exactly the entries this item added, one per path and found
				// by equality: an item releases what it took, never a lock an
				// overlapping path of somebody else's spelling happens to
				// match.
				for _, p := range items[idx].occupied() {
					for k, d := range heldDirs {
						if d == p {
							heldDirs = append(heldDirs[:k], heldDirs[k+1:]...)
							break
						}
					}
				}
				running--
				cond.Broadcast()
				mu.Unlock()
			}()
			run(ctx, idx)
		}(idx)
	}
	return out
}
