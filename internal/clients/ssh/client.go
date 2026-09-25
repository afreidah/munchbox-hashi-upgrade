// -------------------------------------------------------------------------------
// SSH Client - Credentials and Host CA Verification
//
// Author: Alex Freidah
//
// Construction and the credential material it rests on: the private key, the
// certificate offered ahead of it, and the certificate authority remote host
// keys are checked against.
//
// All three are parsed once, here, so a path that is wrong or a certificate
// that has expired is reported before the run touches a node rather than
// halfway through one. A run is short-lived enough that nothing can be re-signed
// underneath it.
// -------------------------------------------------------------------------------

package ssh

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"slices"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	defaultTimeout = 30 * time.Second
	defaultPort    = 22
)

// Config is the credential material a client authenticates with.
//
// HostCAPath accepts either a bare public key or a known_hosts file, because an
// operator workstation that trusts the cluster already holds the authority as an
// @cert-authority line and should not have to keep a second copy of it in a
// second format.
type Config struct {
	KeyPath    string        // private key, required
	CertPath   string        // signed certificate; key-only auth when empty
	HostCAPath string        // host certificate authority, required
	Timeout    time.Duration // dial timeout; 30s when zero
}

// Target is a host to connect to and how to log in. Sudo covers the nodes that
// refuse a root login and are administered through an unprivileged account.
type Target struct {
	Host string
	User string
	Port int
	Sudo bool
}

// Client runs commands on hosts. It holds no per-host state, so one is built
// per run and shared across every node the run touches.
type Client struct {
	auth    []ssh.AuthMethod
	hostKey ssh.HostKeyCallback
	timeout time.Duration
}

// New parses the configured credentials and returns a ready client.
func New(cfg Config) (*Client, error) {
	signer, err := loadSigner(cfg.KeyPath)
	if err != nil {
		return nil, err
	}
	auth, err := authMethods(signer, cfg.CertPath)
	if err != nil {
		return nil, err
	}
	hostKey, err := hostKeyVerifier(cfg.HostCAPath)
	if err != nil {
		return nil, err
	}
	return &Client{
		auth:    auth,
		hostKey: hostKey,
		timeout: cmp.Or(cfg.Timeout, defaultTimeout),
	}, nil
}

// loadSigner reads and parses the private key at path.
func loadSigner(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ssh key %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key %s: %w", path, err)
	}
	return signer, nil
}

// authMethods returns certificate-then-key auth, or key-only when no
// certificate is configured. Order is the point: both methods present the same
// underlying key, and a host that would answer the bare key with a forced
// command accepts the certificate first and never reaches the file that carries
// it.
//
// A configured certificate that cannot be used is a hard error rather than a
// quiet fall back to the key, which would otherwise show up as a run that
// succeeds against every node except the ones that needed the certificate.
func authMethods(signer ssh.Signer, certPath string) ([]ssh.AuthMethod, error) {
	var auth []ssh.AuthMethod
	if certPath != "" {
		cert, err := loadCert(certPath)
		if err != nil {
			return nil, err
		}
		certSigner, err := ssh.NewCertSigner(cert, signer)
		if err != nil {
			return nil, fmt.Errorf("build cert signer from %s: %w", certPath, err)
		}
		auth = append(auth, ssh.PublicKeys(certSigner))
	}
	return append(auth, ssh.PublicKeys(signer)), nil
}

// loadCert reads the certificate at path.
func loadCert(path string) (*ssh.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ssh cert %s: %w", path, err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse ssh cert %s: %w", path, err)
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("%s is a public key, not a certificate", path)
	}
	return cert, nil
}

// hostKeyVerifier returns a callback that accepts a host key only when it
// carries a certificate signed by a configured authority.
//
// Checking the authority rather than the key means a node that has been rebuilt
// still verifies. The alternative stalls a run on a changed host key, at the one
// moment when the fleet is expected to be changing.
func hostKeyVerifier(path string) (ssh.HostKeyCallback, error) {
	authorities, err := loadAuthorities(path)
	if err != nil {
		return nil, err
	}
	checker := &ssh.CertChecker{
		IsHostAuthority: func(key ssh.PublicKey, _ string) bool {
			marshalled := key.Marshal()
			return slices.ContainsFunc(authorities, func(a ssh.PublicKey) bool {
				return bytes.Equal(a.Marshal(), marshalled)
			})
		},
	}
	return checker.CheckHostKey, nil
}

// loadAuthorities reads the host certificate authorities at path, from either
// file shape.
func loadAuthorities(path string) ([]ssh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read host CA %s: %w", path, err)
	}
	if keys, isKnownHosts := knownHostAuthorities(data); isKnownHosts {
		if len(keys) == 0 {
			return nil, fmt.Errorf("host CA %s holds no @cert-authority entry", path)
		}
		return keys, nil
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse host CA %s: %w", path, err)
	}
	return []ssh.PublicKey{key}, nil
}

// knownHostAuthorities reports whether data is a known_hosts file, and the
// @cert-authority entries in it when it is.
//
// The two file shapes have to be told apart before either is read, not after:
// a known_hosts line also parses as an authorized_keys entry, with its host
// pattern taken for an option, so a file whose authorities were simply missed
// would come back holding a host's own key and the client would trust that host
// to vouch for every other one.
//
// Parsing stops at the first line it cannot read. A single malformed entry
// partway down a long known_hosts file should not cost the caller the
// authorities above it.
func knownHostAuthorities(data []byte) ([]ssh.PublicKey, bool) {
	var keys []ssh.PublicKey
	entries := 0
	for len(data) > 0 {
		marker, _, key, _, rest, err := ssh.ParseKnownHosts(data)
		if err != nil {
			break
		}
		entries++
		if marker == "cert-authority" {
			keys = append(keys, key)
		}
		data = rest
	}
	return keys, entries > 0
}
