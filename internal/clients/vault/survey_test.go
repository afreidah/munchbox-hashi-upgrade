// -------------------------------------------------------------------------------
// Survey Tests - What Each Node's State Is Read As
//
// Author: Alex Freidah
//
// assemble is where two views of a cluster are reconciled: the active node's
// list of the HA set, and each node's own answer about itself. Which of those
// wins, and what a missing answer is read as, is the whole of the logic worth
// testing -- so these tests drive it directly rather than through a client.
//
// The cases that matter are the ones a run actually meets. A node restarting
// into a seal, a standby that has stopped echoing, and a cluster with no active
// node are the three states that must not be read as healthy.
// -------------------------------------------------------------------------------

package vault

import (
	"testing"
	"time"

	"github.com/hashicorp/vault/api"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

var surveyedAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// node builds one entry of the HA set.
func node(name string, active bool, version string, echo time.Time) api.HANode {
	n := api.HANode{
		Hostname:   name,
		APIAddress: "https://" + name + ".munchbox.cc:8200",
		ActiveNode: active,
		Version:    version,
	}
	if !echo.IsZero() {
		n.LastEcho = &echo
	}
	return n
}

// health builds one node's answer about itself.
func health(version string, sealed, standby bool) *api.HealthResponse {
	return &api.HealthResponse{
		Initialized: true,
		Sealed:      sealed,
		Standby:     standby,
		Version:     version,
		ClusterName: "munchbox-vault",
	}
}

// find returns a member by name, failing the test when it is absent.
func find(t *testing.T, cluster plan.Cluster, name string) plan.Member {
	t.Helper()
	for _, m := range cluster.Members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no member named %q in %d members", name, len(cluster.Members))
	return plan.Member{}
}

// trio is the shape of the real cluster: one active node and two standbys, all
// answering for themselves.
func trio() (*api.HAStatusResponse, map[string]*api.HealthResponse) {
	ha := &api.HAStatusResponse{Nodes: []api.HANode{
		node("goren", false, "2.0.4", surveyedAt.Add(-2*time.Second)),
		node("nomad-server-03", true, "2.0.4", time.Time{}),
		node("stabler", false, "2.0.4", surveyedAt.Add(-3*time.Second)),
	}}
	return ha, map[string]*api.HealthResponse{
		"goren":           health("2.0.4", false, true),
		"nomad-server-03": health("2.0.4", false, false),
		"stabler":         health("2.0.4", false, true),
	}
}

// surveyed assembles a snapshot at the fixed survey time, so a case that does
// not care about timing reads as one call.
func surveyed(ha *api.HAStatusResponse, h map[string]*api.HealthResponse) plan.Cluster {
	return assemble(ha, h, surveyedAt)
}

// -------------------------------------------------------------------------
// THE ORDINARY CASE
// -------------------------------------------------------------------------

func TestEveryNodeIsAServerAndNoneIsAVoter(t *testing.T) {
	cluster := surveyed(trio())

	if len(cluster.Members) != 3 {
		t.Fatalf("members = %d, want 3", len(cluster.Members))
	}
	for _, m := range cluster.Members {
		if m.Kind != plan.KindServer {
			t.Errorf("%s kind = %v, want server", m.Name, m.Kind)
		}
		// Quorum lives in the storage backend, so nothing here votes.
		if m.Voter {
			t.Errorf("%s is recorded as a voter, but this cluster holds no raft state", m.Name)
		}
	}
}

func TestTheActiveNodeIsThePrimary(t *testing.T) {
	cluster := surveyed(trio())

	active := find(t, cluster, "nomad-server-03")
	if !active.Primary || active.Status != statusActive {
		t.Errorf("active node: primary = %v, status = %q", active.Primary, active.Status)
	}

	standby := find(t, cluster, "goren")
	if standby.Primary || standby.Status != statusStandby {
		t.Errorf("standby: primary = %v, status = %q", standby.Primary, standby.Status)
	}
}

// The address is reached over ssh, so the URL Vault reports has to come back as
// a host the run can connect to.
func TestTheApiUrlBecomesABareHost(t *testing.T) {
	cluster := surveyed(trio())

	if addr := find(t, cluster, "goren").Addr; addr != "goren.munchbox.cc" {
		t.Errorf("addr = %q, want the host out of the api url", addr)
	}
}

func TestTheClusterTakesItsNameFromANode(t *testing.T) {
	cluster := surveyed(trio())

	if cluster.Name != "munchbox-vault" {
		t.Errorf("name = %q, want munchbox-vault", cluster.Name)
	}
}

// Nothing reports a tolerance, so it is derived: a cluster serves while one
// node is unsealed, which leaves every serving node but one to spare.
func TestToleranceIsDerivedFromTheServingNodes(t *testing.T) {
	cluster := surveyed(trio())

	if cluster.Tolerance != 2 {
		t.Errorf("tolerance = %d, want 2 for three serving nodes", cluster.Tolerance)
	}
	if !cluster.Healthy {
		t.Error("a cluster with an active node and three unsealed nodes is healthy")
	}
}

// -------------------------------------------------------------------------
// A NODE THAT CAME BACK SEALED
// -------------------------------------------------------------------------

// The state a run actually waits out. Auto-unseal makes it brief, but between
// the restart and the KMS answering, a node is in the HA set and serving
// nothing -- and a gate that read it as healthy would move on too early.
func TestASealedNodeIsNeitherHealthyNorServing(t *testing.T) {
	ha, h := trio()
	h["goren"] = health("2.0.4", true, true)

	cluster := assemble(ha, h, surveyedAt)

	sealed := find(t, cluster, "goren")
	if sealed.Status != statusSealed {
		t.Errorf("status = %q, want %q", sealed.Status, statusSealed)
	}
	if sealed.Healthy || sealed.Eligible {
		t.Errorf("a sealed node reads as healthy = %v, serving = %v", sealed.Healthy, sealed.Eligible)
	}
	if cluster.Tolerance != 1 {
		t.Errorf("tolerance = %d, want 1 with one of three sealed", cluster.Tolerance)
	}
}

// An uninitialised node is not serving either, and saying so as "sealed" is
// closer than saying it is up.
func TestAnUninitialisedNodeIsReadAsSealed(t *testing.T) {
	ha, h := trio()
	h["goren"] = &api.HealthResponse{Initialized: false, Version: "2.0.4"}

	if got := find(t, assemble(ha, h, surveyedAt), "goren").Status; got != statusSealed {
		t.Errorf("status = %q, want %q", got, statusSealed)
	}
}

// -------------------------------------------------------------------------
// A NODE THAT WILL NOT ANSWER
// -------------------------------------------------------------------------

// A node mid-restart refuses connections, so the only thing left is the active
// node's record of when it last echoed. Recent means it is still in the set.
func TestAStandbyWithNoAnswerFallsBackToItsEcho(t *testing.T) {
	ha, h := trio()
	delete(h, "goren")

	if got := find(t, assemble(ha, h, surveyedAt), "goren").Status; got != statusStandby {
		t.Errorf("status = %q, want %q from a recent echo", got, statusStandby)
	}
}

// A stale echo is a node the active one has stopped hearing from, which is not
// the same as a node that answered and said it was fine.
func TestAStandbyThatStoppedEchoingIsOutOfTouch(t *testing.T) {
	ha, h := trio()
	delete(h, "stabler")
	ha.Nodes[2] = node("stabler", false, "2.0.4", surveyedAt.Add(-2*echoGrace))

	lost := find(t, assemble(ha, h, surveyedAt), "stabler")
	if lost.Status != statusLost {
		t.Errorf("status = %q, want %q", lost.Status, statusLost)
	}
	if lost.Healthy {
		t.Error("a node out of touch reads as healthy")
	}
}

// A standby that has never echoed has nothing to go on, so it is not assumed
// to be fine.
func TestAStandbyWithNoEchoAtAllIsOutOfTouch(t *testing.T) {
	ha, h := trio()
	delete(h, "stabler")
	ha.Nodes[2] = node("stabler", false, "2.0.4", time.Time{})

	if got := find(t, assemble(ha, h, surveyedAt), "stabler").Status; got != statusLost {
		t.Errorf("status = %q, want %q", got, statusLost)
	}
}

// The active node served the HA status, so it is answering by definition even
// when a direct read of it fails.
func TestTheActiveNodeIsActiveEvenWithNoDirectAnswer(t *testing.T) {
	ha, h := trio()
	delete(h, "nomad-server-03")

	if got := find(t, assemble(ha, h, surveyedAt), "nomad-server-03").Status; got != statusActive {
		t.Errorf("status = %q, want %q", got, statusActive)
	}
}

// -------------------------------------------------------------------------
// NO ACTIVE NODE
// -------------------------------------------------------------------------

// Unsealed nodes with none of them active is a cluster that is up and serving
// nothing -- mid-election, or stuck. It is not healthy, and a run must not
// proceed into it.
func TestACLusterWithNoActiveNodeIsNotHealthy(t *testing.T) {
	ha, h := trio()
	ha.Nodes[1] = node("nomad-server-03", false, "2.0.4", surveyedAt.Add(-time.Second))
	h["nomad-server-03"] = health("2.0.4", false, true)

	cluster := assemble(ha, h, surveyedAt)

	if cluster.Healthy {
		t.Error("a cluster with no active node is healthy")
	}
	// The nodes themselves are fine; it is the cluster that is not.
	if cluster.Tolerance != 2 {
		t.Errorf("tolerance = %d, want 2 -- all three still serve", cluster.Tolerance)
	}
}

// -------------------------------------------------------------------------
// VERSIONS
// -------------------------------------------------------------------------

// The gate after an upgrade asks whether this host is on the new version, so a
// node's own answer has to win over the active node's record of it.
func TestANodesOwnVersionWinsOverTheReportedOne(t *testing.T) {
	ha, h := trio()
	h["goren"] = health("2.1.1", false, true)

	if got := find(t, assemble(ha, h, surveyedAt), "goren").Version; got != "2.1.1" {
		t.Errorf("version = %q, want the node's own answer", got)
	}
}

// With no answer from the node there is nothing better to use.
func TestTheReportedVersionIsUsedWhenANodeIsSilent(t *testing.T) {
	ha, h := trio()
	delete(h, "goren")

	if got := find(t, assemble(ha, h, surveyedAt), "goren").Version; got != "2.0.4" {
		t.Errorf("version = %q, want the reported one", got)
	}
}

// -------------------------------------------------------------------------
// NOTHING TO READ
// -------------------------------------------------------------------------

func TestAnEmptyStatusIsAnEmptyCluster(t *testing.T) {
	cluster := assemble(&api.HAStatusResponse{}, nil, surveyedAt)

	if len(cluster.Members) != 0 || cluster.Healthy || cluster.Tolerance != 0 {
		t.Errorf("empty status gave %+v", cluster)
	}
}

func TestANilStatusIsAnEmptyCluster(t *testing.T) {
	if cluster := assemble(nil, nil, surveyedAt); len(cluster.Members) != 0 {
		t.Errorf("nil status gave %d members", len(cluster.Members))
	}
}

// -------------------------------------------------------------------------
// ADDRESSES
// -------------------------------------------------------------------------

func TestHostOf(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"https://192.168.68.58:8200", "192.168.68.58"},
		{"http://goren:8200", "goren"},
		{"https://goren.munchbox.cc", "goren.munchbox.cc"},
		// Not a URL at all: handed back rather than mangled into nothing.
		{"192.168.68.58", "192.168.68.58"},
		{"", ""},
	} {
		if got := hostOf(c.in); got != c.want {
			t.Errorf("hostOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
