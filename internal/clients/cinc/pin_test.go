// -------------------------------------------------------------------------------
// Version Pin Tests - Reading, Upserting, and the States In Between
//
// Author: Alex Freidah
//
// Run against an embedded configuration server rather than canned replies, so
// each case asserts a state the server is actually in: a tool nobody has
// pinned, a bag that does not exist yet, a pin that has been moved. Writing a
// pin and reading it back is the whole contract, and it is the one thing a
// table of expected requests cannot check.
//
// The faults a live server will not produce on request -- a malformed item, a
// refusing server -- are driven separately, in fault_test.go.
// -------------------------------------------------------------------------------

package cinc

import (
	"testing"
)

func TestPin(t *testing.T) {
	// A tool nobody has pinned is what a first upgrade starts from, so it reads
	// as empty rather than refusing to answer. On a fresh server the bag itself
	// is absent too, which is the same answer by a different route.
	t.Run("an unpinned tool is empty, not an error", func(t *testing.T) {
		got, err := live(t).Pin(t.Context(), "consul")
		if err != nil {
			t.Fatalf("Pin against an empty server: %v", err)
		}
		if got != "" {
			t.Errorf("Pin = %q, want empty", got)
		}
	})

	// The bag exists and holds other tools, but not this one. Distinct from the
	// case above: the item is missing rather than the whole bag.
	t.Run("an absent item in an existing bag is empty", func(t *testing.T) {
		c := live(t)
		if err := c.SetPin(t.Context(), "nomad", "2.0.6"); err != nil {
			t.Fatalf("SetPin: %v", err)
		}

		got, err := c.Pin(t.Context(), "vault")
		if err != nil {
			t.Fatalf("Pin of an unpinned tool in a populated bag: %v", err)
		}
		if got != "" {
			t.Errorf("Pin = %q, want empty", got)
		}
	})

	t.Run("rejects an empty tool", func(t *testing.T) {
		if _, err := live(t).Pin(t.Context(), ""); err == nil {
			t.Error("expected an error for an empty tool")
		}
	})
}

func TestSetPin(t *testing.T) {
	// Onboarding a tool: neither the bag nor the item exists, and both are
	// created without a manual bootstrap step.
	t.Run("creates the bag and the item when neither exists", func(t *testing.T) {
		c := live(t)

		if err := c.SetPin(t.Context(), "consul", "1.22.1"); err != nil {
			t.Fatalf("SetPin: %v", err)
		}

		got, err := c.Pin(t.Context(), "consul")
		if err != nil {
			t.Fatalf("Pin: %v", err)
		}
		if got != "1.22.1" {
			t.Errorf("Pin = %q, want 1.22.1", got)
		}
	})

	// The steady state, and what every upgrade after the first one does.
	t.Run("moves a pin that is already set", func(t *testing.T) {
		c := live(t)
		if err := c.SetPin(t.Context(), "nomad", "2.0.5"); err != nil {
			t.Fatalf("SetPin: %v", err)
		}
		if err := c.SetPin(t.Context(), "nomad", "2.0.6"); err != nil {
			t.Fatalf("SetPin again: %v", err)
		}

		got, err := c.Pin(t.Context(), "nomad")
		if err != nil {
			t.Fatalf("Pin: %v", err)
		}
		if got != "2.0.6" {
			t.Errorf("Pin = %q, want the version written second", got)
		}
	})

	// The bag already existing is the state this call wanted, so the second
	// tool is created into it rather than failing on the conflict.
	t.Run("pins a second tool into an existing bag", func(t *testing.T) {
		c := live(t)
		if err := c.SetPin(t.Context(), "nomad", "2.0.6"); err != nil {
			t.Fatalf("SetPin nomad: %v", err)
		}
		if err := c.SetPin(t.Context(), "vault", "1.21.0"); err != nil {
			t.Fatalf("SetPin vault: %v", err)
		}

		for tool, want := range map[string]string{"nomad": "2.0.6", "vault": "1.21.0"} {
			got, err := c.Pin(t.Context(), tool)
			if err != nil {
				t.Fatalf("Pin %s: %v", tool, err)
			}
			if got != want {
				t.Errorf("Pin %s = %q, want %q", tool, got, want)
			}
		}
	})

	t.Run("rejects empty arguments", func(t *testing.T) {
		c := live(t)
		if err := c.SetPin(t.Context(), "", "2.0.6"); err == nil {
			t.Error("expected an error for an empty tool")
		}
		if err := c.SetPin(t.Context(), "nomad", ""); err == nil {
			t.Error("expected an error for an empty version")
		}
	})
}

func TestClearPin(t *testing.T) {
	// How a run puts back a pin that was not set before it ran: the tool reads
	// as unpinned again, which is the state it started from.
	t.Run("removes a pin so the tool reads as unpinned", func(t *testing.T) {
		c := live(t)
		if err := c.SetPin(t.Context(), "nomad", "2.0.7"); err != nil {
			t.Fatalf("SetPin: %v", err)
		}
		if err := c.ClearPin(t.Context(), "nomad"); err != nil {
			t.Fatalf("ClearPin: %v", err)
		}

		got, err := c.Pin(t.Context(), "nomad")
		if err != nil {
			t.Fatalf("Pin: %v", err)
		}
		if got != "" {
			t.Errorf("Pin = %q, want empty after clearing", got)
		}
	})

	// An item already gone is the wanted state. A compensation that ran twice,
	// or one for a pin the run never managed to write, must not fail.
	t.Run("clearing a pin that is not there is not an error", func(t *testing.T) {
		if err := live(t).ClearPin(t.Context(), "consul"); err != nil {
			t.Errorf("ClearPin of an absent pin: %v", err)
		}
	})

	t.Run("rejects an empty tool", func(t *testing.T) {
		if err := live(t).ClearPin(t.Context(), ""); err == nil {
			t.Error("expected an error for an empty tool")
		}
	})
}
