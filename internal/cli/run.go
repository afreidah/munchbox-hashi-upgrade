// -------------------------------------------------------------------------------
// Run Command - Carry Out A Run File
//
// Author: Alex Freidah
//
// Assembles the clients a step reaches the cluster through, hands them to the
// runner alongside the file, and lets the runner own order and persistence.
// Nothing here decides what happens next: the file already says, and a run
// interrupted at any point is resumed by naming the same file again.
//
// Three modes. A no-op proves the file parses and prints the sequence; a dry
// run additionally performs the steps that only read, so the cluster is proven
// to answer before anything is written; a live run does the work.
// -------------------------------------------------------------------------------

package cli

import (
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	cincclient "github.com/afreidah/munchbox-hashi-upgrade/internal/clients/cinc"
	nomadclient "github.com/afreidah/munchbox-hashi-upgrade/internal/clients/nomad"
	sshclient "github.com/afreidah/munchbox-hashi-upgrade/internal/clients/ssh"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/execute"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/ready"
	"github.com/afreidah/munchbox-hashi-upgrade/internal/runner"
)

type runOptions struct {
	noOp   bool
	dryRun bool
	yes    bool
	reset  []string

	address string
	region  string

	cincServer string
	cincClient string
	cincKey    string
	cincCA     string

	sshKey    string
	sshCert   string
	sshHostCA string
	sshUser   string
	sshPort   int
	sshSudo   bool
}

func newRunCmd() *cobra.Command {
	var opts runOptions

	cmd := &cobra.Command{
		Use:   "run <run-file>",
		Short: "Carry out a run file against the cluster it was generated from",
		Long: "Works through the tasks in a run file, writing the outcome of each back to it. " +
			"A run that stops for any reason is resumed by naming the same file again.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doRun(cmd, args[0], opts)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&opts.noOp, "no-op", false, "print what each task would do and change nothing")
	f.BoolVar(&opts.dryRun, "dry-run", false, "carry out the read-only tasks for real; print the rest")
	f.BoolVar(&opts.yes, "yes", false, "assent to every gated task without asking")
	f.StringSliceVar(&opts.reset, "reset", nil,
		"forget what happened to these tasks so the run reaches them again; repeatable")

	f.StringVar(&opts.address, "address", "", "cluster address, overriding the environment")
	f.StringVar(&opts.region, "region", "", "cluster region, overriding the environment")

	f.StringVar(&opts.cincServer, "cinc-server", "", "configuration server URL")
	f.StringVar(&opts.cincClient, "cinc-client", "", "identity to sign configuration server requests as")
	f.StringVar(&opts.cincKey, "cinc-key", "", "private key for that identity")
	f.StringVar(&opts.cincCA, "cinc-ca", "",
		"the configuration server's certificate authority: a PEM file, or a directory of them")

	f.StringVar(&opts.sshKey, "ssh-key", "", "private key to reach hosts with")
	f.StringVar(&opts.sshCert, "ssh-cert", "", "signed certificate for that key")
	f.StringVar(&opts.sshHostCA, "ssh-host-ca", "", "host certificate authority, or a known_hosts file holding it")
	f.StringVar(&opts.sshUser, "ssh-user", "root", "user to log into hosts as")
	f.IntVar(&opts.sshPort, "ssh-port", 22, "port to reach hosts on")
	f.BoolVar(&opts.sshSudo, "ssh-sudo", false, "run commands through sudo, for hosts that refuse a root login")

	return cmd
}

func doRun(cmd *cobra.Command, path string, opts runOptions) error {
	if opts.noOp && opts.dryRun {
		return errors.New("--no-op and --dry-run ask for different things; choose one")
	}

	run, err := plan.Open(path)
	if err != nil {
		return err
	}

	if err := reset(cmd, run, opts.reset); err != nil {
		return err
	}

	deps, err := assemble(cmd, run, opts)
	if err != nil {
		return err
	}

	// Only a live run writes its outcomes back. A rehearsal that journalled
	// would settle every task and leave the file finished, so the real run
	// against it would find nothing to do and say so.
	r, err := runner.New(runner.Options{
		Run:     run,
		Steps:   execute.Steps(deps),
		Out:     cmd.OutOrStdout(),
		Confirm: confirmerFor(cmd, opts),
		Journal: mode(opts) == execute.Live,
	})
	if err != nil {
		return err
	}

	return r.Apply(cmd.Context())
}

// reset forgets what happened to the named tasks and saves the file, so the
// run reaches them again.
//
// A run refuses to step over a task that failed or was left active, and tells
// the operator to look at the host. This is the other half of that: once they
// have, they name the task and it is put back into play. Saved before anything
// else happens, so the decision survives whatever the run does next.
//
// A name no task carries is refused rather than ignored: it is a typo, and
// silently resetting nothing would look like it worked.
func reset(cmd *cobra.Command, run *plan.Run, ids []string) error {
	for _, id := range ids {
		if !slices.ContainsFunc(run.Tasks, func(t plan.Task) bool { return t.ID == id }) {
			return fmt.Errorf("no task %q in %s", id, run.Path())
		}

		if run.Reset(id) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "forgetting %s\n", id)
		}
	}

	if len(ids) == 0 {
		return nil
	}
	return run.Save()
}

// confirmerFor returns what gated tasks are put through. --yes assents to
// everything, which is for a run already reviewed and restarted, not for the
// first pass over one.
func confirmerFor(cmd *cobra.Command, opts runOptions) runner.Confirmer {
	if opts.yes || opts.noOp {
		return runner.Always()
	}
	return newConfirmer(cmd.InOrStdin(), cmd.OutOrStdout())
}

// assemble builds the clients the steps reach the cluster through.
//
// A no-op reaches nothing, so it is given none: building them would demand
// credentials for a mode whose whole point is that it touches nothing.
func assemble(cmd *cobra.Command, run *plan.Run, opts runOptions) (*execute.Deps, error) {
	deps := &execute.Deps{
		Run:    run,
		Out:    cmd.OutOrStdout(),
		Mode:   mode(opts),
		Target: resolver(run.Cluster, opts),
		Hosts:  hosts(run.Cluster, opts),
	}
	if deps.Mode == execute.NoOp {
		return deps, nil
	}

	nomad, err := nomadclient.New(nomadclient.Options{Address: opts.address, Region: opts.region})
	if err != nil {
		return nil, err
	}
	deps.Survey = nomad
	deps.Coordination = nomad
	deps.Drains = nomad

	// The gate reports what it is waiting for. Without somewhere to write it, a
	// wait of up to three minutes is indistinguishable from a hang.
	gate, err := ready.New(ready.Options{Cluster: nomad, Out: cmd.OutOrStdout()})
	if err != nil {
		return nil, err
	}
	deps.Wait = gate

	// A dry run performs only the steps that read, and the cluster is the one
	// thing it reads. Demanding configuration-server and ssh credentials for a
	// rehearsal that will not use them would put them behind a flag wall.
	if deps.Mode == execute.DryRun {
		return deps, nil
	}

	pins, err := cincclient.New(cincclient.Options{
		ServerURL:    opts.cincServer,
		ClientName:   opts.cincClient,
		KeyPath:      opts.cincKey,
		TrustedCerts: opts.cincCA,
	})
	if err != nil {
		return nil, err
	}
	deps.Versions = pins

	ssh, err := sshclient.New(sshclient.Config{
		KeyPath:    opts.sshKey,
		CertPath:   opts.sshCert,
		HostCAPath: opts.sshHostCA,
	})
	if err != nil {
		return nil, err
	}

	fleet, err := cincclient.NewFleet(cincclient.OverSSH{Client: ssh}, cmd.OutOrStdout())
	if err != nil {
		return nil, err
	}
	deps.Fleet = fleet

	return deps, nil
}

// mode reads the flags as one choice, since they are mutually exclusive and
// the steps take a single value.
func mode(opts runOptions) execute.Mode {
	switch {
	case opts.noOp:
		return execute.NoOp
	case opts.dryRun:
		return execute.DryRun
	default:
		return execute.Live
	}
}

// resolver maps a member name to somewhere ssh can land.
//
// The survey records an advertise address, which is what the cluster reaches
// the host on and therefore what this run should too: a name that resolves
// differently from the workstation would upgrade a different machine.
func resolver(cluster plan.Cluster, opts runOptions) func(string) (sshclient.Target, error) {
	byName := make(map[string]plan.Member, len(cluster.Members))
	for _, m := range cluster.Members {
		byName[m.Name] = m
	}

	return func(name string) (sshclient.Target, error) {
		m, ok := byName[name]
		if !ok {
			return sshclient.Target{}, fmt.Errorf("the survey has no member %q", name)
		}
		return target(m, opts), nil
	}
}

// hosts is every host in the survey, for the tasks that act on the fleet.
func hosts(cluster plan.Cluster, opts runOptions) []sshclient.Target {
	out := make([]sshclient.Target, 0, len(cluster.Members))
	for _, m := range cluster.Members {
		out = append(out, target(m, opts))
	}
	return out
}

// target is how this run logs into one host. The login is uniform across the
// fleet: a host needing different credentials is a reason to run separately,
// not to carry a second set of flags.
func target(m plan.Member, opts runOptions) sshclient.Target {
	return sshclient.Target{
		Host: m.Addr,
		User: opts.sshUser,
		Port: opts.sshPort,
		Sudo: opts.sshSudo,
	}
}
