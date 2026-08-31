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
	"sync"
)

// Item is one unit of work: what to call it, and the host ports it holds for
// as long as it runs.
//
// The ports are the whole of the constraint. Two items publishing the same one
// cannot run together — not because of anything they mean to each other, but
// because the second would fail to bind.
type Item struct {
	Name  string
	Ports []int
}

// Conflict is a pair of items that share a host port, and the port they share.
//
// It is derived from the declaration rather than observed at run time, so it
// says the same thing whether or not the two ever came close to overlapping.
// What is observed is Outcome.MaxConcurrent, and the two answer different
// questions: this one names the constraint, that one says the scheduler
// reached it.
type Conflict struct {
	A, B string
	Port int
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
}

// Conflicts returns every pair of items sharing a host port.
//
// The planner uses this to keep such a pair out of one shard, where they would
// serialise; the scheduler uses it for the report. Both read the same
// predicate, because two that agreed today would come apart the day one learned
// about a port syntax the other had not.
func Conflicts(items []Item) []Conflict {
	var out []Conflict
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			for _, port := range shared(items[i].Ports, items[j].Ports) {
				out = append(out, Conflict{A: items[i].Name, B: items[j].Name, Port: port})
			}
		}
	}
	return out
}

// shared returns the host ports two items both publish, sorted so a report
// reads the same on every run.
func shared(a, b []int) []int {
	set := make(map[int]struct{}, len(a))
	for _, p := range a {
		set[p] = struct{}{}
	}
	var out []int
	for _, p := range b {
		if _, ok := set[p]; ok {
			out = append(out, p)
		}
	}
	sort.Ints(out)
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
		mu      sync.Mutex
		cond    = sync.NewCond(&mu)
		held    = make(map[int]struct{})
		running int
		out     = Outcome{Conflicts: Conflicts(items)}
	)

	// Indices rather than items, so the slot a result belongs in survives the
	// item being taken out of the pending list.
	pending := make([]int, len(items))
	for i := range items {
		pending[i] = i
	}

	free := func(ports []int) bool {
		for _, p := range ports {
			if _, taken := held[p]; taken {
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
				if free(items[idx].Ports) {
					next = k
					break
				}
			}
		}
		if next == -1 {
			// Nothing startable. This cannot wait forever: with nothing
			// running no port is held, so the first pending item is always
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
		for _, p := range items[idx].Ports {
			held[p] = struct{}{}
		}
		running++
		if running > out.MaxConcurrent {
			out.MaxConcurrent = running
		}

		go func(idx int) {
			defer func() {
				mu.Lock()
				for _, p := range items[idx].Ports {
					delete(held, p)
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
