// -------------------------------------------------------------------------------
// Draining A Real Node
//
// Author: Alex Freidah
//
// Drain is the one client method with no meaningful unit test. It issues the
// update and then reads MonitorDrain to exhaustion, and MonitorDrain is a
// blocking-query loop: stubbing it would be asserting a guess about which
// endpoints it watches and how it decides it is done. Against a real agent the
// question is simply whether the call returns when the drain has finished.
// -------------------------------------------------------------------------------

//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	nomadclient "github.com/afreidah/munchbox-hashi-upgrade/internal/clients/nomad"
)

// How long a drain of an empty node is given. Nothing is running on it, so this
// is a bound on the call returning at all rather than on work moving.
const drainDeadline = 30 * time.Second

// Each test leaves the node eligible again, so the next one inherits a fleet
// in the state it found it.
func restore(t *testing.T, api *nomadclient.Nomad, id string) {
	t.Helper()

	t.Cleanup(func() {
		if err := api.Undrain(context.Background(), id); err != nil {
			t.Errorf("restore eligibility: %v", err)
		}
	})
}

// The whole contract: the call returns rather than hanging, and it returns
// because the drain finished rather than because a deadline elapsed.
func TestDrainReturnsWhenTheNodeIsEmpty(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), drainDeadline)
	defer cancel()

	api, target := requireFleet(t)
	restore(t, api, target.ID)

	started := time.Now()
	if err := api.Drain(ctx, target.ID, 5*time.Second); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if elapsed := time.Since(started); elapsed >= drainDeadline {
		t.Errorf("Drain took %s, which is the deadline rather than a completion", elapsed)
	}
}

// Undrain exists to make a host eligible again, not merely to cancel the
// drain. A host left ineligible is quietly out of the cluster's capacity, and
// the client gate a run waits on requires eligibility.
func TestUndrainMakesTheNodeEligibleAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), drainDeadline)
	defer cancel()

	api, target := requireFleet(t)
	restore(t, api, target.ID)

	if err := api.Drain(ctx, target.ID, 5*time.Second); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := api.Undrain(ctx, target.ID); err != nil {
		t.Fatalf("Undrain: %v", err)
	}

	cluster, err := api.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	for _, m := range cluster.Members {
		if m.ID == target.ID && !m.Eligible {
			t.Error("the node is still ineligible after being undrained")
		}
	}
}

// A node the cluster does not hold is reported rather than waited on, so a run
// naming a host that has gone away stops instead of sitting out the deadline.
func TestDrainOfAnUnknownNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), drainDeadline)
	defer cancel()

	err := requireAgent(t).Drain(ctx, "00000000-0000-0000-0000-000000000000", 5*time.Second)
	if err == nil {
		t.Error("draining a node that does not exist was accepted")
	}
}
