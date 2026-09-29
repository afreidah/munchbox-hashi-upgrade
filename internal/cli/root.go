// -------------------------------------------------------------------------------
// Root Command - Command Tree Construction
//
// Author: Alex Freidah
//
// Assembles the subcommands into a single tree and holds the tool version that
// every generated plan records. Root returns a freshly-constructed tree rather
// than a package-level singleton so tests can build an isolated command per
// example without state leaking between them.
// -------------------------------------------------------------------------------

package cli

import "github.com/spf13/cobra"

// Version is the tool version, stamped by the build and written into plan files
// so a plan records which binary produced it.
var Version = "dev"

// Root returns a freshly-constructed root command tree.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "hashi-upgrade",
		Short: "Roll Nomad, Consul and Vault upgrades across a cluster",
		Long: "Discovers cluster topology from the live API, generates a plan whose " +
			"step order and gates are derived from what each node carries, and drives " +
			"cinc-client node by node against it.",
		Version: Version,
		// Errors are printed once by main; cobra printing usage on top of an
		// execution failure buries the message that matters.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newPlanCmd())
	root.AddCommand(newRunCmd())
	return root
}
