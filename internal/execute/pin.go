// -------------------------------------------------------------------------------
// Pin - Move The Version The Fleet Converges Toward
//
// Author: Alex Freidah
//
// Writing the pin is what makes the upgrade real: every later converge installs
// the new binary because the data bag says so. It is written once, centrally,
// before any host is touched, so the hosts differ only in when they are
// converged and never in what they converge toward.
//
// The pin as it stood is read first and handed back as the compensation, so a
// run that fails before the first host is upgraded leaves the fleet aimed where
// it was.
// -------------------------------------------------------------------------------

package execute

import (
	"context"
	"errors"
	"fmt"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
)

// pin sets the version pin for the tool the run upgrades.
func pin(deps *Deps) runner.Step {
	return func(ctx context.Context, task plan.Task) (runner.Result, error) {
		tool, err := arg(task, "tool")
		if err != nil {
			return runner.Result{}, err
		}
		version, err := arg(task, "version")
		if err != nil {
			return runner.Result{}, err
		}

		was, err := deps.Versions.Pin(ctx, tool)
		if err != nil {
			return runner.Result{}, fmt.Errorf("reading the current pin: %w", err)
		}
		if was == version {
			return runner.Result{Outcome: plan.Unnecessary}, nil
		}

		if err := deps.Versions.SetPin(ctx, tool, version); err != nil {
			return runner.Result{}, err
		}

		// Putting back an absent pin means removing the item, not writing an
		// empty version: nothing reads an item with no version and a converge
		// against one fails, so the two are different states. Leaving the pin
		// at the new version would be worse still -- the timers come back on
		// as the run unwinds, and every host would then converge to it
		// unattended, which is the thing freezing before pinning prevents.
		return runner.Result{
			Undo: &runner.Compensation{
				Label: restoring(tool, was),
				Undo: func(ctx context.Context) error {
					if was == "" {
						return deps.Versions.ClearPin(ctx, tool)
					}
					return deps.Versions.SetPin(ctx, tool, was)
				},
			},
		}, nil
	}
}

// restoring is what the operator is told is being unwound. A pin that was not
// set before the run is put back by removal, and "restoring the pin to
// nothing" reads as a defect rather than as the intent.
func restoring(tool, was string) string {
	if was == "" {
		return fmt.Sprintf("removing the %s pin, which was not set before the run", tool)
	}
	return fmt.Sprintf("restoring the %s pin to %s", tool, was)
}

// arg reads a string argument a task's action declares.
//
// A missing argument is the run file disagreeing with the code that built it,
// which is a defect rather than a cluster condition, so it stops the run.
func arg(task plan.Task, key string) (string, error) {
	v, ok := task.Action.Args[key].(string)
	if !ok || v == "" {
		return "", errors.New("task " + task.ID + " declares no " + key)
	}
	return v, nil
}
