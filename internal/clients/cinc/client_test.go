// -------------------------------------------------------------------------------
// CINC Client Tests - Construction, and the Server the Pin Tests Run Against
//
// Author: Alex Freidah
//
// Covers construction, and provides the stub configuration server the pin tests
// use. Request signing happens on the client, so a plain HTTP server is enough
// to exercise the calls; the library's own test helper lives under internal/
// and cannot be imported.
// -------------------------------------------------------------------------------

package cinc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// reply is the answer the stub server gives one request.
type reply struct {
	status int
	body   any
}

// stub is a configuration server that answers the data bag endpoints from a
// table and records what it was asked.
type stub struct {
	server  *httptest.Server
	replies map[string]reply

	mu   sync.Mutex
	seen []string
	sent []map[string]any
}

// serve starts a stub server. Keys in replies are "METHOD /trailing/path",
// where the path is what follows the organization prefix.
func serve(t *testing.T, replies map[string]reply) *stub {
	t.Helper()
	s := &stub{replies: replies}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

// handle answers one request from the table, defaulting to 404 so an
// unconfigured path exercises the not-found paths rather than hanging.
func (s *stub) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if i := strings.Index(path, "/data"); i >= 0 {
		path = path[i:]
	}
	key := r.Method + " " + path

	s.mu.Lock()
	s.seen = append(s.seen, key)
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body != nil {
			s.sent = append(s.sent, body)
		}
	}
	s.mu.Unlock()

	w.Header().Set("X-Ops-Server-API-Version", `{"min_version":"0","max_version":"2"}`)
	w.Header().Set("Content-Type", "application/json")

	rep, ok := s.replies[key]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": []string{"not found"}})
		return
	}
	w.WriteHeader(rep.status)
	if rep.body != nil {
		_ = json.NewEncoder(w).Encode(rep.body)
	}
}

// requests returns the requests the server was asked, in order.
func (s *stub) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

// bodies returns the JSON bodies the server was sent, in order.
func (s *stub) bodies() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.sent...)
}

// client builds a client pointed at the stub server.
func (s *stub) client(t *testing.T) *Cinc {
	t.Helper()
	c, err := New(Options{
		ServerURL:  s.server.URL + "/organizations/munchbox",
		ClientName: "hashi-upgrade",
		KeyPath:    testKeyPath(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
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
