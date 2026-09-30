// -------------------------------------------------------------------------------
// Registry Tests - Coverage Of The Table, And What Each Mode Costs
//
// Author: Alex Freidah
//
// The table is joined to steps.Build by a string, which the compiler does not
// check, so a command that loses its implementation surfaces only when a real
// run reaches it. These hold the two together, and hold the modes to the
// promise that neither NoOp nor DryRun touches anything that writes.
//
// An internal test because impls is unexported, and its wiring is the
// package's own business.
// -------------------------------------------------------------------------------

package execute

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/steps"
)

// commands returns every command steps.Build can emit, from a cluster shaped to
// produce all of them: a primary, a second server and a client.
func commands() map[string]bool {
	cluster := plan.Cluster{Members: []plan.Member{
		{Name: "server-a", Kind: plan.KindServer, Primary: true},
		{Name: "server-b", Kind: plan.KindServer},
		{Name: "client-a", Kind: plan.KindClient},
	}}

	out := map[string]bool{}
	for _, task := range steps.Build(plan.Spec{Tool: plan.Nomad, To: "2.0.6"}, cluster) {
		out[task.Action.Command] = true
	}
	return out
}

// -------------------------------------------------------------------------
// WIRING
// -------------------------------------------------------------------------

// A registered command that no task names is dead code: it can never run, and
// it is usually a key that was renamed on one side only.
func TestImplsAreAllRealCommands(t *testing.T) {
	real := commands()

	for command := range impls {
		if !real[command] {
			t.Errorf("impls registers %q, which steps.Build never emits", command)
		}
	}
}

// The other direction: a command a task can name and no step implements stops
// a run partway, so every one steps.Build emits has to be registered here.
func TestEveryCommandHasAnImplementation(t *testing.T) {
	for command := range commands() {
		if _, ok := impls[command]; !ok {
			t.Errorf("steps.Build emits %q, which impls does not register", command)
		}
	}
}

func TestStepsCoversEveryImplementation(t *testing.T) {
	if got, want := len(Steps(&Deps{})), len(impls); got != want {
		t.Errorf("Steps returned %d steps, want %d", got, want)
	}
}

// -------------------------------------------------------------------------
// MODES
// -------------------------------------------------------------------------

// A no-op reports what it would do and settles the task as unnecessary, so the
// run advances without the file ever claiming the work was done.
func TestNoOpRunsNothingAndSettlesUnnecessary(t *testing.T) {
	ctrl := gomock.NewController(t)

	var out bytes.Buffer
	deps := &Deps{Fleet: NewMockFleeter(ctrl), Out: &out, Mode: NoOp}

	task := plan.Task{Title: "Hold the converges", Action: plan.Action{Command: steps.CommandFreeze}}
	result, err := Steps(deps)[steps.CommandFreeze](context.Background(), task)

	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if result.Outcome != plan.Unnecessary {
		t.Errorf("outcome = %q, want %q", result.Outcome, plan.Unnecessary)
	}
	if !strings.Contains(out.String(), "would run ("+steps.CommandFreeze+")") {
		t.Errorf("output = %q, want it to report what it would run", out.String())
	}
}

// A dry run is print-only for a step that writes. The mock carries no
// expectations, so any call to the fleet fails the test.
func TestDryRunDoesNotCallAStepThatWrites(t *testing.T) {
	ctrl := gomock.NewController(t)

	var out bytes.Buffer
	deps := &Deps{Fleet: NewMockFleeter(ctrl), Out: &out, Mode: DryRun}

	task := plan.Task{Title: "Hold the converges", Action: plan.Action{Command: steps.CommandFreeze}}
	result, err := Steps(deps)[steps.CommandFreeze](context.Background(), task)

	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if result.Outcome != plan.Unnecessary {
		t.Errorf("outcome = %q, want %q", result.Outcome, plan.Unnecessary)
	}
	if !strings.Contains(out.String(), "would write ("+steps.CommandFreeze+")") {
		t.Errorf("output = %q, want it to report what it would write", out.String())
	}
}

// The point of a dry run over a no-op: a step that only reads is carried out,
// so the rehearsal proves the cluster answers.
func TestDryRunCallsAStepThatOnlyReads(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().
		Health(gomock.Any()).
		Return(plan.Cluster{
			Healthy: true,
			Members: []plan.Member{{Name: "server-a", Version: "2.0.6"}},
		}, nil)

	deps := &Deps{Survey: survey, Out: &bytes.Buffer{}, Mode: DryRun}

	task := plan.Task{
		Title:  "Confirm the fleet",
		Action: plan.Action{Command: steps.CommandVerify, Args: map[string]any{"version": "2.0.6"}},
	}
	if _, err := Steps(deps)[steps.CommandVerify](context.Background(), task); err != nil {
		t.Fatalf("verify: %v", err)
	}
}
