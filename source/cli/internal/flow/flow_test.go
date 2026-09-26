package flow

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// component is a StageComponent whose Run is a closure.
type component struct {
	name string
	run  func(ctx context.Context, in View) (Result, error)
}

func (c component) Name() string { return c.name }

func (c component) Run(ctx context.Context, in View) (Result, error) {
	return c.run(ctx, in)
}

func writes(name string, policy OutcomePolicy, w ...Write) component {
	return component{name: name, run: func(context.Context, View) (Result, error) {
		return Result{Policy: policy, Writes: w}, nil
	}}
}

func fails(name string, policy OutcomePolicy, err error) component {
	return component{name: name, run: func(context.Context, View) (Result, error) {
		return Result{Policy: policy}, err
	}}
}

// waitFor fails the test rather than hanging when a synchronisation point is
// never reached. Passing depends on synchronisation, never on the timeout.
func waitFor(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Errorf("timed out waiting for %s", what)
	}
}

func TestGetReportsAbsentForAKeyNeverSet(t *testing.T) {
	c := NewContext()
	k := NewKey[int]("count")
	if v, ok := Get(c, k); ok || v != 0 {
		t.Fatalf("Get on an empty Context = %d, %t; want 0, false", v, ok)
	}
	Set(c, k, 3)
	if v, ok := Get(c, k); !ok || v != 3 {
		t.Fatalf("Get after Set = %d, %t; want 3, true", v, ok)
	}
	if v, ok := Get(c.View(), k); !ok || v != 3 {
		t.Fatalf("Get through a View = %d, %t; want 3, true", v, ok)
	}
}

func TestKeysSharingANameAreDistinct(t *testing.T) {
	c := NewContext()
	a := NewKey[string]("shared")
	b := NewKey[int]("shared")
	Set(c, a, "text")
	if _, ok := Get(c, b); ok {
		t.Fatal("a key read back a value set under a different key of the same name")
	}
}

func TestAZeroKeyIsRefused(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Set with a zero Key did not panic")
		}
	}()
	Set(NewContext(), Key[int]{}, 1)
}

func TestStagesRunInOrderAndSeeEarlierStagesJoined(t *testing.T) {
	first := NewKey[string]("first")
	second := NewKey[string]("second")
	var saw string
	f := Flow{Stages: []Stage{
		{Name: "one", Components: []StageComponent{writes("a", FailFlow, Put(first, "1"))}},
		{Name: "two", Components: []StageComponent{component{name: "b", run: func(_ context.Context, in View) (Result, error) {
			v, _ := Get(in, first)
			saw = v
			return Result{Policy: FailFlow, Writes: []Write{Put(second, v+"2")}}, nil
		}}}},
	}}
	c := NewContext()
	if err := f.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if saw != "1" {
		t.Errorf("stage two read %q from stage one; want %q", saw, "1")
	}
	if v, _ := Get(c, second); v != "12" {
		t.Errorf("second = %q; want %q", v, "12")
	}
}

// The join runs in declaration order whatever order the siblings finish in:
// each sibling here waits for the one declared after it, so they complete in
// reverse, and the later-declared write to a shared key still wins.
func TestJoinFollowsDeclarationOrderNotCompletionOrder(t *testing.T) {
	shared := NewKey[string]("shared")
	const n = 4
	done := make([]chan struct{}, n+1)
	for i := range done {
		done[i] = make(chan struct{})
	}
	close(done[n])
	names := []string{"a", "b", "c", "d"}
	var comps []StageComponent
	for i := range n {
		comps = append(comps, component{name: names[i], run: func(context.Context, View) (Result, error) {
			defer close(done[i])
			waitFor(t, "the next sibling to finish", done[i+1])
			return Result{Policy: RecordAndContinue, Writes: []Write{Put(shared, names[i])}}, errors.New(names[i])
		}})
	}
	// The same shape, succeeding, so the writes reach the Context.
	done2 := make([]chan struct{}, n+1)
	for i := range done2 {
		done2[i] = make(chan struct{})
	}
	close(done2[n])
	var writers []StageComponent
	for i := range n {
		writers = append(writers, component{name: names[i], run: func(context.Context, View) (Result, error) {
			defer close(done2[i])
			waitFor(t, "the next sibling to finish", done2[i+1])
			return Result{Policy: FailFlow, Writes: []Write{Put(shared, names[i])}}, nil
		}})
	}

	c := NewContext()
	f := Flow{Stages: []Stage{{Name: "errs", Components: comps}, {Name: "writes", Components: writers}}}
	if err := f.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range c.Errors() {
		got = append(got, e.Component)
	}
	if strings.Join(got, ",") != "a,b,c,d" {
		t.Errorf("recorded errors in order %v; want declaration order a,b,c,d", got)
	}
	if v, _ := Get(c, shared); v != "d" {
		t.Errorf("shared = %q; want the last-declared sibling's %q", v, "d")
	}
}

// A sibling that has finished must not reach the Context while another is
// still running. The reader below keeps reading the key the writer returned,
// after the writer has returned; an engine that merged each result as it
// arrived would write the map concurrently with those reads, which -race
// reports, and would make the value visible, which the reader reports.
func TestNoSiblingObservesAnotherSiblingsResultBeforeTheJoin(t *testing.T) {
	k := NewKey[int]("k")
	writerDone := make(chan struct{})
	var observed atomic.Bool
	writer := component{name: "writer", run: func(context.Context, View) (Result, error) {
		defer close(writerDone)
		return Result{Policy: FailFlow, Writes: []Write{Put(k, 1)}}, nil
	}}
	reader := component{name: "reader", run: func(_ context.Context, in View) (Result, error) {
		waitFor(t, "the writer to return", writerDone)
		deadline := time.Now().Add(50 * time.Millisecond)
		for time.Now().Before(deadline) {
			if _, ok := Get(in, k); ok {
				observed.Store(true)
			}
			time.Sleep(time.Millisecond)
		}
		return Result{Policy: FailFlow}, nil
	}}
	var afterJoin bool
	next := component{name: "next", run: func(_ context.Context, in View) (Result, error) {
		_, afterJoin = Get(in, k)
		return Result{Policy: FailFlow}, nil
	}}

	f := Flow{Stages: []Stage{
		{Name: "parallel", Components: []StageComponent{writer, reader}},
		{Name: "after", Components: []StageComponent{next}},
	}}
	c := NewContext()
	if err := f.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if observed.Load() {
		t.Error("a sibling observed another sibling's write before the Stage joined")
	}
	if !afterJoin {
		t.Error("the next Stage did not observe the joined write")
	}
}

// Every sibling runs to completion even when one fails the Flow: the failing
// sibling returns first, and the other is still running, uncancelled, after.
func TestFailFlowAbortsLaterStagesButNotSiblings(t *testing.T) {
	boom := errors.New("boom")
	joined := NewKey[bool]("joined")
	failDone := make(chan struct{})
	var siblingCancelled, laterRan atomic.Bool
	fail := component{name: "fail", run: func(context.Context, View) (Result, error) {
		defer close(failDone)
		return Result{Policy: FailFlow}, boom
	}}
	sibling := component{name: "sibling", run: func(ctx context.Context, _ View) (Result, error) {
		waitFor(t, "the failing sibling to return", failDone)
		siblingCancelled.Store(ctx.Err() != nil)
		return Result{Policy: FailFlow, Writes: []Write{Put(joined, true)}}, nil
	}}
	later := component{name: "later", run: func(context.Context, View) (Result, error) {
		laterRan.Store(true)
		return Result{Policy: FailFlow}, nil
	}}

	c := NewContext()
	f := Flow{Stages: []Stage{
		{Name: "first", Components: []StageComponent{fail, sibling}},
		{Name: "second", Components: []StageComponent{later}},
	}}
	err := f.Run(context.Background(), c)
	if !errors.Is(err, boom) {
		t.Fatalf("Run = %v; want it to carry %v", err, boom)
	}
	var ce *ComponentError
	if !errors.As(err, &ce) || ce.Stage != "first" || ce.Component != "fail" {
		t.Errorf("Run = %v; want a ComponentError naming first/fail", err)
	}
	if siblingCancelled.Load() {
		t.Error("a sibling's context was cancelled by another sibling's failure")
	}
	if v, _ := Get(c, joined); !v {
		t.Error("the succeeding sibling's result was not joined")
	}
	if laterRan.Load() {
		t.Error("a Stage after a failed one ran")
	}
	if len(c.Errors()) != 0 {
		t.Errorf("a FailFlow error was recorded onto the Context: %v", c.Errors())
	}
}

func TestRecordAndContinueRecordsAndProceeds(t *testing.T) {
	bad := errors.New("bad")
	var laterRan bool
	var sawRecorded []*ComponentError
	c := NewContext()
	f := Flow{Stages: []Stage{
		{Name: "first", Components: []StageComponent{fails("rec", RecordAndContinue, bad)}},
		{Name: "second", Components: []StageComponent{component{name: "later", run: func(_ context.Context, in View) (Result, error) {
			laterRan = true
			sawRecorded = in.Errors()
			return Result{Policy: FailFlow}, nil
		}}}},
	}}
	if err := f.Run(context.Background(), c); err != nil {
		t.Fatalf("Run = %v; want nil", err)
	}
	if !laterRan {
		t.Error("the Stage after a recorded error did not run")
	}
	errs := c.Errors()
	if len(errs) != 1 || !errors.Is(errs[0], bad) || errs[0].Stage != "first" || errs[0].Component != "rec" {
		t.Errorf("recorded %v; want one first/rec error carrying %v", errs, bad)
	}
	if len(sawRecorded) != 1 {
		t.Errorf("the later Stage saw %d recorded errors; want 1", len(sawRecorded))
	}
}

func TestBestEffortOnlyLogs(t *testing.T) {
	var log bytes.Buffer
	k := NewKey[int]("k")
	var laterRan bool
	c := NewContext()
	f := Flow{Log: &log, Stages: []Stage{
		{Name: "first", Components: []StageComponent{component{name: "try", run: func(context.Context, View) (Result, error) {
			return Result{Policy: BestEffort, Writes: []Write{Put(k, 1)}}, errors.New("nope")
		}}}},
		{Name: "second", Components: []StageComponent{component{name: "later", run: func(context.Context, View) (Result, error) {
			laterRan = true
			return Result{Policy: FailFlow}, nil
		}}}},
	}}
	if err := f.Run(context.Background(), c); err != nil {
		t.Fatalf("Run = %v; want nil", err)
	}
	if !laterRan {
		t.Error("the Stage after a best-effort error did not run")
	}
	if len(c.Errors()) != 0 {
		t.Errorf("a best-effort error was recorded: %v", c.Errors())
	}
	if _, ok := Get(c, k); ok {
		t.Error("an erroring component's writes were joined")
	}
	if got := log.String(); !strings.Contains(got, "first/try: nope") {
		t.Errorf("log = %q; want it to name first/try and its error", got)
	}
}

func TestAnUndeclaredPolicyFailsTheFlowEvenWithoutAnError(t *testing.T) {
	var laterRan bool
	f := Flow{Stages: []Stage{
		{Name: "first", Components: []StageComponent{writes("forgot", policyUnset)}},
		{Name: "second", Components: []StageComponent{component{name: "later", run: func(context.Context, View) (Result, error) {
			laterRan = true
			return Result{Policy: FailFlow}, nil
		}}}},
	}}
	err := f.Run(context.Background(), NewContext())
	if err == nil || !strings.Contains(err.Error(), "first/forgot") {
		t.Fatalf("Run = %v; want an error naming first/forgot", err)
	}
	if laterRan {
		t.Error("a Stage after an undeclared policy ran")
	}
}

func TestACancelledContextStartsNoFurtherStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var laterRan bool
	f := Flow{Stages: []Stage{
		{Name: "first", Components: []StageComponent{component{name: "cancel", run: func(context.Context, View) (Result, error) {
			cancel()
			return Result{Policy: FailFlow}, nil
		}}}},
		{Name: "second", Components: []StageComponent{component{name: "later", run: func(context.Context, View) (Result, error) {
			laterRan = true
			return Result{Policy: FailFlow}, nil
		}}}},
	}}
	if err := f.Run(ctx, NewContext()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v; want %v", err, context.Canceled)
	}
	if laterRan {
		t.Error("a Stage started after the context was cancelled")
	}
}

// recordingSink is a Sink that keeps what it read from the final state.
type recordingSink struct {
	got map[string]int
}

func (s *recordingSink) Write(_ context.Context, final View) error {
	v, ok := Get(final, sinkKey)
	if !ok {
		return errors.New("nothing to write")
	}
	s.got["total"] = v
	return nil
}

var sinkKey = NewKey[int]("total")

func TestASinkReadsTheFinalState(t *testing.T) {
	c := NewContext()
	f := Flow{Stages: []Stage{{Name: "only", Components: []StageComponent{writes("sum", FailFlow, Put(sinkKey, 7))}}}}
	if err := f.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	var s Sink = &recordingSink{got: map[string]int{}}
	if err := s.Write(context.Background(), c.View()); err != nil {
		t.Fatal(err)
	}
	if got := s.(*recordingSink).got["total"]; got != 7 {
		t.Errorf("sink wrote %d; want 7", got)
	}
}
