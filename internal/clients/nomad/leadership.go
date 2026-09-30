// -------------------------------------------------------------------------------
// Leadership - Moving Coordination Off A Server
//
// Author: Alex Freidah
//
// Restarting the coordinating server works without this: the cluster notices it
// gone and elects another. Handing coordination over first makes that a
// deliberate act at a moment of the run's choosing rather than a side effect of
// an agent going away, which is the difference between an election the operator
// is watching and one they are told about afterwards.
//
// The raft API names the server leadership goes TO and requires one, so a
// destination is always named. Raft accepts any voter; which one is the
// caller's choice.
// -------------------------------------------------------------------------------

package nomad

import (
	"context"
	"fmt"

	"github.com/hashicorp/nomad/api"
)

// Handoff asks the current leader to transfer coordination to the server with
// the given raft ID.
//
// It returns once the cluster has accepted the request, which is before the
// election has finished: a caller that needs coordination to have actually
// moved waits for the cluster to say so.
func (n *Nomad) Handoff(ctx context.Context, id string) error {
	w := (&api.WriteOptions{}).WithContext(ctx)

	if err := n.client.Operator().RaftTransferLeadershipByID(id, w); err != nil {
		return fmt.Errorf("transfer leadership to %s via %s: %w", id, n.Address(), err)
	}
	return nil
}
