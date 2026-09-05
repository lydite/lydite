// Package scheduler runs a shard's items concurrently under the two
// constraints that are physical rather than logical: how many may run at once,
// and which of them hold the same host port.
//
// It knows nothing about components, compose or reports. An item is a name and
// a set of host ports, and the caller supplies the function that runs one — so
// the constraint this package exists to enforce can be tested without a
// container runtime, and so the port-conflict predicate has one implementation
// rather than one here and another in the planner that groups shards.
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
// Both are physical, which is the whole of the constraint. Two items
// publishing the same host port cannot run together because the second would
// fail to bind; two rooted at the same directory cannot because they install
// into, build in and write their output to one tree — an `npm ci` removing and
// recreating a node_modules another item is importing from is not a race
// either of their suites can report honestly.
//
// Nothing logical belongs here. Two items that merely mean something to each
// other are not in conflict, and serialising them would cost parallelism to
// express a claim their author never made.
type Item struct {
	Name  string
	Dir   string
	Ports []int
}

// portLocks are the host ports an item holds, named the way a report says them.
func (it Item) portLocks() []string {
	out := make([]string, 0, len(it.Ports))
	for _, p := range it.Ports {
		out = append(out, "port "+strconv.Itoa(p))
	}
	return out
}

// dirsOverlap reports whether two component roots are the same tree or one
// contains the other.
//
// Containment and not equality, because the reason a directory is a lock is
// that its whole tree is written into: a component at the repository root
// running `go test ./...` and one rooted at `web/` are building in the same
// files, and a lock that compared the two strings would let them do it at
// once. Paths arrive cleaned and slash-separated, and "." is the root, which
// contains everything.
func dirsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return contains(a, b) || contains(b, a)
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
			if dirsOverlap(a.Dir, b.Dir) {
				// The outer of the two, since that is the tree they share.
				on := a.Dir
				if contains(b.Dir, a.Dir) {
					on = b.Dir
				}
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

// Run runs every item, at most limit at a time, never running two that publish
// a host port in common at the same time. run is called with the item's index,
// concurrently, and its result is the caller's to store.
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
			if dirsOverlap(d, it.Dir) {
				return false
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
		if items[idx].Dir != "" {
			heldDirs = append(heldDirs, items[idx].Dir)
		}
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
				for k, d := range heldDirs {
					if d == items[idx].Dir {
						heldDirs = append(heldDirs[:k], heldDirs[k+1:]...)
						break
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
