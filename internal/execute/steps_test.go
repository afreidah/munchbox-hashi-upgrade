// -------------------------------------------------------------------------------
// Step Tests - What Each Command Does Against A Cluster That Answers
//
// Author: Alex Freidah
//
// Every case here runs in Live mode: the mode wrapper is covered next door, and
// what is worth asserting about a step is the call it makes, the compensation
// it leaves behind, and the work it declines to do twice.
// -------------------------------------------------------------------------------

package execute

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/ssh"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/steps"
)

var errCluster = errors.New("the cluster said no")

// live returns deps in Live mode with somewhere to write, since a step that
// prints to a nil writer is not what any of these are testing.
func live(deps *Deps) *Deps {
	deps.Mode = Live
	deps.Out = &bytes.Buffer{}
	return deps
}

// task builds a task naming a command and its arguments.
func task(command string, args map[string]any) plan.Task {
	return plan.Task{ID: command, Title: command, Action: plan.Action{Command: command, Args: args}}
}

// -------------------------------------------------------------------------
// FREEZE AND THAW
// -------------------------------------------------------------------------

// The compensation is the point of the freeze: a run that fails later must not
// leave the fleet with its automation switched off.
func TestFreezeStopsTheTimersAndCompensatesByReleasingThem(t *testing.T) {
	ctrl := gomock.NewController(t)
	hosts := []ssh.Target{{}, {}}

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Freeze(gomock.Any(), hosts).Return(nil)
	fleet.EXPECT().Thaw(gomock.Any(), hosts).Return(nil)

	deps := live(&Deps{Fleet: fleet, Hosts: hosts})

	result, err := freeze(deps)(context.Background(), task(steps.CommandFreeze, nil))
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if result.Undo == nil {
		t.Fatal("freeze left no compensation")
	}
	if err := result.Undo.Undo(context.Background()); err != nil {
		t.Fatalf("compensation: %v", err)
	}
}

// A freeze that failed has switched nothing on, so there is nothing to unwind.
func TestFreezeThatFailsLeavesNoCompensation(t *testing.T) {
	ctrl := gomock.NewController(t)

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Freeze(gomock.Any(), gomock.Any()).Return(errCluster)

	deps := live(&Deps{Fleet: fleet})

	result, err := freeze(deps)(context.Background(), task(steps.CommandFreeze, nil))
	if !errors.Is(err, errCluster) {
		t.Fatalf("err = %v, want %v", err, errCluster)
	}
	if result.Undo != nil {
		t.Error("a failed freeze left a compensation")
	}
}

func TestThawReleasesTheTimers(t *testing.T) {
	ctrl := gomock.NewController(t)
	hosts := []ssh.Target{{}}

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Thaw(gomock.Any(), hosts).Return(nil)

	deps := live(&Deps{Fleet: fleet, Hosts: hosts})

	if _, err := thaw(deps)(context.Background(), task(steps.CommandThaw, nil)); err != nil {
		t.Fatalf("thaw: %v", err)
	}
}

// -------------------------------------------------------------------------
// PIN
// -------------------------------------------------------------------------

func TestPinWritesTheNewVersionAndCompensatesWithTheOld(t *testing.T) {
	ctrl := gomock.NewController(t)

	versions := NewMockPinner(ctrl)
	versions.EXPECT().Pin(gomock.Any(), "nomad").Return("2.0.5", nil)
	versions.EXPECT().SetPin(gomock.Any(), "nomad", "2.0.6").Return(nil)
	versions.EXPECT().SetPin(gomock.Any(), "nomad", "2.0.5").Return(nil)

	deps := live(&Deps{Versions: versions})

	result, err := pin(deps)(context.Background(), task(steps.CommandPin, map[string]any{
		"tool": "nomad", "version": "2.0.6",
	}))
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	if result.Undo == nil {
		t.Fatal("pin left no compensation")
	}
	if !strings.Contains(result.Undo.Label, "2.0.5") {
		t.Errorf("label = %q, want it to name the version being restored", result.Undo.Label)
	}
	if err := result.Undo.Undo(context.Background()); err != nil {
		t.Fatalf("compensation: %v", err)
	}
}

// A pin already at the target is left alone, so a resumed run does not rewrite
// what it wrote the first time.
func TestPinAlreadyAtTheTargetIsUnnecessary(t *testing.T) {
	ctrl := gomock.NewController(t)

	versions := NewMockPinner(ctrl)
	versions.EXPECT().Pin(gomock.Any(), "nomad").Return("2.0.6", nil)

	deps := live(&Deps{Versions: versions})

	result, err := pin(deps)(context.Background(), task(steps.CommandPin, map[string]any{
		"tool": "nomad", "version": "2.0.6",
	}))
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	if result.Outcome != plan.Unnecessary {
		t.Errorf("outcome = %q, want %q", result.Outcome, plan.Unnecessary)
	}
	if result.Undo != nil {
		t.Error("a pin that wrote nothing left a compensation")
	}
}

func TestPinWithoutItsArgumentsStops(t *testing.T) {
	cases := map[string]map[string]any{
		"no tool":    {"version": "2.0.6"},
		"no version": {"tool": "nomad"},
		"empty tool": {"tool": "", "version": "2.0.6"},
	}

	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			deps := live(&Deps{Versions: NewMockPinner(gomock.NewController(t))})

			if _, err := pin(deps)(context.Background(), task(steps.CommandPin, args)); err == nil {
				t.Fatal("a task missing an argument was carried out anyway")
			}
		})
	}
}

// -------------------------------------------------------------------------
// UPGRADE
// -------------------------------------------------------------------------

// run builds a run whose survey holds one server and one client, both behind.
func run() *plan.Run {
	return &plan.Run{
		Spec: plan.Spec{Tool: plan.Nomad, To: "2.0.6"},
		Cluster: plan.Cluster{Members: []plan.Member{
			{Name: "server-a", Kind: plan.KindServer, Version: "2.0.5"},
			{Name: "client-a", Kind: plan.KindClient, Version: "2.0.5"},
			{Name: "done-a", Kind: plan.KindClient, Version: "2.0.6"},
		}},
	}
}

func targets(member string) (ssh.Target, error) { return ssh.Target{}, nil }

// A server has to rejoin the quorum, which is a different gate from a client
// merely answering again.
func TestUpgradeWaitsOnTheServerGateForAServer(t *testing.T) {
	ctrl := gomock.NewController(t)

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Converge(gomock.Any(), gomock.Any(), gomock.Any()).Return(ssh.Result{}, nil)

	wait := NewMockWaiter(ctrl)
	wait.EXPECT().Server(gomock.Any(), "server-a", "2.0.6", gomock.Any()).Return(nil)

	deps := live(&Deps{Run: run(), Fleet: fleet, Wait: wait, Target: targets})

	if _, err := upgrade(deps)(context.Background(), task(steps.CommandUpgrade, map[string]any{
		"member": "server-a",
	})); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
}

func TestUpgradeWaitsOnTheClientGateForAClient(t *testing.T) {
	ctrl := gomock.NewController(t)

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Converge(gomock.Any(), gomock.Any(), gomock.Any()).Return(ssh.Result{}, nil)

	wait := NewMockWaiter(ctrl)
	wait.EXPECT().Client(gomock.Any(), "client-a", "2.0.6").Return(nil)

	deps := live(&Deps{Run: run(), Fleet: fleet, Wait: wait, Target: targets})

	if _, err := upgrade(deps)(context.Background(), task(steps.CommandUpgrade, map[string]any{
		"member": "client-a",
	})); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
}

// This is what makes a resumed run cheap: the hosts already done cost one
// comparison each and are never restarted a second time.
func TestUpgradeSkipsAHostAlreadyAtTheTarget(t *testing.T) {
	ctrl := gomock.NewController(t)

	// No expectations on either: touching the host at all fails the test.
	deps := live(&Deps{
		Run:    run(),
		Fleet:  NewMockFleeter(ctrl),
		Wait:   NewMockWaiter(ctrl),
		Target: targets,
	})

	result, err := upgrade(deps)(context.Background(), task(steps.CommandUpgrade, map[string]any{
		"member": "done-a",
	}))
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if result.Outcome != plan.Unnecessary {
		t.Errorf("outcome = %q, want %q", result.Outcome, plan.Unnecessary)
	}
}

func TestUpgradeOfAHostTheSurveyDoesNotHoldStops(t *testing.T) {
	ctrl := gomock.NewController(t)

	deps := live(&Deps{
		Run:    run(),
		Fleet:  NewMockFleeter(ctrl),
		Wait:   NewMockWaiter(ctrl),
		Target: targets,
	})

	_, err := upgrade(deps)(context.Background(), task(steps.CommandUpgrade, map[string]any{
		"member": "ghost-a",
	}))
	if err == nil || !strings.Contains(err.Error(), "ghost-a") {
		t.Fatalf("err = %v, want it to name the missing member", err)
	}
}

// A converge that fails stops the run before the gate, so the run does not
// wait out a timeout on a host that never restarted.
func TestUpgradeThatFailsToConvergeNeverReachesTheGate(t *testing.T) {
	ctrl := gomock.NewController(t)

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Converge(gomock.Any(), gomock.Any(), gomock.Any()).Return(ssh.Result{}, errCluster)

	deps := live(&Deps{Run: run(), Fleet: fleet, Wait: NewMockWaiter(ctrl), Target: targets})

	if _, err := upgrade(deps)(context.Background(), task(steps.CommandUpgrade, map[string]any{
		"member": "server-a",
	})); !errors.Is(err, errCluster) {
		t.Fatalf("err = %v, want %v", err, errCluster)
	}
}

// -------------------------------------------------------------------------
// VERIFY
// -------------------------------------------------------------------------

func TestVerifyPassesWhenEveryMemberIsOnTheTarget(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(plan.Cluster{
		Healthy: true,
		Members: []plan.Member{
			{Name: "server-a", Version: "2.0.6"},
			{Name: "client-a", Version: "2.0.6"},
		},
	}, nil)

	deps := live(&Deps{Survey: survey})

	if _, err := verify(deps)(context.Background(), task(steps.CommandVerify, map[string]any{
		"version": "2.0.6",
	})); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// A host whose converge was a no-op passes every per-host gate and still runs
// the old binary, which is the whole reason this step re-reads the cluster.
func TestVerifyNamesTheMembersStillBehind(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(plan.Cluster{
		Healthy: true,
		Members: []plan.Member{
			{Name: "server-a", Version: "2.0.6"},
			{Name: "client-a", Version: "2.0.5"},
		},
	}, nil)

	deps := live(&Deps{Survey: survey})

	_, err := verify(deps)(context.Background(), task(steps.CommandVerify, map[string]any{
		"version": "2.0.6",
	}))
	if err == nil || !strings.Contains(err.Error(), "client-a=2.0.5") {
		t.Fatalf("err = %v, want it to name the member left behind", err)
	}
}

// Versions are checked first, so a fleet that is fully upgraded but still
// settling reports the settling and not a version problem it does not have.
func TestVerifyReportsAnUnhealthyClusterOnceVersionsAgree(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(plan.Cluster{
		Healthy: false,
		Members: []plan.Member{{Name: "server-a", Version: "2.0.6"}},
	}, nil)

	deps := live(&Deps{Survey: survey})

	_, err := verify(deps)(context.Background(), task(steps.CommandVerify, map[string]any{
		"version": "2.0.6",
	}))
	if err == nil || !strings.Contains(err.Error(), "not healthy") {
		t.Fatalf("err = %v, want it to report the cluster's health", err)
	}
}

func TestVerifyThatCannotReadTheClusterStops(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(plan.Cluster{}, errCluster)

	deps := live(&Deps{Survey: survey})

	if _, err := verify(deps)(context.Background(), task(steps.CommandVerify, map[string]any{
		"version": "2.0.6",
	})); !errors.Is(err, errCluster) {
		t.Fatalf("err = %v, want %v", err, errCluster)
	}
}

// Guards the runner's contract: a step that returns no outcome is taken as
// succeeded, so a successful step must not accidentally report otherwise.
func TestASuccessfulStepReportsNoOutcome(t *testing.T) {
	ctrl := gomock.NewController(t)

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Thaw(gomock.Any(), gomock.Any()).Return(nil)

	deps := live(&Deps{Fleet: fleet})

	result, err := thaw(deps)(context.Background(), task(steps.CommandThaw, nil))
	if err != nil {
		t.Fatalf("thaw: %v", err)
	}
	if result.Outcome != (runner.Result{}).Outcome {
		t.Errorf("outcome = %q, want it left empty", result.Outcome)
	}
}
