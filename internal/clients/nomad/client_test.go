// -------------------------------------------------------------------------------
// Nomad Client Tests - Environment and Overrides
//
// Author: Alex Freidah
//
// Construction reads the environment through the library, so the behaviour
// worth pinning is which source wins: an explicit option, or the environment
// an operator has already sourced.
// -------------------------------------------------------------------------------

package nomad_test

import (
	"testing"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/nomad"
)

func TestNew_UsesEnvironmentByDefault(t *testing.T) {
	t.Setenv("NOMAD_ADDR", "https://from-env:4646")

	client, err := nomad.New(nomad.Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := client.Address(); got != "https://from-env:4646" {
		t.Errorf("Address() = %q, want the environment value", got)
	}
}

func TestNew_OptionsOverrideEnvironment(t *testing.T) {
	t.Setenv("NOMAD_ADDR", "https://from-env:4646")

	client, err := nomad.New(nomad.Options{Address: "https://from-option:4646"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := client.Address(); got != "https://from-option:4646" {
		t.Errorf("Address() = %q, want the option to win", got)
	}
}

// An empty option must not blank out what the environment supplied, which is
// the failure mode of assigning overrides unconditionally.
func TestNew_EmptyOptionLeavesEnvironmentAlone(t *testing.T) {
	t.Setenv("NOMAD_ADDR", "https://from-env:4646")

	client, err := nomad.New(nomad.Options{Address: ""})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := client.Address(); got != "https://from-env:4646" {
		t.Errorf("Address() = %q, want the environment value retained", got)
	}
}
