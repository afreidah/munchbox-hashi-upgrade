// -------------------------------------------------------------------------------
// Gate Tests - Construction, Waiting, Giving Up
//
// Author: Alex Freidah
//
// The harness the assertion tests are written against, and the waiting itself.
// What matters about a timeout is the message: a gate that says only that it
// waited three minutes sends the reader nowhere.
// -------------------------------------------------------------------------------

package ready

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// When the run restarted the host, and the target it restarted it onto.
var (
	restarted = time.Date(2026, time.September, 27, 12, 0, 0, 0, time.UTC)
	settled   = restarted.Add(30 * time.Second)
	target    = "2.0.6"
)

// read is one answer from the cluster.
type read struct {
	cluster plan.Cluster
	err     error
}

// gate returns a gate that reads the given answers in order and then repeats
// the last one, which is what lets a test drive a gate to its timeout.
//
// The timeout is short enough that a test asserting one costs milliseconds.
func gate(t *testing.T, reads ...read) *Gate {
	t.Helper()

	ctrl := gomock.NewController(t)
	cluster := NewMockCluster(ctrl)

	var i int
	cluster.EXPECT().Health(gomock.Any()).AnyTimes().DoAndReturn(
		func(context.Context) (plan.Cluster, error) {
			r := reads[min(i, len(reads)-1)]
			i++
			return r.cluster, r.err
		})

	g, err := New(Options{Cluster: cluster, Timeout: 50 * time.Millisecond, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return g
}

// fleet is a cluster that passes every gate: three servers at target, all
// voters, healthy, settled after the restart, and one client in service.
func fleet() plan.Cluster {
	server := func(name string, leader bool) plan.Member {
		return plan.Member{
			Name:        name,
			Kind:        plan.KindServer,
			Version:     target,
			Status:      "alive",
			Primary:     leader,
			Voter:       true,
			Healthy:     true,
			StableSince: settled,
		}
	}

	return plan.Cluster{
		Healthy:   true,
		Tolerance: 1,
		Members: []plan.Member{
			server("server-c", true),
			server("server-a", false),
			server("server-b", false),
			{
				Name:     "client-a",
				Kind:     plan.KindClient,
				Version:  target,
				Status:   "ready",
				Healthy:  true,
				Eligible: true,
			},
		},
	}
}

// unvoting is a cluster whose coordination does not run on a quorum of its own
// members: a Vault cluster keeping its data in Consul has no voters anywhere.
// Every other condition holds.
func unvoting() plan.Cluster {
	cluster := fleet()
	for i := range cluster.Members {
		cluster.Members[i].Voter = false
	}
	return cluster
}

// dent returns the fleet with one member changed, for a test that fails a single
// condition.
func dent(name string, change func(*plan.Member)) plan.Cluster {
	cluster := fleet()
	for i := range cluster.Members {
		if cluster.Members[i].Name == name {
			change(&cluster.Members[i])
		}
	}

	return cluster
}

func TestNew(t *testing.T) {
	t.Run("rejects a missing cluster", func(t *testing.T) {
		if _, err := New(Options{}); err == nil {
			t.Error("expected an error for a missing cluster")
		}
	})

	t.Run("fills in the waiting defaults", func(t *testing.T) {
		g, err := New(Options{Cluster: NewMockCluster(gomock.NewController(t))})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if g.timeout != DefaultTimeout || g.interval != DefaultInterval {
			t.Errorf("timeout %s interval %s, want the defaults", g.timeout, g.interval)
		}
	})
}

func TestAwait(t *testing.T) {
	// The host is expected to be briefly unfit: a gate that refused on the first
	// read would fail every upgrade it exists to protect.
	t.Run("waits out a host that is not back yet", func(t *testing.T) {
		g := gate(t,
			read{cluster: dent("server-a", func(m *plan.Member) { m.Voter = false })},
			read{cluster: dent("server-a", func(m *plan.Member) { m.Version = "2.0.5" })},
			read{cluster: fleet()},
		)

		if err := g.Server(t.Context(), "server-a", target); err != nil {
			t.Errorf("Server: %v", err)
		}
	})

	// A cluster refusing connections is one of the things being waited out, not
	// a reason to abandon the run.
	t.Run("waits out a cluster that cannot be read", func(t *testing.T) {
		g := gate(t,
			read{err: errors.New("connection refused")},
			read{cluster: fleet()},
		)

		if err := g.Barrier(t.Context()); err != nil {
			t.Errorf("Barrier: %v", err)
		}
	})

	// Voting is asked about only where coordination runs on a quorum of these
	// members. A cluster that keeps its data elsewhere has no voters at all,
	// and requiring one would hold every host at this gate until it timed out.
	t.Run("does not require a voter where nothing votes", func(t *testing.T) {
		g := gate(t, read{cluster: unvoting()})

		if err := g.Server(t.Context(), "server-a", target); err != nil {
			t.Errorf("Server: %v", err)
		}
	})

	// Where the cluster does vote, a host that has not rejoined its quorum is
	// still not back, so the condition is not merely dropped.
	t.Run("still requires a voter where the cluster votes", func(t *testing.T) {
		g := gate(t, read{cluster: dent("server-a", func(m *plan.Member) { m.Voter = false })})

		if err := g.Server(t.Context(), "server-a", target); err == nil {
			t.Error("a non-voting host in a voting cluster passed the gate")
		}
	})

	// A timeout that reports only its own duration sends the reader nowhere, so
	// the last unmet condition travels with it.
	t.Run("names the condition it gave up on", func(t *testing.T) {
		g := gate(t, read{cluster: dent("server-a", func(m *plan.Member) { m.Voter = false })})

		err := g.Server(t.Context(), "server-a", target)
		if err == nil {
			t.Fatal("expected a timeout")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error %v, want a deadline", err)
		}
		for _, want := range []string{"server-a", "not a voter", "rejoining the cluster"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("reports the read that kept failing", func(t *testing.T) {
		want := errors.New("connection refused")
		g := gate(t, read{err: want})

		if err := g.Barrier(t.Context()); !errors.Is(err, want) {
			t.Errorf("Barrier error = %v, want %v wrapped", err, want)
		}
	})

	t.Run("stops when the caller's context is cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		g := gate(t, read{cluster: fleet()})
		if err := g.Barrier(ctx); err == nil {
			t.Error("expected an error for a cancelled context")
		}
	})
}
