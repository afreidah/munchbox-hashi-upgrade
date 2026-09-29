// -------------------------------------------------------------------------------
// Upgrade - Converge One Host And Wait For The Cluster To Take It Back
//
// Author: Alex Freidah
//
// The converge installs the pinned binary and restarts the agent; the gate is
// what makes the run safe to continue. A host is not upgraded when its converge
// exits zero, it is upgraded when the cluster says it is back, which is why the
// wait is against the cluster's own verdict rather than a sleep.
//
// Which gate depends on what the host is. A server has to rejoin as a voter
// before the next server is touched, or the run spends fault tolerance it has
// not got back. A client only has to return to service.
// -------------------------------------------------------------------------------

package execute

import (
	"context"
	"fmt"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
)

// upgrade converges one host and waits for the cluster to absorb it.
func upgrade(deps *Deps) runner.Step {
	return func(ctx context.Context, task plan.Task) (runner.Result, error) {
		name, err := arg(task, "member")
		if err != nil {
			return runner.Result{}, err
		}

		// The survey carries the kind and the version; the task carries only a
		// name, because a task is a thing to do and not a copy of the fleet.
		member, ok := find(deps.Run.Cluster, name)
		if !ok {
			return runner.Result{}, fmt.Errorf("the survey has no member %q", name)
		}

		// A host already at the target is left alone rather than restarted for
		// the sake of uniformity. This is what makes a resumed run cheap: the
		// hosts it already did cost one comparison each.
		want := deps.Run.Spec.To
		if member.Version == want {
			return runner.Result{Outcome: plan.Unnecessary}, nil
		}

		target, err := deps.Target(name)
		if err != nil {
			return runner.Result{}, err
		}

		// Stamped before the converge, not after. The server gate compares the
		// cluster's stability clock against this instant to tell a host that has
		// come back from one that has not gone down yet; taking it afterwards
		// would accept the pre-restart verdict as proof of the restart.
		restarted := time.Now().UTC()

		if _, err := deps.Fleet.Converge(ctx, target, deps.Out); err != nil {
			return runner.Result{}, err
		}

		// A server rejoining the quorum is a stronger condition than a client
		// returning to service, and the run cannot touch the next server until
		// this one is a voter again.
		if member.Kind == plan.KindServer {
			err = deps.Wait.Server(ctx, name, want, restarted)
		} else {
			err = deps.Wait.Client(ctx, name, want)
		}
		if err != nil {
			return runner.Result{}, err
		}

		// No compensation: the host is running the new binary and putting the
		// old one back is a downgrade, which this tool does not do.
		return runner.Result{}, nil
	}
}

// find returns a member of the survey by name.
func find(cluster plan.Cluster, name string) (plan.Member, bool) {
	for _, m := range cluster.Members {
		if m.Name == name {
			return m, true
		}
	}
	return plan.Member{}, false
}
