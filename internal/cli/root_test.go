// -------------------------------------------------------------------------------
// Root Command Tests
//
// Author: Alex Freidah
//
// Covers the properties the rest of the tree depends on: that Root hands back
// an independent command each call, and that cobra is configured to leave
// error reporting to main rather than printing usage over a failure.
// -------------------------------------------------------------------------------

package cli_test

import (
	"testing"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/cli"
)

func TestRoot_UseIsBinaryName(t *testing.T) {
	if got := cli.Root().Use; got != "hashi-upgrade" {
		t.Errorf("Use = %q, want %q", got, "hashi-upgrade")
	}
}

// Root must not hand out a shared command: cobra stores parsed flag values on
// the command itself, so a singleton would leak one test's arguments into the
// next.
func TestRoot_ReturnsIndependentTrees(t *testing.T) {
	first, second := cli.Root(), cli.Root()
	if first == second {
		t.Error("Root returned the same command twice; it must construct a fresh tree")
	}
}

func TestRoot_LeavesErrorReportingToMain(t *testing.T) {
	root := cli.Root()
	if !root.SilenceUsage {
		t.Error("SilenceUsage = false; usage printed over an execution failure buries the error")
	}
	if !root.SilenceErrors {
		t.Error("SilenceErrors = false; main prints the error, so cobra printing it too duplicates it")
	}
}
