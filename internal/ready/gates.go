// -------------------------------------------------------------------------------
// Assertions - Server, Client, Coordination, Barrier
//
// Author: Alex Freidah
//
// The gates:
//
//	Server        one coordinating host: right version, healthy, voting again.
//	Client        one host that carries work: right version, ready, eligible.
//	Coordination  a named host no longer being the one that coordinates.
//	Barrier       the cluster: healthy, every server a voter, and failure
//	              tolerance high enough to lose the next one.
//
// Each is a plain function of one health snapshot. The polling lives in await,
// so nothing here sleeps or retries.
//
// Barrier is not yet called by any step. The per-host gates cover what a step
// did to the host it touched; the barrier is what it did to the cluster.
// -------------------------------------------------------------------------------

package ready

import (
	"context"
	"errors"
	"fmt"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// Tolerance required before a run touches another server: enough to lose one
// and keep coordinating, which is what the next step spends.
const minTolerance = 1

// Server waits until a coordinating host has rejoined and the cluster trusts it
// again at target.
//
// The version is what proves the host restarted. It is read from the running
// agent, not from the pin, so a host that has not been restarted still reports
// the old one however healthy it looks. A gate that also asked when the
// cluster's health verdict last changed would be asking for something it does
// not need and does not get: an agent that goes down and comes back inside one
// health interval is never observed unhealthy, so that timestamp still reads
// from before the restart and the gate would wait out its timeout on a host
// that had already arrived.
func (g *Gate) Server(ctx context.Context, name, target string) error {
	what := fmt.Sprintf("%s rejoining the cluster at %s", name, target)
	arrived := fmt.Sprintf("%s is running %s, healthy, and back in the cluster", name, target)

	return g.await(ctx, what, arrived, func(cluster plan.Cluster) error {
		m, err := member(cluster, name)
		if err != nil {
			return err
		}

		switch {
		case m.Version != target:
			return fmt.Errorf("%s is running %s, not %s", name, m.Version, target)
		case !m.Healthy:
			return fmt.Errorf("%s is not healthy", name)
		// Asked only where coordination runs on a quorum of these members. A
		// cluster whose quorum lives in its storage backend has no voters at
		// all, and requiring one would hold every host here until it timed out.
		case cluster.Votes() && !m.Voter:
			return fmt.Errorf("%s is not a voter", name)
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
	arrived := fmt.Sprintf("%s is running %s, ready, and accepting work again", name, target)

	return g.await(ctx, what, arrived, func(cluster plan.Cluster) error {
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
	arrived := fmt.Sprintf("%s is no longer coordinating", from)

	return g.await(ctx, what, arrived, func(cluster plan.Cluster) error {
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
	return g.await(ctx,
		"the cluster settling before the next host",
		"the cluster has settled and can lose another server",
		func(cluster plan.Cluster) error {
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
