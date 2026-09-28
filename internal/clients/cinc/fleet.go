// -------------------------------------------------------------------------------
// Fleet - Converging Nodes Over SSH
//
// Author: Alex Freidah
//
// The half of a configuration server that has no API. Nodes poll it and it
// offers nothing to push at them, so triggering a converge and stopping the
// hourly timers both happen on the node.
//
// Separate from Cinc because the two halves share no dependency: the pin needs
// a signing key and no SSH, and these need SSH and no key. A run that does one
// should not have to hold credentials for the other.
// -------------------------------------------------------------------------------

package cinc

import (
	"context"
	"errors"
	"io"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/ssh"
)

// Commands run on a node. The unit names are the configuration server's own;
// the lock is the one the configuration client takes for the length of a run.
const (
	converge  = "cinc-client"
	timerUnit = "cinc-client.timer"
	runLock   = "/var/cinc/cache/cinc-client-running.pid"
)

//go:generate mockgen -destination=mock_generated_test.go -package=cinc github.com/afreidah/munchbox-hashi-upgrade/internal/clients/cinc Dialer,Session

// Session is one open connection to a node.
type Session interface {
	Run(ctx context.Context, cmd string, out io.Writer) (ssh.Result, error)
	Close() error
}

// Dialer opens a session to a node.
//
// Declared here rather than in the transport, because this is the package that
// needs it: the transport knows nothing about converges, and a fake satisfying
// these two methods is the whole of what the tests need.
type Dialer interface {
	Connect(ssh.Target) (Session, error)
}

// OverSSH adapts an SSH client to Dialer.
type OverSSH struct {
	Client *ssh.Client
}

// Connect opens an SSH connection to t.
func (o OverSSH) Connect(t ssh.Target) (Session, error) {
	return o.Client.Connect(t)
}

// Fleet runs configuration-management commands on nodes.
type Fleet struct {
	dial Dialer
}

// NewFleet returns a fleet that acts through dial.
func NewFleet(dial Dialer) (*Fleet, error) {
	if dial == nil {
		return nil, errors.New("dialer is required")
	}
	return &Fleet{dial: dial}, nil
}

// Converge runs the configuration client on one node, streaming its output to
// out as it arrives.
//
// A converge that fails is a result rather than an error: the exit status is
// what a caller decides about and the output is worth keeping either way. An
// error here means the command never ran.
func (f *Fleet) Converge(ctx context.Context, target ssh.Target, out io.Writer) (ssh.Result, error) {
	session, err := f.dial.Connect(target)
	if err != nil {
		return ssh.Result{}, err
	}
	defer func() { _ = session.Close() }()

	return session.Run(ctx, converge, out)
}
