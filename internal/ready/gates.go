// -------------------------------------------------------------------------------
// Assertions - Server, Client, Barrier
//
// Author: Alex Freidah
//
// The three gates:
//
//   Server   one coordinating host: right version, healthy, a voter again, and
//            its health verdict formed after the restart rather than before it.
//   Client   one host that carries work: right version, ready, eligible.
//   Barrier  the cluster: healthy, every server a voter, and failure tolerance
//            high enough to lose the next one.
//
// Each is a plain function of one health snapshot. The polling lives in await,
// so nothing here sleeps or retries.
// -------------------------------------------------------------------------------

package ready

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// Tolerance required before a run touches another server: enough to lose one
// and keep coordinating, which is what the next step spends.
const minTolerance = 1

// Server waits until a coordinating host has rejoined and the cluster trusts it
// again at target.
//
// restarted is when the step that restarted the host began. A health verdict
// older than that was formed before the restart, and a host that has not gone
// down yet reads as perfectly healthy -- so without this the gate would pass on
// stale health and move to the next server while this one is still coming back.
func (g *Gate) Server(ctx context.Context, name, target string, restarted time.Time) error {
	what := fmt.Sprintf("%s rejoining as a voter at %s", name, target)

	return g.await(ctx, what, func(cluster plan.Cluster) error {
		m, err := member(cluster, name)
		if err != nil {
			return err
		}

		switch {
		case m.Version != target:
			return fmt.Errorf("%s is running %s, not %s", name, m.Version, target)
		case !m.Healthy:
			return fmt.Errorf("%s is not healthy", name)
		case !m.Voter:
			return fmt.Errorf("%s is not a voter", name)
		case !m.StableSince.After(restarted):
			return fmt.Errorf("%s has read healthy since %s, which is from before the restart",
				name, m.StableSince.Format(time.RFC3339))
		case !cluster.Healthy:
			return fmt.Errorf("%s rejoined but the cluster is not healthy", name)
		case cluster.Tolerance < minTolerance:
			return fmt.Errorf("failure tolerance is %d, want at least %d", cluster.Tolerance, minTolerance)
		}

		return nil
	})
}

// Client waits until a host that carries work is back in service at target.
func (g *Gate) Client(ctx context.Context, name, target string) error {
	what := fmt.Sprintf("%s returning to service at %s", name, target)

	return g.await(ctx, what, func(cluster plan.Cluster) error {
		m, err := member(cluster, name)
		if err != nil {
			return err
		}

		switch {
		case m.Version != target:
			return fmt.Errorf("%s is running %s, not %s", name, m.Version, target)
		case !m.Healthy:
			return fmt.Errorf("%s is %s, not ready", name, m.Status)
		case !m.Eligible:
			return fmt.Errorf("%s is not eligible for work", name)
		}

		return nil
	})
}

// Coordination waits until a named host is no longer the one coordinating.
//
// The transfer request returns once raft has accepted it, not once the election
// has finished, so the run would otherwise restart the old leader while it
// still held the term. Which host took over is not asserted: raft chooses among
// the voters and any of them is a valid outcome.
func (g *Gate) Coordination(ctx context.Context, from string) error {
	what := fmt.Sprintf("coordination moving off %s", from)

	return g.await(ctx, what, func(cluster plan.Cluster) error {
		primary, ok := cluster.Primary()
		switch {
		// An election in progress has no primary at all. That is a step on the
		// way rather than the end of it: the run needs someone holding the
		// term before it restarts the host that used to.
		case !ok:
			return errors.New("no host is coordinating yet")
		case primary.Name == from:
			return fmt.Errorf("%s is still coordinating", from)
		case !cluster.Healthy:
			return fmt.Errorf("coordination moved to %s but the cluster is not healthy", primary.Name)
		}

		return nil
	})
}

// Barrier waits until the cluster could absorb losing another coordinating
// host. It runs between hosts: what a step did to the host it touched is the
// Server gate's business, and what it did to the cluster is this one's.
func (g *Gate) Barrier(ctx context.Context) error {
	return g.await(ctx, "the cluster settling before the next host", func(cluster plan.Cluster) error {
		if !cluster.Healthy {
			return errors.New("the cluster is not healthy")
		}

		for _, m := range cluster.OfKind(plan.KindServer) {
			if !m.Voter {
				return fmt.Errorf("%s is not a voter", m.Name)
			}
		}

		if cluster.Tolerance < minTolerance {
			return fmt.Errorf("failure tolerance is %d, want at least %d", cluster.Tolerance, minTolerance)
		}

		return nil
	})
}
