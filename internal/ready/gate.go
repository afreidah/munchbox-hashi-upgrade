// -------------------------------------------------------------------------------
// Gate - Waiting On A Cluster To Settle
//
// Author: Alex Freidah
//
// Construction, and the polling loop every assertion runs through. A host that
// has just restarted needs time to rejoin and be trusted again, so a gate reads
// the cluster on an interval until its condition holds, and on timeout reports
// the condition that was still unmet.
//
// The cluster is read through a one-method interface declared here, over the
// neutral types the client packages return. So this package never imports a
// tool's API, and the gates are tested against a fake instead of a cluster.
// -------------------------------------------------------------------------------

package ready

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/plan"
)

// How long a gate waits, and how often it looks, when the caller says nothing.
//
// Ten minutes because the gate is waiting out a converge that installs a couple
// of hundred megabytes, restarts a service, and rejoins a raft cluster. Three
// minutes is inside that on a slow host, and a gate that gives up early fails a
// run that was going to succeed -- which costs more than waiting, since the
// operator then has to work out whether the host is wrong or merely unhurried.
const (
	DefaultTimeout  = 10 * time.Minute
	DefaultInterval = 5 * time.Second
)

//go:generate mockgen -destination=mock_generated_test.go -package=ready github.com/afreidah/munchbox-hashi-upgrade/internal/ready Cluster

// Cluster reads a cluster's verdict on itself.
//
// One method, because that is the whole of what a gate needs: the client
// package that owns the tool's vocabulary has already normalised it.
type Cluster interface {
	Health(ctx context.Context) (plan.Cluster, error)
}

// Options configure a gate. A zero Timeout or Interval takes the default, so
// the usual caller supplies a cluster and nothing else.
// Out is where a gate reports what it is waiting for. A gate that waits up to
// three minutes in silence cannot be told apart from one that has hung, which
// is the wrong thing to be wondering during an upgrade. Nil discards.
type Options struct {
	Cluster  Cluster
	Timeout  time.Duration
	Interval time.Duration
	Out      io.Writer
}

// Gate asserts that a cluster has absorbed a step.
type Gate struct {
	cluster  Cluster
	timeout  time.Duration
	interval time.Duration
	out      io.Writer
}

// New returns a gate reading through opts.Cluster.
func New(opts Options) (*Gate, error) {
	if opts.Cluster == nil {
		return nil, errors.New("cluster is required")
	}

	g := &Gate{cluster: opts.Cluster, timeout: opts.Timeout, interval: opts.Interval, out: opts.Out}
	if g.out == nil {
		g.out = io.Discard
	}
	if g.timeout <= 0 {
		g.timeout = DefaultTimeout
	}
	if g.interval <= 0 {
		g.interval = DefaultInterval
	}

	return g, nil
}

// How much of an objection is worth printing while waiting. An API error can
// carry a whole response body, and several hundred characters of JSON on a
// five-second poll buries the run it is reporting on.
const briefly = 120

// sayf reports progress. A write that fails is discarded: the gate's business
// is the cluster, and failing a run because its narration could not be printed
// would be the wrong thing to stop for.
func (g *Gate) sayf(format string, args ...any) {
	_, _ = fmt.Fprintf(g.out, format, args...)
}

// brief shortens a reason to something that fits a line.
func brief(reason string) string {
	if len(reason) <= briefly {
		return reason
	}
	return reason[:briefly] + "..."
}

// await reads the cluster until assert is satisfied, and reports what assert
// last objected to when it gives up.
//
// The unmet condition is what makes a timeout worth reading: that a gate waited
// three minutes says nothing, and that it spent them waiting for a host to
// become a voter again says where to look.
// arrived is printed once the condition holds, stated as the fact it
// establishes rather than as "ready".
func (g *Gate) await(ctx context.Context, what, arrived string, assert func(plan.Cluster) error) error {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()

	// The context is checked before each read rather than only after one. A
	// cancelled caller has abandoned the run, and a condition that happens to
	// pass on the way out is not permission to move to the next host.
	g.sayf("  waiting for %s\n", what)
	began := time.Now()

	// Reported only when it changes. The same objection every five seconds is
	// noise, and a new one is the only thing that says progress was made.
	var said string

	var unmet error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w", what, errors.Join(err, unmet))
		}

		cluster, err := g.cluster.Health(ctx)
		switch {
		case err != nil:
			// A read that failed is not a condition that failed. A cluster
			// refusing connections mid-restart is one of the things being
			// waited out.
			unmet = err
		default:
			if unmet = assert(cluster); unmet == nil {
				g.sayf("    %s (%s)\n", arrived, time.Since(began).Round(time.Second))
				return nil
			}
		}

		if unmet != nil && unmet.Error() != said {
			said = unmet.Error()
			g.sayf("    %s\n", brief(said))
		}

		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// member returns the named host, or an error naming what the cluster did report.
//
// A host absent from the cluster's own view is the ordinary state for the
// seconds after a restart, so it is a condition to wait on rather than a fault.
func member(cluster plan.Cluster, name string) (plan.Member, error) {
	for _, m := range cluster.Members {
		if m.Name == name {
			return m, nil
		}
	}

	return plan.Member{}, fmt.Errorf("%s is not in the cluster", name)
}
