// -------------------------------------------------------------------------------
// Fleet Tests - Converging Over A Connection That Is Not There
//
// Author: Alex Freidah
//
// A converge is minutes long, needs a signing key and an SSH certificate, and
// changes the machine it runs on, so what is asserted here is the commands sent
// and what is made of the answers. The mock connection is the point: the node
// behaviour worth testing is the failing node, which a real one will not be on
// demand.
// -------------------------------------------------------------------------------

package cinc

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/ssh"
)

// answer is what one command reports.
type answer struct {
	res ssh.Result
	err error
}

// node is one host and the answers it gives.
type node struct {
	host    string
	replies map[string]answer

	// dialErr fails the connection instead of answering anything.
	dialErr error

	// ran is every command the node was asked to run, in order.
	ran []string
}

// ok is the answer an unlisted command gives, since most commands in a run are
// ones whose success is unremarkable.
var ok = answer{res: ssh.Result{}}

// stand builds a fleet over the given nodes.
func stand(t *testing.T, nodes ...*node) (*Fleet, []ssh.Target) {
	t.Helper()

	ctrl := gomock.NewController(t)
	dialer := NewMockDialer(ctrl)
	targets := make([]ssh.Target, 0, len(nodes))

	for _, n := range nodes {
		target := ssh.Target{Host: n.host, User: "root"}
		targets = append(targets, target)

		if n.dialErr != nil {
			dialer.EXPECT().Connect(target).Return(nil, n.dialErr)
			continue
		}
		dialer.EXPECT().Connect(target).Return(session(ctrl, n), nil)
	}

	fleet, err := NewFleet(dialer, io.Discard)
	if err != nil {
		t.Fatalf("NewFleet: %v", err)
	}

	// The real waits are minutes. A test that exercises the waiting path
	// should cost milliseconds.
	fleet.wait = 50 * time.Millisecond
	fleet.poll = time.Millisecond

	return fleet, targets
}

// session returns a connection answering from the node's table and recording
// what it was asked. Close is expected, because a connection left open on a
// node that failed is a leak per failure.
func session(ctrl *gomock.Controller, n *node) *MockSession {
	s := NewMockSession(ctrl)

	s.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(_ context.Context, cmd string, out io.Writer) (ssh.Result, error) {
			n.ran = append(n.ran, cmd)

			reply, listed := n.replies[cmd]
			if !listed {
				reply = ok
			}
			if out != nil && reply.res.Output != "" {
				_, _ = io.WriteString(out, reply.res.Output)
			}

			return reply.res, reply.err
		})
	s.EXPECT().Close().Return(nil)

	return s
}

func TestNewFleet(t *testing.T) {
	if _, err := NewFleet(nil, io.Discard); err == nil {
		t.Error("expected an error for a missing dialer")
	}

	// A nil writer is not an error: a fleet that reports nowhere is a fleet
	// that reports nothing, which is what a caller with no output wants.
	if _, err := NewFleet(NewMockDialer(gomock.NewController(t)), nil); err != nil {
		t.Errorf("NewFleet with no writer: %v", err)
	}
}

func TestConverge(t *testing.T) {
	t.Run("runs the converge and streams its output", func(t *testing.T) {
		n := &node{host: "server-a", replies: map[string]answer{
			converge: {res: ssh.Result{Output: "resolving cookbooks\n"}},
		}}
		fleet, targets := stand(t, n)

		var out strings.Builder
		res, err := fleet.Converge(t.Context(), targets[0], &out)
		if err != nil {
			t.Fatalf("Converge: %v", err)
		}
		if !res.OK() {
			t.Errorf("Converge exited %d, want 0", res.Code)
		}
		if out.String() != "resolving cookbooks\n" {
			t.Errorf("streamed %q, want the converge output", out.String())
		}
		if len(n.ran) != 1 || n.ran[0] != "cinc-client" {
			t.Errorf("ran %v, want the converge alone", n.ran)
		}
	})

	// A converge that fails is the case the runner exists to handle, so the exit
	// status reaches it rather than being turned into an error here.
	t.Run("a failed converge is a result, not an error", func(t *testing.T) {
		n := &node{host: "server-b", replies: map[string]answer{
			converge: {res: ssh.Result{Output: "1 resource failed\n", Code: 1}},
		}}
		fleet, targets := stand(t, n)

		res, err := fleet.Converge(t.Context(), targets[0], nil)
		if err != nil {
			t.Fatalf("Converge: %v", err)
		}
		if res.Code != 1 {
			t.Errorf("Converge exited %d, want 1", res.Code)
		}
	})

	t.Run("reports a node it cannot reach", func(t *testing.T) {
		want := errors.New("no route to host")
		fleet, targets := stand(t, &node{host: "oraclenode1", dialErr: want})

		if _, err := fleet.Converge(t.Context(), targets[0], nil); !errors.Is(err, want) {
			t.Errorf("Converge error = %v, want %v", err, want)
		}
	})

	t.Run("reports a command that never ran", func(t *testing.T) {
		want := errors.New("session closed")
		n := &node{host: "server-a", replies: map[string]answer{converge: {err: want}}}
		fleet, targets := stand(t, n)

		if _, err := fleet.Converge(t.Context(), targets[0], nil); !errors.Is(err, want) {
			t.Errorf("Converge error = %v, want %v", err, want)
		}
	})
}
