// -------------------------------------------------------------------------------
// Run Document - Spec, Cluster Survey, Tasks, Progress
//
// Author: Alex Freidah
//
// The shape of a run file. The spec, survey and task list are written once and
// never change; progress is a separate record per task id, so what happened
// accumulates against a fixed plan instead of overwriting it. Reading the file
// therefore answers both what was intended and how far it got, without one
// obscuring the other.
// -------------------------------------------------------------------------------

package plan

import (
	"cmp"
	"slices"
	"time"
)

// Schema is the run file format version. Open refuses anything else: a run is
// resumed by a later invocation, possibly a later build, and reinterpreting
// fields that have moved would resume the wrong upgrade.
const Schema = 1

// -------------------------------------------------------------------------
// SPEC
// -------------------------------------------------------------------------

// Tool is a HashiCorp binary this build knows how to roll.
type Tool string

const (
	Nomad  Tool = "nomad"
	Consul Tool = "consul"
	Vault  Tool = "vault"
)

// Known reports whether t is a tool this build understands.
func (t Tool) Known() bool {
	switch t {
	case Nomad, Consul, Vault:
		return true
	default:
		return false
	}
}

// Schedules reports whether t places work on its hosts.
//
// Only Nomad does. The others run an agent per host and carry nothing that
// could be moved off one, so draining has no meaning for them: a run against
// one neither plans a drain nor is given anything to drain with.
func (t Tool) Schedules() bool { return t == Nomad }

// Spec is what the run carries out.
//
// From records the pin as it stood when the run was generated, so a run states
// the change it represents and a resumed one cannot quietly become a different
// upgrade. Drain empties a client before restarting it and is off by default:
// restarting an agent does not stop its workloads, so draining relocates the
// whole cluster to avoid a disruption that does not occur.
type Spec struct {
	Tool  Tool
	From  string `yaml:",omitempty"`
	To    string
	Drain bool `yaml:",omitempty"`
}

// -------------------------------------------------------------------------
// CLUSTER SURVEY
// -------------------------------------------------------------------------

// Kind is what a host is to the cluster.
//
// A host that participates in coordination is KindServer even when it also
// accepts work: it holds one binary and one service, so it is upgraded once,
// with the servers. Counting it among the clients as well would restart a
// coordinating host twice and spend the cluster's fault tolerance twice for
// one host.
type Kind string

const (
	KindServer Kind = "server"
	KindClient Kind = "client"
)

// Member is one host as the survey found it.
//
// Addr is what the server and client views of a cluster are reconciled on;
// they disagree on name shape and do not both list every host.
//
// Primary is the leader, or whatever the tool calls the one host that
// coordinates. Voter is false for a tool whose coordination does not run on
// its own quorum. Status is recorded in the tool's own vocabulary rather than
// normalised, because collapsing three health vocabularies into one is an
// interpretation, and the survey observes.
//
// Healthy is the tool's own verdict on the host, normalised by the client
// package that speaks its vocabulary. For a Nomad server that is autopilot,
// which already folds trailing log distance, last contact and a stabilisation
// period into one answer; a second opinion here would be a second place for it
// to be wrong. StableSince is when that verdict last changed, which is what
// tells a host that has come back from a restart apart from one that has not
// gone down yet, and is zero where the tool does not report it.
//
// Eligible is whether the host accepts new work. A host that is up and
// ineligible is a host the fleet is no longer scheduling onto.
type Member struct {
	ID          string
	Name        string
	Addr        string
	Kind        Kind
	Version     string
	Status      string            `yaml:",omitempty"`
	Primary     bool              `yaml:",omitempty"`
	Voter       bool              `yaml:",omitempty"`
	Healthy     bool              `yaml:",omitempty"`
	Eligible    bool              `yaml:",omitempty"`
	StableSince time.Time         `yaml:"stable_since,omitempty"`
	Labels      map[string]string `yaml:",omitempty"`
}

// Cluster is the fleet as it was when the run was generated. It travels with
// the tasks so a run can be reviewed, resumed or handed on without re-querying
// a cluster that has since moved.
//
// Name is what the cluster calls itself, read from the cluster rather than
// configured here. Empty when the cluster does not publish one, which is not
// an error: it only costs the run a nicer filename.
//
// Tolerance is how many servers the cluster could lose without losing
// coordination, as the cluster itself reports it rather than as arithmetic on
// a voter count. A tool whose coordination does not run on its own quorum
// reports zero. Healthy is the same kind of fact one level up: the tool's
// verdict on the coordinating set as a whole.
type Cluster struct {
	Name       string    `yaml:",omitempty"`
	SurveyedAt time.Time `yaml:"surveyed_at"`
	Tolerance  int
	Healthy    bool `yaml:",omitempty"`
	Members    []Member
}

// Sort puts servers ahead of clients and orders each group by name, so one
// cluster always surveys to the same snapshot and two surveys can be compared.
//
// Presentation only: the order a run touches hosts in is derived from what
// they carry, not from this. Shared because every client package that builds a
// snapshot wants the same answer, and two of them sorting differently would
// make two clusters incomparable for no reason.
func (c Cluster) Sort() {
	slices.SortStableFunc(c.Members, func(a, b Member) int {
		if a.Kind != b.Kind {
			if a.Kind == KindServer {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
}

// Votes is whether coordination in this cluster runs on a quorum of its own
// members.
//
// Read from the cluster rather than decided by the tool, because it is a
// property of how the cluster is deployed and not of what it is. A Vault
// cluster keeping its data in Consul has no voters; the same Vault with
// integrated raft storage does. Asking the members means neither case has to
// be configured, and a cluster that is migrated between them is read correctly
// without being told.
//
// One host mid-restart has dropped out of its quorum while its peers have not,
// so any voter makes this a voting cluster.
func (c Cluster) Votes() bool {
	for _, m := range c.Members {
		if m.Voter {
			return true
		}
	}
	return false
}

// OfKind returns the members of one kind, in survey order.
func (c Cluster) OfKind(k Kind) []Member {
	var out []Member
	for _, m := range c.Members {
		if m.Kind == k {
			out = append(out, m)
		}
	}
	return out
}

// Primary returns the coordinating member, and whether the survey found one. A
// survey taken during an election legitimately has none.
func (c Cluster) Primary() (Member, bool) {
	for _, m := range c.Members {
		if m.Primary {
			return m, true
		}
	}
	return Member{}, false
}

// -------------------------------------------------------------------------
// TASKS
// -------------------------------------------------------------------------

// Stage groups tasks by the part of the run they belong to. Stages run in the
// order declared here and a run never interleaves them: every server is
// upgraded before the first client, because a cluster tolerates its servers
// ahead of its clients and not the reverse.
type Stage string

const (
	StageSurvey  Stage = "survey"  // freeze automation, prove the cluster is fit to start
	StageServers Stage = "servers" // coordinating hosts, one at a time, primary last
	StageClients Stage = "clients" // everything that does not coordinate
	StageVerify  Stage = "verify"  // confirm the end state, unwind what survey set up
)

// Confirm is how much an operator has to assent to a task before it runs.
//
// It is a property of the task rather than a decision the runner makes, so the
// run file itself records which boundaries need a person. Restarting a client
// is routine; moving coordination off the primary is not.
type Confirm string

const (
	ConfirmNone   Confirm = "none"   // runs unattended
	ConfirmPrompt Confirm = "prompt" // yes or no
	ConfirmTyped  Confirm = "typed"  // type a value back, so it cannot be reflex
)

// Action is how a task is carried out, named as the subcommand that carries it
// out. Recording it this way means any task can be run on its own, which is
// how a run that fails partway is driven by hand.
type Action struct {
	Command string
	Args    map[string]any `yaml:",omitempty"`
}

// Task is one unit of a run.
//
// Member is the host the task acts on, empty for a task that acts on the
// cluster as a whole. Irreversible marks the first task that changes the
// cluster in a way the run cannot walk away from: before it, abandoning costs
// nothing; after it, the cluster is part-upgraded and finishing is the safe
// direction. Checks is what must hold afterwards for the run to continue,
// verified independently of whether the task itself reported success.
type Task struct {
	ID           string
	Title        string
	Stage        Stage
	Member       string `yaml:",omitempty"`
	Action       Action
	Confirm      Confirm
	Irreversible bool           `yaml:",omitempty"`
	Checks       map[string]any `yaml:",omitempty"`
}

// -------------------------------------------------------------------------
// PROGRESS
// -------------------------------------------------------------------------

// Outcome is how a task ended, or that it has not.
//
// Active is the state an interrupt leaves behind: the task was started and
// never reported back, so whether it took effect is unknown and it needs
// checking by hand rather than blind retry. Unnecessary covers work that did
// not need doing, most often a member already at the target version.
type Outcome string

const (
	Waiting     Outcome = "waiting"
	Active      Outcome = "active"
	Succeeded   Outcome = "succeeded"
	Failed      Outcome = "failed"
	Unnecessary Outcome = "unnecessary"
)

// Settled reports whether a run may move past a task that ended this way.
// Failed is deliberately not settled: a resumed run has to land on the failure
// rather than step over it.
func (o Outcome) Settled() bool {
	return o == Succeeded || o == Unnecessary
}

// Record is what happened to one task. Absent from a run's progress map means
// the task has not been reached.
type Record struct {
	Outcome    Outcome
	StartedAt  *time.Time     `yaml:"started_at,omitempty"`
	FinishedAt *time.Time     `yaml:"finished_at,omitempty"`
	Exit       map[string]int `yaml:",omitempty"`
	Output     string         `yaml:",omitempty"`
	Err        string         `yaml:",omitempty"`
}

// -------------------------------------------------------------------------
// RUN
// -------------------------------------------------------------------------

// Run is the whole document and the source of truth for an upgrade.
// Everything needed to resume, audit or hand one off lives here.
//
// The file it came from is remembered rather than passed back in on every
// write, so a caller that holds a run cannot save it somewhere the rest of the
// run is not.
type Run struct {
	path string

	Schema    int       `yaml:"schema"`
	Built     string    `yaml:"built_by"`
	CreatedAt time.Time `yaml:"created_at"`

	Spec     Spec
	Cluster  Cluster
	Tasks    []Task
	Progress map[string]Record `yaml:",omitempty"`
}

// Path returns the file this run reads from and writes to.
func (r *Run) Path() string { return r.path }

// Outcome returns how a task ended, or Waiting when it has not been reached.
func (r *Run) Outcome(id string) Outcome {
	rec, ok := r.Progress[id]
	if !ok || rec.Outcome == "" {
		return Waiting
	}
	return rec.Outcome
}

// Next returns the first task whose outcome is not settled, and whether one
// remains. This is the resume point: a run is picked back up from here rather
// than from the beginning.
func (r *Run) Next() (*Task, bool) {
	for i := range r.Tasks {
		if !r.Outcome(r.Tasks[i].ID).Settled() {
			return &r.Tasks[i], true
		}
	}
	return nil, false
}

// Finished reports whether every task has settled.
func (r *Run) Finished() bool {
	_, ok := r.Next()
	return !ok
}

// Committed reports whether the run has completed a task it cannot walk away
// from. Abandoning after this leaves the cluster part-upgraded.
func (r *Run) Committed() bool {
	for _, t := range r.Tasks {
		if t.Irreversible && r.Outcome(t.ID) == Succeeded {
			return true
		}
	}
	return false
}

// Begin marks a task active and stamps its start. The caller saves; recording
// and persisting are separate so a caller can make several changes and write
// once.
func (r *Run) Begin(id string, at time.Time) {
	rec := r.record(id)
	rec.Outcome = Active
	rec.StartedAt = &at
	r.Progress[id] = rec
}

// Settle records how a task ended and stamps its finish.
func (r *Run) Settle(id string, outcome Outcome, at time.Time, err error) {
	rec := r.record(id)
	rec.Outcome = outcome
	rec.FinishedAt = &at
	if err != nil {
		rec.Err = err.Error()
	}
	r.Progress[id] = rec
}

// Reset forgets what happened to a task, so a run reaches it again as though
// it had not been tried.
//
// This is how a failed or interrupted task is put back into play. A run refuses
// to step over one, because whether it took effect is unknown and the answer is
// an operator looking at the host -- so the record is cleared deliberately,
// once they have, rather than by a retry that decides for them.
//
// Reports whether there was anything to forget, which tells a caller naming a
// task apart from one naming a task that had not run.
func (r *Run) Reset(id string) bool {
	if _, ok := r.Progress[id]; !ok {
		return false
	}

	delete(r.Progress, id)
	return true
}

// record returns the existing record for a task, or a blank one, initialising
// the map so a run built without progress can still be written to.
func (r *Run) record(id string) Record {
	if r.Progress == nil {
		r.Progress = make(map[string]Record)
	}
	return r.Progress[id]
}
