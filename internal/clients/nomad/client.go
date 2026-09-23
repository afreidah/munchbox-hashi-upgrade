// -------------------------------------------------------------------------------
// Nomad Client - Construction
//
// Author: Alex Freidah
//
// Builds the client every other method in this package queries through. The
// library reads the standard NOMAD_ environment itself, so an operator who has
// sourced their cluster environment passes nothing; Options cover the case
// where the target is not the cluster the environment describes.
//
// Construction performs no I/O. Whether the cluster answers, and whether it is
// fit to upgrade, are conditions a run checks against a live cluster rather
// than properties of holding a client.
// -------------------------------------------------------------------------------

package nomad

import (
	"cmp"
	"fmt"

	"github.com/hashicorp/nomad/api"
)

// Nomad answers questions about one Nomad cluster.
type Nomad struct {
	client *api.Client
}

// Options override what the environment supplies. A zero Options takes the
// environment as it stands, which is the usual case.
type Options struct {
	Address string
	Region  string
}

// New builds a Nomad from the standard NOMAD_ environment, applying any
// overrides.
func New(opts Options) (*Nomad, error) {
	cfg := api.DefaultConfig()
	cfg.Address = cmp.Or(opts.Address, cfg.Address)
	cfg.Region = cmp.Or(opts.Region, cfg.Region)

	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("build nomad client for %s: %w", cfg.Address, err)
	}
	return &Nomad{client: client}, nil
}

// Address is where this client talks to, for messages that need to say which
// cluster they mean.
func (n *Nomad) Address() string { return n.client.Address() }
