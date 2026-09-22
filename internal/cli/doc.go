// Package cli builds the hashi-upgrade command tree.
//
// Each subcommand lives in its own file and is constructed by a new*Cmd
// function that Root wires in. Commands own argument parsing and operator
// output only; the planning, discovery and execution logic they call lives in
// the sibling internal packages, so a command body stays thin enough to read
// as a description of the operation it performs.
package cli
