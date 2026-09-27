// -------------------------------------------------------------------------------
// CINC Client - Construction and Credentials
//
// Author: Alex Freidah
//
// Construction and the signing key it rests on. The server URL arrives in the
// combined form, organization and all, because that is the shape Chef's own
// credentials files and CHEF_SERVER_URL store and so the shape an operator
// already has to hand; it is split here rather than asked for twice.
//
// The key is read at construction so a wrong path fails before the run reaches
// a cluster.
// -------------------------------------------------------------------------------

package cinc

import (
	"fmt"

	cinc "github.com/cinc-project/cinc-api"
)

// Options is what the client needs to authenticate.
type Options struct {
	ServerURL  string // combined form: https://host[:port]/organizations/<org>
	ClientName string // the client or user the request is signed as
	KeyPath    string // that identity's RSA private key
}

// Cinc reads and writes the version pin on one configuration server.
type Cinc struct {
	client *cinc.Client
	server string
}

// New parses the credentials and returns a ready client.
func New(opts Options) (*Cinc, error) {
	server, org, err := cinc.ParseServerURL(opts.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("parse cinc server url %q: %w", opts.ServerURL, err)
	}
	if opts.ClientName == "" {
		return nil, fmt.Errorf("cinc client name is required")
	}

	key, err := cinc.LoadKeyFile(opts.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("read cinc key %s: %w", opts.KeyPath, err)
	}

	client, err := cinc.NewClient(cinc.Config{
		ServerURL:  server,
		Org:        org,
		ClientName: opts.ClientName,
		Key:        key,
	})
	if err != nil {
		return nil, fmt.Errorf("build cinc client for %s: %w", opts.ServerURL, err)
	}

	return &Cinc{client: client, server: opts.ServerURL}, nil
}

// Server returns the server this client was built against, for error messages
// that would otherwise name a path without saying where it was looked for.
func (c *Cinc) Server() string { return c.server }
