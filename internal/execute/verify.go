// -------------------------------------------------------------------------------
// Verify - Prove The Cluster Ended Up Where The Run Aimed It
//
// Author: Alex Freidah
//
// The per-host gates each proved one host came back, which is not the same as
// the fleet having arrived: a host the survey missed, or one whose converge was
// a no-op because its pin never reached it, passes every gate and still runs
// the old binary. This reads the cluster once more, at the end, and holds the
// whole membership against the version the run was for.
//
// It only reads, so it is the one step a dry run performs for real.
// -------------------------------------------------------------------------------

package execute

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
)

// verify confirms every member reports the target version and the cluster is
// healthy.
func verify(deps *Deps) runner.Step {
	return func(ctx context.Context, task plan.Task) (runner.Result, error) {
		want, err := arg(task, "version")
		if err != nil {
			return runner.Result{}, err
		}

		// Read live rather than reusing the survey in the run file. The survey
		// is how the cluster looked before any of this happened.
		cluster, err := deps.Survey.Health(ctx)
		if err != nil {
			return runner.Result{}, err
		}

		if behind := laggards(cluster, want); len(behind) > 0 {
			return runner.Result{}, fmt.Errorf(
				"not on %s: %s", want, strings.Join(behind, ", "))
		}

		// Checked after the versions, so a fleet that is fully upgraded but
		// still settling reports the settling rather than a version problem it
		// does not have.
		if !cluster.Healthy {
			return runner.Result{}, fmt.Errorf("every member is on %s but the cluster is not healthy", want)
		}

		return runner.Result{}, nil
	}
}

// laggards names the members not reporting want, sorted so the same fleet
// reports the same way twice.
func laggards(cluster plan.Cluster, want string) []string {
	var out []string
	for _, m := range cluster.Members {
		if m.Version != want {
			out = append(out, fmt.Sprintf("%s=%s", m.Name, m.Version))
		}
	}
	sort.Strings(out)
	return out
}
