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

	spec := plan.Spec{Tool: tool, From: running(cluster), To: opts.to, Drain: opts.drain}
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

// running is the version the fleet is on, when it is on one.
//
// Read from the hosts rather than from the pin, which would mean giving plan
// credentials for a configuration server it otherwise never touches. It also
// answers the more useful question: the pin says what the fleet is aimed at,
// and this says where it actually is.
//
// A fleet part-way through an upgrade is on no single version, and saying so
// would be a guess. Empty, and a reader is told the start was not recorded
// rather than told a version that is only true of some hosts.
func running(cluster plan.Cluster) string {
	var version string

	for _, m := range cluster.Members {
		switch {
		case m.Version == "":
			continue
		case version == "":
			version = m.Version
		case m.Version != version:
			return ""
		}
	}

	return version
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
	fmt.Fprintf(&b, "%s: %s, %s tolerated\n",
		cmp.Or(run.Cluster.Name, "unnamed cluster"),
		count(len(run.Cluster.Members), "host"),
		count(run.Cluster.Tolerance, "server failure"))
	fmt.Fprintf(&b, "upgrading %s to %s\n\n", run.Spec.Tool, run.Spec.To)
	b.WriteString(topology(run))
	b.WriteString("\n")

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

// count renders a quantity with its noun, pluralised. Only ever reads one
// summary line, so the naive rule is enough for the nouns it is given.
func count(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// topology renders what the survey found, so the plan states the cluster it
// was generated against and not only what it intends to do. Without it the
// task list names hosts the reader has to take on trust, and the numbers the
// ordering was derived from -- who coordinates, who is a voter, who is already
// upgraded -- are in the file and nowhere in front of them.
//
// Members arrive sorted servers-first, so the grouping follows the order
// rather than reordering anything.
func topology(run *plan.Run) string {
	var b strings.Builder

	var width int
	for _, m := range run.Cluster.Members {
		width = max(width, len(m.Name))
	}

	var kind plan.Kind
	for _, m := range run.Cluster.Members {
		if m.Kind != kind {
			kind = m.Kind
			fmt.Fprintf(&b, "%ss\n", kind)
		}

		fmt.Fprintf(&b, "  %-*s  %-8s", width, m.Name, cmp.Or(m.Version, "unknown"))
		if notes := condition(m, run.Spec.To); len(notes) > 0 {
			fmt.Fprintf(&b, "  %s", strings.Join(notes, ", "))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// condition names what is worth saying about a host beyond its version.
//
// Silence means ordinary: healthy, a voter if it coordinates, eligible if it
// carries work. Only the departures are printed, so a fleet that is fine reads
// as one.
func condition(m plan.Member, target string) []string {
	var out []string

	if m.Primary {
		out = append(out, "coordinating")
	}
	if m.Kind == plan.KindServer && !m.Voter {
		out = append(out, "not a voter")
	}
	if !m.Healthy {
		out = append(out, cmp.Or(m.Status, "unhealthy"))
	}
	if m.Kind == plan.KindClient && !m.Eligible {
		out = append(out, "not accepting work")
	}

	// The hosts a run will pass over. Worth stating up front: a plan whose
	// hosts are already upgraded does far less than its task list suggests.
	if m.Version == target {
		out = append(out, "already at "+target)
	}

	return out
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
