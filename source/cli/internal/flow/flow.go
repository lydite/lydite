// Package flow runs a flow: a named, strictly sequential list of stages, wired
// together by a Builder and checked by reflection before anything runs.
//
// A stage is a plain function, func(context.Context, In) (Out, error), whose In
// and Out are its own struct types (struct{} when it takes or gives nothing).
// A stage reads nothing but its In and returns nothing but its Out: it never
// sees the flow, another stage or any shared state, and it never chooses what
// its own error does. Everything else is the flow's to declare:
//
//   - where each exported field of In comes from — a key of the Inputs the flow
//     is run with (FromInput), an exported field of an earlier stage's Out
//     (FromStage), or a constant (Literal). Every exported field is bound
//     exactly once: an unbound field is a Build error, never a silent zero.
//   - whether the stage runs at all — When and Unless conditions, each bound
//     to a bool, all of which must hold.
//   - what its error does — FailFlow, RecordAndContinue or BestEffort.
//
// Build checks every binding against the field it feeds, so a flow that builds
// fails at run time only for the reasons Run documents.
//
// Stages run one at a time in declaration order. There is no dependency graph
// and none is to be grown here: order is declaration order and nothing else,
// and concurrency under physical constraints is internal/scheduler's concern.
package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
)

// Policy is what a stage's error does to the run. The flow declares it per
// stage with OnError; a stage never chooses its own.
type Policy int

const (
	// FailFlow stops the run at the stage's error, which Run returns. It is
	// what a stage declaring no policy gets.
	FailFlow Policy = iota
	// RecordAndContinue records the stage's error in the Result and runs the
	// next stage. The stage's output is unavailable.
	RecordAndContinue
	// BestEffort writes the stage's error to the flow's log and runs the next
	// stage. The error reaches nothing else, and the stage's output is
	// unavailable.
	BestEffort
)

func (p Policy) String() string {
	switch p {
	case FailFlow:
		return "fail-flow"
	case RecordAndContinue:
		return "record-and-continue"
	case BestEffort:
		return "best-effort"
	default:
		return fmt.Sprintf("Policy(%d)", int(p))
	}
}

type bindingKind int

const (
	fromInput bindingKind = iota + 1
	fromStage
	literal
)

// Binding is where one value a stage reads comes from. The zero Binding
// binds nothing, and Build refuses it.
type Binding struct {
	kind  bindingKind
	key   string
	stage string
	field string
	value any
}

// FromInput binds to the value the Inputs passed to Run hold under key.
func FromInput(key string) Binding {
	return Binding{kind: fromInput, key: key}
}

// FromStage binds to the exported field of stage's Out, which must be declared
// before the stage reading it.
func FromStage(stage, field string) Binding {
	return Binding{kind: fromStage, stage: stage, field: field}
}

// Literal binds to v on every run.
func Literal(v any) Binding {
	return Binding{kind: literal, value: v}
}

// Builder declares a flow. Every method but Stage and Log applies to the most
// recently added stage. Errors are collected, not returned, and Build reports
// every one of them.
type Builder struct {
	name   string
	log    io.Writer
	stages []*stageSpec
	errs   []error
}

type stageSpec struct {
	name      string
	fn        any
	withs     []with
	conds     []cond
	policy    Policy
	policySet bool
}

type with struct {
	field   string
	binding Binding
}

// cond holds when the value its binding resolves to equals want: true for
// When, false for Unless.
type cond struct {
	binding Binding
	want    bool
}

// New starts declaring the flow called name.
func New(name string) *Builder {
	return &Builder{name: name}
}

// Stage adds a stage called name, run by fn, after every stage already added.
func (b *Builder) Stage(name string, fn any) *Builder {
	b.stages = append(b.stages, &stageSpec{name: name, fn: fn})
	return b
}

// With binds the field of the stage's In called field.
func (b *Builder) With(field string, binding Binding) *Builder {
	if s := b.last("With"); s != nil {
		s.withs = append(s.withs, with{field: field, binding: binding})
	}
	return b
}

// When runs the stage only if binding resolves to true.
func (b *Builder) When(binding Binding) *Builder {
	if s := b.last("When"); s != nil {
		s.conds = append(s.conds, cond{binding: binding, want: true})
	}
	return b
}

// Unless runs the stage only if binding resolves to false.
func (b *Builder) Unless(binding Binding) *Builder {
	if s := b.last("Unless"); s != nil {
		s.conds = append(s.conds, cond{binding: binding, want: false})
	}
	return b
}

// OnError sets what the stage's error does to the run. A stage declares it at
// most once.
func (b *Builder) OnError(p Policy) *Builder {
	s := b.last("OnError")
	switch {
	case s == nil:
	case s.policySet:
		b.errs = append(b.errs, fmt.Errorf("flow %q: stage %q: OnError is declared twice", b.name, s.name))
	default:
		s.policy, s.policySet = p, true
	}
	return b
}

// Log sets where BestEffort stages' errors are written. A flow with no log, or
// a nil one, discards them.
func (b *Builder) Log(w io.Writer) *Builder {
	b.log = w
	return b
}

func (b *Builder) last(method string) *stageSpec {
	if len(b.stages) == 0 {
		b.errs = append(b.errs, fmt.Errorf("flow %q: %s is called before any Stage", b.name, method))
		return nil
	}
	return b.stages[len(b.stages)-1]
}

var (
	contextType = reflect.TypeFor[context.Context]()
	errorType   = reflect.TypeFor[error]()
	boolType    = reflect.TypeFor[bool]()
)

// Build checks the whole declaration and returns the flow it describes, or
// every error found in it joined.
//
// An input key has one type: every field and condition it feeds must have
// exactly that type, so that one value supplied to Run is right for all of
// them.
func (b *Builder) Build() (*Flow, error) {
	f := &Flow{
		name:   b.name,
		log:    b.log,
		index:  map[string]int{},
		inputs: map[string]reflect.Type{},
	}
	if f.log == nil {
		f.log = io.Discard
	}
	errs := append([]error(nil), b.errs...)
	for _, spec := range b.stages {
		s, stageErrs := f.compile(spec)
		for _, err := range stageErrs {
			errs = append(errs, fmt.Errorf("flow %q: stage %q: %w", b.name, spec.name, err))
		}
		if _, dup := f.index[spec.name]; !dup {
			f.index[spec.name] = len(f.stages)
		}
		f.stages = append(f.stages, s)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return f, nil
}

// compile resolves one stage against the stages declared before it. The
// returned stage is kept even when it is in error, so that stages after it
// resolve against it rather than reporting it undeclared.
func (f *Flow) compile(spec *stageSpec) (*stage, []error) {
	s := &stage{name: spec.name, policy: spec.policy}
	var errs []error
	if spec.name == "" {
		errs = append(errs, errors.New("a stage needs a name"))
	}
	if _, dup := f.index[spec.name]; dup {
		errs = append(errs, errors.New("the name is declared twice"))
	}
	switch spec.policy {
	case FailFlow, RecordAndContinue, BestEffort:
	default:
		errs = append(errs, fmt.Errorf("OnError declares %v, which is not a policy", spec.policy))
	}
	for _, c := range spec.conds {
		src, err := f.resolve(c.binding, boolType)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", condName(c.want), err))
			continue
		}
		s.conds = append(s.conds, condition{src: src, want: c.want})
	}
	fn, in, out, err := signature(spec.fn)
	if err != nil {
		return s, append(errs, err)
	}
	s.fn, s.in, s.out = fn, in, out
	bound := map[string]bool{}
	for _, w := range spec.withs {
		sf, ok := exportedField(in, w.field)
		if !ok {
			errs = append(errs, fmt.Errorf("With: %s has no exported field %q", in, w.field))
			continue
		}
		if bound[w.field] {
			errs = append(errs, fmt.Errorf("With: field %q is bound twice", w.field))
			continue
		}
		bound[w.field] = true
		src, err := f.resolve(w.binding, sf.Type)
		if err != nil {
			errs = append(errs, fmt.Errorf("field %q: %w", w.field, err))
			continue
		}
		s.fields = append(s.fields, fieldBinding{name: w.field, index: sf.Index[0], src: src})
	}
	for i := range in.NumField() {
		sf := in.Field(i)
		if sf.IsExported() && !bound[sf.Name] {
			errs = append(errs, fmt.Errorf("field %q is not bound", sf.Name))
		}
	}
	return s, errs
}

// resolve checks that b can feed a value of type target.
func (f *Flow) resolve(b Binding, target reflect.Type) (source, error) {
	switch b.kind {
	case fromInput:
		if prev, ok := f.inputs[b.key]; ok && prev != target {
			return source{}, fmt.Errorf("input %q is %s elsewhere in the flow, not %s", b.key, prev, target)
		}
		f.inputs[b.key] = target
		return source{kind: fromInput, key: b.key}, nil
	case fromStage:
		i, ok := f.index[b.stage]
		if !ok {
			return source{}, fmt.Errorf("stage %q is not declared before this one", b.stage)
		}
		from := f.stages[i]
		if from.out == nil {
			return source{}, fmt.Errorf("stage %q is not a stage function", b.stage)
		}
		sf, ok := exportedField(from.out, b.field)
		if !ok {
			return source{}, fmt.Errorf("stage %q's %s has no exported field %q", b.stage, from.out, b.field)
		}
		if !sf.Type.AssignableTo(target) {
			return source{}, fmt.Errorf("%q of stage %q is %s, which is not assignable to %s", b.field, b.stage, sf.Type, target)
		}
		return source{kind: fromStage, stage: i, field: sf.Index[0], name: b.field}, nil
	case literal:
		if b.value == nil {
			if !nillable(target) {
				return source{}, fmt.Errorf("a nil literal is not assignable to %s", target)
			}
			return source{kind: literal}, nil
		}
		v := reflect.ValueOf(b.value)
		if !v.Type().AssignableTo(target) {
			return source{}, fmt.Errorf("a literal %s is not assignable to %s", v.Type(), target)
		}
		return source{kind: literal, value: v}, nil
	default:
		return source{}, errors.New("the binding is not made by FromInput, FromStage or Literal")
	}
}

// signature checks that fn is a stage function and returns it with its In and
// Out types.
func signature(fn any) (v reflect.Value, in, out reflect.Type, err error) {
	v = reflect.ValueOf(fn)
	if v.Kind() != reflect.Func || v.IsNil() {
		return v, nil, nil, fmt.Errorf("%T is not a function", fn)
	}
	t := v.Type()
	if t.NumIn() != 2 || t.In(0) != contextType || t.In(1).Kind() != reflect.Struct ||
		t.NumOut() != 2 || t.Out(0).Kind() != reflect.Struct || t.Out(1) != errorType {
		return v, nil, nil, fmt.Errorf("%s is not func(context.Context, In) (Out, error) with In and Out structs", t)
	}
	return v, t.In(1), t.Out(0), nil
}

// exportedField is t's own exported field called name. A field promoted from
// an embedded struct is not one: only the embedded field itself is, and
// walking t's own fields rather than resolving the name never reaches a
// promoted one.
func exportedField(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		if sf := t.Field(i); sf.Name == name {
			return sf, sf.IsExported()
		}
	}
	return reflect.StructField{}, false
}

func nillable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return true
	default:
		return false
	}
}

func condName(want bool) string {
	if want {
		return "When"
	}
	return "Unless"
}
