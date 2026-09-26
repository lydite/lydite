// Package flow runs a fixed two-level orchestration: a Flow is a strictly
// sequential list of Stages, and a Stage is a set of StageComponents that run
// in parallel and are then joined into a shared Context one at a time.
//
// There is no dependency graph and none is to be grown here. Ordering is
// expressed by which Stage a component is declared in, and nothing else;
// component-level concurrency with physical constraints is internal/scheduler's
// concern, not this package's.
package flow

// Key names one typed value held by a Context.
//
// Two keys are the same key only when one was copied from the other: identity
// is the NewKey call that made it, never its name, so two packages choosing the
// same name for values of different types cannot read each other's value back
// as the wrong type.
type Key[T any] struct {
	id *keyID
}

type keyID struct {
	name string
}

// NewKey makes a key for values of type T. The name is only for reports.
func NewKey[T any](name string) Key[T] {
	return Key[T]{id: &keyID{name: name}}
}

// Name is the name the key was made with.
func (k Key[T]) Name() string {
	if k.id == nil {
		return ""
	}
	return k.id.name
}

// Context is the state a Flow's stages read from and are joined into.
//
// It is not safe for concurrent mutation. Only the Flow writes to it, and only
// in a Stage's sequential join, after every sibling in that Stage has returned;
// while components run they see it through a View, which has no way to write.
type Context struct {
	values map[*keyID]any
	errors []*ComponentError
}

// NewContext is an empty Context.
func NewContext() *Context {
	return &Context{values: map[*keyID]any{}}
}

// Reader is anything a value can be read from by Get: a *Context or a View.
type Reader interface {
	lookup(id *keyID) (any, bool)
}

func (c *Context) lookup(id *keyID) (any, bool) {
	v, ok := c.values[id]
	return v, ok
}

// Get reads the value k holds, reporting false when nothing was ever set.
func Get[T any](r Reader, k Key[T]) (T, bool) {
	var zero T
	if k.id == nil {
		return zero, false
	}
	v, ok := r.lookup(k.id)
	if !ok {
		return zero, false
	}
	return v.(T), true
}

// Set stores v under k, replacing whatever k held.
func Set[T any](c *Context, k Key[T], v T) {
	mustBeMade(k.id)
	c.values[k.id] = v
}

// Errors are the errors recorded by components whose policy is
// RecordAndContinue, in the order they were joined.
func (c *Context) Errors() []*ComponentError {
	return append([]*ComponentError(nil), c.errors...)
}

// View is read-only access to a Context. It is what a running component is
// given, so a component cannot write to state its siblings are reading.
type View struct {
	c *Context
}

// View is read-only access to c.
func (c *Context) View() View {
	return View{c: c}
}

func (v View) lookup(id *keyID) (any, bool) {
	return v.c.lookup(id)
}

// Errors are the errors recorded onto the Context so far.
func (v View) Errors() []*ComponentError {
	return v.c.Errors()
}

// Write is one value a component's Result asks the join to store.
type Write struct {
	id    *keyID
	value any
}

// Put is a Write storing v under k.
func Put[T any](k Key[T], v T) Write {
	mustBeMade(k.id)
	return Write{id: k.id, value: v}
}

func (w Write) apply(c *Context) {
	c.values[w.id] = w.value
}

// mustBeMade refuses a zero Key. Every zero Key of every type would otherwise
// share one slot, which is exactly the collision NewKey's identity rules out.
func mustBeMade(id *keyID) {
	if id == nil {
		panic("flow: a Key must be made by NewKey")
	}
}
