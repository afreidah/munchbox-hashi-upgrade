// -------------------------------------------------------------------------------
// Registry - Which Step Carries Out Which Command
//
// Author: Alex Freidah
//
// The one place a command is joined to the code that performs it. A command
// absent from the table has no implementation, and a live run refuses it by
// name rather than stepping over it: a run that silently skipped the version
// pin would report success over a cluster it never touched.
//
// The table is keyed on the command constants rather than their string values,
// so a rename is a compile error instead of a run that stops halfway through a
// real upgrade.
//
// The clients are taken as interfaces declared here rather than as the client
// packages' own types, so this package's tests run against generated mocks and
// never need a cluster, a cinc server or an ssh agent.
// -------------------------------------------------------------------------------

package execute

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/ssh"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/steps"
)

//go:generate mockgen -destination=mock_generated_test.go -package=execute github.com/afreidah/munchbox-hashi-upgrade/internal/execute Pinner,Fleeter,Surveyor,Waiter,Coordinator,Drainer

// -------------------------------------------------------------------------
// WHAT A STEP NEEDS
// -------------------------------------------------------------------------

// Pinner reads and writes the version the fleet converges toward.
type Pinner interface {
	Pin(ctx context.Context, tool string) (string, error)
	SetPin(ctx context.Context, tool, version string) error
}

// Fleeter drives cinc over ssh: the converge itself, and the timers that would
// otherwise run one unobserved.
type Fleeter interface {
	Freeze(ctx context.Context, targets []ssh.Target) error
	Thaw(ctx context.Context, targets []ssh.Target) error
	Converge(ctx context.Context, target ssh.Target, out io.Writer) (ssh.Result, error)
}

// Surveyor reads the cluster as it is now, which is what the closing
// verification holds the run against.
type Surveyor interface {
	Health(ctx context.Context) (plan.Cluster, error)
}

// Waiter blocks until the cluster has taken a host back. Server is the
// stronger condition of the two: rejoining the quorum, not merely answering.
// Coordination waits the other way, for a host to stop being the one that
// coordinates.
type Waiter interface {
	Server(ctx context.Context, name, target string, restarted time.Time) error
	Client(ctx context.Context, name, target string) error
	Coordination(ctx context.Context, from string) error
}

// Coordinator moves coordination between servers. Separate from Surveyor
// because reading a cluster and changing which host leads it are different
// permissions, and only one step needs the second.
type Coordinator interface {
	Handoff(ctx context.Context, id string) error
}

// Drainer empties a host of work and puts it back in service. Only reached
// when the run asked to drain, which is off by default.
type Drainer interface {
	Drain(ctx context.Context, id string, deadline time.Duration) error
	Undrain(ctx context.Context, id string) error
}

// -------------------------------------------------------------------------
// MODE
// -------------------------------------------------------------------------

// Mode is how much of a run actually happens.
//
// DryRun invokes the steps that only read and prints what the rest would do,
// which is the difference from NoOp: a dry run proves the cluster answers, a
// no-op proves only that the run file parses.
type Mode string

const (
	Live   Mode = "live"
	DryRun Mode = "dry-run"
	NoOp   Mode = "no-op"
)

// -------------------------------------------------------------------------
// DEPENDENCIES
// -------------------------------------------------------------------------

// Deps are what a step reaches the cluster through.
//
// Run is the document being carried out. A step is handed one task, which
// names a host but not the version the run moves it to nor what that host is
// to the cluster, so the spec and the survey are reached through here.
//
// Target resolves a task's member name to somewhere ssh can land, because a
// survey records what the cluster calls a host and not how to log into it.
// Hosts is every host in the survey, for the tasks that act on the fleet
// rather than on one member and so have no member name to resolve.
type Deps struct {
	Run          *plan.Run
	Versions     Pinner
	Fleet        Fleeter
	Survey       Surveyor
	Wait         Waiter
	Coordination Coordinator
	Drains       Drainer
	Target       func(member string) (ssh.Target, error)
	Hosts        []ssh.Target
	Out          io.Writer
	Mode         Mode
}

// -------------------------------------------------------------------------
// TABLE
// -------------------------------------------------------------------------

// builder makes a step bound to the clients it needs.
type builder func(*Deps) runner.Step

// impl is an implementation plus whether it is safe under DryRun. A step that
// only reads behaves the same in every mode; one that writes is print-only.
type impl struct {
	build    builder
	readOnly bool
}

// impls joins each command to its implementation. Entries follow the order
// steps.Build emits them.
var impls = map[string]impl{
	steps.CommandFreeze:  {build: freeze, readOnly: false},
	steps.CommandPin:     {build: pin, readOnly: false},
	steps.CommandUpgrade: {build: upgrade, readOnly: false},
	steps.CommandHandoff: {build: handoff, readOnly: false},
	steps.CommandVerify:  {build: verify, readOnly: true},
	steps.CommandThaw:    {build: thaw, readOnly: false},
}

// -------------------------------------------------------------------------
// ASSEMBLY
// -------------------------------------------------------------------------

// Steps returns the step set for a runner, bound to deps.
//
// The runner already refuses a task whose command it holds no step for, so
// absence is the refusal and there is no second way to express it.
func Steps(deps *Deps) map[string]runner.Step {
	out := make(map[string]runner.Step, len(impls))
	for command, i := range impls {
		out[command] = gate(i, deps)
	}
	return out
}

// gate wraps a step in the mode's behaviour, so a step is written as though it
// always runs and the table decides whether it does.
func gate(i impl, deps *Deps) runner.Step {
	step := i.build(deps)

	return func(ctx context.Context, task plan.Task) (runner.Result, error) {
		switch {
		case deps.Mode == NoOp:
			return describe(deps.Out, task, "would run")
		case deps.Mode == DryRun && !i.readOnly:
			return describe(deps.Out, task, "would write")
		default:
			return step(ctx, task)
		}
	}
}

// describe prints what a task would have done and settles it as unnecessary,
// so a rehearsed run advances through the file without claiming the work
// succeeded.
//
// The runner has already announced the task, so this adds only what the mode
// withheld and names the command, which the title does not.
func describe(out io.Writer, task plan.Task, verb string) (runner.Result, error) {
	if out != nil {
		fmt.Fprintf(out, "  %s (%s)\n", verb, task.Action.Command)
	}
	return runner.Result{Outcome: plan.Unnecessary}, nil
}
