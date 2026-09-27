package flow

import (
	"bytes"
	"context"
	"io"
	"maps"
	"reflect"
	"strings"
	"testing"
)

type none struct{}

type nameIn struct{ Name string }

type countIn struct{ Count int }

type readerIn struct{ R io.Reader }

type ptrIn struct{ P *int }

type mixedIn struct {
	Name   string
	hidden int
}

type Inner struct{ Name string }

type embedIn struct{ Inner }

type namedBool bool

type srcOut struct {
	Name    string
	Count   int
	Ready   bool
	Named   namedBool
	Buf     *bytes.Buffer
	private string
}

func nothing(context.Context, none) (none, error) { return none{}, nil }

func takesName(context.Context, nameIn) (none, error) { return none{}, nil }

func takesCount(context.Context, countIn) (none, error) { return none{}, nil }

func takesReader(context.Context, readerIn) (none, error) { return none{}, nil }

func takesPtr(context.Context, ptrIn) (none, error) { return none{}, nil }

func takesMixed(_ context.Context, in mixedIn) (none, error) {
	_ = in.hidden
	return none{}, nil
}

func takesEmbed(context.Context, embedIn) (none, error) { return none{}, nil }

func source1(context.Context, none) (srcOut, error) { return srcOut{private: "p"}, nil }

func givesEmbed(context.Context, none) (embedIn, error) { return embedIn{}, nil }

func TestBuildRefusesAFunctionOfTheWrongShape(t *testing.T) {
	const shape = " is not func(context.Context, In) (Out, error) with In and Out structs"
	var typedNil func(context.Context, none) (none, error)
	tests := []struct {
		name string
		fn   any
		want string
	}{
		{"not a function", 42, "int is not a function"},
		{"untyped nil", nil, "<nil> is not a function"},
		{"typed nil", typedNil, "func(context.Context, flow.none) (flow.none, error) is not a function"},
		{"one parameter", func(none) (none, error) { return none{}, nil },
			"func(flow.none) (flow.none, error)" + shape},
		{"three parameters", func(context.Context, none, none) (none, error) { return none{}, nil },
			"func(context.Context, flow.none, flow.none) (flow.none, error)" + shape},
		{"first parameter not a context", func(string, none) (none, error) { return none{}, nil },
			"func(string, flow.none) (flow.none, error)" + shape},
		{"In not a struct", func(context.Context, int) (none, error) { return none{}, nil },
			"func(context.Context, int) (flow.none, error)" + shape},
		{"In a pointer to a struct", func(context.Context, *none) (none, error) { return none{}, nil },
			"func(context.Context, *flow.none) (flow.none, error)" + shape},
		{"one result", func(context.Context, none) error { return nil },
			"func(context.Context, flow.none) error" + shape},
		{"three results", func(context.Context, none) (none, error, error) { return none{}, nil, nil },
			"func(context.Context, flow.none) (flow.none, error, error)" + shape},
		{"Out not a struct", func(context.Context, none) (int, error) { return 0, nil },
			"func(context.Context, flow.none) (int, error)" + shape},
		{"second result not an error", func(context.Context, none) (none, string) { return none{}, "" },
			"func(context.Context, flow.none) (flow.none, string)" + shape},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New("f").Stage("a", tt.fn).Build()
			assertError(t, err, `flow "f": stage "a": `+tt.want)
		})
	}
}

func TestBuildRefusesAMiswiredDeclaration(t *testing.T) {
	tests := []struct {
		name  string
		build *Builder
		want  []string
	}{
		{
			"an empty stage name",
			New("f").Stage("", nothing),
			[]string{`flow "f": stage "": a stage needs a name`},
		},
		{
			"a stage name declared twice",
			New("f").Stage("a", nothing).Stage("a", nothing),
			[]string{`flow "f": stage "a": the name is declared twice`},
		},
		{
			"a stage name declared twice, read by a later stage",
			New("f").Stage("a", source1).Stage("a", nothing).
				Stage("b", takesName).With("Name", FromStage("a", "Name")),
			[]string{`flow "f": stage "a": the name is declared twice`},
		},
		{
			"With before any stage",
			New("f").With("Name", Literal("x")),
			[]string{`flow "f": With is called before any Stage`},
		},
		{
			"When before any stage",
			New("f").When(Literal(true)),
			[]string{`flow "f": When is called before any Stage`},
		},
		{
			"Unless before any stage",
			New("f").Unless(Literal(true)),
			[]string{`flow "f": Unless is called before any Stage`},
		},
		{
			"OnError before any stage",
			New("f").OnError(BestEffort),
			[]string{`flow "f": OnError is called before any Stage`},
		},
		{
			"OnError declared twice",
			New("f").Stage("a", nothing).OnError(BestEffort).OnError(BestEffort),
			[]string{`flow "f": stage "a": OnError is declared twice`},
		},
		{
			"a policy past the last one",
			New("f").Stage("a", nothing).OnError(Policy(3)),
			[]string{`flow "f": stage "a": OnError declares Policy(3), which is not a policy`},
		},
		{
			"a policy before the first one",
			New("f").Stage("a", nothing).OnError(Policy(-1)),
			[]string{`flow "f": stage "a": OnError declares Policy(-1), which is not a policy`},
		},
		{
			"a field In does not have",
			New("f").Stage("a", takesName).With("Name", Literal("x")).With("Nope", Literal("x")),
			[]string{`flow "f": stage "a": With: flow.nameIn has no exported field "Nope"`},
		},
		{
			"an unexported field",
			New("f").Stage("a", takesMixed).With("Name", Literal("x")).With("hidden", Literal(1)),
			[]string{`flow "f": stage "a": With: flow.mixedIn has no exported field "hidden"`},
		},
		{
			"a field promoted from an embedded struct",
			New("f").Stage("a", takesEmbed).With("Inner", Literal(Inner{})).With("Name", Literal("x")),
			[]string{`flow "f": stage "a": With: flow.embedIn has no exported field "Name"`},
		},
		{
			"a field bound twice",
			New("f").Stage("a", takesName).With("Name", Literal("x")).With("Name", Literal("y")),
			[]string{`flow "f": stage "a": With: field "Name" is bound twice`},
		},
		{
			"an unbound field",
			New("f").Stage("a", takesName),
			[]string{`flow "f": stage "a": field "Name" is not bound`},
		},
		{
			"the zero Binding",
			New("f").Stage("a", takesName).With("Name", Binding{}),
			[]string{`flow "f": stage "a": field "Name": the binding is not made by FromInput, FromStage or Literal`},
		},
		{
			"an undeclared stage",
			New("f").Stage("a", takesName).With("Name", FromStage("ghost", "Name")),
			[]string{`flow "f": stage "a": field "Name": stage "ghost" is not declared before this one`},
		},
		{
			"a later stage",
			New("f").Stage("a", takesName).With("Name", FromStage("src", "Name")).Stage("src", source1),
			[]string{`flow "f": stage "a": field "Name": stage "src" is not declared before this one`},
		},
		{
			"the stage itself",
			New("f").Stage("a", takesName).With("Name", FromStage("a", "Name")),
			[]string{`flow "f": stage "a": field "Name": stage "a" is not declared before this one`},
		},
		{
			"a stage that is not a stage function",
			New("f").Stage("bad", 1).Stage("a", takesName).With("Name", FromStage("bad", "Name")),
			[]string{
				`flow "f": stage "bad": int is not a function`,
				`flow "f": stage "a": field "Name": stage "bad" is not a stage function`,
			},
		},
		{
			"a field the source's Out does not have",
			New("f").Stage("src", source1).Stage("a", takesName).With("Name", FromStage("src", "Nope")),
			[]string{`flow "f": stage "a": field "Name": stage "src"'s flow.srcOut has no exported field "Nope"`},
		},
		{
			"an unexported field of the source's Out",
			New("f").Stage("src", source1).Stage("a", takesName).With("Name", FromStage("src", "private")),
			[]string{`flow "f": stage "a": field "Name": stage "src"'s flow.srcOut has no exported field "private"`},
		},
		{
			"a field promoted in the source's Out",
			New("f").Stage("src", givesEmbed).Stage("a", takesName).With("Name", FromStage("src", "Name")),
			[]string{`flow "f": stage "a": field "Name": stage "src"'s flow.embedIn has no exported field "Name"`},
		},
		{
			"a source field of another type",
			New("f").Stage("src", source1).Stage("a", takesName).With("Name", FromStage("src", "Count")),
			[]string{`flow "f": stage "a": field "Name": "Count" of stage "src" is int, which is not assignable to string`},
		},
		{
			"a literal of another type",
			New("f").Stage("a", takesName).With("Name", Literal(1)),
			[]string{`flow "f": stage "a": field "Name": a literal int is not assignable to string`},
		},
		{
			"a nil literal for a type that cannot be nil",
			New("f").Stage("a", takesName).With("Name", Literal(nil)),
			[]string{`flow "f": stage "a": field "Name": a nil literal is not assignable to string`},
		},
		{
			"an input feeding two types",
			New("f").Stage("a", takesName).With("Name", FromInput("k")).
				Stage("b", takesCount).With("Count", FromInput("k")),
			[]string{`flow "f": stage "b": field "Count": input "k" is string elsewhere in the flow, not int`},
		},
		{
			"an input feeding a field and a condition",
			New("f").Stage("a", takesName).With("Name", FromInput("k")).When(FromInput("k")),
			[]string{`flow "f": stage "a": field "Name": input "k" is bool elsewhere in the flow, not string`},
		},
		{
			"a When bound to a string",
			New("f").Stage("src", source1).Stage("a", nothing).When(FromStage("src", "Name")),
			[]string{`flow "f": stage "a": When: "Name" of stage "src" is string, which is not assignable to bool`},
		},
		{
			"an Unless bound to a named bool",
			New("f").Stage("src", source1).Stage("a", nothing).Unless(FromStage("src", "Named")),
			[]string{`flow "f": stage "a": Unless: "Named" of stage "src" is flow.namedBool, which is not assignable to bool`},
		},
		{
			"a When bound to a literal string",
			New("f").Stage("a", nothing).When(Literal("yes")),
			[]string{`flow "f": stage "a": When: a literal string is not assignable to bool`},
		},
		{
			"an Unless bound to a nil literal",
			New("f").Stage("a", nothing).Unless(Literal(nil)),
			[]string{`flow "f": stage "a": Unless: a nil literal is not assignable to bool`},
		},
		{
			"a condition on a stage that is not a stage function",
			New("f").Stage("a", 1).When(FromStage("ghost", "Ready")),
			[]string{
				`flow "f": stage "a": When: stage "ghost" is not declared before this one`,
				`flow "f": stage "a": int is not a function`,
			},
		},
		{
			"every error at once, in declaration order",
			New("f").OnError(FailFlow).
				Stage("a", takesName).With("Nope", Literal(1)).
				Stage("a", takesCount).With("Count", Literal("x")),
			[]string{
				`flow "f": OnError is called before any Stage`,
				`flow "f": stage "a": With: flow.nameIn has no exported field "Nope"`,
				`flow "f": stage "a": field "Name" is not bound`,
				`flow "f": stage "a": the name is declared twice`,
				`flow "f": stage "a": field "Count": a literal string is not assignable to int`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := tt.build.Build()
			if f != nil {
				t.Errorf("Build returned a flow alongside its error")
			}
			assertError(t, err, strings.Join(tt.want, "\n"))
		})
	}
}

func TestBuildAcceptsEveryWellTypedBinding(t *testing.T) {
	tests := []struct {
		name  string
		build *Builder
	}{
		{"no stages", New("f")},
		{"a stage that reads nothing", New("f").Stage("a", nothing)},
		{"an unexported field left unbound", New("f").Stage("a", takesMixed).With("Name", Literal("x"))},
		{"an embedded struct bound as a whole", New("f").Stage("a", takesEmbed).With("Inner", Literal(Inner{Name: "x"}))},
		{"a source field of the same type", New("f").Stage("src", source1).Stage("a", takesName).With("Name", FromStage("src", "Name"))},
		{"a source field assignable to an interface", New("f").Stage("src", source1).Stage("a", takesReader).With("R", FromStage("src", "Buf"))},
		{"a literal assignable to an interface", New("f").Stage("a", takesReader).With("R", Literal(&bytes.Buffer{}))},
		{"a nil literal for a pointer", New("f").Stage("a", takesPtr).With("P", Literal(nil))},
		{"a nil literal for an interface", New("f").Stage("a", takesReader).With("R", Literal(nil))},
		{"one input feeding two fields of one type", New("f").
			Stage("a", takesName).With("Name", FromInput("k")).
			Stage("b", takesName).With("Name", FromInput("k"))},
		{"every condition kind", New("f").Stage("src", source1).Stage("a", nothing).
			When(FromStage("src", "Ready")).Unless(FromInput("skip")).When(Literal(true)).Unless(Literal(false))},
		{"each policy", New("f").
			Stage("a", nothing).OnError(FailFlow).
			Stage("b", nothing).OnError(RecordAndContinue).
			Stage("c", nothing).OnError(BestEffort)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := tt.build.Build()
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if f == nil {
				t.Fatal("Build returned no flow and no error")
			}
		})
	}
}

func TestFlowReportsItsNameAndTheInputsItRequires(t *testing.T) {
	f := mustBuild(t, New("clearance").
		Stage("a", takesName).With("Name", FromInput("name")).When(FromInput("go")).
		Stage("b", takesReader).With("R", FromInput("reader")).
		Stage("c", takesName).With("Name", FromInput("name")))
	if got := f.Name(); got != "clearance" {
		t.Errorf("Name = %q, want clearance", got)
	}
	want := map[string]reflect.Type{
		"name":   reflect.TypeFor[string](),
		"go":     reflect.TypeFor[bool](),
		"reader": reflect.TypeFor[io.Reader](),
	}
	got := f.Inputs()
	if !maps.Equal(got, want) {
		t.Errorf("Inputs = %v, want %v", got, want)
	}
	delete(got, "name")
	if _, ok := f.Inputs()["name"]; !ok {
		t.Error("changing the map Inputs returned changed the flow's own")
	}
}

func TestNillable(t *testing.T) {
	tests := []struct {
		t    reflect.Type
		want bool
	}{
		{reflect.TypeFor[chan int](), true},
		{reflect.TypeFor[func()](), true},
		{reflect.TypeFor[io.Reader](), true},
		{reflect.TypeFor[map[string]int](), true},
		{reflect.TypeFor[*int](), true},
		{reflect.TypeFor[[]int](), true},
		{reflect.TypeFor[int](), false},
		{reflect.TypeFor[string](), false},
		{reflect.TypeFor[none](), false},
		{reflect.TypeFor[[1]int](), false},
	}
	for _, tt := range tests {
		if got := nillable(tt.t); got != tt.want {
			t.Errorf("nillable(%s) = %t, want %t", tt.t, got, tt.want)
		}
	}
}

func TestPolicyString(t *testing.T) {
	tests := map[Policy]string{
		FailFlow:          "fail-flow",
		RecordAndContinue: "record-and-continue",
		BestEffort:        "best-effort",
		Policy(7):         "Policy(7)",
	}
	for p, want := range tests {
		if got := p.String(); got != want {
			t.Errorf("Policy(%d).String() = %q, want %q", int(p), got, want)
		}
	}
}

func mustBuild(t *testing.T, b *Builder) *Flow {
	t.Helper()
	f, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return f
}

func assertError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %q", want)
	}
	if got := err.Error(); got != want {
		t.Errorf("err =\n%s\nwant\n%s", got, want)
	}
}
