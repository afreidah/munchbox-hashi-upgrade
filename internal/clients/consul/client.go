// -------------------------------------------------------------------------------
// Consul Client - Construction
//
// Author: Alex Freidah
//
// Builds the client every other method in this package queries through. The
// library reads the standard CONSUL_ environment itself, so an operator who has
// sourced their cluster environment passes nothing; Options cover the case
// where the target is not the cluster the environment describes.
//
// Construction performs no I/O. Whether the cluster answers, and whether it is
// fit to upgrade, are conditions a run checks against a live cluster rather
// than properties of holding a client.
// -------------------------------------------------------------------------------

package consul

import (
	"cmp"
	"fmt"

	"github.com/hashicorp/consul/api"
)

// Consul answers questions about one Consul datacenter.
type Consul struct {
	client *api.Client
	addr   string
}

// Options override what the environment supplies. A zero Options takes the
// environment as it stands, which is the usual case.
type Options struct {
	Address    string
	Datacenter string
}

// New builds a Consul from the standard CONSUL_ environment, applying any
// overrides.
func New(opts Options) (*Consul, error) {
	cfg := api.DefaultConfig()
	cfg.Address = cmp.Or(opts.Address, cfg.Address)
	cfg.Datacenter = cmp.Or(opts.Datacenter, cfg.Datacenter)

	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("build consul client for %s: %w", cfg.Address, err)
	}

	return &Consul{client: client, addr: cfg.Address}, nil
}

// Address is where this client talks to, for messages that need to say which
// cluster they mean.
func (c *Consul) Address() string { return c.addr }
