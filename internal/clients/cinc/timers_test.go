// -------------------------------------------------------------------------------
// Timer Tests - Freeze, Thaw, And The Node That Refuses
//
// Author: Alex Freidah
//
// Freeze passes only when both halves hold, so each half is failed on its own:
// a timer that survived the disable and a converge that was already running are
// different faults with the same consequence. The commands themselves are
// asserted verbatim, because a freeze that runs the wrong unit name reports
// success on a node that is still on its hourly schedule.
// -------------------------------------------------------------------------------

package cinc

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/afreidah/munchbox-hashi-upgrade/internal/clients/ssh"
)

// The commands a freeze and a thaw are expected to send, spelled out rather
// than built from the same constants the code uses.
const (
	disableCmd  = "systemctl disable --now cinc-client.timer"
	enableCmd   = "systemctl enable --now cinc-client.timer"
	isActiveCmd = "systemctl is-active cinc-client.timer"
	lockCmd     = "flock -n -E 9 /var/cinc/cache/cinc-client-running.pid true"
)

// stopped is what a node whose timer went down answers: systemd exits non-zero
// on an inactive unit, and the run lock is free.
func stopped() map[string]answer {
	return map[string]answer{
		isActiveCmd: {res: ssh.Result{Output: "inactive\n", Code: 3}},
	}
}

func TestFreeze(t *testing.T) {
	t.Run("stops the timer and confirms both halves", func(t *testing.T) {
		n := &node{host: "server-a", replies: stopped()}
		fleet, targets := stand(t, n)

		if err := fleet.Freeze(t.Context(), targets); err != nil {
			t.Fatalf("Freeze: %v", err)
		}
		want := []string{disableCmd, isActiveCmd, lockCmd}
		if !slices.Equal(n.ran, want) {
			t.Errorf("ran %v, want %v", n.ran, want)
		}
	})

	// A timer that is still up after being disabled has not been frozen, and a
	// clean exit from is-active is what says so.
	t.Run("refuses a timer that survived the disable", func(t *testing.T) {
		n := &node{host: "server-a", replies: map[string]answer{
			isActiveCmd: {res: ssh.Result{Output: "active\n"}},
		}}
		fleet, targets := stand(t, n)

		err := fleet.Freeze(t.Context(), targets)
		if !errors.Is(err, ErrTimerActive) {
			t.Fatalf("Freeze error = %v, want %v", err, ErrTimerActive)
		}
		if slices.Contains(n.ran, lockCmd) {
			t.Errorf("ran %v, want no lock check once the timer failed", n.ran)
		}
	})

	// A node mid-converge is applying the cookbooks the run is about to change,
	// so it fails rather than being waited on.
	t.Run("refuses a converge already in flight", func(t *testing.T) {
		replies := stopped()
		replies[lockCmd] = answer{res: ssh.Result{Code: lockBusyCode}}
		fleet, targets := stand(t, &node{host: "server-b", replies: replies})

		if err := fleet.Freeze(t.Context(), targets); !errors.Is(err, ErrConvergeInFlight) {
			t.Errorf("Freeze error = %v, want %v", err, ErrConvergeInFlight)
		}
	})

	// flock reporting anything but its conflict code means the check itself
	// failed, which is not the same as the node being busy.
	t.Run("reports a lock check that failed", func(t *testing.T) {
		replies := stopped()
		replies[lockCmd] = answer{res: ssh.Result{Output: "flock: bad file descriptor", Code: 1}}
		fleet, targets := stand(t, &node{host: "server-b", replies: replies})

		err := fleet.Freeze(t.Context(), targets)
		switch {
		case err == nil:
			t.Fatal("expected an error for a failing lock check")
		case errors.Is(err, ErrConvergeInFlight):
			t.Errorf("Freeze error = %v, want a broken check rather than a busy node", err)
		}
	})

	t.Run("reports a disable that was refused", func(t *testing.T) {
		n := &node{host: "server-a", replies: map[string]answer{
			disableCmd: {res: ssh.Result{Output: "Failed to disable unit", Code: 1}},
		}}
		fleet, targets := stand(t, n)

		if err := fleet.Freeze(t.Context(), targets); err == nil {
			t.Fatal("expected an error for a refused disable")
		}
		if len(n.ran) != 1 {
			t.Errorf("ran %v, want nothing after the disable failed", n.ran)
		}
	})

	// A command that never ran says nothing about the timer, so it fails the
	// freeze wherever in the sequence it happens.
	t.Run("reports a command that never ran", func(t *testing.T) {
		for _, cmd := range []string{disableCmd, isActiveCmd, lockCmd} {
			t.Run(cmd, func(t *testing.T) {
				want := errors.New("session closed")
				replies := stopped()
				replies[cmd] = answer{err: want}
				fleet, targets := stand(t, &node{host: "server-a", replies: replies})

				if err := fleet.Freeze(t.Context(), targets); !errors.Is(err, want) {
					t.Errorf("Freeze error = %v, want %v", err, want)
				}
			})
		}
	})

	// Every node is attempted and every failure is named, because a freeze that
	// reported one bad node would leave the rest unaccounted for.
	t.Run("attempts every node and names each failure", func(t *testing.T) {
		busy := stopped()
		busy[lockCmd] = answer{res: ssh.Result{Code: lockBusyCode}}

		good := &node{host: "server-a", replies: stopped()}
		fleet, targets := stand(t,
			good,
			&node{host: "server-b", replies: busy},
			&node{host: "oraclenode1", dialErr: errors.New("no route to host")},
		)

		err := fleet.Freeze(t.Context(), targets)
		if err == nil {
			t.Fatal("expected an error naming the nodes that failed")
		}
		for _, host := range []string{"server-b", "oraclenode1"} {
			if !strings.Contains(err.Error(), host) {
				t.Errorf("Freeze error %q does not name %s", err, host)
			}
		}
		if strings.Contains(err.Error(), good.host) {
			t.Errorf("Freeze error %q names the node that succeeded", err)
		}
		if len(good.ran) != 3 {
			t.Errorf("the healthy node ran %v, want the full freeze", good.ran)
		}
	})
}

func TestThaw(t *testing.T) {
	t.Run("starts the timer on every node", func(t *testing.T) {
		nodes := []*node{{host: "server-a"}, {host: "server-b"}}
		fleet, targets := stand(t, nodes[0], nodes[1])

		if err := fleet.Thaw(t.Context(), targets); err != nil {
			t.Fatalf("Thaw: %v", err)
		}
		for _, n := range nodes {
			if !slices.Equal(n.ran, []string{enableCmd}) {
				t.Errorf("%s ran %v, want %v", n.host, n.ran, enableCmd)
			}
		}
	})

	// Thaw is the last compensation to unwind, so one node refusing must not
	// leave the others on a stopped timer.
	t.Run("thaws the rest when one node refuses", func(t *testing.T) {
		bad := &node{host: "server-a", replies: map[string]answer{
			enableCmd: {res: ssh.Result{Output: "Failed to enable unit", Code: 1}},
		}}
		good := &node{host: "server-b"}
		fleet, targets := stand(t, bad, good)

		err := fleet.Thaw(t.Context(), targets)
		if err == nil {
			t.Fatal("expected an error for a refused enable")
		}
		if !strings.Contains(err.Error(), bad.host) {
			t.Errorf("Thaw error %q does not name %s", err, bad.host)
		}
		if !slices.Equal(good.ran, []string{enableCmd}) {
			t.Errorf("%s ran %v, want the thaw to have been attempted", good.host, good.ran)
		}
	})
}

func TestNodeError(t *testing.T) {
	inner := errors.New("boom")
	err := &NodeError{Target: ssh.Target{Host: "server-a"}, Err: inner}

	if !strings.Contains(err.Error(), "server-a") {
		t.Errorf("Error() = %q, want the host named", err)
	}
	if !errors.Is(err, inner) {
		t.Errorf("Unwrap did not reach %v", inner)
	}
}
