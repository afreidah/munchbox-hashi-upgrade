// -------------------------------------------------------------------------------
// Assertion Tests - One Failed Condition At A Time
//
// Author: Alex Freidah
//
// Each case dents exactly one field of a cluster that would otherwise pass, so a
// gate that stopped checking something fails a test that names it. The stale
// health case is the one worth reading twice: it is the only condition that a
// host which never came back at all would satisfy.
// -------------------------------------------------------------------------------

package ready

import (
	"strings"
	"testing"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// refuses asserts that the gate failed and said why.
func refuses(t *testing.T, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected the gate to refuse, wanting %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not mention %q", err, want)
	}
}

func TestServer(t *testing.T) {
	t.Run("passes a host that rejoined at the target", func(t *testing.T) {
		if err := gate(t, read{cluster: fleet()}).Server(t.Context(), "goren", target); err != nil {
			t.Errorf("Server: %v", err)
		}
	})

	// The version is the condition a host nothing touched cannot satisfy: it is
	// read from the running agent rather than from the pin, so a host that has
	// not restarted still reports the old one however healthy it looks.
	t.Run("refuses a host that is still on the old version", func(t *testing.T) {
		cluster := dent("goren", func(m *plan.Member) { m.Version = "2.0.5" })

		err := gate(t, read{cluster: cluster}).Server(t.Context(), "goren", target)
		refuses(t, err, "running 2.0.5, not "+target)
	})

	// An agent that goes down and comes back inside one health interval is
	// never observed unhealthy, so its stability timestamp still reads from
	// before the restart. Asking about it would have the gate wait out its
	// timeout on a host that had already arrived, which is what it did.
	t.Run("passes a host whose health verdict predates the restart", func(t *testing.T) {
		cluster := dent("goren", func(m *plan.Member) { m.StableSince = settled.Add(-time.Hour) })

		if err := gate(t, read{cluster: cluster}).Server(t.Context(), "goren", target); err != nil {
			t.Errorf("Server: %v", err)
		}
	})

	t.Run("refuses a host autopilot calls unhealthy", func(t *testing.T) {
		cluster := dent("goren", func(m *plan.Member) { m.Healthy = false })

		err := gate(t, read{cluster: cluster}).Server(t.Context(), "goren", target)
		refuses(t, err, "goren is not healthy")
	})

	t.Run("refuses a host that rejoined as a non-voter", func(t *testing.T) {
		cluster := dent("goren", func(m *plan.Member) { m.Voter = false })

		err := gate(t, read{cluster: cluster}).Server(t.Context(), "goren", target)
		refuses(t, err, "not a voter")
	})

	// The host can be back and trusted while the cluster is not yet fit for the
	// next step, which is what the tolerance is read for.
	t.Run("refuses a cluster that could not lose another server", func(t *testing.T) {
		cluster := fleet()
		cluster.Tolerance = 0

		err := gate(t, read{cluster: cluster}).Server(t.Context(), "goren", target)
		refuses(t, err, "failure tolerance is 0")
	})

	t.Run("refuses an unhealthy cluster", func(t *testing.T) {
		cluster := fleet()
		cluster.Healthy = false

		err := gate(t, read{cluster: cluster}).Server(t.Context(), "goren", target)
		refuses(t, err, "the cluster is not healthy")
	})

	// A host missing from the cluster's own view is ordinary in the seconds
	// after a restart, so it is waited on and then reported as absent.
	t.Run("refuses a host the cluster does not report", func(t *testing.T) {
		err := gate(t, read{cluster: fleet()}).Server(t.Context(), "munch", target)
		refuses(t, err, "munch is not in the cluster")
	})
}

func TestClient(t *testing.T) {
	const client = "nomad-client-01"

	t.Run("passes a host back in service", func(t *testing.T) {
		if err := gate(t, read{cluster: fleet()}).Client(t.Context(), client, target); err != nil {
			t.Errorf("Client: %v", err)
		}
	})

	t.Run("refuses a host that is still on the old version", func(t *testing.T) {
		cluster := dent(client, func(m *plan.Member) { m.Version = "2.0.5" })

		err := gate(t, read{cluster: cluster}).Client(t.Context(), client, target)
		refuses(t, err, "running 2.0.5, not "+target)
	})

	// The status travels into the message in Nomad's own vocabulary, because
	// "down" and "initializing" send the reader to different places.
	t.Run("refuses a host that is not ready", func(t *testing.T) {
		cluster := dent(client, func(m *plan.Member) {
			m.Healthy = false
			m.Status = "initializing"
		})

		err := gate(t, read{cluster: cluster}).Client(t.Context(), client, target)
		refuses(t, err, "is initializing, not ready")
	})

	// A host that came back ineligible is up and carrying its work while the
	// fleet quietly schedules nothing new onto it.
	t.Run("refuses a host that accepts no work", func(t *testing.T) {
		cluster := dent(client, func(m *plan.Member) { m.Eligible = false })

		err := gate(t, read{cluster: cluster}).Client(t.Context(), client, target)
		refuses(t, err, "not eligible for work")
	})

	t.Run("refuses a host the cluster does not report", func(t *testing.T) {
		err := gate(t, read{cluster: fleet()}).Client(t.Context(), "munch", target)
		refuses(t, err, "munch is not in the cluster")
	})
}

func TestBarrier(t *testing.T) {
	t.Run("passes a settled cluster", func(t *testing.T) {
		if err := gate(t, read{cluster: fleet()}).Barrier(t.Context()); err != nil {
			t.Errorf("Barrier: %v", err)
		}
	})

	t.Run("refuses an unhealthy cluster", func(t *testing.T) {
		cluster := fleet()
		cluster.Healthy = false

		refuses(t, gate(t, read{cluster: cluster}).Barrier(t.Context()), "the cluster is not healthy")
	})

	// Every server, not only the one just restarted: a host demoted earlier in
	// the run is the case a per-host gate cannot see.
	t.Run("refuses while any server is a non-voter", func(t *testing.T) {
		cluster := dent("stabler", func(m *plan.Member) { m.Voter = false })

		refuses(t, gate(t, read{cluster: cluster}).Barrier(t.Context()), "stabler is not a voter")
	})

	// Clients are not part of the coordinating set, so one being out does not
	// hold the barrier.
	t.Run("ignores the hosts that do not coordinate", func(t *testing.T) {
		cluster := dent("nomad-client-01", func(m *plan.Member) {
			m.Healthy = false
			m.Eligible = false
		})

		if err := gate(t, read{cluster: cluster}).Barrier(t.Context()); err != nil {
			t.Errorf("Barrier: %v", err)
		}
	})

	t.Run("refuses a cluster that could not lose another server", func(t *testing.T) {
		cluster := fleet()
		cluster.Tolerance = 0

		refuses(t, gate(t, read{cluster: cluster}).Barrier(t.Context()), "failure tolerance is 0")
	})
}
