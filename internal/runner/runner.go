// -------------------------------------------------------------------------------
// Runner - The Loop
//
// Author: Alex Freidah
//
// Reads the next unsettled task, carries it out, records what happened and
// writes the file, until the run is finished or something stops it.
//
// The write after every task is the point. A run that is interrupted, fails, or
// dies with the machine leaves its progress on disk, so resuming is opening the
// same file rather than reasoning about how far it got.
// -------------------------------------------------------------------------------

package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// Why a run stopped short of finishing. Each is a state the operator acts on
// rather than a fault to report, so they are distinguishable.
var (
	ErrInterrupted = errors.New("stopped after the current task")
	ErrDeclined    = errors.New("stopped: confirmation declined")
	ErrUnsettled   = errors.New("a task needs attention before the run can go on")
	ErrNoStep      = errors.New("no step is registered for the task's command")
)

// Options configure a runner. Now and Confirm have defaults, so the usual
// caller supplies a run, its steps and somewhere to write.
type Options struct {
	Run     *plan.Run
	Steps   map[string]Step
	Out     io.Writer
	Confirm Confirmer
	Now     func() time.Time
}

// Runner carries out a run.
type Runner struct {
	run     *plan.Run
	steps   map[string]Step
	out     io.Writer
	confirm Confirmer
	now     func() time.Time

	// Compensations from the tasks that have succeeded, innermost last.
	stack []Compensation
}

// New returns a runner over opts.Run.
func New(opts Options) (*Runner, error) {
	switch {
	case opts.Run == nil:
		return nil, errors.New("run is required")
	case len(opts.Steps) == 0:
		return nil, errors.New("at least one step is required")
	case opts.Out == nil:
		return nil, errors.New("output is required")
	}

	r := &Runner{
		run:     opts.Run,
		steps:   opts.Steps,
		out:     opts.Out,
		confirm: opts.Confirm,
		now:     opts.Now,
	}
	if r.confirm == nil {
		r.confirm = Always()
	}
	if r.now == nil {
		r.now = func() time.Time { return time.Now().UTC() }
	}

	return r, nil
}

// Apply carries out every remaining task, in order.
//
// It returns nil only when the run is finished. Every other return leaves the
// file describing exactly how far it got.
func (r *Runner) Apply(ctx context.Context) error {
	for {
		task, ok := r.run.Next()
		if !ok {
			// Said out loud. A run whose last task simply stops printing leaves
			// the reader deciding for themselves whether it finished or died,
			// and the one thing they came for is that it is done.
			//
			// What it says is read off the record rather than assumed from
			// reaching the end: a rehearsal, and a resumed run with nothing
			// left to do, both finish having changed nothing, and claiming the
			// fleet had moved would be the most misleading line in the tool.
			if did := r.carriedOut(); did == 0 {
				r.sayf("\nnothing was changed\n")
			} else {
				r.sayf("\nevery host is on %s %s\n", r.run.Spec.Tool, r.run.Spec.To)
			}
			return nil
		}

		// A task that failed or was left active is landed on rather than
		// stepped over. Whether it took effect is unknown, and the answer is an
		// operator looking at the host, not a blind retry.
		if outcome := r.run.Outcome(task.ID); outcome == plan.Failed || outcome == plan.Active {
			return fmt.Errorf("%w: %s is %s; check the host, then run again with --reset %s",
				ErrUnsettled, task.ID, outcome, task.ID)
		}

		if err := r.Task(ctx, task.ID); err != nil {
			return err
		}

		// Checked between tasks rather than mid-task: an interrupt stops the run
		// at a boundary the file can describe.
		if ctx.Err() != nil {
			r.sayf("stopped after %s\n", task.ID)
			return ErrInterrupted
		}
	}
}

// Task carries out one task by id, whether or not the run is at it. This is how
// a task that failed overnight is retried without the run around it.
func (r *Runner) Task(ctx context.Context, id string) error {
	task, ok := r.find(id)
	if !ok {
		return fmt.Errorf("no task %q in %s", id, r.run.Path())
	}

	step, ok := r.steps[task.Action.Command]
	if !ok {
		return fmt.Errorf("%w: %s names %q", ErrNoStep, task.ID, task.Action.Command)
	}

	// Announced before it is put to the operator, so a prompt follows the name
	// of what it is about rather than repeating it.
	r.sayf("%s\n", task.Title)

	if task.Confirm != plan.ConfirmNone {
		assented, err := r.confirm.Confirm(ctx, *task)
		if err != nil {
			return fmt.Errorf("confirm %s: %w", task.ID, err)
		}
		if !assented {
			r.unwind(ctx)
			return ErrDeclined
		}
	}

	r.run.Begin(task.ID, r.now())
	if err := r.run.Save(); err != nil {
		return err
	}

	result, err := step(ctx, *task)
	if err != nil {
		r.run.Settle(task.ID, plan.Failed, r.now(), err)
		if saved := r.run.Save(); saved != nil {
			// The failure is what the operator needs; losing the journal as well
			// is the worse problem, so both are reported.
			return errors.Join(err, saved)
		}

		r.unwind(ctx)
		return fmt.Errorf("%s: %w", task.ID, err)
	}

	outcome := result.Outcome
	if outcome == "" {
		outcome = plan.Succeeded
	}

	r.run.Settle(task.ID, outcome, r.now(), nil)
	if err := r.run.Save(); err != nil {
		return err
	}

	if result.Undo != nil {
		r.stack = append(r.stack, *result.Undo)
	}

	return nil
}

// unwind runs the stacked compensations in reverse.
//
// Every one is attempted even after one fails, and none of them stops the
// unwinding: they exist to leave the fleet running its own automation again, and
// stopping halfway would leave it in the state this is undoing.
func (r *Runner) unwind(ctx context.Context) {
	for _, comp := range slices.Backward(r.stack) {

		r.sayf("unwinding: %s\n", comp.Label)

		if err := comp.Undo(ctx); err != nil {
			r.sayf("could not unwind %s: %v\n", comp.Label, err)
		}
	}
	r.stack = nil
}

// find returns the task with this id.
func (r *Runner) find(id string) (*plan.Task, bool) {
	for i := range r.run.Tasks {
		if r.run.Tasks[i].ID == id {
			return &r.run.Tasks[i], true
		}
	}

	return nil, false
}

// sayf writes to the operator. Output that cannot be written is not worth
// failing a run over.
// carriedOut counts the tasks that actually did something.
//
// A task that settled unnecessary did not: the host was already at the target,
// or the run was only rehearsing. Both leave the fleet as they found it, and
// the difference is what the closing line turns on.
func (r *Runner) carriedOut() int {
	var n int
	for _, task := range r.run.Tasks {
		if r.run.Outcome(task.ID) == plan.Succeeded {
			n++
		}
	}
	return n
}

func (r *Runner) sayf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.out, format, args...)
}
