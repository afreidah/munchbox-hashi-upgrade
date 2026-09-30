// -------------------------------------------------------------------------------
// Thaw - Give The Fleet Its Scheduled Converges Back
//
// Author: Alex Freidah
//
// The closing half of the freeze. It runs as the last task of a run that
// reached the end, where the freeze's compensation covers a run that did not.
// Both paths call the same fleet operation, which is idempotent, so a run
// whose compensation already fired and which is then resumed does not care
// which of the two released the timers.
// -------------------------------------------------------------------------------

package execute

import (
	"context"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
)

// thaw restarts the converge timers fleet-wide.
func thaw(deps *Deps) runner.Step {
	return func(ctx context.Context, _ plan.Task) (runner.Result, error) {
		if err := deps.Fleet.Thaw(ctx, deps.Hosts); err != nil {
			return runner.Result{}, err
		}
		return runner.Result{}, nil
	}
}
