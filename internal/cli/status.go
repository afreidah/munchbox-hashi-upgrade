// -------------------------------------------------------------------------------
// Status Command - Where A Run Got To
//
// Author: Alex Freidah
//
// Reads a run file and reports it. Nothing else: no cluster, no credentials, no
// change. A run that stopped in the night is read here before deciding what to
// do about it, and reading it should not require the ability to act on it.
//
// What the file holds and what the cluster holds can differ -- the file records
// what this run did, not what has happened since. Confirming the fleet is the
// verify task's job, against a live read.
// -------------------------------------------------------------------------------

package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <run-file>",
		Short: "Report where a run file got to",
		Long: "Reads a run file and prints what happened to each task. Touches no " +
			"cluster and changes nothing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			run, err := plan.Open(args[0])
			if err != nil {
				return err
			}

			_, err = io.WriteString(cmd.OutOrStdout(), report(run))
			return err
		},
	}
}

// report renders a run's progress.
//
// Built as a string rather than written out directly, so the rendering is
// testable on its own and the single write is the only thing that can fail.
func report(run *plan.Run) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s\n\n", run.Path())
	fmt.Fprintf(&b, "%s: %s to %s\n",
		run.Spec.Tool, versionFrom(run.Spec), run.Spec.To)
	fmt.Fprintf(&b, "generated %s by %s\n\n",
		run.CreatedAt.Format(time.RFC3339), run.Built)

	var stage plan.Stage
	for _, task := range run.Tasks {
		if task.Stage != stage {
			stage = task.Stage
			fmt.Fprintf(&b, "%s\n", stage)
		}
		fmt.Fprintf(&b, "  %-12s %s%s\n", run.Outcome(task.ID), task.Title, took(run, task.ID))
	}

	b.WriteString("\n")
	b.WriteString(standing(run))

	return b.String()
}

// versionFrom names where the run started, which a run generated against an
// unpinned fleet does not know.
func versionFrom(spec plan.Spec) string {
	if spec.From == "" {
		return "an unrecorded version"
	}
	return spec.From
}

// took renders how long a task ran, for the ones that finished. A task still
// active has a start and no end, and how long it has been that way is the
// thing worth knowing about it.
func took(run *plan.Run, id string) string {
	rec, ok := run.Progress[id]
	if !ok || rec.StartedAt == nil {
		return ""
	}

	if rec.FinishedAt == nil {
		return fmt.Sprintf("  (started %s ago, never reported back)",
			time.Since(*rec.StartedAt).Round(time.Second))
	}
	return fmt.Sprintf("  (%s)", rec.FinishedAt.Sub(*rec.StartedAt).Round(time.Second))
}

// standing says what the run is waiting for, in the terms a decision is made
// in: finished, stopped on something, or ready to carry on.
func standing(run *plan.Run) string {
	next, ok := run.Next()
	if !ok {
		return fmt.Sprintf("finished: every task settled, %s on %s\n",
			run.Spec.Tool, run.Spec.To)
	}

	var b strings.Builder

	// A task that failed or never reported back is not somewhere a run carries
	// on from, so the way past it is printed with the reason it is there.
	switch outcome := run.Outcome(next.ID); outcome {
	case plan.Failed, plan.Active:
		fmt.Fprintf(&b, "stopped: %s is %s\n", next.ID, outcome)
		if err := run.Progress[next.ID].Err; err != "" {
			fmt.Fprintf(&b, "  %s\n", err)
		}
		fmt.Fprintf(&b, "\ncheck the host, then: run %s --reset %s\n", run.Path(), next.ID)
	default:
		fmt.Fprintf(&b, "next: %s\n", next.Title)
		if run.Committed() {
			b.WriteString("  the cluster is part-upgraded; finishing is the safe direction\n")
		}
		fmt.Fprintf(&b, "\nto carry on: run %s\n", run.Path())
	}

	return b.String()
}
