// -------------------------------------------------------------------------------
// Cluster Survey Tests - Reconciliation, Ordering, Degenerate Clusters
//
// Author: Alex Freidah
//
// Exercises assemble directly with library structs, which is the whole reason
// it is separate from the two API calls that feed it. The cases that matter
// are the ones a cluster produces rather than the ones a struct allows: a host
// running both roles, a server that accepts no work, and a survey taken while
// the cluster has no primary.
// -------------------------------------------------------------------------------

package nomad

import (
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

var surveyedAt = time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)

// byName finds a member so assertions do not depend on slice positions, which
// the ordering test owns.
func byName(t *testing.T, c plan.Cluster, name string) plan.Member {
	t.Helper()
	for _, m := range c.Members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("member %q not in survey %+v", name, c.Members)
	return plan.Member{}
}

func TestAssemble_ServersCarryCoordinationFacts(t *testing.T) {
	health := &api.OperatorHealthReply{
		FailureTolerance: 1,
		Servers: []api.ServerHealth{
			{ID: "s-1", Name: "alpha.global", Address: "10.0.0.1:4647", Version: "2.0.5", SerfStatus: "alive", Leader: true, Voter: true},
			{ID: "s-2", Name: "bravo.global", Address: "10.0.0.2:4647", Version: "2.0.5", SerfStatus: "alive", Voter: true},
		},
	}

	got := assemble(health, nil, surveyedAt)

	if got.Tolerance != 1 {
		t.Errorf("Tolerance = %d, want 1", got.Tolerance)
	}
	if !got.SurveyedAt.Equal(surveyedAt) {
		t.Errorf("SurveyedAt = %v, want %v", got.SurveyedAt, surveyedAt)
	}

	alpha := byName(t, got, "alpha")
	if alpha.Kind != plan.KindServer {
		t.Errorf("Kind = %q, want %q", alpha.Kind, plan.KindServer)
	}
	if alpha.Addr != "10.0.0.1" {
		t.Errorf("Addr = %q, want the raft port dropped", alpha.Addr)
	}
	if !alpha.Primary || !alpha.Voter {
		t.Errorf("Primary/Voter = %v/%v, want both true", alpha.Primary, alpha.Voter)
	}
	if alpha.Status != "alive" {
		t.Errorf("Status = %q, want the tool's own vocabulary", alpha.Status)
	}

	if bravo := byName(t, got, "bravo"); bravo.Primary {
		t.Error("bravo reported as primary; only one host coordinates")
	}
}

// The case the reconciliation exists for: a host in both lists is one member,
// classified as a server, because restarting it twice would spend the
// cluster's fault tolerance twice for one host.
func TestAssemble_HostInBothListsAppearsOnceAsServer(t *testing.T) {
	health := &api.OperatorHealthReply{
		Servers: []api.ServerHealth{
			{ID: "s-1", Name: "dual.global", Address: "10.0.0.1:4647", Version: "2.0.5", SerfStatus: "alive", Leader: true, Voter: true},
		},
	}
	stubs := []*api.NodeListStub{
		{ID: "n-1", Name: "dual", Address: "10.0.0.1", Version: "2.0.5", Status: "ready"},
		{ID: "n-2", Name: "solo", Address: "10.0.0.9", Version: "2.0.5", Status: "ready"},
	}

	got := assemble(health, stubs, surveyedAt)

	if len(got.Members) != 2 {
		t.Fatalf("members = %d, want 2; the dual-role host was counted twice", len(got.Members))
	}
	if dual := byName(t, got, "dual"); dual.Kind != plan.KindServer {
		t.Errorf("dual-role host Kind = %q, want %q", dual.Kind, plan.KindServer)
	}
	if solo := byName(t, got, "solo"); solo.Kind != plan.KindClient {
		t.Errorf("solo Kind = %q, want %q", solo.Kind, plan.KindClient)
	}
}

// A server that accepts no work appears in the server list and nowhere else,
// which is normal rather than a gap.
func TestAssemble_ServerWithoutClientEntry(t *testing.T) {
	health := &api.OperatorHealthReply{
		Servers: []api.ServerHealth{
			{ID: "s-1", Name: "coordinator.global", Address: "10.0.0.1:4647", Version: "2.0.5", Voter: true},
		},
	}

	got := assemble(health, nil, surveyedAt)

	if len(got.Members) != 1 {
		t.Fatalf("members = %d, want 1", len(got.Members))
	}
	if m := got.Members[0]; m.Kind != plan.KindServer || m.Name != "coordinator" {
		t.Errorf("member = %+v, want the server-only host", m)
	}
}

func TestAssemble_ClientsCarryStatusNotCoordination(t *testing.T) {
	stubs := []*api.NodeListStub{
		{ID: "n-1", Name: "worker", Address: "10.0.0.5", Version: "2.0.4", Status: "down"},
	}

	got := assemble(&api.OperatorHealthReply{}, stubs, surveyedAt)

	m := byName(t, got, "worker")
	if m.Kind != plan.KindClient {
		t.Errorf("Kind = %q, want %q", m.Kind, plan.KindClient)
	}
	if m.Version != "2.0.4" {
		t.Errorf("Version = %q, want %q", m.Version, "2.0.4")
	}
	if m.Status != "down" {
		t.Errorf("Status = %q, want %q", m.Status, "down")
	}
	if m.Primary || m.Voter {
		t.Error("a client reported coordination flags")
	}
}

// Autopilot's verdict travels as it is given, along with when it was reached.
// The gates that run between steps compare that time against the restart to
// tell a host that has come back from one that has not gone down yet, so a
// survey that dropped it would leave them nothing to compare.
func TestAssemble_ServersCarryAutopilotsVerdict(t *testing.T) {
	stable := surveyedAt.Add(-10 * time.Minute)
	health := &api.OperatorHealthReply{
		Healthy: true,
		Servers: []api.ServerHealth{
			{ID: "s-1", Name: "alpha.global", Address: "10.0.0.1:4647", Healthy: true, StableSince: stable},
			{ID: "s-2", Name: "bravo.global", Address: "10.0.0.2:4647"},
		},
	}

	got := assemble(health, nil, surveyedAt)

	if !got.Healthy {
		t.Error("Healthy = false, want the reply's own verdict on the cluster")
	}

	alpha := byName(t, got, "alpha")
	if !alpha.Healthy {
		t.Error("alpha Healthy = false, want autopilot's verdict")
	}
	if !alpha.StableSince.Equal(stable) {
		t.Errorf("alpha StableSince = %v, want %v", alpha.StableSince, stable)
	}

	if bravo := byName(t, got, "bravo"); bravo.Healthy {
		t.Error("bravo reported healthy; autopilot did not say so")
	}
}

// A client has no autopilot verdict, so its status is what stands in for one.
// The gates read the normalised field, which keeps Nomad's vocabulary in this
// package.
func TestAssemble_ClientsNormaliseReadyAndEligible(t *testing.T) {
	stubs := []*api.NodeListStub{
		{ID: "n-1", Name: "fit", Address: "10.0.0.5", Status: api.NodeStatusReady, SchedulingEligibility: api.NodeSchedulingEligible},
		{ID: "n-2", Name: "drained", Address: "10.0.0.6", Status: api.NodeStatusReady, SchedulingEligibility: api.NodeSchedulingIneligible},
		{ID: "n-3", Name: "starting", Address: "10.0.0.7", Status: "initializing", SchedulingEligibility: api.NodeSchedulingEligible},
	}

	got := assemble(&api.OperatorHealthReply{}, stubs, surveyedAt)

	if m := byName(t, got, "fit"); !m.Healthy || !m.Eligible {
		t.Errorf("fit Healthy/Eligible = %v/%v, want both true", m.Healthy, m.Eligible)
	}

	// Up and carrying its work while the fleet schedules nothing new onto it.
	if m := byName(t, got, "drained"); !m.Healthy || m.Eligible {
		t.Errorf("drained Healthy/Eligible = %v/%v, want true/false", m.Healthy, m.Eligible)
	}

	if m := byName(t, got, "starting"); m.Healthy {
		t.Error("starting reported healthy; only a ready node is")
	}
}

// A host that is down is still surveyed. Omitting it would silently shrink
// every plan generated against the cluster.
func TestAssemble_KeepsUnhealthyHosts(t *testing.T) {
	health := &api.OperatorHealthReply{
		Servers: []api.ServerHealth{
			{ID: "s-1", Name: "sick.global", Address: "10.0.0.1:4647", SerfStatus: "failed"},
		},
	}
	stubs := []*api.NodeListStub{{ID: "n-1", Name: "gone", Address: "10.0.0.5", Status: "down"}}

	got := assemble(health, stubs, surveyedAt)

	if len(got.Members) != 2 {
		t.Fatalf("members = %d, want both the failed server and the down client", len(got.Members))
	}
}

// Ordering has to be total and repeatable, or two surveys of one cluster
// cannot be compared.
func TestAssemble_OrdersServersFirstThenByName(t *testing.T) {
	health := &api.OperatorHealthReply{
		Servers: []api.ServerHealth{
			{ID: "s-2", Name: "zulu.global", Address: "10.0.0.2:4647"},
			{ID: "s-1", Name: "alpha.global", Address: "10.0.0.1:4647"},
		},
	}
	stubs := []*api.NodeListStub{
		{ID: "n-2", Name: "yankee", Address: "10.0.0.8"},
		{ID: "n-1", Name: "bravo", Address: "10.0.0.7"},
	}

	got := assemble(health, stubs, surveyedAt)

	want := []string{"alpha", "zulu", "bravo", "yankee"}
	for i, name := range want {
		if got.Members[i].Name != name {
			t.Fatalf("order = %v, want %v", names(got), want)
		}
	}
}

func names(c plan.Cluster) []string {
	out := make([]string, 0, len(c.Members))
	for _, m := range c.Members {
		out = append(out, m.Name)
	}
	return out
}

// A cluster mid-election reports no primary, which the planner has to be able
// to tell apart from a survey that simply did not look.
func TestAssemble_NoPrimaryDuringElection(t *testing.T) {
	health := &api.OperatorHealthReply{
		Servers: []api.ServerHealth{
			{ID: "s-1", Name: "alpha.global", Address: "10.0.0.1:4647", Voter: true},
			{ID: "s-2", Name: "bravo.global", Address: "10.0.0.2:4647", Voter: true},
		},
	}

	got := assemble(health, nil, surveyedAt)

	if _, ok := got.Primary(); ok {
		t.Error("Primary() found one in a survey where no server leads")
	}
}

// A failed health read leaves the reply nil, and a snapshot of nothing is
// better than a panic on the way to reporting the failure.
func TestAssemble_NilHealth(t *testing.T) {
	got := assemble(nil, []*api.NodeListStub{{ID: "n-1", Name: "orphan"}}, surveyedAt)

	if len(got.Members) != 0 {
		t.Errorf("members = %v, want none without a server view", got.Members)
	}
	if !got.SurveyedAt.Equal(surveyedAt) {
		t.Error("SurveyedAt was not stamped")
	}
}

func TestAssemble_EmptyCluster(t *testing.T) {
	got := assemble(&api.OperatorHealthReply{}, nil, surveyedAt)

	if len(got.Members) != 0 {
		t.Errorf("members = %v, want none", got.Members)
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"10.0.0.1:4647":     "10.0.0.1",
		"10.0.0.1":          "10.0.0.1",
		"[fd00::1]:4647":    "fd00::1",
		"  10.0.0.1:4647  ": "10.0.0.1",
		"":                  "",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortName(t *testing.T) {
	cases := map[string]string{
		"alpha.global": "alpha",
		"alpha":        "alpha",
		"":             "",
		"a.b.c":        "a",
	}
	for in, want := range cases {
		if got := shortName(in); got != want {
			t.Errorf("shortName(%q) = %q, want %q", in, got, want)
		}
	}
}
