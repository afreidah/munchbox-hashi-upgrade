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
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	cinc "github.com/cinc-project/cinc-api"
)

// Options is what the client needs to authenticate.
//
// TrustedCerts is a configuration server's own certificate authority, as a PEM
// file or a directory of them. A server behind an internal CA is the ordinary
// case -- knife has a trusted_certs directory for exactly this -- and without
// it every request fails the TLS handshake. Empty uses the system roots.
type Options struct {
	ServerURL    string // combined form: https://host[:port]/organizations/<org>
	ClientName   string // the client or user the request is signed as
	KeyPath      string // that identity's RSA private key
	TrustedCerts string // PEM file or directory of them; system roots when empty
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

	var extra []cinc.Option
	if opts.TrustedCerts != "" {
		pool, err := certPool(opts.TrustedCerts)
		if err != nil {
			return nil, err
		}
		extra = append(extra, cinc.WithRootCAs(pool))
	}

	client, err := cinc.NewClient(cinc.Config{
		ServerURL:  server,
		Org:        org,
		ClientName: opts.ClientName,
		Key:        key,
	}, extra...)
	if err != nil {
		return nil, fmt.Errorf("build cinc client for %s: %w", opts.ServerURL, err)
	}

	return &Cinc{client: client, server: opts.ServerURL}, nil
}

// certPool builds a root pool from a PEM file or a directory of them.
//
// A directory because that is how knife keeps them, one file per server, and
// pointing at the same directory is less to get wrong than naming the one file
// inside it that happens to matter.
//
// The system roots are not merged in: a configuration server behind an internal
// CA is the whole reason this exists, and a pool that also trusted every public
// authority would accept a certificate this server would never present.
func certPool(path string) (*x509.CertPool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read cinc trusted certs %s: %w", path, err)
	}

	files := []string{path}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("read cinc trusted certs %s: %w", path, err)
		}

		files = nil
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
	}

	pool := x509.NewCertPool()
	for _, f := range files {
		pem, err := os.ReadFile(f) //nolint:gosec // the path is the operator's own argument
		if err != nil {
			return nil, fmt.Errorf("read cinc trusted cert %s: %w", f, err)
		}
		pool.AppendCertsFromPEM(pem)
	}

	// Reported rather than left to fail as a handshake error several calls
	// later, which names neither this path nor the reason.
	if len(pool.Subjects()) == 0 { //nolint:staticcheck // counting what was loaded, not matching against system roots
		return nil, fmt.Errorf("no certificates found in %s", path)
	}

	return pool, nil
}

// Server returns the server this client was built against, for error messages
// that would otherwise name a path without saying where it was looked for.
func (c *Cinc) Server() string { return c.server }
