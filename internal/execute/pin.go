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
// The pin is the run's intent rather than a reversible side effect, so a run
// that fails leaves it where it put it. Converges are frozen for the whole run,
// so a pin nothing is reading harms nothing; rolling it back does harm, in two
// ways. A fleet half-converged onto the new version and pinned to the old one
// converges backwards the moment the timers return, and a resume skips this
// step as already done -- so every host after it installs the old version and
// waits at a gate for a version that is no longer coming.
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

		return runner.Result{}, nil
	}
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
