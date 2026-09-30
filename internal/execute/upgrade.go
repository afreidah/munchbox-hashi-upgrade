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

// How long a host is given to shed its work before the remaining allocations
// are told to stop. Long enough for a slow container to exit cleanly, short
// enough that one stuck host does not hold a fleet-wide run open all night.
const drainDeadline = 10 * time.Minute

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

		// Draining is off by default and only ever applies to a host that
		// carries work. A server's allocations are not the reason it is being
		// restarted, and marking one ineligible does nothing for the quorum.
		var undo *runner.Compensation
		if deps.Run.Spec.Drain && member.Kind == plan.KindClient {
			if undo, err = drain(ctx, deps, member); err != nil {
				return runner.Result{}, err
			}
		}

		// Labelled with the host, because the converge's own output does not
		// say which one it came from and several in a row otherwise read as
		// one host repeating itself.
		out := withPrefix(deps.Out, name)
		_, err = deps.Fleet.Converge(ctx, target, out)
		if flusher, ok := out.(interface{ Flush() error }); ok {
			_ = flusher.Flush()
		}
		if err != nil {
			return runner.Result{}, err
		}

		// Before the gate, not after: the client gate requires the host to be
		// eligible for work, which a drained host is not. Leaving it until
		// afterwards would wait out a condition this step is holding shut.
		if undo != nil {
			if err := deps.Drains.Undrain(ctx, member.ID); err != nil {
				return runner.Result{}, err
			}
		}

		// A server rejoining the quorum is a stronger condition than a client
		// returning to service, and the run cannot touch the next server until
		// this one is a voter again.
		if member.Kind == plan.KindServer {
			err = deps.Wait.Server(ctx, name, want)
		} else {
			err = deps.Wait.Client(ctx, name, want)
		}
		if err != nil {
			return runner.Result{}, err
		}

		// The compensation covers only the drain. The host is running the new
		// binary and putting the old one back is a downgrade, which this tool
		// does not do.
		return runner.Result{Undo: undo}, nil
	}
}

// drain empties a host and returns the compensation that puts it back.
//
// The deadline is the run's, not the host's: a drain that cannot finish is a
// reason to stop the run rather than to wait indefinitely on one host.
func drain(ctx context.Context, deps *Deps, member plan.Member) (*runner.Compensation, error) {
	if err := deps.Drains.Drain(ctx, member.ID, drainDeadline); err != nil {
		return nil, err
	}

	return &runner.Compensation{
		Label: fmt.Sprintf("making %s eligible for work again", member.Name),
		Undo: func(ctx context.Context) error {
			return deps.Drains.Undrain(ctx, member.ID)
		},
	}, nil
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
