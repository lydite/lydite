package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// OutcomePolicy is what a component's error does to the Flow. A component
// declares it on its Result; the engine never infers it from the error.
type OutcomePolicy int

const (
	// policyUnset is the zero value. A Result that declares no policy, or one
	// outside the three below, fails the Flow, error or not: an undeclared
	// policy is a component bug, and reading it as anything more lenient would
	// let the bug pass silently.
	policyUnset OutcomePolicy = iota
	// FailFlow aborts the Flow after the current Stage's join when the
	// component errors.
	FailFlow
	// RecordAndContinue records the component's error onto the Context and
	// lets the Flow proceed.
	RecordAndContinue
	// BestEffort only logs the component's error. It is never recorded onto
	// the Context and never aborts the Flow.
	BestEffort
)

func (p OutcomePolicy) String() string {
	switch p {
	case FailFlow:
		return "fail-flow"
	case RecordAndContinue:
		return "record-and-continue"
	case BestEffort:
		return "best-effort"
	default:
		return fmt.Sprintf("undeclared(%d)", int(p))
	}
}

func (p OutcomePolicy) declared() bool {
	return p == FailFlow || p == RecordAndContinue || p == BestEffort
}

// Result is one component's isolated outcome. Its Writes reach the Context
// only in the Stage's join, and only when the component returned no error.
type Result struct {
	Policy OutcomePolicy
	Writes []Write
}

// StageComponent is one unit of work in a Stage.
//
// Run is called concurrently with every sibling in its Stage. It reads through
// in and must not reach the Context any other way; everything it contributes
// goes back through its Result.
type StageComponent interface {
	Name() string
	Run(ctx context.Context, in View) (Result, error)
}

// Stage is a set of components run in parallel and then joined.
type Stage struct {
	Name       string
	Components []StageComponent
}

// Flow is a strictly sequential list of Stages.
type Flow struct {
	Stages []Stage
	// Log receives BestEffort components' errors. Nil discards them.
	Log io.Writer
}

// Sink consumes a Flow's final state.
type Sink interface {
	Write(ctx context.Context, final View) error
}

// ComponentError is an error a component returned, named by where it ran.
type ComponentError struct {
	Stage     string
	Component string
	Err       error
}

func (e *ComponentError) Error() string {
	return e.Stage + "/" + e.Component + ": " + e.Err.Error()
}

func (e *ComponentError) Unwrap() error {
	return e.Err
}

// Run runs every Stage in order against c. It stops after the join of the
// first Stage in which a FailFlow component errored or a component declared
// no policy, returning those components' errors joined, and never starts the
// Stage after it. It also stops, before starting a Stage, once ctx is done.
func (f Flow) Run(ctx context.Context, c *Context) error {
	log := f.Log
	if log == nil {
		log = io.Discard
	}
	for _, s := range f.Stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.run(ctx, c, log); err != nil {
			return err
		}
	}
	return nil
}

type outcome struct {
	result Result
	err    error
}

// run starts every component at once and waits for all of them, whatever any
// of them returns: no sibling's error cancels another, because a Stage's
// components are independent by construction and each one's outcome is worth
// having whether or not the Flow goes on.
//
// The join happens only after the wait, on this goroutine, in declaration
// order. Before the wait nothing writes to c, so concurrent reads through a
// View are safe; after it, completion order has no effect on the Context —
// two siblings writing one key resolve to the later-declared one's value on
// every run.
func (s Stage) run(ctx context.Context, c *Context, log io.Writer) error {
	outcomes := make([]outcome, len(s.Components))
	in := c.View()
	var wg sync.WaitGroup
	for i, comp := range s.Components {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := comp.Run(ctx, in)
			outcomes[i] = outcome{result: r, err: err}
		}()
	}
	wg.Wait()

	var failed []error
	for i, o := range outcomes {
		name := s.Components[i].Name()
		if !o.result.Policy.declared() {
			err := fmt.Errorf("declares no outcome policy (%v)", o.result.Policy)
			if o.err != nil {
				err = fmt.Errorf("%w: %w", err, o.err)
			}
			failed = append(failed, &ComponentError{Stage: s.Name, Component: name, Err: err})
			continue
		}
		if o.err == nil {
			for _, w := range o.result.Writes {
				w.apply(c)
			}
			continue
		}
		ce := &ComponentError{Stage: s.Name, Component: name, Err: o.err}
		switch o.result.Policy {
		case FailFlow:
			failed = append(failed, ce)
		case RecordAndContinue:
			c.errors = append(c.errors, ce)
		case BestEffort:
			// A log that cannot be written to is not a reason to abort a
			// Flow this policy promises never to abort.
			_, _ = fmt.Fprintf(log, "flow: %v\n", ce)
		}
	}
	return errors.Join(failed...)
}
