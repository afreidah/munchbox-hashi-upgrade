// -------------------------------------------------------------------------------
// Handoff - Move Coordination Before Restarting The Host That Holds It
//
// Author: Alex Freidah
//
// The coordinating server is upgraded last, and this is what empties it first.
// Restarting it without handing over works -- the cluster elects another -- but
// then the election happens because an agent vanished, during the window the
// run is least able to observe it.
//
// The destination is chosen here rather than recorded in the run file. By the
// time this runs every other server is already on the new version, so the
// voters are interchangeable and a choice made now is made against current
// health instead of a survey taken before the run started.
// -------------------------------------------------------------------------------

package execute

import (
	"context"
	"errors"
	"fmt"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
)

// handoff transfers coordination away from the host the task names.
func handoff(deps *Deps) runner.Step {
	return func(ctx context.Context, task plan.Task) (runner.Result, error) {
		from, err := arg(task, "from")
		if err != nil {
			return runner.Result{}, err
		}

		cluster, err := deps.Survey.Health(ctx)
		if err != nil {
			return runner.Result{}, err
		}

		// Coordination may have moved on its own since the run was planned, by
		// an election this run had nothing to do with. The host is empty either
		// way, which is all the next task needs.
		primary, ok := cluster.Primary()
		if !ok || primary.Name != from {
			return runner.Result{Outcome: plan.Unnecessary}, nil
		}

		to, err := successor(cluster, from)
		if err != nil {
			return runner.Result{}, err
		}

		if err := deps.Coordination.Handoff(ctx, to.ID); err != nil {
			return runner.Result{}, err
		}

		// The transfer returns on acceptance, not on completion, so without
		// this the run would restart the old leader while it still held the
		// term.
		if err := deps.Wait.Coordination(ctx, from); err != nil {
			return runner.Result{}, err
		}

		return runner.Result{}, nil
	}
}

// successor picks the voter coordination is handed to.
//
// Ordered by name rather than by whatever order the cluster reported, so the
// same cluster hands over to the same host twice and a run that is replayed
// reads alike. Any healthy voter is a valid destination; determinism is the
// only thing being bought here.
func successor(cluster plan.Cluster, from string) (plan.Member, error) {
	var unfit []string

	for _, m := range cluster.OfKind(plan.KindServer) {
		if m.Name == from {
			continue
		}
		if m.Voter && m.Healthy {
			return m, nil
		}
		unfit = append(unfit, m.Name)
	}

	if len(unfit) == 0 {
		return plan.Member{}, errors.New("no other server can take coordination")
	}
	return plan.Member{}, fmt.Errorf(
		"no healthy voter can take coordination from %s; unfit: %v", from, unfit)
}
