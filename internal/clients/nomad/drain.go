// -------------------------------------------------------------------------------
// Drain - Emptying A Host Before It Restarts
//
// Author: Alex Freidah
//
// Restarting an agent does not stop the work already running on it, so a run
// drains nothing by default. Draining is for the case where the restart itself
// is not the concern: an operator who would rather move the work somewhere
// healthy first and accept the relocation.
//
// Draining is orchestration state, not a version, so it is the one thing a
// failed run can and must put back. The caller pairs every drain with the
// undrain that reverses it.
// -------------------------------------------------------------------------------

package nomad

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/nomad/api"
)

// Drain marks a host ineligible and waits for its work to move off.
//
// System jobs are left alone. They are meant to run on every host, so draining
// them serves nothing: the restart brings them back on the same host, and
// waiting for them to leave would wait for the deadline every time.
func (n *Nomad) Drain(ctx context.Context, id string, deadline time.Duration) error {
	w := (&api.WriteOptions{}).WithContext(ctx)

	resp, err := n.client.Nodes().UpdateDrainOpts(id, &api.DrainOptions{
		DrainSpec:    &api.DrainSpec{Deadline: deadline, IgnoreSystemJobs: true},
		MarkEligible: false,
	}, w)
	if err != nil {
		return fmt.Errorf("drain %s via %s: %w", id, n.Address(), err)
	}

	// The channel closes when the drain finishes or the context ends, so
	// reading it to exhaustion is the wait. A cancelled caller stops here
	// rather than after the deadline.
	for msg := range n.client.Nodes().MonitorDrain(ctx, id, resp.NodeModifyIndex, true) {
		if msg.Level == api.MonitorMsgLevelError {
			return fmt.Errorf("drain %s: %s", id, msg.Message)
		}
	}

	return ctx.Err()
}

// Undrain cancels a drain and lets the host take work again.
//
// A nil DrainSpec cancels rather than drains, and MarkEligible is what
// actually puts the host back in service: cancelling alone would leave it
// ineligible and quietly out of the cluster's capacity.
func (n *Nomad) Undrain(ctx context.Context, id string) error {
	w := (&api.WriteOptions{}).WithContext(ctx)

	if _, err := n.client.Nodes().UpdateDrainOpts(id, &api.DrainOptions{
		DrainSpec:    nil,
		MarkEligible: true,
	}, w); err != nil {
		return fmt.Errorf("undrain %s via %s: %w", id, n.Address(), err)
	}
	return nil
}
