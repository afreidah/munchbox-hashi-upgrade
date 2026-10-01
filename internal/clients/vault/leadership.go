// -------------------------------------------------------------------------------
// Leadership - Moving Coordination Off A Node
//
// Author: Alex Freidah
//
// Restarting the active node works without this: the standbys contend for the
// lock and one of them takes over. Stepping down first makes that a deliberate
// act at a moment of the run's choosing rather than a side effect of a process
// going away.
//
// Vault names no destination. Consul takes one optionally and Nomad requires
// one, but a step-down only says "stop being active" -- which of the standbys
// wins the lock is the storage backend's business. The successor a run picked
// is therefore accepted and ignored, and confirming that coordination actually
// moved is the gate's job, as it is for the other two.
// -------------------------------------------------------------------------------

package vault

import (
	"context"
	"fmt"
)

// Handoff asks the active node to stop being active.
//
// The id is ignored: Vault offers no way to name a successor. It returns once
// the cluster has accepted the request, which is before another node has taken
// over: a caller that needs coordination to have actually moved waits for the
// cluster to say so.
func (v *Vault) Handoff(ctx context.Context, _ string) error {
	// Addressed to the cluster rather than to a node. A standby forwards this
	// to the active node, so the request lands where it means to whichever
	// node the environment points at.
	if err := v.client.Sys().StepDownWithContext(ctx); err != nil {
		return fmt.Errorf("step down via %s: %w", v.addr, err)
	}

	return nil
}
