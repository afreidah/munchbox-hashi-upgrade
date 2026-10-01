// -------------------------------------------------------------------------------
// Survey Tests - Reconciling Two Views Of One Datacenter
//
// Author: Alex Freidah
//
// Autopilot describes the servers; the gossip pool describes every agent,
// servers included. What is worth testing is the join: a host that runs both
// must appear once, as a server, because it carries one binary and one service
// and upgrading it twice would spend the datacenter's fault tolerance twice for
// one host.
// -------------------------------------------------------------------------------

package consul

import (
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

var surveyedAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// server is one row of an autopilot reply.
func server(name, addr string, leader bool) api.ServerHealth {
	return api.ServerHealth{
		ID: "id-" + name, Name: name, Address: addr + ":8300",
		SerfStatus: "alive", Version: "2.0.3",
		Leader: leader, Voter: true, Healthy: true,
	}
}

// member is one row of the gossip pool. role tells a server from an agent.
func member(name, addr, role string, status int) *api.AgentMember {
	return &api.AgentMember{
		Name: name, Addr: addr, Status: status,
		Tags: map[string]string{roleTag: role, buildTag: "2.0.3:d0f2be93"},
	}
}

// find returns a member of the snapshot by name.
func find(t *testing.T, cluster plan.Cluster, name string) plan.Member {
	t.Helper()

	for _, m := range cluster.Members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("%s is not in the snapshot", name)
	return plan.Member{}
}

func reply(servers ...api.ServerHealth) *api.OperatorHealthReply {
	return &api.OperatorHealthReply{Healthy: true, FailureTolerance: 1, Servers: servers}
}

// -------------------------------------------------------------------------
// THE JOIN
// -------------------------------------------------------------------------

// A host that runs a server also gossips, so it appears in both reads. It is
// one host and is recorded once.
func TestAssembleRecordsADualRoleHostOnce(t *testing.T) {
	cluster := assemble(
		reply(server("alpha", "10.0.0.1", true)),
		[]*api.AgentMember{
			member("alpha", "10.0.0.1", serverRole, alive),
			member("bravo", "10.0.0.2", "node", alive),
		},
		surveyedAt,
	)

	if got := len(cluster.Members); got != 2 {
		t.Fatalf("members = %d, want 2", got)
	}

	alpha := find(t, cluster, "alpha")
	if alpha.Kind != plan.KindServer {
		t.Errorf("alpha kind = %q, want %q", alpha.Kind, plan.KindServer)
	}
	if !alpha.Primary {
		t.Error("alpha is the leader and was not recorded as coordinating")
	}
	if find(t, cluster, "bravo").Kind != plan.KindClient {
		t.Error("bravo carries no raft state and should be a client")
	}
}

// An agent carrying the server role but absent from autopilot is still not a
// client: it is a server the reply did not describe, and counting it among the
// agents would have the run restart it in the wrong stage.
func TestAssembleIgnoresAServerMissingFromAutopilot(t *testing.T) {
	cluster := assemble(
		reply(server("alpha", "10.0.0.1", true)),
		[]*api.AgentMember{
			member("alpha", "10.0.0.1", serverRole, alive),
			member("charlie", "10.0.0.3", serverRole, alive),
		},
		surveyedAt,
	)

	if got := len(cluster.Members); got != 1 {
		t.Errorf("members = %d, want only the server autopilot reported", got)
	}
}

func TestAssembleCarriesTheDatacentersVerdict(t *testing.T) {
	health := reply(server("alpha", "10.0.0.1", true))
	health.Healthy = false
	health.FailureTolerance = 0

	cluster := assemble(health, nil, surveyedAt)

	if cluster.Healthy {
		t.Error("Healthy = true, want the reply's verdict")
	}
	if cluster.Tolerance != 0 {
		t.Errorf("Tolerance = %d, want 0", cluster.Tolerance)
	}
	if !cluster.SurveyedAt.Equal(surveyedAt) {
		t.Errorf("SurveyedAt = %s, want %s", cluster.SurveyedAt, surveyedAt)
	}
}

// Nothing to reconcile is an empty snapshot rather than a panic: a datacenter
// mid-election answers without servers.
func TestAssembleOfNothing(t *testing.T) {
	if got := assemble(nil, nil, surveyedAt); len(got.Members) != 0 {
		t.Errorf("members = %d, want none", len(got.Members))
	}
}

// -------------------------------------------------------------------------
// WHAT A MEMBER CARRIES
// -------------------------------------------------------------------------

// The gossip pool reports a version with the revision attached. The run holds
// hosts against a version, so only that part is kept.
func TestAssembleTakesTheVersionOutOfTheBuildTag(t *testing.T) {
	cluster := assemble(reply(), []*api.AgentMember{member("bravo", "10.0.0.2", "node", alive)}, surveyedAt)

	if got := find(t, cluster, "bravo").Version; got != "2.0.3" {
		t.Errorf("Version = %q, want 2.0.3", got)
	}
}

// Autopilot reports a server's raft address with its port; ssh wants the host.
func TestAssembleDropsTheRaftPort(t *testing.T) {
	cluster := assemble(reply(server("alpha", "10.0.0.1", true)), nil, surveyedAt)

	if got := find(t, cluster, "alpha").Addr; got != "10.0.0.1" {
		t.Errorf("Addr = %q, want the host without its port", got)
	}
}

// An agent that has left the pool is not in service, and the gate that waits
// for a host to come back reads exactly this.
func TestAssembleRecordsAMemberThatIsNotAlive(t *testing.T) {
	const failed = 4

	cluster := assemble(reply(), []*api.AgentMember{member("bravo", "10.0.0.2", "node", failed)}, surveyedAt)

	bravo := find(t, cluster, "bravo")
	if bravo.Healthy {
		t.Error("a failed member reads healthy")
	}
	if bravo.Eligible {
		t.Error("a failed member reads as in service")
	}
	if bravo.Status != "failed" {
		t.Errorf("Status = %q, want failed", bravo.Status)
	}
}

// -------------------------------------------------------------------------
// ORDER
// -------------------------------------------------------------------------

// Servers first, then each group by name, so one datacenter always surveys to
// the same snapshot and two surveys can be compared.
func TestAssembleSortsServersFirstThenByName(t *testing.T) {
	cluster := assemble(
		reply(server("zulu", "10.0.0.9", true), server("alpha", "10.0.0.1", false)),
		[]*api.AgentMember{
			member("yankee", "10.0.0.8", "node", alive),
			member("bravo", "10.0.0.2", "node", alive),
		},
		surveyedAt,
	)

	want := []string{"alpha", "zulu", "bravo", "yankee"}
	for i, name := range want {
		if cluster.Members[i].Name != name {
			t.Errorf("member %d = %s, want %s", i, cluster.Members[i].Name, name)
		}
	}
}
