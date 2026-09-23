// -------------------------------------------------------------------------------
// Task Order Tests - Sequence, Handoff, Determinism
//
// Author: Alex Freidah
//
// The ordering rules are the reason this package exists, so the assertions are
// about sequence and about which host each task names, not about field values.
// Cases are the shapes a cluster comes in: several servers, one, none, and a
// survey taken while nothing is coordinating.
// -------------------------------------------------------------------------------

package steps_test

import (
	"slices"
	"testing"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/steps"
)

var spec = plan.Spec{Tool: plan.Nomad, From: "2.0.5", To: "2.0.6"}

func server(name string, primary bool) plan.Member {
	return plan.Member{Name: name, Addr: name, Kind: plan.KindServer, Version: "2.0.5", Primary: primary, Voter: true}
}

func client(name string) plan.Member {
	return plan.Member{Name: name, Addr: name, Kind: plan.KindClient, Version: "2.0.5"}
}

func cluster(members ...plan.Member) plan.Cluster {
	return plan.Cluster{Members: members}
}

func ids(tasks []plan.Task) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}

func find(t *testing.T, tasks []plan.Task, id string) plan.Task {
	t.Helper()
	i := slices.IndexFunc(tasks, func(task plan.Task) bool { return task.ID == id })
	if i < 0 {
		t.Fatalf("task %q not in %v", id, ids(tasks))
	}
	return tasks[i]
}

// The whole sequence in one assertion, because the order is the contract:
// non-primary servers, the handoff, the primary, then clients.
func TestBuild_Sequence(t *testing.T) {
	got := steps.Build(spec, cluster(
		client("zulu"),
		server("charlie", true),
		client("alpha"),
		server("bravo", false),
		server("delta", false),
	))

	want := []string{
		"upgrade-bravo",
		"upgrade-delta",
		steps.TaskIDHandoff,
		"upgrade-charlie",
		"upgrade-alpha",
		"upgrade-zulu",
	}
	if !slices.Equal(ids(got), want) {
		t.Errorf("order =\n  %v\nwant\n  %v", ids(got), want)
	}
}

// Every surveyed host gets a task, so a host absent from a run can only mean
// the survey never saw it.
func TestBuild_EmitsATaskPerHost(t *testing.T) {
	got := steps.Build(spec, cluster(
		server("alpha", true),
		client("bravo"),
		client("charlie"),
	))

	// Three hosts plus the handoff.
	if len(got) != 4 {
		t.Fatalf("tasks = %v, want one per host plus the handoff", ids(got))
	}
}

// A host already at the target still gets a task. Whether the work is needed
// is settled live, when the task is reached.
func TestBuild_IncludesHostsAlreadyAtTarget(t *testing.T) {
	current := server("alpha", true)
	current.Version = spec.To

	got := steps.Build(spec, cluster(current))

	if !slices.Contains(ids(got), "upgrade-alpha") {
		t.Errorf("tasks = %v, want the host at target still planned", ids(got))
	}
}

func TestBuild_HandoffPrecedesThePrimary(t *testing.T) {
	got := steps.Build(spec, cluster(server("alpha", false), server("bravo", true)))

	handoff := slices.Index(ids(got), steps.TaskIDHandoff)
	primary := slices.Index(ids(got), "upgrade-bravo")
	if handoff < 0 || primary < 0 {
		t.Fatalf("tasks = %v, want both a handoff and the primary upgrade", ids(got))
	}
	if handoff > primary {
		t.Errorf("handoff at %d, primary upgrade at %d; coordination must move first", handoff, primary)
	}
}

// The handoff names no destination: the runner chooses one against a cluster
// it has just checked, not against a survey taken before anything moved.
func TestBuild_HandoffNamesNoDestination(t *testing.T) {
	got := steps.Build(spec, cluster(server("alpha", false), server("bravo", true)))

	handoff := find(t, got, steps.TaskIDHandoff)
	if handoff.Member != "bravo" {
		t.Errorf("Member = %q, want the host being relieved", handoff.Member)
	}
	for _, key := range []string{"to", "target", "destination"} {
		if _, named := handoff.Action.Args[key]; named {
			t.Errorf("handoff names a destination via %q; the runner picks it", key)
		}
	}
}

// A single-server cluster still hands off before restarting it. Whether that
// can succeed is the runner's problem; planning around it here would hide the
// condition from the operator.
func TestBuild_SingleServerStillHandsOff(t *testing.T) {
	got := steps.Build(spec, cluster(server("alone", true)))

	want := []string{steps.TaskIDHandoff, "upgrade-alone"}
	if !slices.Equal(ids(got), want) {
		t.Errorf("tasks = %v, want %v", ids(got), want)
	}
}

// A survey taken mid-election has no primary. There is then nothing to hand
// off, and every server is simply upgraded in turn.
func TestBuild_NoPrimaryEmitsNoHandoff(t *testing.T) {
	got := steps.Build(spec, cluster(server("alpha", false), server("bravo", false)))

	want := []string{"upgrade-alpha", "upgrade-bravo"}
	if !slices.Equal(ids(got), want) {
		t.Errorf("tasks = %v, want %v", ids(got), want)
	}
}

func TestBuild_ClientsOnly(t *testing.T) {
	got := steps.Build(spec, cluster(client("bravo"), client("alpha")))

	want := []string{"upgrade-alpha", "upgrade-bravo"}
	if !slices.Equal(ids(got), want) {
		t.Errorf("tasks = %v, want %v", ids(got), want)
	}
}

func TestBuild_EmptyCluster(t *testing.T) {
	if got := steps.Build(spec, cluster()); len(got) != 0 {
		t.Errorf("tasks = %v, want none", ids(got))
	}
}

// Only the first server restart commits the run: before it, abandoning costs
// nothing.
func TestBuild_FirstServerTaskIsIrreversible(t *testing.T) {
	got := steps.Build(spec, cluster(server("alpha", false), server("bravo", true), client("charlie")))

	if !got[0].Irreversible {
		t.Errorf("first task %q is not marked irreversible", got[0].ID)
	}
	for _, task := range got[1:] {
		if task.Irreversible {
			t.Errorf("task %q also marked irreversible; only the first restart commits the run", task.ID)
		}
	}
}

// With no servers there is no coordination to disturb, so nothing commits the
// run.
func TestBuild_ClientsOnlyCommitsNothing(t *testing.T) {
	for _, task := range steps.Build(spec, cluster(client("alpha"))) {
		if task.Irreversible {
			t.Errorf("task %q marked irreversible without a server to restart", task.ID)
		}
	}
}

// Moving coordination and restarting the host that was coordinating are the
// two boundaries an operator has to assent to deliberately.
func TestBuild_Confirms(t *testing.T) {
	got := steps.Build(spec, cluster(server("alpha", false), server("bravo", true), client("charlie")))

	want := map[string]plan.Confirm{
		"upgrade-alpha":     plan.ConfirmPrompt,
		steps.TaskIDHandoff: plan.ConfirmTyped,
		"upgrade-bravo":     plan.ConfirmTyped,
		"upgrade-charlie":   plan.ConfirmNone,
	}
	for id, confirm := range want {
		if got := find(t, got, id).Confirm; got != confirm {
			t.Errorf("%s confirm = %q, want %q", id, got, confirm)
		}
	}
}

func TestBuild_StagesMatchTheHostKind(t *testing.T) {
	got := steps.Build(spec, cluster(server("alpha", true), client("bravo")))

	want := map[string]plan.Stage{
		"upgrade-alpha":     plan.StageServers,
		steps.TaskIDHandoff: plan.StageServers,
		"upgrade-bravo":     plan.StageClients,
	}
	for id, stage := range want {
		if got := find(t, got, id).Stage; got != stage {
			t.Errorf("%s stage = %q, want %q", id, got, stage)
		}
	}
}

// Every upgrade task carries the version it is aiming at, so the check does
// not depend on the runner remembering the spec.
func TestBuild_UpgradeTasksCarryTheTargetVersion(t *testing.T) {
	got := steps.Build(spec, cluster(server("alpha", false), client("bravo")))

	for _, id := range []string{"upgrade-alpha", "upgrade-bravo"} {
		task := find(t, got, id)
		if task.Checks["version"] != spec.To {
			t.Errorf("%s checks = %v, want version %q", id, task.Checks, spec.To)
		}
		if task.Action.Command != steps.CommandUpgrade {
			t.Errorf("%s command = %q, want %q", id, task.Action.Command, steps.CommandUpgrade)
		}
	}
}

// Survey order must not reach the task list, or the same cluster would plan
// differently depending on what the API happened to return first.
func TestBuild_IndependentOfSurveyOrder(t *testing.T) {
	forward := steps.Build(spec, cluster(
		server("alpha", false), server("bravo", true), client("charlie"), client("delta"),
	))
	reversed := steps.Build(spec, cluster(
		client("delta"), client("charlie"), server("bravo", true), server("alpha", false),
	))

	if !slices.Equal(ids(forward), ids(reversed)) {
		t.Errorf("order depends on survey order:\n  %v\n  %v", ids(forward), ids(reversed))
	}
}

// Build must not reorder the caller's survey as a side effect; the run stores
// that survey and it is meant to record what was seen.
func TestBuild_LeavesTheSurveyAlone(t *testing.T) {
	c := cluster(server("zulu", true), server("alpha", false))
	before := []string{c.Members[0].Name, c.Members[1].Name}

	steps.Build(spec, c)

	after := []string{c.Members[0].Name, c.Members[1].Name}
	if !slices.Equal(before, after) {
		t.Errorf("survey reordered: %v became %v", before, after)
	}
}
