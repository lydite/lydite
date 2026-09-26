package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"reflect"
	"slices"
)

// ErrUnavailable is wrapped by every error reporting that a stage's output
// does not exist: the stage was skipped, failed, or was never reached.
var ErrUnavailable = errors.New("output unavailable")

// Inputs are the values a flow is run with, keyed as FromInput names them.
// Keys no stage reads are ignored.
type Inputs map[string]any

// Flow is a built flow. It holds no state between runs.
type Flow struct {
	name   string
	log    io.Writer
	stages []*stage
	index  map[string]int
	inputs map[string]reflect.Type
}

type stage struct {
	name   string
	fn     reflect.Value
	in     reflect.Type
	out    reflect.Type
	fields []fieldBinding
	conds  []condition
	policy Policy
}

type fieldBinding struct {
	name  string
	index int
	src   source
}

type condition struct {
	src  source
	want bool
}

// source is a Binding resolved against the flow: a stage index and field
// index for fromStage, a key for fromInput, a value for literal. A literal
// whose value is invalid is nil, which is the zero value its target already
// holds.
type source struct {
	kind  bindingKind
	key   string
	stage int
	field int
	name  string
	value reflect.Value
}

// Name is the name the flow was declared with.
func (f *Flow) Name() string {
	return f.name
}

// Inputs are the keys Run requires, each with the type its value must be
// assignable to.
func (f *Flow) Inputs() map[string]reflect.Type {
	return maps.Clone(f.inputs)
}

// Status is how far a stage got in one run.
type Status int

const (
	// StatusNotReached is a stage the run stopped before, or one the flow
	// does not declare.
	StatusNotReached Status = iota
	// StatusSkipped is a stage one of whose conditions did not hold.
	StatusSkipped
	// StatusSucceeded is a stage that returned no error.
	StatusSucceeded
	// StatusFailed is a stage that returned an error.
	StatusFailed
)

func (s Status) String() string {
	switch s {
	case StatusNotReached:
		return "not reached"
	case StatusSkipped:
		return "skipped"
	case StatusSucceeded:
		return "succeeded"
	case StatusFailed:
		return "failed"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// StageError is an error raised by or for one stage of a flow.
type StageError struct {
	Flow  string
	Stage string
	Err   error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("flow %q: stage %q: %v", e.Flow, e.Stage, e.Err)
}

func (e *StageError) Unwrap() error {
	return e.Err
}

// Result is what one run produced.
type Result struct {
	flow    *Flow
	states  []Status
	outputs []reflect.Value
	errors  []*StageError
}

// Status is how far stage got.
func (r *Result) Status(stage string) Status {
	i, ok := r.flow.index[stage]
	if !ok {
		return StatusNotReached
	}
	return r.states[i]
}

// Skipped reports whether stage was skipped because a condition did not hold.
func (r *Result) Skipped(stage string) bool {
	return r.Status(stage) == StatusSkipped
}

// Errors are the errors of RecordAndContinue stages, in the order they
// occurred.
func (r *Result) Errors() []*StageError {
	return slices.Clone(r.errors)
}

// Output is the Out stage returned. It is an error when the flow declares no
// such stage, when the stage has no output — wrapping ErrUnavailable — and
// when its Out is not a T.
func Output[T any](r *Result, stage string) (T, error) {
	var zero T
	i, ok := r.flow.index[stage]
	if !ok {
		return zero, fmt.Errorf("flow %q has no stage %q", r.flow.name, stage)
	}
	if r.states[i] != StatusSucceeded {
		return zero, &StageError{Flow: r.flow.name, Stage: stage, Err: fmt.Errorf("%v: %w", r.states[i], ErrUnavailable)}
	}
	v, ok := r.outputs[i].Interface().(T)
	if !ok {
		return zero, fmt.Errorf("flow %q: stage %q's output is %s, not %s", r.flow.name, stage, r.outputs[i].Type(), reflect.TypeFor[T]())
	}
	return v, nil
}

// Run checks in against the inputs the flow requires, then runs every stage in
// declaration order.
//
// For each stage it first stops if ctx is done, returning ctx's error. It then
// evaluates the stage's conditions in declaration order; the first that does
// not hold skips the stage, and the rest are not evaluated. A stage that runs
// is handed its In, and its error is handled by its policy: a FailFlow error
// stops the run and is returned as a *StageError.
//
// A condition or field bound to the output of a stage that was skipped or
// failed stops the run with a *StageError naming both stages and wrapping
// ErrUnavailable, whatever the reading stage's policy: a flow reading an
// output it did not guard is miswired, and a zero value in its place would
// read as a real one.
//
// Run always returns a non-nil Result. Alongside an error it holds what the
// run produced before it stopped.
func (f *Flow) Run(ctx context.Context, in Inputs) (*Result, error) {
	r := &Result{
		flow:    f,
		states:  make([]Status, len(f.stages)),
		outputs: make([]reflect.Value, len(f.stages)),
	}
	if err := f.check(in); err != nil {
		return r, err
	}
	for i, s := range f.stages {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		holds, err := f.holds(r, s, in)
		if err != nil {
			return r, err
		}
		if !holds {
			r.states[i] = StatusSkipped
			continue
		}
		arg, err := f.argument(r, s, in)
		if err != nil {
			return r, err
		}
		out := s.fn.Call([]reflect.Value{reflect.ValueOf(ctx), arg})
		if err, _ := out[1].Interface().(error); err != nil {
			r.states[i] = StatusFailed
			serr := &StageError{Flow: f.name, Stage: s.name, Err: err}
			switch s.policy {
			case RecordAndContinue:
				r.errors = append(r.errors, serr)
			case BestEffort:
				// A log that cannot be written to is no reason to stop a run
				// this policy promises to continue.
				_, _ = fmt.Fprintln(f.log, serr)
			default:
				return r, serr
			}
			continue
		}
		r.states[i] = StatusSucceeded
		r.outputs[i] = out[0]
	}
	return r, nil
}

// check reports every required input that is missing or whose value is not
// assignable to its type. A nil value is assignable to a nillable type.
func (f *Flow) check(in Inputs) error {
	var errs []error
	for _, key := range slices.Sorted(maps.Keys(f.inputs)) {
		want := f.inputs[key]
		v, ok := in[key]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("flow %q: input %q is missing", f.name, key))
		case v == nil:
			if !nillable(want) {
				errs = append(errs, fmt.Errorf("flow %q: input %q is nil, which is not assignable to %s", f.name, key, want))
			}
		case !reflect.TypeOf(v).AssignableTo(want):
			errs = append(errs, fmt.Errorf("flow %q: input %q is %T, which is not assignable to %s", f.name, key, v, want))
		}
	}
	return errors.Join(errs...)
}

// holds reports whether every one of s's conditions holds, stopping at the
// first that does not.
func (f *Flow) holds(r *Result, s *stage, in Inputs) (bool, error) {
	for _, c := range s.conds {
		v, err := f.value(r, s, condName(c.want)+" condition", c.src, in)
		if err != nil {
			return false, err
		}
		if v.Bool() != c.want {
			return false, nil
		}
	}
	return true, nil
}

// argument builds s's In from its bindings.
func (f *Flow) argument(r *Result, s *stage, in Inputs) (reflect.Value, error) {
	arg := reflect.New(s.in).Elem()
	for _, fb := range s.fields {
		v, err := f.value(r, s, fmt.Sprintf("field %q", fb.name), fb.src, in)
		if err != nil {
			return reflect.Value{}, err
		}
		if v.IsValid() {
			arg.Field(fb.index).Set(v)
		}
	}
	return arg, nil
}

// value is what src resolves to in this run, invalid when it is nil. what
// names the reader for an error.
func (f *Flow) value(r *Result, reader *stage, what string, src source, in Inputs) (reflect.Value, error) {
	switch src.kind {
	case fromInput:
		v := in[src.key]
		if v == nil {
			return reflect.Value{}, nil
		}
		return reflect.ValueOf(v), nil
	case fromStage:
		from := f.stages[src.stage]
		if st := r.states[src.stage]; st != StatusSucceeded {
			return reflect.Value{}, &StageError{
				Flow:  f.name,
				Stage: reader.name,
				Err:   fmt.Errorf("%s reads %q of stage %q (%v): %w", what, src.name, from.name, st, ErrUnavailable),
			}
		}
		return r.outputs[src.stage].Field(src.field), nil
	default:
		return src.value, nil
	}
}
