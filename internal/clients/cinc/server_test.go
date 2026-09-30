// -------------------------------------------------------------------------------
// Embedded Configuration Server - The Real Thing, In Process
//
// Author: Alex Freidah
//
// cinc-server-ng is the configuration server as a Go library: real data bags,
// real Mixlib signature verification, in memory, in about a hundred
// milliseconds. The pin tests run against it rather than against canned
// replies, so what they prove is that a pin written here can be read back --
// not that the client called the paths its author expected.
//
// It needs no Docker and no build tag, which is why these are ordinary unit
// tests.
// -------------------------------------------------------------------------------

package cinc

import (
	"context"
	"testing"

	"github.com/cinc-project/cinc-server-ng/server"
)

// The organization every test's server is created with.
const testOrg = "munchbox"

// live starts a configuration server and returns a client that authenticates
// to it as the bootstrap admin.
func live(t *testing.T) *Cinc {
	t.Helper()

	srv, err := server.New(server.Options{Orgs: []string{testOrg}})
	if err != nil {
		t.Fatalf("new configuration server: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("start configuration server: %v", err)
	}
	t.Cleanup(func() {
		if err := srv.Stop(context.Background()); err != nil {
			t.Errorf("stop configuration server: %v", err)
		}
	})

	c, err := New(Options{
		ServerURL:  srv.URL() + "/organizations/" + testOrg,
		ClientName: srv.AdminName(),
		KeyPath:    writeTemp(t, "admin.pem", srv.AdminKey()),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// The round trip the whole client exists for, and the one a stub cannot prove:
// a pin written through the real API, read back through it, with genuine
// signature verification on both.
func TestPinRoundTrip(t *testing.T) {
	c := live(t)

	if err := c.SetPin(t.Context(), "nomad", "2.0.6"); err != nil {
		t.Fatalf("SetPin: %v", err)
	}

	got, err := c.Pin(t.Context(), "nomad")
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if got != "2.0.6" {
		t.Errorf("Pin = %q, want 2.0.6", got)
	}
}
