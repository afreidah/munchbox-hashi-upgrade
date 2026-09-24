// -------------------------------------------------------------------------------
// Plan Command - Survey a Cluster and Write a Run
//
// Author: Alex Freidah
//
// Generates a run file and nothing else. It reads the cluster, derives the
// task order and writes the result to the working directory; it changes no
// pin, stops no timer and touches no host. Everything that alters anything is
// a task in the file it produces, carried out later against that file.
// -------------------------------------------------------------------------------

package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	nomadclient "github.com/afreidah/munchbox-hashi-upgrade/internal/clients/nomad"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/steps"
)

// fileTimestamp orders run files by name as well as by date, and carries no
// separators that need quoting on a command line.
const fileTimestamp = "20060102T150405Z"

type planOptions struct {
	to      string
	address string
	region  string
	drain   bool
	dir     string
}

func newPlanCmd() *cobra.Command {
	var opts planOptions

	cmd := &cobra.Command{
		Use:   "plan <tool>",
		Short: "Survey a cluster and write a run file",
		Long: "Surveys the cluster, derives the order its hosts are upgraded in, and writes " +
			"the result to a file. Nothing is changed: the file is reviewed, then applied.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd, plan.Tool(args[0]), opts)
		},
	}

	cmd.Flags().StringVar(&opts.to, "to", "", "version to upgrade to (required)")
	cmd.Flags().StringVar(&opts.address, "address", "", "cluster address, overriding the environment")
	cmd.Flags().StringVar(&opts.region, "region", "", "cluster region, overriding the environment")
	cmd.Flags().BoolVar(&opts.drain, "drain", false,
		"empty each client before restarting it; off because restarting an agent does not stop its workloads")
	cmd.Flags().StringVar(&opts.dir, "dir", ".", "directory to write the run file to")
	_ = cmd.MarkFlagRequired("to")

	return cmd
}

func runPlan(cmd *cobra.Command, tool plan.Tool, opts planOptions) error {
	if !tool.Known() {
		return fmt.Errorf("unknown tool %q; expected nomad, consul or vault", tool)
	}
	if tool != plan.Nomad {
		return fmt.Errorf("%s is not supported yet; only nomad is", tool)
	}

	client, err := nomadclient.New(nomadclient.Options{Address: opts.address, Region: opts.region})
	if err != nil {
		return err
	}

	cluster, err := client.Survey(cmd.Context())
	if err != nil {
		return err
	}
	if len(cluster.Members) == 0 {
		return errors.New("the survey found no hosts; check the address and token")
	}

	spec := plan.Spec{Tool: tool, To: opts.to, Drain: opts.drain}
	run := plan.Create(
		filepath.Join(opts.dir, runFilename(tool, cluster, time.Now().UTC())),
		Version,
		spec,
		cluster,
		steps.Build(spec, cluster),
	)

	if err := run.Save(); err != nil {
		return err
	}

	_, err = io.WriteString(cmd.OutOrStdout(), summarise(run))
	return err
}

// runFilename names a run after the cluster it was generated against, so two
// clusters planned from one working directory do not read alike. A cluster
// that publishes no name is left out rather than given a placeholder.
func runFilename(tool plan.Tool, cluster plan.Cluster, at time.Time) string {
	parts := []string{string(tool)}
	if cluster.Name != "" {
		parts = append(parts, cluster.Name)
	}
	parts = append(parts, at.Format(fileTimestamp))
	return strings.Join(parts, "-") + ".yaml"
}

// summarise renders the sequence in the order it will run, so the ordering can
// be checked before anything acts on it. Built as a string rather than written
// out directly, so the rendering is testable on its own and the single write
// is the only thing that can fail.
func summarise(run *plan.Run) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s\n\n", run.Path())
	fmt.Fprintf(&b, "%s: %d hosts, %d server failures tolerated\n",
		cmp.Or(run.Cluster.Name, "unnamed cluster"), len(run.Cluster.Members), run.Cluster.Tolerance)
	fmt.Fprintf(&b, "upgrading %s to %s\n\n", run.Spec.Tool, run.Spec.To)

	var stage plan.Stage
	for _, task := range run.Tasks {
		if task.Stage != stage {
			stage = task.Stage
			fmt.Fprintf(&b, "%s\n", stage)
		}
		fmt.Fprintf(&b, "  %s%s\n", task.Title, annotation(task))
	}
	return b.String()
}

// annotation marks the tasks that do not simply run: the ones that stop for an
// operator, and the one past which the cluster is committed.
func annotation(task plan.Task) string {
	var notes []string
	if task.Confirm != plan.ConfirmNone {
		notes = append(notes, string(task.Confirm))
	}
	if task.Irreversible {
		notes = append(notes, "point of no return")
	}
	if len(notes) == 0 {
		return ""
	}
	return "  [" + strings.Join(notes, ", ") + "]"
}
