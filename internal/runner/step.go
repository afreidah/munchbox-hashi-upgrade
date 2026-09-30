// -------------------------------------------------------------------------------
// Steps - What A Task Is Carried Out By
//
// Author: Alex Freidah
//
// The types the caller supplies: a step per command a task can name, and the
// prompt a task marked for confirmation is put through.
//
// A step reports an outcome rather than only an error, because a task that did
// not need doing and a task that was done are different things to resume from.
// It may also hand back a compensation, which the runner stacks and unwinds if
// a later task fails.
// -------------------------------------------------------------------------------

package runner

import (
	"context"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// Step carries out one task.
type Step func(ctx context.Context, task plan.Task) (Result, error)

// Result is what a step did.
//
// An empty Outcome is taken as Succeeded, so a step that has nothing to say
// beyond returning no error says nothing.
type Result struct {
	Outcome plan.Outcome
	Undo    *Compensation
}

// Compensation undoes the orchestration state a step switched on.
//
// Label is what the operator is told is being unwound, so a run that fails at
// 3am reports releasing the converge timers rather than a line number.
type Compensation struct {
	Label string
	Undo  func(context.Context) error
}

// Confirmer puts a task to the operator. Returning false stops the run, and an
// error means the question could not be asked.
type Confirmer interface {
	Confirm(ctx context.Context, task plan.Task) (bool, error)
}

// ConfirmerFunc adapts a function to Confirmer.
type ConfirmerFunc func(ctx context.Context, task plan.Task) (bool, error)

// Confirm calls f.
func (f ConfirmerFunc) Confirm(ctx context.Context, task plan.Task) (bool, error) {
	return f(ctx, task)
}

// Always is a Confirmer that assents, for a run the operator has already said
// to carry out without stopping.
func Always() Confirmer {
	return ConfirmerFunc(func(context.Context, plan.Task) (bool, error) { return true, nil })
}
