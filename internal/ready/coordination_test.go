// -------------------------------------------------------------------------------
// Coordination Gate Tests - Waiting For A Host To Stop Leading
//
// Author: Alex Freidah
//
// This gate waits for an absence rather than a presence, so the cases are the
// three ways "no longer coordinating" can be wrong: still leading, nobody
// leading yet, and led by someone the cluster is not happy about.
// -------------------------------------------------------------------------------

package ready

import (
	"testing"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// leader is the host the test fleet coordinates on.
const leader = "server-c"

func TestCoordination(t *testing.T) {
	t.Run("passes a host that is not the one coordinating", func(t *testing.T) {
		if err := gate(t, read{cluster: fleet()}).Coordination(t.Context(), "server-a"); err != nil {
			t.Errorf("Coordination: %v", err)
		}
	})

	t.Run("refuses while the host still coordinates", func(t *testing.T) {
		err := gate(t, read{cluster: fleet()}).Coordination(t.Context(), leader)
		refuses(t, err, "still coordinating")
	})

	// An election in progress leaves no primary at all. The run needs someone
	// holding the term before it restarts the host that used to.
	t.Run("refuses while nothing is coordinating", func(t *testing.T) {
		cluster := dent(leader, func(m *plan.Member) { m.Primary = false })

		err := gate(t, read{cluster: cluster}).Coordination(t.Context(), leader)
		refuses(t, err, "no host is coordinating yet")
	})

	t.Run("refuses when coordination moved but the cluster is unwell", func(t *testing.T) {
		cluster := fleet()
		cluster.Healthy = false

		err := gate(t, read{cluster: cluster}).Coordination(t.Context(), "server-a")
		refuses(t, err, "not healthy")
	})

	// The transfer returns on acceptance, so the first read legitimately still
	// shows the old leader. The gate polls until it does not.
	t.Run("passes once a later read shows the move", func(t *testing.T) {
		moved := dent(leader, func(m *plan.Member) { m.Primary = false })
		for i := range moved.Members {
			if moved.Members[i].Name == "server-a" {
				moved.Members[i].Primary = true
			}
		}

		g := gate(t, read{cluster: fleet()}, read{cluster: moved})
		if err := g.Coordination(t.Context(), leader); err != nil {
			t.Errorf("Coordination: %v", err)
		}
	})
}
