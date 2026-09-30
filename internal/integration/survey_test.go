// -------------------------------------------------------------------------------
// Survey Against A Real Agent
//
// Author: Alex Freidah
//
// What the unit tests cannot reach: that the endpoints the client reads are the
// ones Nomad serves, and that a dev agent -- server and client in one process
// -- is reconciled into a single member rather than counted twice. That
// reconciliation is the reason the survey exists, and a dev agent is the
// smallest cluster that can get it wrong.
// -------------------------------------------------------------------------------

//go:build integration

package integration

import (
	"testing"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

func TestHealthAgainstARealAgent(t *testing.T) {
	cluster, err := requireAgent(t).Health(t.Context())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	if len(cluster.Members) == 0 {
		t.Fatal("the survey found no hosts")
	}

	// A dev agent is one host holding both roles. The survey records it as a
	// server, because it is upgraded once and with the servers.
	servers := cluster.OfKind(plan.KindServer)
	if len(servers) != 1 {
		t.Errorf("servers = %d, want 1", len(servers))
	}
	if got := cluster.OfKind(plan.KindClient); len(got) != 0 {
		t.Errorf("clients = %d, want 0: a dual-role host must not be counted twice", len(got))
	}

	primary, ok := cluster.Primary()
	if !ok {
		t.Fatal("no host is coordinating")
	}
	if primary.Version == "" {
		t.Error("the coordinating host reports no version")
	}
	if !primary.Voter {
		t.Error("the coordinating host is not a voter")
	}
}

// A cluster that has never been named surveys fine and simply goes unnamed,
// which is the case Peek exists to make ordinary. A fresh agent has no such
// variable, so this is that case for real rather than as a stubbed 404.
func TestSurveyOfAnUnnamedCluster(t *testing.T) {
	cluster, err := requireAgent(t).Survey(t.Context())
	if err != nil {
		t.Fatalf("Survey: %v", err)
	}

	if cluster.Name != "" {
		t.Errorf("Name = %q, want empty for a cluster that publishes none", cluster.Name)
	}
	if len(cluster.Members) == 0 {
		t.Error("the survey found no hosts")
	}
}
