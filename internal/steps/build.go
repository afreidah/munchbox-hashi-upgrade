// -------------------------------------------------------------------------------
// Task Order - Servers, Handoff, Clients
//
// Author: Alex Freidah
//
// Turns a cluster survey into the ordered tasks that upgrade it. Every host in
// the survey gets a task, including one already at the target version: a
// host absent from a run cannot then be read as either finished or overlooked,
// and whether the work is needed is settled against the live cluster when the
// task is reached.
// -------------------------------------------------------------------------------

package steps

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// Subcommands a task names as the thing that carries it out. Recorded in the
// task so any one of them can be run on its own, which is how a run that fails
// partway is driven by hand.
const (
	CommandFreeze  = "freeze-converge"
	CommandPin     = "set-version-pin"
	CommandUpgrade = "upgrade-member"
	CommandHandoff = "hand-off-coordination"
	CommandVerify  = "verify-cluster"
	CommandThaw    = "thaw-converge"
)

// Task ids for the steps that act on the fleet rather than on one host.
const (
	TaskIDFreeze  = "freeze-converge-timers"
	TaskIDPin     = "set-version-pin"
	TaskIDHandoff = "hand-off-coordination"
	TaskIDVerify  = "verify-cluster"
	TaskIDThaw    = "thaw-converge-timers"
)

const upgradeIDPrefix = "upgrade"

// Build returns the tasks that carry out spec against cluster.
//
// The run is bracketed: the converge timers are stopped and the version pin
// set before any host is touched, and the timers released only once the end
// state has been confirmed. In between, servers go one at a time with the
// coordinating host last, then clients one at a time. Within each group the
// order is by name, so one survey always produces one task list.
//
// An empty cluster still gets the bracket. A run against nothing is a strange
// thing to ask for, but silently producing no tasks would read as success.
func Build(spec plan.Spec, cluster plan.Cluster) []plan.Task {
	servers := sortedByName(cluster.OfKind(plan.KindServer))
	clients := sortedByName(cluster.OfKind(plan.KindClient))

	tasks := make([]plan.Task, 0, len(servers)+len(clients)+5)
	tasks = append(tasks, freezeTask(), pinTask(spec))
	tasks = append(tasks, serverTasks(spec, servers)...)
	// Confirmed like the servers. Restarting a client costs the cluster no
	// fault tolerance, but it does interrupt the work running on it, and a host
	// that restarts with nobody watching is one nobody notices failing.
	for _, c := range clients {
		tasks = append(tasks, upgradeTask(spec, c, plan.StageClients, plan.ConfirmPrompt))
	}
	return append(tasks, verifyTask(spec), thawTask())
}

// freezeTask stops the scheduled converges fleet-wide.
//
// It runs before the pin is set, not after: a timer that fires between the two
// would converge that host to the new version outside the run's order, with
// none of its gates or health checks. The checks cover the whole fleet because
// the task does -- one task, every node underneath -- and they include that no
// converge is already in flight, since one that is will happily pick up the new
// pin the moment it is written.
func freezeTask() plan.Task {
	return plan.Task{
		ID:      TaskIDFreeze,
		Title:   "Stop scheduled converges across the fleet",
		Stage:   plan.StageSurvey,
		Action:  plan.Action{Command: CommandFreeze},
		Confirm: plan.ConfirmPrompt,
		Checks:  map[string]any{"timers_stopped": true, "converge_running": false},
	}
}

// pinTask records the version every host will converge to. Nothing installs
// anything until a host is reached, but from here the fleet's declared version
// is the new one, which is why the timers are stopped first.
func pinTask(spec plan.Spec) plan.Task {
	return plan.Task{
		ID:      TaskIDPin,
		Title:   fmt.Sprintf("Pin %s to %s", spec.Tool, spec.To),
		Stage:   plan.StageSurvey,
		Action:  plan.Action{Command: CommandPin, Args: map[string]any{"tool": string(spec.Tool), "version": spec.To}},
		Confirm: plan.ConfirmTyped,
		Checks:  map[string]any{"pin": spec.To},
	}
}

// verifyTask confirms the fleet arrived where the run intended before the
// timers are released.
func verifyTask(spec plan.Spec) plan.Task {
	return plan.Task{
		ID:      TaskIDVerify,
		Title:   fmt.Sprintf("Confirm the fleet is running %s", spec.To),
		Stage:   plan.StageVerify,
		Action:  plan.Action{Command: CommandVerify, Args: map[string]any{"version": spec.To}},
		Confirm: plan.ConfirmNone,
		Checks:  map[string]any{"version": spec.To, "healthy": true},
	}
}

// thawTask restores the scheduled converges. It is last so nothing converges
// on its own until the run has said the fleet is where it should be.
func thawTask() plan.Task {
	return plan.Task{
		ID:      TaskIDThaw,
		Title:   "Restore scheduled converges across the fleet",
		Stage:   plan.StageVerify,
		Action:  plan.Action{Command: CommandThaw},
		Confirm: plan.ConfirmNone,
		Checks:  map[string]any{"timers_stopped": false},
	}
}

// serverTasks orders the coordinating hosts: every other server first, then
// the handoff, then the host that was coordinating.
//
// The primary goes last so the cluster spends most of the stage with settled
// coordination, and the handoff is separate so it can be confirmed, retried
// and read on its own. It names no destination: the runner picks one when it
// gets there, from a cluster it has just checked, rather than from a survey
// taken before anything was touched.
func serverTasks(spec plan.Spec, servers []plan.Member) []plan.Task {
	var (
		others  []plan.Member
		primary *plan.Member
	)
	for i, s := range servers {
		if s.Primary && primary == nil {
			primary = &servers[i]
			continue
		}
		others = append(others, s)
	}

	tasks := make([]plan.Task, 0, len(servers)+1)
	for _, s := range others {
		tasks = append(tasks, upgradeTask(spec, s, plan.StageServers, plan.ConfirmPrompt))
	}
	if primary == nil {
		return markFirstIrreversible(tasks)
	}

	tasks = append(tasks,
		handoffTask(*primary),
		upgradeTask(spec, *primary, plan.StageServers, plan.ConfirmTyped),
	)
	return markFirstIrreversible(tasks)
}

// upgradeTask is one host's upgrade.
func upgradeTask(spec plan.Spec, m plan.Member, stage plan.Stage, confirm plan.Confirm) plan.Task {
	return plan.Task{
		ID:      fmt.Sprintf("%s-%s", upgradeIDPrefix, m.Name),
		Title:   fmt.Sprintf("Upgrade %s to %s", m.Name, spec.To),
		Stage:   stage,
		Member:  m.Name,
		Action:  plan.Action{Command: CommandUpgrade, Args: map[string]any{"member": m.Name}},
		Confirm: confirm,
		Checks:  map[string]any{"version": spec.To},
	}
}

// handoffTask moves coordination off a host before that host is restarted.
func handoffTask(primary plan.Member) plan.Task {
	return plan.Task{
		ID:      TaskIDHandoff,
		Title:   fmt.Sprintf("Hand coordination off %s", primary.Name),
		Stage:   plan.StageServers,
		Member:  primary.Name,
		Action:  plan.Action{Command: CommandHandoff, Args: map[string]any{"from": primary.Name}},
		Confirm: plan.ConfirmTyped,
		Checks:  map[string]any{"primary": false},
	}
}

// markFirstIrreversible marks the point a run can no longer simply be
// abandoned. Restarting the first server puts the cluster mid-upgrade, and
// from there finishing is the safe direction; before it, walking away costs
// nothing.
func markFirstIrreversible(tasks []plan.Task) []plan.Task {
	if len(tasks) > 0 {
		tasks[0].Irreversible = true
	}
	return tasks
}

// sortedByName copies and orders members so Build does not depend on survey
// order and does not disturb the caller's slice.
func sortedByName(members []plan.Member) []plan.Member {
	out := slices.Clone(members)
	slices.SortStableFunc(out, func(a, b plan.Member) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}
