// -------------------------------------------------------------------------------
// Vault Client - Construction
//
// Author: Alex Freidah
//
// Builds the client every other method in this package queries through. The
// library reads the standard VAULT_ environment itself -- address, token and CA
// -- so an operator who has sourced their cluster environment passes nothing;
// Options cover the case where the target is not the cluster the environment
// describes.
//
// Construction performs no I/O. Whether the cluster answers, and whether it is
// fit to upgrade, are conditions a run checks against a live cluster rather
// than properties of holding a client.
// -------------------------------------------------------------------------------

package vault

import (
	"cmp"
	"fmt"

	"github.com/hashicorp/vault/api"
)

// Vault answers questions about one Vault cluster.
type Vault struct {
	client *api.Client
	addr   string
}

// Options override what the environment supplies. A zero Options takes the
// environment as it stands, which is the usual case.
type Options struct {
	Address string
}

// New builds a Vault from the standard VAULT_ environment, applying any
// overrides.
func New(opts Options) (*Vault, error) {
	cfg := api.DefaultConfig()
	if cfg.Error != nil {
		return nil, fmt.Errorf("read vault environment: %w", cfg.Error)
	}
	cfg.Address = cmp.Or(opts.Address, cfg.Address)

	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("build vault client for %s: %w", cfg.Address, err)
	}

	return &Vault{client: client, addr: cfg.Address}, nil
}

// Address is where this client talks to, for messages that need to say which
// cluster they mean.
func (v *Vault) Address() string { return v.addr }
