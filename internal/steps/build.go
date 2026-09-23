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
	CommandUpgrade  = "upgrade-member"
	CommandHandoff  = "hand-off-coordination"
	TaskIDHandoff   = "hand-off-coordination"
	upgradeIDPrefix = "upgrade"
)

// Build returns the tasks that carry out spec against cluster.
//
// Servers come first, one at a time, with the coordinating host last and its
// coordination handed off in a task of its own beforehand. Clients follow, one
// at a time. Within each group the order is by name, so one survey always
// produces one task list.
func Build(spec plan.Spec, cluster plan.Cluster) []plan.Task {
	servers := sortedByName(cluster.OfKind(plan.KindServer))
	clients := sortedByName(cluster.OfKind(plan.KindClient))

	tasks := make([]plan.Task, 0, len(servers)+len(clients)+1)
	tasks = append(tasks, serverTasks(spec, servers)...)
	for _, c := range clients {
		tasks = append(tasks, upgradeTask(spec, c, plan.StageClients, plan.ConfirmNone))
	}
	return tasks
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
