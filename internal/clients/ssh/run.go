// -------------------------------------------------------------------------------
// SSH Execution - Connections, Sessions, Streamed Output
//
// Author: Alex Freidah
//
// Dialling a host and running commands on it. A connection is opened once and
// reused for the several commands a node needs, because each command costs a
// session rather than a handshake.
//
// Output is written as it arrives and kept as it is written, so a converge can
// be watched while it runs and still be recorded once it finishes. How a command
// exited is reported rather than judged: a non-zero status is a result, and what
// it means belongs to the caller that chose the command.
// -------------------------------------------------------------------------------

package ssh

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Result is what a command reported.
type Result struct {
	Output string
	Code   int
}

// OK reports whether the command exited cleanly.
func (r Result) OK() bool { return r.Code == 0 }

// Conn is an open connection to one host. The caller closes it.
type Conn struct {
	client *ssh.Client
	sudo   bool
}

// Connect dials t.
func (c *Client) Connect(t Target) (*Conn, error) {
	addr := net.JoinHostPort(t.Host, strconv.Itoa(cmp.Or(t.Port, defaultPort)))
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            t.User,
		Auth:            c.auth,
		HostKeyCallback: c.hostKey,
		Timeout:         c.timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s as %s: %w", addr, t.User, err)
	}
	return &Conn{client: client, sudo: t.Sudo}, nil
}

// Close tears down the connection.
func (c *Conn) Close() error { return c.client.Close() }

// Run executes cmd and returns its combined output and exit status, copying the
// output to out as it arrives when out is not nil.
//
// Cancelling ctx closes the session, which is how a command is actually stopped:
// signal forwarding is optional in the protocol and openssh does not implement
// it, so a run that asked politely would go on running.
func (c *Conn) Run(ctx context.Context, cmd string, out io.Writer) (Result, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("ssh session: %w", err)
	}
	defer func() { _ = session.Close() }()

	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()

	var captured strings.Builder
	sink := serialise(&captured, out)
	session.Stdout = sink
	session.Stderr = sink

	err = session.Run(c.line(cmd))
	result := Result{Output: captured.String()}
	switch {
	case err == nil:
		return result, nil
	case ctx.Err() != nil:
		return result, ctx.Err()
	default:
		if exit, ok := errors.AsType[*ssh.ExitError](err); ok {
			result.Code = exit.ExitStatus()
			return result, nil
		}
		return result, fmt.Errorf("ssh run %q: %w", cmd, err)
	}
}

// line applies the sudo prefix the target asks for. The whole command is
// prefixed, so a caller that needs a pipeline elevated passes it as one shell
// invocation rather than relying on sudo to reach past the first word.
func (c *Conn) line(cmd string) string {
	if c.sudo {
		return "sudo " + cmd
	}
	return cmd
}

// lockWriter serialises writes. Standard output and standard error are copied
// by separate goroutines, and both are pointed at one sink here so that what is
// recorded is what the operator watched, interleaved the same way.
type lockWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// serialise returns the single sink both output streams are written to.
func serialise(captured io.Writer, out io.Writer) io.Writer {
	if out != nil {
		captured = io.MultiWriter(out, captured)
	}
	return &lockWriter{w: captured}
}
