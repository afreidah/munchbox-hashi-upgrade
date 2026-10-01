// -------------------------------------------------------------------------------
// Handoff Tests - Who Takes Over, And When Not To Bother
//
// Author: Alex Freidah
//
// The destination is chosen here rather than recorded in the run file, so these
// cover the choosing: that it lands on a healthy voter, that it is the same
// choice twice, and that it refuses rather than guessing when no server is fit.
// -------------------------------------------------------------------------------

package execute

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/steps"
)

// coordinating returns a cluster whose primary is server-b.
func coordinating(members ...plan.Member) plan.Cluster {
	return plan.Cluster{Healthy: true, Members: members}
}

func voter(name string, primary bool) plan.Member {
	return plan.Member{
		ID: "id-" + name, Name: name, Kind: plan.KindServer,
		Voter: true, Healthy: true, Primary: primary,
	}
}

// unvoting is a server in a cluster whose coordination does not run on a
// quorum of its own members, as a Vault cluster keeping its data in Consul
// does not.
func unvoting(name string, primary bool) plan.Member {
	m := voter(name, primary)
	m.Voter = false
	return m
}

func handoffTask() plan.Task {
	return task(steps.CommandHandoff, map[string]any{"from": "server-b"})
}

// -------------------------------------------------------------------------
// THE NORMAL PATH
// -------------------------------------------------------------------------

// Coordination is transferred to another healthy voter, by its raft ID, and the
// step waits for the move to finish rather than returning on acceptance.
func TestHandoffTransfersToAHealthyVoterAndWaits(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(coordinating(
		voter("server-a", false),
		voter("server-b", true),
		voter("server-c", false),
	), nil)

	coord := NewMockCoordinator(ctrl)
	coord.EXPECT().Handoff(gomock.Any(), "id-server-a").Return(nil)

	wait := NewMockWaiter(ctrl)
	wait.EXPECT().Coordination(gomock.Any(), "server-b").Return(nil)

	deps := live(&Deps{Survey: survey, Coordination: coord, Wait: wait})

	if _, err := handoff(deps)(context.Background(), handoffTask()); err != nil {
		t.Fatalf("handoff: %v", err)
	}
}

// Voting is required of a successor only where coordination runs on a quorum
// of these hosts. Where it does not, every host would be unfit and there would
// be no successor to hand to -- which leaves a run unable to touch the host
// that coordinates.
func TestSuccessorDoesNotRequireAVoterWhereNothingVotes(t *testing.T) {
	cluster := coordinating(
		unvoting("server-a", false),
		unvoting("server-b", true),
		unvoting("server-c", false),
	)

	got, err := successor(cluster, "server-b")
	if err != nil {
		t.Fatalf("successor: %v", err)
	}
	if got.Name != "server-a" {
		t.Errorf("successor = %s, want server-a", got.Name)
	}
}

// Where the cluster does vote, a non-voter cannot take coordination, so the
// requirement is not merely dropped.
func TestSuccessorStillRequiresAVoterWhereTheClusterVotes(t *testing.T) {
	a := voter("server-a", false)
	a.Voter = false

	cluster := coordinating(a, voter("server-b", true))

	if _, err := successor(cluster, "server-b"); err == nil {
		t.Error("a non-voter was picked in a voting cluster")
	}
}

// An unhealthy host cannot take coordination whether anything votes or not.
func TestSuccessorRefusesAnUnhealthyHost(t *testing.T) {
	a := unvoting("server-a", false)
	a.Healthy = false

	cluster := coordinating(a, unvoting("server-b", true))

	if _, err := successor(cluster, "server-b"); err == nil {
		t.Error("an unhealthy host was picked")
	}
}

// Ordered by name so a replayed run hands over to the same host, which is the
// only thing the ordering buys: any healthy voter would do.
func TestHandoffPicksTheSameSuccessorTwice(t *testing.T) {
	cluster := coordinating(
		voter("server-c", false),
		voter("server-b", true),
		voter("server-a", false),
	)

	first, err := successor(cluster, "server-b")
	if err != nil {
		t.Fatalf("successor: %v", err)
	}
	second, err := successor(cluster, "server-b")
	if err != nil {
		t.Fatalf("successor: %v", err)
	}

	if first.Name != second.Name {
		t.Errorf("successor = %s then %s, want the same host", first.Name, second.Name)
	}
}

// -------------------------------------------------------------------------
// ALREADY DONE
// -------------------------------------------------------------------------

// An election this run had nothing to do with may have moved coordination
// already. The host is empty either way, which is all the next task needs.
func TestHandoffIsUnnecessaryWhenTheHostNoLongerCoordinates(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(coordinating(
		voter("server-a", true),
		voter("server-b", false),
	), nil)

	// No expectations: transferring anything here would be acting on a
	// condition that has already resolved itself.
	deps := live(&Deps{
		Survey:       survey,
		Coordination: NewMockCoordinator(ctrl),
		Wait:         NewMockWaiter(ctrl),
	})

	result, err := handoff(deps)(context.Background(), handoffTask())
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if result.Outcome != plan.Unnecessary {
		t.Errorf("outcome = %q, want %q", result.Outcome, plan.Unnecessary)
	}
}

// An election in progress has no primary at all, which is likewise not a
// reason to transfer.
func TestHandoffIsUnnecessaryDuringAnElection(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(coordinating(
		voter("server-a", false),
		voter("server-b", false),
	), nil)

	deps := live(&Deps{
		Survey:       survey,
		Coordination: NewMockCoordinator(ctrl),
		Wait:         NewMockWaiter(ctrl),
	})

	result, err := handoff(deps)(context.Background(), handoffTask())
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if result.Outcome != plan.Unnecessary {
		t.Errorf("outcome = %q, want %q", result.Outcome, plan.Unnecessary)
	}
}

// -------------------------------------------------------------------------
// NOWHERE TO GO
// -------------------------------------------------------------------------

// A single-server cluster has nowhere to hand over to. Stopping is right: the
// alternative is restarting the only coordinator while pretending otherwise.
func TestHandoffStopsWhenThereIsNoOtherServer(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(coordinating(voter("server-b", true)), nil)

	deps := live(&Deps{
		Survey:       survey,
		Coordination: NewMockCoordinator(ctrl),
		Wait:         NewMockWaiter(ctrl),
	})

	_, err := handoff(deps)(context.Background(), handoffTask())
	if err == nil || !strings.Contains(err.Error(), "no other server") {
		t.Fatalf("err = %v, want it to report there is nowhere to hand over to", err)
	}
}

// An unhealthy peer or a non-voter is not a destination, and the run says which
// hosts it rejected rather than only that it found none.
func TestHandoffStopsAndNamesTheUnfitPeers(t *testing.T) {
	ctrl := gomock.NewController(t)

	sick := voter("server-a", false)
	sick.Healthy = false
	nonVoter := voter("server-c", false)
	nonVoter.Voter = false

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(coordinating(
		sick, voter("server-b", true), nonVoter,
	), nil)

	deps := live(&Deps{
		Survey:       survey,
		Coordination: NewMockCoordinator(ctrl),
		Wait:         NewMockWaiter(ctrl),
	})

	_, err := handoff(deps)(context.Background(), handoffTask())
	if err == nil {
		t.Fatal("an unfit peer was accepted as a destination")
	}
	for _, name := range []string{"server-a", "server-c"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("err = %v, want it to name %s as unfit", err, name)
		}
	}
}

// A client is not a candidate however healthy it is; only servers coordinate.
func TestHandoffIgnoresClients(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(coordinating(
		voter("server-b", true),
		plan.Member{ID: "id-client-a", Name: "client-a", Kind: plan.KindClient, Healthy: true},
	), nil)

	deps := live(&Deps{
		Survey:       survey,
		Coordination: NewMockCoordinator(ctrl),
		Wait:         NewMockWaiter(ctrl),
	})

	if _, err := handoff(deps)(context.Background(), handoffTask()); err == nil {
		t.Fatal("a client was accepted as a destination")
	}
}

// -------------------------------------------------------------------------
// FAILURES
// -------------------------------------------------------------------------

// A refused transfer stops the run before the wait, so it does not sit out a
// timeout waiting for a move that was never accepted.
func TestHandoffThatIsRefusedNeverWaits(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(coordinating(
		voter("server-a", false), voter("server-b", true),
	), nil)

	coord := NewMockCoordinator(ctrl)
	coord.EXPECT().Handoff(gomock.Any(), "id-server-a").Return(errCluster)

	deps := live(&Deps{Survey: survey, Coordination: coord, Wait: NewMockWaiter(ctrl)})

	if _, err := handoff(deps)(context.Background(), handoffTask()); !errors.Is(err, errCluster) {
		t.Fatalf("err = %v, want %v", err, errCluster)
	}
}

func TestHandoffWithoutItsArgumentStops(t *testing.T) {
	ctrl := gomock.NewController(t)

	deps := live(&Deps{
		Survey:       NewMockSurveyor(ctrl),
		Coordination: NewMockCoordinator(ctrl),
		Wait:         NewMockWaiter(ctrl),
	})

	if _, err := handoff(deps)(context.Background(), task(steps.CommandHandoff, nil)); err == nil {
		t.Fatal("a handoff with no host named was carried out anyway")
	}
}

func TestHandoffThatCannotReadTheClusterStops(t *testing.T) {
	ctrl := gomock.NewController(t)

	survey := NewMockSurveyor(ctrl)
	survey.EXPECT().Health(gomock.Any()).Return(plan.Cluster{}, errCluster)

	deps := live(&Deps{
		Survey:       survey,
		Coordination: NewMockCoordinator(ctrl),
		Wait:         NewMockWaiter(ctrl),
	})

	if _, err := handoff(deps)(context.Background(), handoffTask()); !errors.Is(err, errCluster) {
		t.Fatalf("err = %v, want %v", err, errCluster)
	}
}
