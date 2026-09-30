// -------------------------------------------------------------------------------
// Freeze - Hold The Scheduled Converges Off The Fleet
//
// Author: Alex Freidah
//
// A scheduled converge landing mid-upgrade would install the new pin on a host
// the run has not reached, out of order and unobserved. Freezing stops the
// timers across every host before the pin is written, and the compensation
// releases them again, so a run that fails partway does not leave the fleet
// with its automation switched off.
// -------------------------------------------------------------------------------

package execute

import (
	"context"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
)

// freeze stops the converge timers fleet-wide.
func freeze(deps *Deps) runner.Step {
	return func(ctx context.Context, _ plan.Task) (runner.Result, error) {
		if err := deps.Fleet.Freeze(ctx, deps.Hosts); err != nil {
			return runner.Result{}, err
		}

		return runner.Result{
			Undo: &runner.Compensation{
				Label: "releasing the converge timers",
				Undo: func(ctx context.Context) error {
					return deps.Fleet.Thaw(ctx, deps.Hosts)
				},
			},
		}, nil
	}
}
