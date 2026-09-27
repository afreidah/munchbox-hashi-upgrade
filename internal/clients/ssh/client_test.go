// -------------------------------------------------------------------------------
// SSH Credential Tests - Keys, Certificates, Host Authorities
//
// Author: Alex Freidah
//
// Exercises the parsing that construction does, because a wrong path or a
// public key where a certificate was expected has to be reported before a run
// starts rather than against the first node.
//
// The cases that matter are the ones an operator's own machine produces: a
// certificate that is really a bare public key, and a host authority that lives
// in a known_hosts file rather than in a file of its own.
// -------------------------------------------------------------------------------

package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

// writeTemp writes data to a uniquely-named file and returns its path.
func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// testSigner returns an ed25519 signer and the same key in PEM form.
func testSigner(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return signer, pem.EncodeToMemory(block)
}

// signUserCert returns a user certificate for signer's public key, valid for
// principal, signed by ca.
func signUserCert(t *testing.T, ca, signer ssh.Signer, principal string) *ssh.Certificate {
	t.Helper()
	cert := &ssh.Certificate{
		Key:             signer.PublicKey(),
		CertType:        ssh.UserCert,
		ValidPrincipals: []string{principal},
		ValidBefore:     ssh.CertTimeInfinity,
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatalf("sign user cert: %v", err)
	}
	return cert
}

func TestLoadSigner(t *testing.T) {
	_, keyPEM := testSigner(t)

	if _, err := loadSigner(writeTemp(t, "id", keyPEM)); err != nil {
		t.Errorf("valid key: %v", err)
	}
	if _, err := loadSigner(writeTemp(t, "bad", []byte("not a key"))); err == nil {
		t.Error("garbage should not parse as a key")
	}
	if _, err := loadSigner(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing key file should be an error")
	}
}

func TestAuthMethods(t *testing.T) {
	ca, _ := testSigner(t)
	signer, _ := testSigner(t)

	t.Run("key only when no certificate is configured", func(t *testing.T) {
		auth, err := authMethods(signer, "")
		if err != nil {
			t.Fatalf("authMethods: %v", err)
		}
		if len(auth) != 1 {
			t.Errorf("got %d auth methods, want 1", len(auth))
		}
	})

	t.Run("certificate ahead of the key", func(t *testing.T) {
		cert := signUserCert(t, ca, signer, "root")
		path := writeTemp(t, "id-cert.pub", ssh.MarshalAuthorizedKey(cert))

		auth, err := authMethods(signer, path)
		if err != nil {
			t.Fatalf("authMethods: %v", err)
		}
		if len(auth) != 2 {
			t.Errorf("got %d auth methods, want 2", len(auth))
		}
	})

	// A configured certificate that cannot be used has to fail construction. The
	// alternative is a run that authenticates with the bare key and reports a
	// forced command's output as the converge's.
	broken := map[string]string{
		"unparseable":              writeTemp(t, "broken", []byte("garbage")),
		"public key, not a cert":   writeTemp(t, "pub", ssh.MarshalAuthorizedKey(signer.PublicKey())),
		"missing file":             filepath.Join(t.TempDir(), "absent"),
		"cert for a different key": writeTemp(t, "other-cert.pub", otherCert(t, ca)),
	}
	for name, path := range broken {
		t.Run(name, func(t *testing.T) {
			if _, err := authMethods(signer, path); err == nil {
				t.Error("expected an error rather than a fall back to the key")
			}
		})
	}
}

// otherCert returns a certificate over a key the caller does not hold, which
// cannot be turned into a signer.
func otherCert(t *testing.T, ca ssh.Signer) []byte {
	t.Helper()
	stranger, _ := testSigner(t)
	return ssh.MarshalAuthorizedKey(signUserCert(t, ca, stranger, "root"))
}

func TestLoadAuthorities(t *testing.T) {
	ca, _ := testSigner(t)
	caLine := ssh.MarshalAuthorizedKey(ca.PublicKey())
	host, _ := testSigner(t)

	t.Run("a file holding the authority alone", func(t *testing.T) {
		keys, err := loadAuthorities(writeTemp(t, "ca.pub", caLine))
		if err != nil {
			t.Fatalf("loadAuthorities: %v", err)
		}
		if len(keys) != 1 {
			t.Fatalf("got %d authorities, want 1", len(keys))
		}
	})

	t.Run("a known_hosts file", func(t *testing.T) {
		known := append([]byte("example.test "), ssh.MarshalAuthorizedKey(host.PublicKey())...)
		known = append(known, []byte("@cert-authority * ")...)
		known = append(known, caLine...)

		keys, err := loadAuthorities(writeTemp(t, "known_hosts", known))
		if err != nil {
			t.Fatalf("loadAuthorities: %v", err)
		}
		if len(keys) != 1 {
			t.Fatalf("got %d authorities, want 1", len(keys))
		}
		if got, want := string(ssh.MarshalAuthorizedKey(keys[0])), string(caLine); got != want {
			t.Errorf("read the wrong key out of known_hosts:\n got %s\nwant %s", got, want)
		}
	})

	t.Run("a known_hosts file holding no authority", func(t *testing.T) {
		known := append([]byte("example.test "), ssh.MarshalAuthorizedKey(host.PublicKey())...)
		if _, err := loadAuthorities(writeTemp(t, "known_hosts", known)); err == nil {
			t.Error("a file with no authority in it should be an error")
		}
	})

	t.Run("unreadable and unparseable", func(t *testing.T) {
		if _, err := loadAuthorities(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Error("a missing host CA file should be an error")
		}
		if _, err := loadAuthorities(writeTemp(t, "bad", []byte("garbage"))); err == nil {
			t.Error("garbage should not parse as a host CA")
		}
	})
}

func TestNew(t *testing.T) {
	ca, _ := testSigner(t)
	signer, keyPEM := testSigner(t)
	keyPath := writeTemp(t, "id", keyPEM)
	caPath := writeTemp(t, "ca.pub", ssh.MarshalAuthorizedKey(ca.PublicKey()))

	c, err := New(Config{KeyPath: keyPath, HostCAPath: caPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.timeout != defaultTimeout {
		t.Errorf("timeout %v, want the %v default", c.timeout, defaultTimeout)
	}

	certPath := writeTemp(t, "id-cert.pub", ssh.MarshalAuthorizedKey(signUserCert(t, ca, signer, "root")))
	if _, err := New(Config{KeyPath: keyPath, CertPath: certPath, HostCAPath: caPath}); err != nil {
		t.Errorf("New with a certificate: %v", err)
	}

	absent := filepath.Join(t.TempDir(), "absent")
	if _, err := New(Config{KeyPath: absent, HostCAPath: caPath}); err == nil {
		t.Error("New should reject a missing key")
	}
	if _, err := New(Config{KeyPath: keyPath, CertPath: absent, HostCAPath: caPath}); err == nil {
		t.Error("New should reject a missing certificate")
	}
	if _, err := New(Config{KeyPath: keyPath, HostCAPath: absent}); err == nil {
		t.Error("New should reject a missing host CA")
	}
}
