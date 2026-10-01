// -------------------------------------------------------------------------------
// Leadership - Moving Coordination Off A Server
//
// Author: Alex Freidah
//
// Restarting the leader works without this: the datacenter notices it gone and
// elects another. Handing coordination over first makes that a deliberate act
// at a moment of the run's choosing rather than a side effect of an agent going
// away.
//
// Consul names the destination optionally, unlike Nomad, which requires one.
// An empty id leaves the choice to raft, which is what a run wants: the point
// is that the host it is about to restart stops leading, not which of its peers
// takes over.
// -------------------------------------------------------------------------------

package consul

import (
	"context"
	"fmt"

	"github.com/hashicorp/consul/api"
)

// Handoff asks the current leader to transfer coordination away.
//
// id names where it goes; empty leaves raft to choose. It returns once the
// datacenter has accepted the request, which is before the election has
// finished: a caller that needs coordination to have actually moved waits for
// the cluster to say so.
func (c *Consul) Handoff(ctx context.Context, id string) error {
	q := (&api.QueryOptions{}).WithContext(ctx)

	res, err := c.client.Operator().RaftLeaderTransfer(id, q)
	if err != nil {
		return fmt.Errorf("transfer leadership via %s: %w", c.addr, err)
	}

	// The call can answer without having done it, so the reply is read rather
	// than the absence of an error taken as success.
	if res != nil && !res.Success {
		return fmt.Errorf("transfer leadership via %s: refused", c.addr)
	}

	return nil
}
