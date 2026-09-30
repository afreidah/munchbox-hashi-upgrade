// -------------------------------------------------------------------------------
// Drain Tests - When A Host Is Emptied, And When It Is Put Back
//
// Author: Alex Freidah
//
// Draining is off by default, so the first thing worth asserting is that a run
// which did not ask for it never touches eligibility. The rest is ordering: the
// host has to be eligible again before the gate that requires it, and it has to
// be put back whether the run succeeded or not.
// -------------------------------------------------------------------------------

package execute

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/ssh"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/steps"
)

// draining returns a run that asks for hosts to be emptied first.
func draining() *plan.Run {
	r := run()
	r.Spec.Drain = true
	return r
}

func upgradeTask(member string) plan.Task {
	return task(steps.CommandUpgrade, map[string]any{"member": member})
}

// -------------------------------------------------------------------------
// OFF BY DEFAULT
// -------------------------------------------------------------------------

// Restarting an agent does not stop its work, so a run that did not ask to
// drain must not relocate anything. The mock carries no expectations, so any
// call to it fails the test.
func TestUpgradeDoesNotDrainUnlessAsked(t *testing.T) {
	ctrl := gomock.NewController(t)

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Converge(gomock.Any(), gomock.Any(), gomock.Any()).Return(ssh.Result{}, nil)

	wait := NewMockWaiter(ctrl)
	wait.EXPECT().Client(gomock.Any(), "client-a", "2.0.6").Return(nil)

	deps := live(&Deps{
		Run: run(), Fleet: fleet, Wait: wait,
		Drains: NewMockDrainer(ctrl), Target: targets,
	})

	result, err := upgrade(deps)(context.Background(), upgradeTask("client-a"))
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if result.Undo != nil {
		t.Error("a run that did not drain left a compensation")
	}
}

// A server's allocations are not why it is restarted, and marking one
// ineligible does nothing for the quorum.
func TestUpgradeNeverDrainsAServer(t *testing.T) {
	ctrl := gomock.NewController(t)

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Converge(gomock.Any(), gomock.Any(), gomock.Any()).Return(ssh.Result{}, nil)

	wait := NewMockWaiter(ctrl)
	wait.EXPECT().Server(gomock.Any(), "server-a", "2.0.6").Return(nil)

	deps := live(&Deps{
		Run: draining(), Fleet: fleet, Wait: wait,
		Drains: NewMockDrainer(ctrl), Target: targets,
	})

	if _, err := upgrade(deps)(context.Background(), upgradeTask("server-a")); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
}

// -------------------------------------------------------------------------
// THE DRAINING PATH
// -------------------------------------------------------------------------

// The order is the point: drain, converge, put the host back, then wait. The
// client gate requires eligibility, so undraining after it would wait out a
// condition this step is holding shut.
func TestUpgradeDrainsThenRestoresEligibilityBeforeTheGate(t *testing.T) {
	ctrl := gomock.NewController(t)

	drains := NewMockDrainer(ctrl)
	fleet := NewMockFleeter(ctrl)
	wait := NewMockWaiter(ctrl)

	gomock.InOrder(
		drains.EXPECT().Drain(gomock.Any(), "id-client-a", drainDeadline).Return(nil),
		fleet.EXPECT().Converge(gomock.Any(), gomock.Any(), gomock.Any()).Return(ssh.Result{}, nil),
		drains.EXPECT().Undrain(gomock.Any(), "id-client-a").Return(nil),
		wait.EXPECT().Client(gomock.Any(), "client-a", "2.0.6").Return(nil),
	)

	deps := live(&Deps{
		Run: draining(), Fleet: fleet, Wait: wait,
		Drains: drains, Target: targets,
	})

	if _, err := upgrade(deps)(context.Background(), upgradeTask("client-a")); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
}

// Eligibility is orchestration state, so it is the one thing a failed run can
// and must put back. The compensation is what a later failure unwinds.
func TestADrainedHostIsPutBackByTheCompensation(t *testing.T) {
	ctrl := gomock.NewController(t)

	drains := NewMockDrainer(ctrl)
	drains.EXPECT().Drain(gomock.Any(), "id-client-a", drainDeadline).Return(nil)
	// Once on the way through, once more when the compensation fires.
	drains.EXPECT().Undrain(gomock.Any(), "id-client-a").Return(nil).Times(2)

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Converge(gomock.Any(), gomock.Any(), gomock.Any()).Return(ssh.Result{}, nil)

	wait := NewMockWaiter(ctrl)
	wait.EXPECT().Client(gomock.Any(), "client-a", "2.0.6").Return(nil)

	deps := live(&Deps{
		Run: draining(), Fleet: fleet, Wait: wait,
		Drains: drains, Target: targets,
	})

	result, err := upgrade(deps)(context.Background(), upgradeTask("client-a"))
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if result.Undo == nil {
		t.Fatal("a drained host left no compensation")
	}
	if err := result.Undo.Undo(context.Background()); err != nil {
		t.Fatalf("compensation: %v", err)
	}
}

// -------------------------------------------------------------------------
// FAILURES
// -------------------------------------------------------------------------

// A drain that cannot finish stops the run before the host is restarted, so a
// host that would not shed its work is not restarted anyway.
func TestADrainThatFailsNeverConverges(t *testing.T) {
	ctrl := gomock.NewController(t)

	drains := NewMockDrainer(ctrl)
	drains.EXPECT().Drain(gomock.Any(), "id-client-a", drainDeadline).Return(errCluster)

	deps := live(&Deps{
		Run: draining(), Fleet: NewMockFleeter(ctrl), Wait: NewMockWaiter(ctrl),
		Drains: drains, Target: targets,
	})

	if _, err := upgrade(deps)(context.Background(), upgradeTask("client-a")); !errors.Is(err, errCluster) {
		t.Fatalf("err = %v, want %v", err, errCluster)
	}
}

// A host left ineligible is quietly out of the cluster's capacity, so failing
// to put it back is a failure of the step rather than a detail to log.
func TestAFailureToRestoreEligibilityStopsTheRun(t *testing.T) {
	ctrl := gomock.NewController(t)

	drains := NewMockDrainer(ctrl)
	drains.EXPECT().Drain(gomock.Any(), "id-client-a", drainDeadline).Return(nil)
	drains.EXPECT().Undrain(gomock.Any(), "id-client-a").Return(errCluster)

	fleet := NewMockFleeter(ctrl)
	fleet.EXPECT().Converge(gomock.Any(), gomock.Any(), gomock.Any()).Return(ssh.Result{}, nil)

	deps := live(&Deps{
		Run: draining(), Fleet: fleet, Wait: NewMockWaiter(ctrl),
		Drains: drains, Target: targets,
	})

	if _, err := upgrade(deps)(context.Background(), upgradeTask("client-a")); !errors.Is(err, errCluster) {
		t.Fatalf("err = %v, want %v", err, errCluster)
	}
}
