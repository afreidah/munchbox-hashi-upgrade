// -------------------------------------------------------------------------------
// Failure Tests - Stopping, Unwinding, Retrying By Hand
//
// Author: Alex Freidah
//
// A failure is the path this package exists for. What is asserted is that the
// failure is recorded, the compensations already stacked are unwound innermost
// first, and the task can be driven again on its own afterwards.
// -------------------------------------------------------------------------------

package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// stacking returns a step that succeeds and leaves a compensation behind,
// appending its label to unwound when that compensation runs.
func stacking(label string, unwound *[]string, undoErr error) Step {
	return func(context.Context, plan.Task) (Result, error) {
		return Result{Undo: &Compensation{
			Label: label,
			Undo: func(context.Context) error {
				*unwound = append(*unwound, label)
				return undoErr
			},
		}}, nil
	}
}

// failing returns a step that reports err.
func failing(err error) Step {
	return func(context.Context, plan.Task) (Result, error) { return Result{}, err }
}

func TestFailure(t *testing.T) {
	t.Run("records the failure and stops", func(t *testing.T) {
		boom := errors.New("converge exited 1")
		steps := every(new([]string))
		steps["upgrade-member"] = failing(boom)

		r := run(t)
		runner, _ := build(t, r, steps, nil)

		err := runner.Apply(t.Context())
		if !errors.Is(err, boom) {
			t.Fatalf("Apply error = %v, want %v", err, boom)
		}
		if got := r.Outcome("upgrade-server-a"); got != plan.Failed {
			t.Errorf("upgrade-server-a = %s, want %s", got, plan.Failed)
		}
		if got := r.Outcome("handoff"); got != plan.Waiting {
			t.Errorf("handoff = %s, want the run stopped before it", got)
		}
	})

	// The reason has to survive the process, or an operator returning to a run
	// that failed overnight has an outcome and no cause.
	t.Run("writes the reason to the file", func(t *testing.T) {
		steps := every(new([]string))
		steps["upgrade-member"] = failing(errors.New("converge exited 1"))

		r := run(t)
		runner, _ := build(t, r, steps, nil)
		_ = runner.Apply(t.Context())

		reopened, err := plan.Open(r.Path())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got := reopened.Progress["upgrade-server-a"].Err; !strings.Contains(got, "converge exited 1") {
			t.Errorf("recorded error = %q, want the cause", got)
		}
	})

	// Innermost first: the timers were stopped before the host was drained, so
	// the host is undrained before the timers are released.
	t.Run("unwinds compensations in reverse", func(t *testing.T) {
		var unwound []string
		steps := map[string]Step{
			"freeze-converge":       stacking("release the converge timers", &unwound, nil),
			"upgrade-member":        stacking("make server-a eligible again", &unwound, nil),
			"hand-off-coordination": failing(errors.New("no other voter")),
		}

		runner, out := build(t, run(t), steps, nil)
		if err := runner.Apply(t.Context()); err == nil {
			t.Fatal("expected the failure to be reported")
		}

		want := "make server-a eligible again,release the converge timers"
		if strings.Join(unwound, ",") != want {
			t.Errorf("unwound %v, want %s", unwound, want)
		}
		if !strings.Contains(out.String(), "unwinding: release the converge timers") {
			t.Errorf("output %q does not name what it unwound", out)
		}
	})

	// A compensation that fails is reported and the rest still run, because what
	// they restore is the fleet's own automation.
	t.Run("keeps unwinding after a compensation fails", func(t *testing.T) {
		var unwound []string
		steps := map[string]Step{
			"freeze-converge":       stacking("release the converge timers", &unwound, nil),
			"upgrade-member":        stacking("make server-a eligible again", &unwound, errors.New("node gone")),
			"hand-off-coordination": failing(errors.New("no other voter")),
		}

		runner, out := build(t, run(t), steps, nil)
		_ = runner.Apply(t.Context())

		if len(unwound) != 2 {
			t.Errorf("unwound %v, want both attempted", unwound)
		}
		if !strings.Contains(out.String(), "could not unwind make server-a eligible again") {
			t.Errorf("output %q does not report the failed compensation", out)
		}
	})

	// Nothing is unwound for a run that fails on its first task, and a task that
	// did not succeed leaves no compensation behind.
	t.Run("unwinds nothing when the first task fails", func(t *testing.T) {
		var unwound []string
		steps := every(new([]string))
		steps["freeze-converge"] = failing(errors.New("timer refused"))
		steps["upgrade-member"] = stacking("never stacked", &unwound, nil)

		runner, out := build(t, run(t), steps, nil)
		_ = runner.Apply(t.Context())

		if len(unwound) != 0 {
			t.Errorf("unwound %v, want nothing", unwound)
		}
		if strings.Contains(out.String(), "unwinding") {
			t.Errorf("output %q unwound something", out)
		}
	})

	// Compensations unwind on a declined confirmation too: the operator stopping
	// the run leaves the same automation switched off as a failure does.
	t.Run("unwinds when the operator declines", func(t *testing.T) {
		var unwound []string
		steps := every(new([]string))
		steps["freeze-converge"] = stacking("release the converge timers", &unwound, nil)

		decline := ConfirmerFunc(func(context.Context, plan.Task) (bool, error) { return false, nil })
		runner, _ := build(t, run(t), steps, decline)
		_ = runner.Apply(t.Context())

		if strings.Join(unwound, ",") != "release the converge timers" {
			t.Errorf("unwound %v, want the timers released", unwound)
		}
	})
}

func TestTask(t *testing.T) {
	// How a task that failed overnight is retried: on its own, after the host
	// has been looked at.
	t.Run("runs one task the loop would refuse to reach", func(t *testing.T) {
		var ran []string
		r := run(t)
		r.Settle("upgrade-server-a", plan.Failed, at, errors.New("boom"))

		runner, _ := build(t, r, every(&ran), nil)
		if err := runner.Task(t.Context(), "upgrade-server-a"); err != nil {
			t.Fatalf("Task: %v", err)
		}
		if got := r.Outcome("upgrade-server-a"); got != plan.Succeeded {
			t.Errorf("upgrade-server-a = %s, want the retry recorded", got)
		}

		// And the loop now gets past it.
		if err := runner.Apply(t.Context()); err != nil {
			t.Errorf("Apply after the retry: %v", err)
		}
	})

	t.Run("reports an id that is not in the run", func(t *testing.T) {
		runner, _ := build(t, run(t), every(new([]string)), nil)

		err := runner.Task(t.Context(), "upgrade-nothing")
		if err == nil {
			t.Fatal("expected an error for an unknown task")
		}
		if !strings.Contains(err.Error(), "upgrade-nothing") {
			t.Errorf("error %q does not name the id", err)
		}
	})
}
