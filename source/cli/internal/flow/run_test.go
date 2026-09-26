package flow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

var errBoom = errors.New("boom")

// recorder is a set of stages that append their name to calls when run.
type recorder struct {
	calls []string
}

func (r *recorder) ok(name string) func(context.Context, none) (none, error) {
	return func(context.Context, none) (none, error) {
		r.calls = append(r.calls, name)
		return none{}, nil
	}
}

func (r *recorder) fails(name string, err error) func(context.Context, none) (none, error) {
	return func(context.Context, none) (none, error) {
		r.calls = append(r.calls, name)
		return none{}, err
	}
}

func (r *recorder) name(name string) func(context.Context, nameIn) (none, error) {
	return func(context.Context, nameIn) (none, error) {
		r.calls = append(r.calls, name)
		return none{}, nil
	}
}

func (r *recorder) assert(t *testing.T, want ...string) {
	t.Helper()
	if strings.Join(r.calls, ",") != strings.Join(want, ",") {
		t.Errorf("stages run = %v, want %v", r.calls, want)
	}
}

type allIn struct {
	Input   string
	Stage   string
	Literal int
	Reader  io.Reader
	Ptr     *int
	Nil     *int
}

type ready struct{ Ready bool }

func TestRunHandsEachStageTheValuesItIsBoundTo(t *testing.T) {
	var got allIn
	buf := &bytes.Buffer{}
	seven := 7
	f := mustBuild(t, New("f").
		Stage("src", func(context.Context, none) (srcOut, error) {
			return srcOut{Name: "from-stage", Buf: buf}, nil
		}).
		Stage("sink", func(_ context.Context, in allIn) (none, error) {
			got = in
			return none{}, nil
		}).
		With("Input", FromInput("in")).
		With("Stage", FromStage("src", "Name")).
		With("Literal", Literal(42)).
		With("Reader", FromStage("src", "Buf")).
		With("Ptr", FromInput("ptr")).
		With("Nil", Literal(nil)))
	if _, err := f.Run(context.Background(), Inputs{"in": "from-input", "ptr": &seven}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := allIn{Input: "from-input", Stage: "from-stage", Literal: 42, Reader: buf, Ptr: &seven}
	if got != want {
		t.Errorf("In = %+v, want %+v", got, want)
	}
}

func TestRunLeavesAFieldBoundToANilInputNil(t *testing.T) {
	got := ptrIn{P: new(int)}
	f := mustBuild(t, New("f").
		Stage("a", func(_ context.Context, in ptrIn) (none, error) {
			got = in
			return none{}, nil
		}).With("P", FromInput("p")))
	if _, err := f.Run(context.Background(), Inputs{"p": nil}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.P != nil {
		t.Errorf("P = %v, want nil", got.P)
	}
}

func TestRunPassesTheContextItIsGiven(t *testing.T) {
	type key struct{}
	var got any
	f := mustBuild(t, New("f").Stage("a", func(ctx context.Context, _ none) (none, error) {
		got = ctx.Value(key{})
		return none{}, nil
	}))
	ctx := context.WithValue(context.Background(), key{}, "marked")
	if _, err := f.Run(ctx, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "marked" {
		t.Errorf("stage saw context value %v, want marked", got)
	}
}

func TestRunRunsStagesInDeclarationOrder(t *testing.T) {
	var r recorder
	f := mustBuild(t, New("f").Stage("c", r.ok("c")).Stage("a", r.ok("a")).Stage("b", r.ok("b")))
	res, err := f.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r.assert(t, "c", "a", "b")
	for _, s := range []string{"a", "b", "c"} {
		if got := res.Status(s); got != StatusSucceeded {
			t.Errorf("Status(%q) = %v, want succeeded", s, got)
		}
	}
}

func TestRunChecksEveryInputBeforeRunningAnything(t *testing.T) {
	build := func(r *recorder) *Flow {
		return mustBuild(t, New("f").
			Stage("first", r.ok("first")).
			Stage("a", r.name("a")).With("Name", FromInput("name")).
			Stage("b", func(context.Context, ptrIn) (none, error) { return none{}, nil }).With("P", FromInput("ptr")).
			Stage("c", func(context.Context, readerIn) (none, error) { return none{}, nil }).With("R", FromInput("reader")).
			Stage("d", r.ok("d")).When(FromInput("go")))
	}
	valid := func() Inputs {
		return Inputs{"name": "n", "ptr": new(int), "reader": &bytes.Buffer{}, "go": true}
	}
	tests := []struct {
		name   string
		change func(Inputs)
		want   []string
	}{
		{"a missing input", func(in Inputs) { delete(in, "name") },
			[]string{`flow "f": input "name" is missing`}},
		{"a missing condition input", func(in Inputs) { delete(in, "go") },
			[]string{`flow "f": input "go" is missing`}},
		{"a value of the wrong type", func(in Inputs) { in["name"] = 3 },
			[]string{`flow "f": input "name" is int, which is not assignable to string`}},
		{"a value not implementing an interface", func(in Inputs) { in["reader"] = "text" },
			[]string{`flow "f": input "reader" is string, which is not assignable to io.Reader`}},
		{"a named bool for a condition", func(in Inputs) { in["go"] = namedBool(true) },
			[]string{`flow "f": input "go" is flow.namedBool, which is not assignable to bool`}},
		{"nil for a type that cannot be nil", func(in Inputs) { in["name"] = nil },
			[]string{`flow "f": input "name" is nil, which is not assignable to string`}},
		{"every error, by key", func(in Inputs) {
			delete(in, "reader")
			in["name"] = 1
			in["go"] = nil
		}, []string{
			`flow "f": input "go" is nil, which is not assignable to bool`,
			`flow "f": input "name" is int, which is not assignable to string`,
			`flow "f": input "reader" is missing`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder
			in := valid()
			tt.change(in)
			res, err := build(&r).Run(context.Background(), in)
			assertError(t, err, strings.Join(tt.want, "\n"))
			r.assert(t)
			if res == nil {
				t.Fatal("Run returned a nil Result")
			}
			if got := res.Status("first"); got != StatusNotReached {
				t.Errorf("Status(first) = %v, want not reached", got)
			}
		})
	}
}

func TestRunAcceptsInputsItCanAssign(t *testing.T) {
	tests := []struct {
		name string
		in   Inputs
	}{
		{"exact types", Inputs{"name": "n", "ptr": new(int), "reader": &bytes.Buffer{}}},
		{"nil for a pointer and an interface", Inputs{"name": "n", "ptr": nil, "reader": nil}},
		{"keys no stage reads", Inputs{"name": "n", "ptr": nil, "reader": nil, "extra": 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder
			f := mustBuild(t, New("f").
				Stage("a", r.name("a")).With("Name", FromInput("name")).
				Stage("b", func(context.Context, ptrIn) (none, error) { return none{}, nil }).With("P", FromInput("ptr")).
				Stage("c", func(context.Context, readerIn) (none, error) { return none{}, nil }).With("R", FromInput("reader")))
			if _, err := f.Run(context.Background(), tt.in); err != nil {
				t.Fatalf("Run: %v", err)
			}
			r.assert(t, "a")
		})
	}
}

func TestRunRunsAStageOnlyWhenEveryConditionHolds(t *testing.T) {
	tests := []struct {
		name  string
		conds func(*Builder) *Builder
		runs  bool
	}{
		{"no condition", func(b *Builder) *Builder { return b }, true},
		{"When true", func(b *Builder) *Builder { return b.When(FromInput("yes")) }, true},
		{"When false", func(b *Builder) *Builder { return b.When(FromInput("no")) }, false},
		{"Unless true", func(b *Builder) *Builder { return b.Unless(FromInput("yes")) }, false},
		{"Unless false", func(b *Builder) *Builder { return b.Unless(FromInput("no")) }, true},
		{"When true and Unless false", func(b *Builder) *Builder {
			return b.When(FromInput("yes")).Unless(FromInput("no"))
		}, true},
		{"When true and Unless true", func(b *Builder) *Builder {
			return b.When(FromInput("yes")).Unless(Literal(true))
		}, false},
		{"When false and Unless false", func(b *Builder) *Builder {
			return b.When(Literal(false)).Unless(FromInput("no"))
		}, false},
		{"two Whens, the second false", func(b *Builder) *Builder {
			return b.When(FromStage("src", "Ready")).When(FromInput("no"))
		}, false},
		{"two Whens, both true", func(b *Builder) *Builder {
			return b.When(FromStage("src", "Ready")).When(Literal(true))
		}, true},
		{"two Unlesses, the second true", func(b *Builder) *Builder {
			return b.Unless(FromInput("no")).Unless(FromInput("yes"))
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r recorder
			b := New("f").
				Stage("src", func(context.Context, none) (ready, error) { return ready{Ready: true}, nil }).
				Stage("x", r.ok("x"))
			f := mustBuild(t, tt.conds(b).Stage("after", r.ok("after")))
			res, err := f.Run(context.Background(), Inputs{"yes": true, "no": false})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			want, status := []string{"after"}, StatusSkipped
			if tt.runs {
				want, status = []string{"x", "after"}, StatusSucceeded
			}
			r.assert(t, want...)
			if got := res.Status("x"); got != status {
				t.Errorf("Status(x) = %v, want %v", got, status)
			}
			if got := res.Skipped("x"); got != !tt.runs {
				t.Errorf("Skipped(x) = %t, want %t", got, !tt.runs)
			}
		})
	}
}

func TestRunStopsAtAFailFlowError(t *testing.T) {
	for _, declared := range []bool{false, true} {
		var r recorder
		b := New("f").
			Stage("a", func(context.Context, none) (srcOut, error) {
				r.calls = append(r.calls, "a")
				return srcOut{Name: "done"}, nil
			}).
			Stage("b", r.fails("b", errBoom))
		if declared {
			b = b.OnError(FailFlow)
		}
		f := mustBuild(t, b.Stage("c", r.ok("c")))
		res, err := f.Run(context.Background(), nil)
		assertError(t, err, `flow "f": stage "b": boom`)
		if !errors.Is(err, errBoom) {
			t.Error("the returned error does not wrap the stage's own")
		}
		var se *StageError
		if !errors.As(err, &se) || se.Flow != "f" || se.Stage != "b" || se.Err != errBoom {
			t.Errorf("errors.As StageError = %+v, want f/b/boom", se)
		}
		r.assert(t, "a", "b")
		if out, err := Output[srcOut](res, "a"); err != nil || out.Name != "done" {
			t.Errorf("Output(a) = %+v, %v; want the partial result's output", out, err)
		}
		wantStatus := map[string]Status{"a": StatusSucceeded, "b": StatusFailed, "c": StatusNotReached}
		for s, want := range wantStatus {
			if got := res.Status(s); got != want {
				t.Errorf("Status(%q) = %v, want %v", s, got, want)
			}
		}
		if got := res.Errors(); len(got) != 0 {
			t.Errorf("Errors = %v, want none: a FailFlow error is returned, not recorded", got)
		}
	}
}

func TestRunRecordsARecordAndContinueErrorAndGoesOn(t *testing.T) {
	var r recorder
	var log bytes.Buffer
	errOther := errors.New("other")
	f := mustBuild(t, New("f").Log(&log).
		Stage("a", r.fails("a", errBoom)).OnError(RecordAndContinue).
		Stage("b", r.ok("b")).
		Stage("c", r.fails("c", errOther)).OnError(RecordAndContinue))
	res, err := f.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r.assert(t, "a", "b", "c")
	got := res.Errors()
	if len(got) != 2 {
		t.Fatalf("Errors = %v, want two", got)
	}
	if got[0].Flow != "f" || got[0].Stage != "a" || got[0].Err != errBoom {
		t.Errorf("Errors[0] = %+v, want f/a/boom", got[0])
	}
	if got[1].Stage != "c" || got[1].Err != errOther {
		t.Errorf("Errors[1] = %+v, want c/other", got[1])
	}
	if res.Status("a") != StatusFailed || res.Status("b") != StatusSucceeded {
		t.Errorf("Status a, b = %v, %v; want failed, succeeded", res.Status("a"), res.Status("b"))
	}
	if log.Len() != 0 {
		t.Errorf("log = %q, want nothing: a recorded error is not logged", log.String())
	}
	got[0] = nil
	if res.Errors()[0] == nil {
		t.Error("changing the slice Errors returned changed the Result's own")
	}
}

func TestRunLogsABestEffortErrorAndGoesOn(t *testing.T) {
	var r recorder
	var log bytes.Buffer
	f := mustBuild(t, New("f").Log(&log).
		Stage("a", r.fails("a", errBoom)).OnError(BestEffort).
		Stage("b", r.ok("b")))
	res, err := f.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r.assert(t, "a", "b")
	if got, want := log.String(), "flow \"f\": stage \"a\": boom\n"; got != want {
		t.Errorf("log = %q, want %q", got, want)
	}
	if got := res.Errors(); len(got) != 0 {
		t.Errorf("Errors = %v, want none: a best-effort error reaches only the log", got)
	}
	if got := res.Status("a"); got != StatusFailed {
		t.Errorf("Status(a) = %v, want failed", got)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestRunGoesOnPastABestEffortErrorItCannotLog(t *testing.T) {
	for name, w := range map[string]io.Writer{"no log": nil, "a failing log": failingWriter{}} {
		t.Run(name, func(t *testing.T) {
			var r recorder
			f := mustBuild(t, New("f").Log(w).
				Stage("a", r.fails("a", errBoom)).OnError(BestEffort).
				Stage("b", r.ok("b")))
			if _, err := f.Run(context.Background(), nil); err != nil {
				t.Fatalf("Run: %v", err)
			}
			r.assert(t, "a", "b")
		})
	}
}

func TestRunFailsLoudOnAnUnavailableOutput(t *testing.T) {
	sources := map[string]func(*recorder) *Builder{
		"skipped": func(*recorder) *Builder {
			return New("f").Stage("src", source1).When(Literal(false))
		},
		"failed": func(*recorder) *Builder {
			return New("f").Stage("src", func(context.Context, none) (srcOut, error) {
				return srcOut{}, errBoom
			}).OnError(RecordAndContinue)
		},
	}
	readers := []struct {
		name  string
		read  func(*Builder, *recorder) *Builder
		label string
	}{
		{"a field", func(b *Builder, r *recorder) *Builder {
			return b.Stage("reader", r.name("reader")).With("Name", FromStage("src", "Name"))
		}, `field "Name" reads "Name"`},
		{"a When", func(b *Builder, r *recorder) *Builder {
			return b.Stage("reader", r.ok("reader")).When(FromStage("src", "Ready"))
		}, `When condition reads "Ready"`},
		{"an Unless", func(b *Builder, r *recorder) *Builder {
			return b.Stage("reader", r.ok("reader")).Unless(FromStage("src", "Ready"))
		}, `Unless condition reads "Ready"`},
		{"a field of a stage that forgives its own errors", func(b *Builder, r *recorder) *Builder {
			return b.Stage("reader", r.name("reader")).With("Name", FromStage("src", "Name")).OnError(BestEffort)
		}, `field "Name" reads "Name"`},
	}
	for status, source := range sources {
		for _, rd := range readers {
			t.Run(status+"/"+rd.name, func(t *testing.T) {
				var r recorder
				f := mustBuild(t, rd.read(source(&r), &r).Stage("after", r.ok("after")))
				res, err := f.Run(context.Background(), nil)
				assertError(t, err, `flow "f": stage "reader": `+rd.label+` of stage "src" (`+status+`): output unavailable`)
				if !errors.Is(err, ErrUnavailable) {
					t.Error("the error does not wrap ErrUnavailable")
				}
				var se *StageError
				if !errors.As(err, &se) || se.Stage != "reader" {
					t.Errorf("errors.As StageError = %+v, want stage reader", se)
				}
				r.assert(t)
				if got := res.Status("reader"); got != StatusNotReached {
					t.Errorf("Status(reader) = %v, want not reached", got)
				}
			})
		}
	}
}

func TestRunFailsLoudOnAnOutputABestEffortStageNeverProduced(t *testing.T) {
	f := mustBuild(t, New("f").
		Stage("src", func(context.Context, none) (srcOut, error) { return srcOut{}, errBoom }).OnError(BestEffort).
		Stage("reader", takesName).With("Name", FromStage("src", "Name")))
	_, err := f.Run(context.Background(), nil)
	assertError(t, err, `flow "f": stage "reader": field "Name" reads "Name" of stage "src" (failed): output unavailable`)
}

func TestRunSkipsAStageGuardedAwayFromAnUnavailableOutput(t *testing.T) {
	var r recorder
	f := mustBuild(t, New("f").
		Stage("src", source1).When(FromInput("go")).
		Stage("reader", r.name("reader")).
		When(FromInput("go")).
		When(FromStage("src", "Ready")).
		With("Name", FromStage("src", "Name")).
		Stage("after", r.ok("after")))
	res, err := f.Run(context.Background(), Inputs{"go": false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	r.assert(t, "after")
	if !res.Skipped("src") || !res.Skipped("reader") {
		t.Errorf("Skipped src, reader = %t, %t; want both", res.Skipped("src"), res.Skipped("reader"))
	}
}

func TestRunStopsOnceTheContextIsDone(t *testing.T) {
	t.Run("before the first stage", func(t *testing.T) {
		var r recorder
		f := mustBuild(t, New("f").Stage("a", r.ok("a")))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, err := f.Run(ctx, nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		r.assert(t)
		if res.Status("a") != StatusNotReached {
			t.Errorf("Status(a) = %v, want not reached", res.Status("a"))
		}
	})
	t.Run("between stages", func(t *testing.T) {
		var r recorder
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := mustBuild(t, New("f").
			Stage("a", func(context.Context, none) (none, error) {
				r.calls = append(r.calls, "a")
				cancel()
				return none{}, nil
			}).
			Stage("b", r.ok("b")))
		res, err := f.Run(ctx, nil)
		if err != context.Canceled {
			t.Errorf("err = %v, want context.Canceled itself", err)
		}
		r.assert(t, "a")
		if res.Status("a") != StatusSucceeded || res.Status("b") != StatusNotReached {
			t.Errorf("Status a, b = %v, %v; want succeeded, not reached", res.Status("a"), res.Status("b"))
		}
	})
}

type describer interface{ Describe() string }

func (o srcOut) Describe() string { return "src:" + o.Name }

func TestOutput(t *testing.T) {
	f := mustBuild(t, New("f").
		Stage("ok", func(context.Context, none) (srcOut, error) { return srcOut{Name: "n", Count: 2}, nil }).
		Stage("skipped", source1).When(Literal(false)).
		Stage("recorded", func(context.Context, none) (srcOut, error) { return srcOut{Name: "x"}, errBoom }).OnError(RecordAndContinue).
		Stage("stops", func(context.Context, none) (srcOut, error) { return srcOut{}, errBoom }).
		Stage("unreached", source1))
	res, err := f.Run(context.Background(), nil)
	if !errors.Is(err, errBoom) {
		t.Fatalf("Run err = %v, want boom", err)
	}

	out, err := Output[srcOut](res, "ok")
	if err != nil || out.Name != "n" || out.Count != 2 {
		t.Errorf("Output[srcOut](ok) = %+v, %v; want n/2", out, err)
	}
	d, err := Output[describer](res, "ok")
	if err != nil || d.Describe() != "src:n" {
		t.Errorf("Output[describer](ok) = %v, %v; want the Out through an interface it implements", d, err)
	}

	tests := []struct {
		stage       string
		want        string
		unavailable bool
	}{
		{"ghost", `flow "f" has no stage "ghost"`, false},
		{"skipped", `flow "f": stage "skipped": skipped: output unavailable`, true},
		{"recorded", `flow "f": stage "recorded": failed: output unavailable`, true},
		{"stops", `flow "f": stage "stops": failed: output unavailable`, true},
		{"unreached", `flow "f": stage "unreached": not reached: output unavailable`, true},
	}
	for _, tt := range tests {
		t.Run(tt.stage, func(t *testing.T) {
			out, err := Output[srcOut](res, tt.stage)
			assertError(t, err, tt.want)
			if errors.Is(err, ErrUnavailable) != tt.unavailable {
				t.Errorf("errors.Is(err, ErrUnavailable) = %t, want %t", !tt.unavailable, tt.unavailable)
			}
			if !reflect.ValueOf(out).IsZero() {
				t.Errorf("Output = %+v alongside an error, want the zero value", out)
			}
		})
	}

	t.Run("the wrong type", func(t *testing.T) {
		out, err := Output[none](res, "ok")
		assertError(t, err, `flow "f": stage "ok"'s output is flow.srcOut, not flow.none`)
		if out != (none{}) {
			t.Errorf("Output = %+v, want the zero value", out)
		}
		n, err := Output[io.Reader](res, "ok")
		assertError(t, err, `flow "f": stage "ok"'s output is flow.srcOut, not io.Reader`)
		if n != nil {
			t.Errorf("Output = %v, want nil", n)
		}
	})
}

func TestStatusOfAStageTheFlowDoesNotDeclare(t *testing.T) {
	f := mustBuild(t, New("f").Stage("a", nothing))
	res, err := f.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.Status("ghost"); got != StatusNotReached {
		t.Errorf("Status(ghost) = %v, want not reached", got)
	}
	if res.Skipped("ghost") {
		t.Error("Skipped(ghost) = true, want false")
	}
}

func TestAFlowRunsAgainWithNothingCarriedOver(t *testing.T) {
	var r recorder
	f := mustBuild(t, New("f").
		Stage("a", r.ok("a")).When(FromInput("go")).
		Stage("b", r.fails("b", errBoom)).OnError(RecordAndContinue))
	first, err := f.Run(context.Background(), Inputs{"go": true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	second, err := f.Run(context.Background(), Inputs{"go": false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if first.Status("a") != StatusSucceeded || second.Status("a") != StatusSkipped {
		t.Errorf("Status(a) = %v then %v, want succeeded then skipped", first.Status("a"), second.Status("a"))
	}
	if len(first.Errors()) != 1 || len(second.Errors()) != 1 {
		t.Errorf("Errors = %d then %d, want one each", len(first.Errors()), len(second.Errors()))
	}
}

func TestStatusString(t *testing.T) {
	tests := map[Status]string{
		StatusNotReached: "not reached",
		StatusSkipped:    "skipped",
		StatusSucceeded:  "succeeded",
		StatusFailed:     "failed",
		Status(9):        "Status(9)",
	}
	for s, want := range tests {
		if got := s.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}
