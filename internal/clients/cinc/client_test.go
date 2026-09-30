// -------------------------------------------------------------------------------
// CINC Client Tests - Construction
//
// Author: Alex Freidah
//
// Construction performs no I/O, so every case here is about what it refuses:
// a URL naming no organization, a missing identity, a key that will not parse.
// What the client then does against a server is covered against a real one in
// pin_test.go.
// -------------------------------------------------------------------------------

package cinc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// testKeyPath writes an RSA private key and returns its path. The key only has
// to parse and sign; nothing verifies it.
func testKeyPath(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return writeTemp(t, "client.pem", encoded)
}

// writeTemp writes data to a uniquely-named file and returns its path.
func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestNew(t *testing.T) {
	key := testKeyPath(t)

	c, err := New(Options{
		ServerURL:  "https://cinc.example.test/organizations/munchbox",
		ClientName: "hashi-upgrade",
		KeyPath:    key,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := c.Server(), "https://cinc.example.test/organizations/munchbox"; got != want {
		t.Errorf("Server() = %q, want %q", got, want)
	}

	t.Run("rejects a url with no organization", func(t *testing.T) {
		if _, err := New(Options{ServerURL: "https://cinc.example.test", ClientName: "x", KeyPath: key}); err == nil {
			t.Error("expected an error for a url carrying no organization")
		}
	})

	t.Run("rejects a missing client name", func(t *testing.T) {
		if _, err := New(Options{ServerURL: "https://cinc.example.test/organizations/mb", KeyPath: key}); err == nil {
			t.Error("expected an error for an empty client name")
		}
	})

	t.Run("rejects an unusable key", func(t *testing.T) {
		absent := filepath.Join(t.TempDir(), "absent.pem")
		if _, err := New(Options{ServerURL: "https://cinc.example.test/organizations/mb", ClientName: "x", KeyPath: absent}); err == nil {
			t.Error("expected an error for a missing key")
		}
		garbage := writeTemp(t, "bad.pem", []byte("not a key"))
		if _, err := New(Options{ServerURL: "https://cinc.example.test/organizations/mb", ClientName: "x", KeyPath: garbage}); err == nil {
			t.Error("expected an error for an unparseable key")
		}
	})
}
