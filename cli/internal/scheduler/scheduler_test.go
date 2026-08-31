package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"
)

// waitFor fails the test rather than hanging when a synchronisation point is
// never reached.
//
// Every test here is written so that the *passing* path depends on
// synchronisation and never on timing: a barrier opens because enough items
// genuinely ran at once, not because a sleep was long enough. The timeout is
// only how a failure is reported, so a scheduler that cannot reach the barrier
// fails in seconds with a message instead of hanging until the package
// timeout.
func waitFor(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s: the scheduler never reached it", what)
	}
}

func runAsync(items []Item, limit int, run func(context.Context, int)) (<-chan struct{}, *Outcome) {
	done := make(chan struct{})
	out := &Outcome{}
	go func() {
		defer close(done)
		*out = Run(context.Background(), items, limit, run)
	}()
	return done, out
}

// TestItemsRunConcurrently forces N-way concurrency with a barrier every item
// must reach before any may leave.
//
// A scheduler that runs items one at a time cannot open this barrier, so the
// test cannot pass by the feature being absent — which is the failure mode a
// test that merely observed overlap would have.
func TestItemsRunConcurrently(t *testing.T) {
	const n = 4
	items := make([]Item, n)
	for i := range items {
		items[i] = Item{Name: string(rune('a' + i))}
	}

	var wg sync.WaitGroup
	wg.Add(n)
	arrived := make(chan struct{})
	go func() { wg.Wait(); close(arrived) }()

	done, out := runAsync(items, n, func(context.Context, int) {
		wg.Done()
		<-arrived
	})
	waitFor(t, "all items to be running at once", arrived)
	waitFor(t, "the run to finish", done)

	if out.MaxConcurrent != n {
		t.Fatalf("MaxConcurrent = %d, want %d", out.MaxConcurrent, n)
	}
}

// TestSharedPortSerialises asserts the ordering rather than the exit code.
//
// Two items publish 5432 and a third publishes nothing. Every item blocks
// until the test releases it, which is what makes the assertion decide the
// question in both directions: without the lock all three are startable, so
// all three record an entry before any records an exit and the intervals
// overlap. An item that returned promptly instead could record its whole
// interval before the first one started, reading as disjoint on a scheduler
// holding no lock at all.
//
// The third item is held alongside the first to prove things are running at
// once at the moment the two are being kept apart — otherwise a scheduler that
// had simply run everything sequentially would satisfy disjointness without
// ever having taken a lock.
func TestSharedPortSerialises(t *testing.T) {
	items := []Item{
		{Name: "go/api", Ports: []int{5432}},
		{Name: "rust", Ports: []int{5432}},
		{Name: "web", Ports: []int{5173}},
	}

	var mu sync.Mutex
	var log []string
	record := func(s string) { mu.Lock(); log = append(log, s); mu.Unlock() }

	release := make(chan struct{})
	entered := map[string]chan struct{}{
		"go/api": make(chan struct{}),
		"rust":   make(chan struct{}),
		"web":    make(chan struct{}),
	}

	done, out := runAsync(items, len(items), func(_ context.Context, i int) {
		name := items[i].Name
		record(name + ":enter")
		close(entered[name])
		// Every item waits, including the one the lock is expected to hold
		// back. A run that never blocks it cannot be told from one that
		// serialised it.
		<-release
		record(name + ":exit")
	})

	waitFor(t, "go/api to start", entered["go/api"])
	waitFor(t, "web to start alongside it", entered["web"])
	// rust is deliberately not waited for: the lock is what keeps it out
	// until go/api is done, and waiting for it here would deadlock a correct
	// scheduler.
	close(release)
	waitFor(t, "the run to finish", done)

	// Disjoint intervals in the recorded log, not a comparison of clocks: a
	// scheduler test that depends on real timing is one that goes flaky in
	// somebody else's CI.
	api, rust := interval(t, log, "go/api"), interval(t, log, "rust")
	if api.start < rust.end && rust.start < api.end {
		t.Fatalf("go/api %v and rust %v overlap, but both publish 5432: %v", api, rust, log)
	}
	if out.MaxConcurrent < 2 {
		t.Fatalf("MaxConcurrent = %d: nothing ever ran at once, so the port lock was never contended", out.MaxConcurrent)
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0].On != "port 5432" {
		t.Fatalf("Conflicts = %v, want the one pair on 5432", out.Conflicts)
	}
}

type span struct{ start, end int }

func interval(t *testing.T, log []string, name string) span {
	t.Helper()
	s := span{start: -1, end: -1}
	for i, e := range log {
		switch e {
		case name + ":enter":
			s.start = i
		case name + ":exit":
			s.end = i
		}
	}
	if s.start < 0 || s.end < 0 {
		t.Fatalf("%s did not run: %v", name, log)
	}
	return s
}

// TestLimitBinds forces exactly limit items to be running at once, so the
// bound is shown to be reached as well as not exceeded. A scheduler that ran
// fewer never opens the gate.
func TestLimitBinds(t *testing.T) {
	for _, limit := range []int{1, 2, 5} {
		t.Run(name(limit), func(t *testing.T) {
			const n = 5
			items := make([]Item, n)
			for i := range items {
				items[i] = Item{Name: string(rune('a' + i))}
			}

			var mu sync.Mutex
			var live int
			var once sync.Once
			gate := make(chan struct{})

			done, out := runAsync(items, limit, func(context.Context, int) {
				mu.Lock()
				live++
				if live == limit {
					once.Do(func() { close(gate) })
				}
				mu.Unlock()
				<-gate
				mu.Lock()
				live--
				mu.Unlock()
			})
			waitFor(t, "the bound to be reached", gate)
			waitFor(t, "the run to finish", done)

			if out.MaxConcurrent != limit {
				t.Fatalf("MaxConcurrent = %d, want exactly %d", out.MaxConcurrent, limit)
			}
		})
	}
}

func name(limit int) string {
	if limit == 1 {
		return "serial"
	}
	return "parallel"
}

// TestEveryItemRuns is the floor the assertions above sit on: a scheduler that
// silently dropped an item would satisfy every ordering claim.
func TestEveryItemRuns(t *testing.T) {
	items := []Item{
		{Name: "a", Ports: []int{5432}},
		{Name: "b", Ports: []int{5432}},
		{Name: "c", Ports: []int{5432}},
		{Name: "d"},
	}
	seen := make([]bool, len(items))
	var mu sync.Mutex
	Run(context.Background(), items, 4, func(_ context.Context, i int) {
		mu.Lock()
		seen[i] = true
		mu.Unlock()
	})
	for i, ok := range seen {
		if !ok {
			t.Fatalf("%s never ran", items[i].Name)
		}
	}
}

// TestCancelledRunStartsNothingFurther asserts a cancelled context stops the
// queue without abandoning what is already running.
//
// The item in flight must still return, because its caller has services to
// tear down and returning before that is what leaks the containers holding the
// ports the next run needs.
func TestCancelledRunStartsNothingFurther(t *testing.T) {
	items := make([]Item, 5)
	for i := range items {
		items[i] = Item{Name: string(rune('a' + i))}
	}

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var started, finished int
	running := make(chan struct{})
	var once sync.Once

	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, items, 1, func(context.Context, int) {
			mu.Lock()
			started++
			mu.Unlock()
			once.Do(func() { close(running); <-ctx.Done() })
			mu.Lock()
			finished++
			mu.Unlock()
		})
	}()

	waitFor(t, "the first item to start", running)
	cancel()
	waitFor(t, "the run to return", done)

	mu.Lock()
	defer mu.Unlock()
	if started == len(items) {
		t.Fatalf("every item started despite cancellation")
	}
	if started != finished {
		t.Fatalf("%d started but %d finished: an item was abandoned mid-flight", started, finished)
	}
}

func TestConflictsPairsEverySharedPort(t *testing.T) {
	got := Conflicts([]Item{
		{Name: "a", Ports: []int{5432, 6379}},
		{Name: "b", Ports: []int{5432, 6379}},
		{Name: "c", Ports: []int{5433}},
	})
	if len(got) != 2 {
		t.Fatalf("Conflicts = %v, want both shared ports", got)
	}
	for _, c := range got {
		if c.A != "a" || c.B != "b" {
			t.Fatalf("unexpected pair %v", c)
		}
	}
	// Two ports shared by one pair is one pair serialised, not two. A report
	// counting conflicts would say the run did more sequencing than it did.
	if n := Pairs(got); n != 1 {
		t.Fatalf("Pairs = %d, want 1", n)
	}
}

// Two components rooted at one directory install into, build in and write
// their output to one tree, so they cannot run at once however their ports are
// arranged. An npm ci removing and recreating a node_modules another component
// is importing from is not a race either suite can report honestly.
func TestSameDirectorySerialises(t *testing.T) {
	items := []Item{
		{Name: "unit", Dir: "web"},
		{Name: "integration", Dir: "web"},
		{Name: "api", Dir: "go/api"},
	}
	got := Conflicts(items)
	if len(got) != 1 || got[0].On != "directory web" {
		t.Fatalf("Conflicts = %v, want the two web components on their directory", got)
	}

	var mu sync.Mutex
	var log []string
	release := make(chan struct{})
	entered := map[string]chan struct{}{
		"unit": make(chan struct{}), "integration": make(chan struct{}), "api": make(chan struct{}),
	}
	done, out := runAsync(items, len(items), func(_ context.Context, i int) {
		name := items[i].Name
		mu.Lock()
		log = append(log, name+":enter")
		mu.Unlock()
		close(entered[name])
		<-release
		mu.Lock()
		log = append(log, name+":exit")
		mu.Unlock()
	})
	waitFor(t, "unit to start", entered["unit"])
	waitFor(t, "api to start alongside it", entered["api"])
	close(release)
	waitFor(t, "the run to finish", done)

	a, b := interval(t, log, "unit"), interval(t, log, "integration")
	if a.start < b.end && b.start < a.end {
		t.Fatalf("unit %v and integration %v overlap in one directory: %v", a, b, log)
	}
	if out.MaxConcurrent < 2 {
		t.Fatalf("MaxConcurrent = %d: nothing ran at once, so the lock was never contended", out.MaxConcurrent)
	}
}
