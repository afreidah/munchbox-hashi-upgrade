// -------------------------------------------------------------------------------
// Shared - What Every Cluster Answers, And Which Client Answers It
//
// Author: Alex Freidah
//
// The one place a tool is turned into the client that speaks to it. Everything
// past this point takes interfaces, so adding a tool is a case here and a
// package beside this file -- not a change to the runner, the steps or the
// gates.
//
// The clusters answer the same three questions: what are you made of, how are
// you now, and move coordination off this host. One interface covers them and
// a caller never learns which it is holding.
// -------------------------------------------------------------------------------

package clients

import (
	"context"
	"fmt"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/consul"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/nomad"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/vault"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// Cluster is what a run reads a fleet through, whichever tool it is.
//
// Draining is deliberately absent. Only Nomad schedules work, so a client that
// can drain says so by implementing execute.Drainer, and is asked at the point
// it would be used rather than every client having to answer for a capability
// most of them do not have.
type Cluster interface {
	Survey(ctx context.Context) (plan.Cluster, error)
	Health(ctx context.Context) (plan.Cluster, error)
	Handoff(ctx context.Context, id string) error
}

// Options override what the environment supplies. A zero Options takes the
// environment as it stands, which is the usual case.
//
// Region is Nomad's; Consul takes its datacenter from the agent it is pointed
// at, which is the agent that would answer anyway.
type Options struct {
	Address string
	Region  string
}

// For returns the client for a tool.
//
// A tool with no client is refused by name rather than attempted, so it fails
// here with something to read instead of somewhere deeper holding a nil.
func For(tool plan.Tool, opts Options) (Cluster, error) {
	switch tool {
	case plan.Nomad:
		return nomad.New(nomad.Options{Address: opts.Address, Region: opts.Region})
	case plan.Consul:
		return consul.New(consul.Options{Address: opts.Address})
	case plan.Vault:
		return vault.New(vault.Options{Address: opts.Address})
	default:
		return nil, fmt.Errorf("unknown tool %q; expected nomad, consul or vault", tool)
	}
}
